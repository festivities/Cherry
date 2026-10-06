package main

import (
	"encoding/json"
	"io"
	"net"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Economy Phase B: per-account mailbox (postbox/*) for staff giveaways.
//
// Native evidence (libgame.so arm64): GET postbox/exist -> ResPostIsExist @0x1b289b4
// (result bool, badge flag); GET postbox/list -> ResPostBoxWithPageAll @0x1a6aa04 ->
// NaPostBoxPopup::MsgProc msg 11131 -> sDataPostList::ParserJSON @0x1c2800c
// (result.{nextCursor,previousCursor,totalCount} asString, items[] of rows) and
// sDataPostList::Post::ParserJSON @0x1c0d378; POST postbox/receive/<postSeq> and
// postbox/reject/<postSeq> -> ResPostReceive/Reject @0x1a6eca8/@0x1a6ee88 (result asBool).
// The cell picks its icon and text by postType ("gem", "cash", "voucher" are built in:
// NaPostBoxUpDownCell::SetItemImage @0x19d698c, SetItemText @0x1a20748). gem/cash text is
// atoi(productData) + "GEMS"/"CASH"; voucher text is "Face Shop tickets x " +
// productDataDetail. A successful receive shows "You received N Gems" (ShowCheckPostPopup
// @0x1a293f8) and re-syncs the balance through coin/balance. expireDate and sent are
// epoch milliseconds as strings. Receive errors are HTTP errors whose errorCode maps in
// NaPostBoxPopup::MsgProc: 62102 "already expired", 62107 "You can only receive this item once".

const (
	mailMaxRows      = 100
	mailDefaultDays  = 30
	mailSender       = "Cherry Staff"
	errMailExpired   = `{"errorCode":"62102","errorMessage":"cherry: mail expired"}`
	errMailNotThere  = `{"errorCode":"62107","errorMessage":"cherry: mail already received"}`
	mailTypeGems     = "gems"
	mailTypeCash     = "cash"
	mailTypeTickets  = "faceTickets"
	mailTitle        = "Gift from Cherry"
	maxMailAmountCap = 1_000_000
)

// mailRow is one unclaimed mail; Created/Expires are epoch milliseconds.
type mailRow struct {
	Seq     int64  `json:"postSeq"`
	Type    string `json:"type"` // gems | cash | faceTickets
	Amount  int64  `json:"amount"`
	Message string `json:"message"`
	Sender  string `json:"sender"`
	Created int64  `json:"created"`
	Expires int64  `json:"expires"`
}

func (m mailRow) deltas() ledgerDeltas {
	switch m.Type {
	case mailTypeCash:
		return ledgerDeltas{Cash: m.Amount}
	case mailTypeTickets:
		return ledgerDeltas{FaceTickets: m.Amount}
	}
	return ledgerDeltas{Gems: m.Amount}
}

func validMailType(t string) bool {
	return t == mailTypeGems || t == mailTypeCash || t == mailTypeTickets
}

// postRow is the native Post JSON; every string field is asString, ints asInt.
func (m mailRow) postRow() map[string]any {
	postType, data, detail := "gem", strconv.FormatInt(m.Amount, 10), ""
	switch m.Type {
	case mailTypeCash:
		postType = "cash"
	case mailTypeTickets:
		postType, data, detail = "voucher", "0", strconv.FormatInt(m.Amount, 10)
	}
	return map[string]any{
		"postSeq": strconv.FormatInt(m.Seq, 10), "postType": postType,
		"productData": data, "productDataDetail": detail, "productImageUrl": "",
		"title": mailTitle, "content": m.Message,
		"expireDate": strconv.FormatInt(m.Expires, 10), "fromBlocked": false,
		"itemType": "", "itemGradeType": "", "dyeType": 0, "itemSpecialEffects": "",
		"sent": strconv.FormatInt(m.Created, 10), "flagType": "", "recycleReward": 0,
		"canReply":     false,
		"senderAvatar": map[string]any{"avatarId": "0", "name": m.Sender, "items": []any{}},
	}
}

// liveMail returns the unexpired rows.
func liveMail(acc *account, now time.Time) []mailRow {
	var out []mailRow
	for _, m := range acc.mail {
		if m.Expires > now.UnixMilli() {
			out = append(out, m)
		}
	}
	return out
}

// addMailLocked appends a row to a fresh slice (rollback copies of *acc share the old one)
// and drops expired rows. It returns false when the mailbox is full.
func addMailLocked(acc *account, typ string, amount int64, message, sender string, days int, now time.Time) bool {
	rows := liveMail(acc, now)
	if len(rows) >= mailMaxRows {
		return false
	}
	acc.nextMailSeq++
	rows = append(rows, mailRow{acc.nextMailSeq, typ, amount, message, sender, now.UnixMilli(),
		now.Add(time.Duration(days) * 24 * time.Hour).UnixMilli()})
	acc.mail = rows
	return true
}

func registerMailRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/v4/postbox/exist", handleMailExist)
	mux.HandleFunc("/v4/postbox/list", handleMailList)
	mux.HandleFunc("/v4/postbox/receive/", handleMailReceive)
	mux.HandleFunc("/v4/postbox/reject/", handleMailReject)
}

func handleMailExist(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		serveNotFound(w)
		return
	}
	accountsMu.Lock()
	acc := sessionAccountLocked(r)
	n := 0
	if acc != nil {
		n = len(liveMail(acc, nowFunc()))
	}
	accountsMu.Unlock()
	if acc == nil {
		writeJSON(w, http.StatusNotFound, unknownSessionBody)
		return
	}
	writeObj(w, map[string]any{"result": n > 0})
}

func handleMailList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		serveNotFound(w)
		return
	}
	accountsMu.Lock()
	acc := sessionAccountLocked(r)
	var rows []mailRow
	if acc != nil {
		rows = liveMail(acc, nowFunc())
	}
	accountsMu.Unlock()
	if acc == nil {
		writeJSON(w, http.StatusNotFound, unknownSessionBody)
		return
	}
	items := make([]map[string]any, 0, len(rows))
	for i := len(rows) - 1; i >= 0; i-- { // newest first
		items = append(items, rows[i].postRow())
	}
	writeObj(w, map[string]any{"result": map[string]any{
		"nextCursor": "0", "previousCursor": "0", "totalCount": strconv.Itoa(len(items)), "items": items,
	}})
}

// findMail returns the index of postSeq in acc.mail, or -1.
func findMail(acc *account, seqText string) int {
	seq, err := strconv.ParseInt(seqText, 10, 64)
	if err != nil {
		return -1
	}
	return slices.IndexFunc(acc.mail, func(m mailRow) bool { return m.Seq == seq })
}

// handleMailReceive credits through the ledger and removes the row in one saved step.
func handleMailReceive(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		serveNotFound(w)
		return
	}
	accountsMu.Lock()
	defer accountsMu.Unlock()
	acc := sessionAccountLocked(r)
	if acc == nil {
		writeJSON(w, http.StatusNotFound, unknownSessionBody)
		return
	}
	idx := findMail(acc, strings.TrimPrefix(r.URL.Path, "/v4/postbox/receive/"))
	if idx < 0 {
		writeJSON(w, http.StatusBadRequest, errMailNotThere)
		return
	}
	prev := *acc
	row := acc.mail[idx]
	rest := slices.Delete(slices.Clone(acc.mail), idx, idx+1)
	expired := row.Expires <= nowFunc().UnixMilli()
	var lines []ledgerLine
	var err error
	if !expired {
		lines, err = spendLocked(acc, row.deltas(), "mail receive "+strconv.FormatInt(row.Seq, 10))
	}
	if err == nil {
		acc.mail = rest
		err = saveAccountsLocked()
	}
	if err != nil {
		*acc = prev
		writeJSON(w, http.StatusInternalServerError, `{"errorCode":"500"}`)
		return
	}
	if expired {
		writeJSON(w, http.StatusBadRequest, errMailExpired)
		return
	}
	flushLedgerLocked(lines)
	writeJSON(w, http.StatusOK, `{"result":true}`)
}

// handleMailReject deletes without credit; an unknown seq is an already-done success.
func handleMailReject(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		serveNotFound(w)
		return
	}
	accountsMu.Lock()
	defer accountsMu.Unlock()
	acc := sessionAccountLocked(r)
	if acc == nil {
		writeJSON(w, http.StatusNotFound, unknownSessionBody)
		return
	}
	if idx := findMail(acc, strings.TrimPrefix(r.URL.Path, "/v4/postbox/reject/")); idx >= 0 {
		prev := *acc
		acc.mail = slices.Delete(slices.Clone(acc.mail), idx, idx+1)
		if err := saveAccountsLocked(); err != nil {
			*acc = prev
			writeJSON(w, http.StatusInternalServerError, `{"errorCode":"500"}`)
			return
		}
	}
	writeJSON(w, http.StatusOK, `{"result":true}`)
}

// ---- staff tool ----

func adminLoopback(r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	ip := net.ParseIP(host)
	return err == nil && ip != nil && ip.IsLoopback() && r.Method == http.MethodPost
}

func readAdminJSON(r *http.Request, dst any) bool {
	raw, err := io.ReadAll(io.LimitReader(r.Body, 4096))
	return err == nil && json.Unmarshal(raw, dst) == nil
}

// POST /admin/mail mails one account (aid) or every account ("all"):
//
//	curl -X POST http://127.0.0.1:8099/admin/mail -d "{\"aid\":\"1002\",\"type\":\"gems\",\"amount\":500,\"message\":\"Thanks for testing\",\"expiresDays\":30}"
//
// type is gems | cash | faceTickets; expiresDays defaults to 30.
func handleAdminMail(w http.ResponseWriter, r *http.Request) {
	if !adminLoopback(r) {
		serveNotFound(w)
		return
	}
	var req struct {
		Aid         string `json:"aid"`
		Type        string `json:"type"`
		Amount      int64  `json:"amount"`
		Message     string `json:"message"`
		ExpiresDays int    `json:"expiresDays"`
	}
	if !readAdminJSON(r, &req) || req.Aid == "" || !validMailType(req.Type) ||
		req.Amount < 1 || req.Amount > maxMailAmountCap || len(req.Message) > 500 || req.ExpiresDays < 0 || req.ExpiresDays > 3650 {
		writeJSON(w, http.StatusBadRequest, badRequestBody)
		return
	}
	if req.ExpiresDays == 0 {
		req.ExpiresDays = mailDefaultDays
	}
	accountsMu.Lock()
	defer accountsMu.Unlock()
	var targets []*account
	if req.Aid == "all" {
		seen := map[*account]bool{}
		for _, a := range accounts {
			if !seen[a] {
				seen[a] = true
				targets = append(targets, a)
			}
		}
	} else if a := accountByAidLocked(req.Aid); a != nil {
		targets = []*account{a}
	}
	if len(targets) == 0 {
		writeJSON(w, http.StatusNotFound, unknownSessionBody)
		return
	}
	prevs := make([]account, len(targets))
	sent := 0
	for i, a := range targets {
		prevs[i] = *a
		if addMailLocked(a, req.Type, req.Amount, req.Message, mailSender, req.ExpiresDays, nowFunc()) {
			sent++
		}
	}
	if sent == 0 {
		writeJSON(w, http.StatusBadRequest, `{"errorCode":"400","errorMessage":"cherry: mailbox full"}`)
		return
	}
	if err := saveAccountsLocked(); err != nil {
		for i, a := range targets {
			*a = prevs[i]
		}
		writeJSON(w, http.StatusInternalServerError, `{"errorCode":"500"}`)
		return
	}
	writeObj(w, map[string]any{"mailed": sent, "accounts": len(targets)})
}

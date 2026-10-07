package economy

import (
	"encoding/json"
	"io"
	"net"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"cherry/internal/httpx"
	"cherry/internal/store"
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
	MailMaxRows      = 100
	mailDefaultDays  = 30
	MailSender       = "Cherry Staff"
	errMailExpired   = `{"errorCode":"62102","errorMessage":"cherry: mail expired"}`
	errMailNotThere  = `{"errorCode":"62107","errorMessage":"cherry: mail already received"}`
	maxMailAmountCap = 1_000_000
)

func validMailType(t string) bool {
	return t == store.MailTypeGems || t == store.MailTypeCash || t == store.MailTypeTickets
}

// liveMail returns the unexpired rows.
func liveMail(acc *store.Account, now time.Time) []store.MailRow {
	var out []store.MailRow
	for _, m := range acc.Mail {
		if m.Expires > now.UnixMilli() {
			out = append(out, m)
		}
	}
	return out
}

// addMailLocked appends a row to a fresh slice (rollback copies of *acc share the old one)
// and drops expired rows. It returns false when the mailbox is full.
func addMailLocked(acc *store.Account, typ string, amount int64, message, sender string, days int, now time.Time) bool {
	rows := liveMail(acc, now)
	if len(rows) >= MailMaxRows {
		return false
	}
	acc.NextMailSeq++
	rows = append(rows, store.MailRow{Seq: acc.NextMailSeq, Type: typ, Amount: amount, Message: message, Sender: sender, Created: now.UnixMilli(),
		Expires: now.Add(time.Duration(days) * 24 * time.Hour).UnixMilli()})
	acc.Mail = rows
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
		httpx.ServeNotFound(w)
		return
	}
	store.AccountsMu.Lock()
	acc := store.SessionAccountLocked(r)
	n := 0
	if acc != nil {
		n = len(liveMail(acc, NowFunc()))
	}
	store.AccountsMu.Unlock()
	if acc == nil {
		httpx.WriteJSON(w, http.StatusNotFound, httpx.UnknownSessionBody)
		return
	}
	httpx.WriteObj(w, map[string]any{"result": n > 0})
}

func handleMailList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpx.ServeNotFound(w)
		return
	}
	store.AccountsMu.Lock()
	acc := store.SessionAccountLocked(r)
	var rows []store.MailRow
	if acc != nil {
		rows = liveMail(acc, NowFunc())
	}
	store.AccountsMu.Unlock()
	if acc == nil {
		httpx.WriteJSON(w, http.StatusNotFound, httpx.UnknownSessionBody)
		return
	}
	items := make([]map[string]any, 0, len(rows))
	for i := len(rows) - 1; i >= 0; i-- { // newest first
		items = append(items, rows[i].PostRow())
	}
	httpx.WriteObj(w, map[string]any{"result": map[string]any{
		"nextCursor": "0", "previousCursor": "0", "totalCount": strconv.Itoa(len(items)), "items": items,
	}})
}

// findMail returns the index of postSeq in acc.mail, or -1.
func findMail(acc *store.Account, seqText string) int {
	seq, err := strconv.ParseInt(seqText, 10, 64)
	if err != nil {
		return -1
	}
	return slices.IndexFunc(acc.Mail, func(m store.MailRow) bool { return m.Seq == seq })
}

// handleMailReceive credits through the ledger and removes the row in one saved step.
func handleMailReceive(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpx.ServeNotFound(w)
		return
	}
	store.AccountsMu.Lock()
	defer store.AccountsMu.Unlock()
	acc := store.SessionAccountLocked(r)
	if acc == nil {
		httpx.WriteJSON(w, http.StatusNotFound, httpx.UnknownSessionBody)
		return
	}
	idx := findMail(acc, strings.TrimPrefix(r.URL.Path, "/v4/postbox/receive/"))
	if idx < 0 {
		httpx.WriteJSON(w, http.StatusBadRequest, errMailNotThere)
		return
	}
	prev := *acc
	row := acc.Mail[idx]
	rest := slices.Delete(slices.Clone(acc.Mail), idx, idx+1)
	expired := row.Expires <= NowFunc().UnixMilli()
	var lines []store.LedgerLine
	var err error
	if !expired {
		lines, err = store.SpendLocked(acc, row.Deltas(), "mail receive "+strconv.FormatInt(row.Seq, 10))
	}
	if err == nil {
		acc.Mail = rest
		err = store.SaveAccountsLocked()
	}
	if err != nil {
		*acc = prev
		httpx.WriteJSON(w, http.StatusInternalServerError, `{"errorCode":"500"}`)
		return
	}
	if expired {
		httpx.WriteJSON(w, http.StatusBadRequest, errMailExpired)
		return
	}
	store.FlushLedgerLocked(lines)
	httpx.WriteJSON(w, http.StatusOK, `{"result":true}`)
}

// handleMailReject deletes without credit; an unknown seq is an already-done success.
func handleMailReject(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpx.ServeNotFound(w)
		return
	}
	store.AccountsMu.Lock()
	defer store.AccountsMu.Unlock()
	acc := store.SessionAccountLocked(r)
	if acc == nil {
		httpx.WriteJSON(w, http.StatusNotFound, httpx.UnknownSessionBody)
		return
	}
	if idx := findMail(acc, strings.TrimPrefix(r.URL.Path, "/v4/postbox/reject/")); idx >= 0 {
		prev := *acc
		acc.Mail = slices.Delete(slices.Clone(acc.Mail), idx, idx+1)
		if err := store.SaveAccountsLocked(); err != nil {
			*acc = prev
			httpx.WriteJSON(w, http.StatusInternalServerError, `{"errorCode":"500"}`)
			return
		}
	}
	httpx.WriteJSON(w, http.StatusOK, `{"result":true}`)
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
		httpx.ServeNotFound(w)
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
		httpx.WriteJSON(w, http.StatusBadRequest, httpx.BadRequestBody)
		return
	}
	if req.ExpiresDays == 0 {
		req.ExpiresDays = mailDefaultDays
	}
	store.AccountsMu.Lock()
	defer store.AccountsMu.Unlock()
	var targets []*store.Account
	if req.Aid == "all" {
		seen := map[*store.Account]bool{}
		for _, a := range store.Accounts {
			if !seen[a] {
				seen[a] = true
				targets = append(targets, a)
			}
		}
	} else if a := store.AccountByAidLocked(req.Aid); a != nil {
		targets = []*store.Account{a}
	}
	if len(targets) == 0 {
		httpx.WriteJSON(w, http.StatusNotFound, httpx.UnknownSessionBody)
		return
	}
	prevs := make([]store.Account, len(targets))
	sent := 0
	for i, a := range targets {
		prevs[i] = *a
		if addMailLocked(a, req.Type, req.Amount, req.Message, MailSender, req.ExpiresDays, NowFunc()) {
			sent++
		}
	}
	if sent == 0 {
		httpx.WriteJSON(w, http.StatusBadRequest, `{"errorCode":"400","errorMessage":"cherry: mailbox full"}`)
		return
	}
	if err := store.SaveAccountsLocked(); err != nil {
		for i, a := range targets {
			*a = prevs[i]
		}
		httpx.WriteJSON(w, http.StatusInternalServerError, `{"errorCode":"500"}`)
		return
	}
	httpx.WriteObj(w, map[string]any{"mailed": sent, "accounts": len(targets)})
}

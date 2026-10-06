package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Economy Phase A: per-account Gems / Cash / Face Shop tickets with an append-only
// transaction log, billing switched off, and the balance/voucher routes.
// Currency is earned in-game or granted by staff only; no real-money route is
// served (bill/*, coin/charge, direct/reserveCash, coin/period/*, seasonpass/bill/info,
// admob/*, quest/video/* all stay 404).

// ponytail: lab accounts keep their showcase grants (room furniture, pets); every
// other account starts fresh. Hard-coded aids; move to a store flag if more are needed.
var labAids = map[string]bool{"1001": true, "1002": true}

const (
	maxBalance    = 1 << 30 // keeps every balance inside the client's int32 asInt
	welcomeGems   = 300
	billingEndURL = "/v4/setting/goodbye/cherry"
)

// ResGoodbyeSevice @0x1a573c8: billingEnd bool, forceRefundPopup bool, refundUrl string.
// forceRefundPopup:true logs the client out and tears down agents; never serve true.
const goodbyeBody = `{"result":{"billingEnd":true,"forceRefundPopup":false,"refundUrl":""}}`

// HTTP business errors: ADI parses errorCode with asString+atoi from a non-2xx reply
// (IsErrorResponse @0x1b3cf00); NaDispatcherShop::ErrCommonShopPurchase @0x1aa9cdc maps
// 61001/61002/61005 to string 2025 "Incorrect purchase information. Please try again."
// No errorCode maps to "Not enough gems" (that popup is client-side, NaFaceShop::CheckPurchase).
const errNotEnoughBody = `{"errorCode":"61001","errorMessage":"cherry: insufficient balance"}`
const errBadProductBody = `{"errorCode":"61001","errorMessage":"cherry: bad product or price"}`

// ledgerDeltas is a set of signed balance changes applied together.
type ledgerDeltas struct{ Gems, Cash, FaceTickets int64 }

// balanceError is returned when a delta would take a balance below 0 or above maxBalance.
type balanceError struct {
	Currency string
	Balance  int64
	Delta    int64
}

func (e *balanceError) Error() string {
	return fmt.Sprintf("ledger: %s balance %d cannot change by %d", e.Currency, e.Balance, e.Delta)
}

type ledgerLine struct {
	Time     string `json:"time"`
	Aid      string `json:"aid"`
	Currency string `json:"currency"`
	Delta    int64  `json:"delta"`
	Balance  int64  `json:"balance"`
	Reason   string `json:"reason"`
}

// spendLocked checks and applies d to acc's in-memory balances and returns the log
// lines. It does not save: the caller (holding accountsMu) saves with the rest of its
// mutation, restores `*acc = previous` on failure, and calls flushLedgerLocked after a
// successful save. Nothing changes when an error is returned.
func spendLocked(acc *account, d ledgerDeltas, reason string) ([]ledgerLine, error) {
	names := [3]string{"gems", "cash", "faceTickets"}
	fields := [3]*int64{&acc.gems, &acc.cash, &acc.faceTickets}
	deltas := [3]int64{d.Gems, d.Cash, d.FaceTickets}
	for i, v := range deltas {
		nb := *fields[i] + v
		if v > maxBalance || v < -maxBalance || nb < 0 || nb > maxBalance {
			return nil, &balanceError{names[i], *fields[i], v}
		}
	}
	now := time.Now().UTC().Format(time.RFC3339)
	var lines []ledgerLine
	for i, v := range deltas {
		if v == 0 {
			continue
		}
		*fields[i] += v
		lines = append(lines, ledgerLine{now, acc.aid, names[i], v, *fields[i], reason})
	}
	return lines, nil
}

// flushLedgerLocked appends lines to ledger.jsonl next to accounts.json. Call only
// after the account save succeeded; a failure here is logged, never fatal.
func flushLedgerLocked(lines []ledgerLine) {
	if len(lines) == 0 || accountStorePath == "" {
		return
	}
	var buf []byte
	for _, l := range lines {
		b, _ := json.Marshal(l)
		buf = append(append(buf, b...), '\n')
	}
	path := filepath.Join(filepath.Dir(accountStorePath), "ledger.jsonl")
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err == nil {
		_, err = f.Write(buf)
		if cerr := f.Close(); err == nil {
			err = cerr
		}
	}
	if err != nil {
		log.Printf("cherry: ledger log write failed: %v", err)
	}
}

// applyDeltasLocked is the standalone transaction: check, apply, save, roll back on a
// save failure, then log. Caller holds accountsMu.
func applyDeltasLocked(acc *account, d ledgerDeltas, reason string) error {
	prev := *acc
	lines, err := spendLocked(acc, d, reason)
	if err != nil {
		return err
	}
	if err := saveAccountsLocked(); err != nil {
		*acc = prev
		return err
	}
	flushLedgerLocked(lines)
	return nil
}

// accountByAidLocked finds an account by aid (aliases share one pointer).
func accountByAidLocked(aid string) *account {
	for _, a := range accounts {
		if a.aid == aid {
			return a
		}
	}
	return nil
}

func registerEconomyRoutes(mux *http.ServeMux) {
	mux.HandleFunc(billingEndURL, handleJSONBody(goodbyeBody))
	mux.HandleFunc("/v4/coin/balance", handleCoinBalance)
	mux.HandleFunc("/v4/vip/balance", handleJSONBody(vipBalanceBody))
	mux.HandleFunc("/v4/heart/extra/balance/detail", handleJSONBody(extraHeartBalanceBody))
	mux.HandleFunc("/v4/voucher/own/count/faceshop", handleVoucherCount)
	mux.HandleFunc("/v4/voucher/own/list/faceshop", handleJSONBody(`{"result":[]}`))
	mux.HandleFunc("/v4/voucher/product/list/faceshop", handleVoucherProductList)
	mux.HandleFunc("/v4/voucher/purchase", handleVoucherPurchase)
	// The only bill/* routes served: the client's "Not enough gems/Cash" popup first fetches
	// a special-chance upsell (NaCreateLackOfMoneyPopup @0x19e9fb4); a 404 turns it into
	// "An unexpected error has occurred." (ErrSpecialChanceGem @0x1aaa404). An empty list
	// offers nothing to buy, and with billingEnd the client shows plain "Not enough gems."
	// (NaNormalLackOfMoneyPopup @0x19ea628). Exact paths only.
	for _, unit := range []string{"gem", "cash", "heart"} {
		mux.HandleFunc("/v4/bill/prod/spot/"+unit, handleJSONBody(emptySpecialChanceBody))
	}
	registerLoginBonusRoutes(mux)
	registerMailRoutes(mux)
}

const emptySpecialChanceBody = `{"result":{"items":[],"location":""}}`

// ResExtraHeartBalanceURL @0x1b53c3c: totalCount, freeTotalCount, paidCount (asInt).
const extraHeartBalanceBody = `{"result":{"totalCount":0,"freeTotalCount":0,"paidCount":0}}`

// sDataVipBalance::ParseJsonValue @0x1b3a6bc: balance/balanceTotal/balanceTotalRevised
// asInt, grade asString, vipGradeTable{NONE..DIAMOND} and vipGradeTableTotal{ROYAL..}
// objects of asInt, isNewVip asBool.
const vipBalanceBody = `{"result":{"balance":0,"grade":"NONE","balanceTotal":0,"balanceTotalRevised":0,"isNewVip":false,` +
	`"vipGradeTable":{"NONE":0,"BRONZE":0,"SILVER":0,"GOLD":0,"PLATINUM":0,"DIAMOND":0},` +
	`"vipGradeTableTotal":{"ROYAL":0,"ROYAL_BLUE":0,"ROYAL_PURPLE":0,"ROYAL_BLACK":0}}}`

// ResCoinBalanceURL @0x1b53a84: total/free/payed asInt. Everything is free currency.
func handleCoinBalance(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		serveNotFound(w)
		return
	}
	accountsMu.Lock()
	acc := accounts[cookieValue(r, "AV_AUTH")]
	var n int64
	if acc != nil {
		n = acc.gems
		if r.URL.Query().Get("coinUnit") == "CASH" {
			n = acc.cash
		}
	}
	accountsMu.Unlock()
	if acc == nil {
		writeJSON(w, http.StatusNotFound, unknownSessionBody)
		return
	}
	writeJSON(w, http.StatusOK, fmt.Sprintf(`{"result":{"totalCoinBalance":%d,"freeCoinBalance":%d,"payedCoinBalance":0}}`, n, n))
}

// handleWelcomeGift serves POST create/complete (tutorialDone, ResTutorialDoneWithCachePolicy
// @0x1a5e918: status asBool, rewardCoin asInt only when status is true). The client retries
// forever on 404 and shows a network-error dialog + retry on status:false, so this always
// answers status:true. The 300 Gems are credited once per account; repeats and an unknown
// requester get rewardCoin 0. The request directly follows create/avatar, so without an
// AV_AUTH match the newest account is the requester.
func handleWelcomeGift(w http.ResponseWriter, r *http.Request) {
	const noGift = `{"result":{"status":true,"rewardCoin":0}}`
	accountsMu.Lock()
	acc := accounts[cookieValue(r, "AV_AUTH")]
	if acc == nil {
		acc = latestAcc
	}
	if acc == nil || acc.welcomed {
		accountsMu.Unlock()
		writeJSON(w, http.StatusOK, noGift)
		return
	}
	prev := *acc
	lines, err := spendLocked(acc, ledgerDeltas{Gems: welcomeGems}, "welcome gift")
	acc.welcomed = true
	if err == nil {
		err = saveAccountsLocked()
	}
	if err != nil {
		*acc = prev
		accountsMu.Unlock()
		writeJSON(w, http.StatusInternalServerError, `{"errorCode":"500"}`)
		return
	}
	flushLedgerLocked(lines)
	accountsMu.Unlock()
	writeJSON(w, http.StatusOK, fmt.Sprintf(`{"result":{"status":true,"rewardCoin":%d}}`, welcomeGems))
}

// ResFaceShopOwnVoucherCount @0x1b2c6a4 reads result.voucherCount via asInt.
func handleVoucherCount(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		serveNotFound(w)
		return
	}
	accountsMu.Lock()
	acc := accounts[cookieValue(r, "AV_AUTH")]
	var n int64
	if acc != nil {
		n = acc.faceTickets
	}
	accountsMu.Unlock()
	if acc == nil {
		writeJSON(w, http.StatusNotFound, unknownSessionBody)
		return
	}
	writeJSON(w, http.StatusOK, fmt.Sprintf(`{"result":{"voucherCount":%d}}`, n))
}

// Server price table for Face Shop tickets, in Gems (client price is never trusted).
var faceVoucherProducts = []struct {
	Code     string
	Tickets  int
	Standard int64 // list price
	Price    int64 // charged
	Name     string
}{
	{"FSV1", 1, 150, 150, "1 Face Shop ticket"},
	{"FSV5", 5, 750, 650, "5 Face Shop tickets"},
}

// ResFaceShopProductVoucherList @0x1b506fc: array of rows; productCode,productName,
// productImagePath,voucherCode,voucherUsePlace,paymentType,sellType,cpId,discountStart,
// discountEnd asString; voucherGiveCount,baseDiscountPercentage,standardPrice,finalPrice
// asInt. Rows with empty productCode or cpId are dropped by the client.
func handleVoucherProductList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		serveNotFound(w)
		return
	}
	rows := make([]map[string]any, 0, len(faceVoucherProducts))
	for _, p := range faceVoucherProducts {
		rows = append(rows, map[string]any{
			"productCode": p.Code, "productName": p.Name, "productImagePath": "",
			"voucherCode": "FACESHOP_TICKET", "voucherUsePlace": "FACESHOP",
			"voucherGiveCount": p.Tickets, "baseDiscountPercentage": int((p.Standard - p.Price) * 100 / p.Standard),
			"paymentType": "GEM", "standardPrice": p.Standard, "finalPrice": p.Price,
			"sellType": "NORMAL", "cpId": "cherry", "discountStart": "", "discountEnd": "",
		})
	}
	body, _ := json.Marshal(map[string]any{"result": rows})
	writeJSON(w, http.StatusOK, string(body))
}

// lenientInt reads a JSON number or numeric string (the client sends some ints quoted).
func lenientInt(raw json.RawMessage) (int64, bool) {
	s := strings.Trim(strings.TrimSpace(string(raw)), `"`)
	n, err := strconv.ParseInt(s, 10, 64)
	return n, err == nil
}

// POST voucher/purchase {language,deviceType,coinUnit,productCode,price,cpId}; reply
// ResFaceShopProductVoucherPurchase @0x1b2c798: avatarId, productCode, paymentType
// ("GEM" writes the Gem total) asString, balance asInt.
func handleVoucherPurchase(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		serveNotFound(w)
		return
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, maxJSONBody+1))
	if err != nil || len(raw) > maxJSONBody {
		writeJSON(w, http.StatusBadRequest, badRequestBody)
		return
	}
	var req struct {
		CoinUnit    string          `json:"coinUnit"`
		ProductCode string          `json:"productCode"`
		Price       json.RawMessage `json:"price"`
	}
	if _, err := unmarshalNativeJSON(raw, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, badRequestBody)
		return
	}
	price, priceOK := lenientInt(req.Price)
	idx := -1
	for i, p := range faceVoucherProducts {
		if p.Code == req.ProductCode {
			idx = i
		}
	}
	if idx < 0 || !priceOK || req.CoinUnit != "GEM" || price != faceVoucherProducts[idx].Price {
		writeJSON(w, http.StatusBadRequest, errBadProductBody)
		return
	}
	p := faceVoucherProducts[idx]
	accountsMu.Lock()
	defer accountsMu.Unlock()
	acc := accounts[cookieValue(r, "AV_AUTH")]
	if acc == nil {
		writeJSON(w, http.StatusNotFound, unknownSessionBody)
		return
	}
	err = applyDeltasLocked(acc, ledgerDeltas{Gems: -p.Price, FaceTickets: int64(p.Tickets)}, "voucher purchase "+p.Code)
	if be, ok := err.(*balanceError); ok && be != nil {
		writeJSON(w, http.StatusBadRequest, errNotEnoughBody)
		return
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, `{"errorCode":"500"}`)
		return
	}
	body, _ := json.Marshal(map[string]any{"result": map[string]any{
		"avatarId": acc.aid, "productCode": p.Code, "paymentType": "GEM", "balance": acc.gems,
	}})
	writeJSON(w, http.StatusOK, string(body))
}

// ---- staff grant tool ----

const adminAddr = "127.0.0.1:8099"

// newAdminMux serves the staff tool; it is bound to loopback only and never mounted on
// the public mux. Example (deltas may be negative; balances never go below 0):
//
//	curl -X POST http://127.0.0.1:8099/admin/grant -d "{\"aid\":\"1002\",\"gems\":500,\"cash\":10,\"faceTickets\":3,\"reason\":\"test\"}"
func newAdminMux() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/admin/grant", handleAdminGrant)
	mux.HandleFunc("/admin/mail", handleAdminMail)
	mux.HandleFunc("/admin/attend", handleAdminAttend)
	return mux
}

func handleAdminGrant(w http.ResponseWriter, r *http.Request) {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if ip := net.ParseIP(host); err != nil || ip == nil || !ip.IsLoopback() || r.Method != http.MethodPost {
		serveNotFound(w)
		return
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, 4096))
	var req struct {
		Aid         string `json:"aid"`
		Gems        int64  `json:"gems"`
		Cash        int64  `json:"cash"`
		FaceTickets int64  `json:"faceTickets"`
		Reason      string `json:"reason"`
	}
	if err != nil || json.Unmarshal(raw, &req) != nil || req.Aid == "" {
		writeJSON(w, http.StatusBadRequest, badRequestBody)
		return
	}
	reason := "staff grant"
	if req.Reason != "" {
		reason += ": " + req.Reason
	}
	accountsMu.Lock()
	defer accountsMu.Unlock()
	acc := accountByAidLocked(req.Aid)
	if acc == nil {
		writeJSON(w, http.StatusNotFound, unknownSessionBody)
		return
	}
	err = applyDeltasLocked(acc, ledgerDeltas{req.Gems, req.Cash, req.FaceTickets}, reason)
	if _, ok := err.(*balanceError); ok {
		writeJSON(w, http.StatusBadRequest, errNotEnoughBody)
		return
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, `{"errorCode":"500"}`)
		return
	}
	body, _ := json.Marshal(map[string]any{"aid": acc.aid, "gems": acc.gems, "cash": acc.cash, "faceTickets": acc.faceTickets})
	writeJSON(w, http.StatusOK, string(body))
}

// serveAdmin starts the loopback listener; a busy port is logged, not fatal.
func serveAdmin(logger *log.Logger) {
	ln, err := net.Listen("tcp", adminAddr)
	if err != nil {
		logger.Printf("cherry: admin listener unavailable on %s: %v", adminAddr, err)
		return
	}
	logger.Printf("cherry: admin grant listening addr=%s", adminAddr)
	srv := &http.Server{Handler: newAdminMux(), ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = srv.Serve(ln) }()
}

// Package economy implements the Gem/Cash/ticket economy: balances and vouchers,
// the loopback staff tool, Face Shop, photozone, the daily calendar (and its
// generated skin) and the mailbox.
package economy

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"cherry/internal/httpx"
	"cherry/internal/store"
)

// Economy Phase A: per-account Gems / Cash / Face Shop tickets with an append-only
// transaction log, billing switched off, and the balance/voucher routes.
// Currency is earned in-game or granted by staff only; no real-money route is
// served (bill/*, coin/charge, direct/reserveCash, coin/period/*, seasonpass/bill/info,
// admob/*, quest/video/* all stay 404).

const (
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

func RegisterEconomyRoutes(mux *http.ServeMux) {
	mux.HandleFunc(billingEndURL, httpx.HandleJSONBody(goodbyeBody))
	mux.HandleFunc("/v4/coin/balance", handleCoinBalance)
	mux.HandleFunc("/v4/vip/balance", httpx.HandleJSONBody(vipBalanceBody))
	mux.HandleFunc("/v4/heart/extra/balance/detail", httpx.HandleJSONBody(extraHeartBalanceBody))
	mux.HandleFunc("/v4/voucher/own/count/faceshop", handleVoucherCount)
	mux.HandleFunc("/v4/voucher/own/list/faceshop", httpx.HandleJSONBody(`{"result":[]}`))
	mux.HandleFunc("/v4/voucher/product/list/faceshop", handleVoucherProductList)
	mux.HandleFunc("/v4/voucher/purchase", handleVoucherPurchase)
	// The only bill/* routes served: the client's "Not enough gems/Cash" popup first fetches
	// a special-chance upsell (NaCreateLackOfMoneyPopup @0x19e9fb4); a 404 turns it into
	// "An unexpected error has occurred." (ErrSpecialChanceGem @0x1aaa404). An empty list
	// offers nothing to buy, and with billingEnd the client shows plain "Not enough gems."
	// (NaNormalLackOfMoneyPopup @0x19ea628). Exact paths only.
	for _, unit := range []string{"gem", "cash", "heart"} {
		mux.HandleFunc("/v4/bill/prod/spot/"+unit, httpx.HandleJSONBody(EmptySpecialChanceBody))
	}
	// Home-icon package status (NaDispatcherPurchase::ReqPurchaseHomeIcon, MENU rows Package Day 1 and Package):
	// ResPurchaseHomeIcon reads result.starterIcon/packageIcon (achieveId asInt; iconType, category,
	// endDateTime asString; available asBool). Both unavailable: nothing is on sale, nothing is offered.
	mux.HandleFunc("/v4/bill/prod/icon", httpx.HandleJSONBody(homeIconBody))
	registerLoginBonusRoutes(mux)
	registerMailRoutes(mux)
	registerShopRoutes(mux)
}

const EmptySpecialChanceBody = `{"result":{"items":[],"location":""}}`

const homeIconBody = `{"result":{"starterIcon":{"achieveId":0,"iconType":"","available":false,"category":"","endDateTime":""},` +
	`"packageIcon":{"achieveId":0,"iconType":"","available":false,"category":"","endDateTime":""}}}`

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
		httpx.ServeNotFound(w)
		return
	}
	store.AccountsMu.Lock()
	acc := store.Accounts[httpx.CookieValue(r, "AV_AUTH")]
	var n int64
	if acc != nil {
		n = acc.Gems
		if r.URL.Query().Get("coinUnit") == "CASH" {
			n = acc.Cash
		}
	}
	store.AccountsMu.Unlock()
	if acc == nil {
		httpx.WriteJSON(w, http.StatusNotFound, httpx.UnknownSessionBody)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, fmt.Sprintf(`{"result":{"totalCoinBalance":%d,"freeCoinBalance":%d,"payedCoinBalance":0}}`, n, n))
}

// Balances returns aid's Gem and Cash balances; ok is false for an unknown aid.
func Balances(aid string) (gems, cash int64, ok bool) {
	store.AccountsMu.Lock()
	defer store.AccountsMu.Unlock()
	acc := store.AccountByAidLocked(aid)
	if acc == nil {
		return 0, 0, false
	}
	return acc.Gems, acc.Cash, true
}

// HandleWelcomeGift serves POST create/complete (tutorialDone, ResTutorialDoneWithCachePolicy
// @0x1a5e918: status asBool, rewardCoin asInt only when status is true). The client retries
// forever on 404 and shows a network-error dialog + retry on status:false, so this always
// answers status:true. The 300 Gems are credited once per account; repeats and an unknown
// requester get rewardCoin 0. The request directly follows create/avatar, so without an
// AV_AUTH match the newest account is the requester.
func HandleWelcomeGift(w http.ResponseWriter, r *http.Request) {
	const noGift = `{"result":{"status":true,"rewardCoin":0}}`
	store.AccountsMu.Lock()
	acc := store.Accounts[httpx.CookieValue(r, "AV_AUTH")]
	if acc == nil {
		acc = store.LatestAcc
	}
	if acc == nil || acc.Welcomed {
		store.AccountsMu.Unlock()
		httpx.WriteJSON(w, http.StatusOK, noGift)
		return
	}
	prev := *acc
	lines, err := store.SpendLocked(acc, store.LedgerDeltas{Gems: ActiveRewards().WelcomeGems}, "welcome gift")
	acc.Welcomed = true
	if err == nil {
		err = store.SaveAccountsLocked()
	}
	if err != nil {
		*acc = prev
		store.AccountsMu.Unlock()
		httpx.WriteJSON(w, http.StatusInternalServerError, `{"errorCode":"500"}`)
		return
	}
	store.FlushLedgerLocked(lines)
	store.AccountsMu.Unlock()
	httpx.WriteJSON(w, http.StatusOK, fmt.Sprintf(`{"result":{"status":true,"rewardCoin":%d}}`, ActiveRewards().WelcomeGems))
}

// ResFaceShopOwnVoucherCount @0x1b2c6a4 reads result.voucherCount via asInt.
func handleVoucherCount(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpx.ServeNotFound(w)
		return
	}
	store.AccountsMu.Lock()
	acc := store.Accounts[httpx.CookieValue(r, "AV_AUTH")]
	var n int64
	if acc != nil {
		n = acc.FaceTickets
	}
	store.AccountsMu.Unlock()
	if acc == nil {
		httpx.WriteJSON(w, http.StatusNotFound, httpx.UnknownSessionBody)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, fmt.Sprintf(`{"result":{"voucherCount":%d}}`, n))
}

// ResFaceShopProductVoucherList @0x1b506fc: array of rows; productCode,productName,
// productImagePath,voucherCode,voucherUsePlace,paymentType,sellType,cpId,discountStart,
// discountEnd asString; voucherGiveCount,baseDiscountPercentage,standardPrice,finalPrice
// asInt. Rows with empty productCode or cpId are dropped by the client.
func handleVoucherProductList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpx.ServeNotFound(w)
		return
	}
	products := ActivePrices().FaceTickets
	rows := make([]map[string]any, 0, len(products))
	for _, p := range products {
		rows = append(rows, map[string]any{
			"productCode": p.Code, "productName": p.Name, "productImagePath": "",
			"voucherCode": "FACESHOP_TICKET", "voucherUsePlace": "FACESHOP",
			"voucherGiveCount": p.Tickets, "baseDiscountPercentage": int((p.Standard - p.Price) * 100 / p.Standard),
			"paymentType": "GEM", "standardPrice": p.Standard, "finalPrice": p.Price,
			"sellType": "NORMAL", "cpId": "cherry", "discountStart": "", "discountEnd": "",
		})
	}
	body, _ := json.Marshal(map[string]any{"result": rows})
	httpx.WriteJSON(w, http.StatusOK, string(body))
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
		httpx.ServeNotFound(w)
		return
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, httpx.MaxJSONBody+1))
	if err != nil || len(raw) > httpx.MaxJSONBody {
		httpx.WriteJSON(w, http.StatusBadRequest, httpx.BadRequestBody)
		return
	}
	var req struct {
		CoinUnit    string          `json:"coinUnit"`
		ProductCode string          `json:"productCode"`
		Price       json.RawMessage `json:"price"`
	}
	if _, err := httpx.UnmarshalNativeJSON(raw, &req); err != nil {
		httpx.WriteJSON(w, http.StatusBadRequest, httpx.BadRequestBody)
		return
	}
	price, priceOK := lenientInt(req.Price)
	idx := -1
	for i, p := range ActivePrices().FaceTickets {
		if p.Code == req.ProductCode {
			idx = i
		}
	}
	if idx < 0 || !priceOK || req.CoinUnit != "GEM" || price != ActivePrices().FaceTickets[idx].Price {
		httpx.WriteJSON(w, http.StatusBadRequest, errBadProductBody)
		return
	}
	p := ActivePrices().FaceTickets[idx]
	store.AccountsMu.Lock()
	defer store.AccountsMu.Unlock()
	acc := store.Accounts[httpx.CookieValue(r, "AV_AUTH")]
	if acc == nil {
		httpx.WriteJSON(w, http.StatusNotFound, httpx.UnknownSessionBody)
		return
	}
	err = store.ApplyDeltasLocked(acc, store.LedgerDeltas{Gems: -p.Price, FaceTickets: int64(p.Tickets)}, "voucher purchase "+p.Code)
	if be, ok := err.(*store.BalanceError); ok && be != nil {
		httpx.WriteJSON(w, http.StatusBadRequest, errNotEnoughBody)
		return
	}
	if err != nil {
		httpx.WriteJSON(w, http.StatusInternalServerError, `{"errorCode":"500"}`)
		return
	}
	body, _ := json.Marshal(map[string]any{"result": map[string]any{
		"avatarId": acc.Aid, "productCode": p.Code, "paymentType": "GEM", "balance": acc.Gems,
	}})
	httpx.WriteJSON(w, http.StatusOK, string(body))
}

// ---- staff grant tool ----

const adminAddr = "127.0.0.1:8099"

// NewAdminMux serves the staff tool; it is bound to loopback only and never mounted on
// the public mux. Example (deltas may be negative; balances never go below 0):
//
//	curl -X POST http://127.0.0.1:8099/admin/grant -d "{\"aid\":\"1002\",\"gems\":500,\"cash\":10,\"faceTickets\":3,\"reason\":\"test\"}"
//
// smiles, bossCoins (Square boss fishing, spent at the exchange office), ridePoints (racing store points,
// spent at the Ride Point shop), raceTickets (today's race tickets back, up to the daily maximum) and materials (Garden Bag item id -> count delta, gathering drops or
// Atelier goods) work the same way:
//
//	curl -X POST http://127.0.0.1:8099/admin/grant -d "{\"aid\":\"1001\",\"smiles\":50,\"materials\":{\"110000\":6}}"
func NewAdminMux() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/admin/grant", handleAdminGrant)
	mux.HandleFunc("/admin/mail", handleAdminMail)
	mux.HandleFunc("/admin/attend", handleAdminAttend)
	mux.HandleFunc("/admin/season", handleAdminSeason)
	mux.HandleFunc("/admin/garden", handleAdminGarden)
	return mux
}

func handleAdminGrant(w http.ResponseWriter, r *http.Request) {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if ip := net.ParseIP(host); err != nil || ip == nil || !ip.IsLoopback() || r.Method != http.MethodPost {
		httpx.ServeNotFound(w)
		return
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, 4096))
	var req struct {
		Aid         string          `json:"aid"`
		Gems        int64           `json:"gems"`
		Cash        int64           `json:"cash"`
		FaceTickets int64           `json:"faceTickets"`
		Smiles      int64           `json:"smiles"`
		BossCoins   int64           `json:"bossCoins"`
		RidePoints  int64           `json:"ridePoints"`
		RaceTickets int64           `json:"raceTickets"` // tickets back for today (a delta on the tickets left)
		Materials   map[int64]int64 `json:"materials"`   // Garden Bag item id -> count delta
		Reason      string          `json:"reason"`
	}
	if err != nil || json.Unmarshal(raw, &req) != nil || req.Aid == "" {
		httpx.WriteJSON(w, http.StatusBadRequest, httpx.BadRequestBody)
		return
	}
	for item := range req.Materials {
		if _, ok := GatherMaterialByID(item); !ok {
			httpx.WriteJSON(w, http.StatusBadRequest, httpx.BadRequestBody)
			return
		}
	}
	reason := "staff grant"
	if req.Reason != "" {
		reason += ": " + req.Reason
	}
	store.AccountsMu.Lock()
	defer store.AccountsMu.Unlock()
	acc := store.AccountByAidLocked(req.Aid)
	if acc == nil {
		httpx.WriteJSON(w, http.StatusNotFound, httpx.UnknownSessionBody)
		return
	}
	prevBag, prevCoins, prevRacing, now := acc.GardenBag, acc.BossCoins, acc.Racing, time.Now()
	acc.BossCoins = max(acc.BossCoins+req.BossCoins, 0) // saved with the deltas below
	if req.RidePoints != 0 {
		setRidePointsLocked(acc, max(ridePointsLocked(acc)+req.RidePoints, 0))
	}
	if req.RaceTickets != 0 {
		rec := raceDay(acc, AttendDayKey(now))
		rec.Used = min(max(rec.Used-req.RaceTickets, 0), ActiveRacing().MaxTickets)
		acc.Racing = &rec
	}
	for item, n := range req.Materials {
		acc.GardenBag = gardenBagChange(acc.GardenBag, item, n, now)
	}
	err = store.ApplyDeltasLocked(acc, store.LedgerDeltas{Gems: req.Gems, Cash: req.Cash, FaceTickets: req.FaceTickets, Smiles: req.Smiles}, reason)
	if err != nil {
		acc.GardenBag, acc.BossCoins, acc.Racing = prevBag, prevCoins, prevRacing
	}
	if _, ok := err.(*store.BalanceError); ok {
		httpx.WriteJSON(w, http.StatusBadRequest, errNotEnoughBody)
		return
	}
	if err != nil {
		httpx.WriteJSON(w, http.StatusInternalServerError, `{"errorCode":"500"}`)
		return
	}
	body, _ := json.Marshal(map[string]any{"aid": acc.Aid, "gems": acc.Gems, "cash": acc.Cash, "faceTickets": acc.FaceTickets, "smiles": acc.Smiles, "bossCoins": acc.BossCoins, "ridePoints": ridePointsLocked(acc), "raceTickets": raceStatus(acc, now).Tickets, "bag": acc.GardenBag})
	httpx.WriteJSON(w, http.StatusOK, string(body))
}

// ServeAdmin starts the loopback listener; a busy port is logged, not fatal.
func ServeAdmin(logger *log.Logger) {
	ln, err := net.Listen("tcp", adminAddr)
	if err != nil {
		logger.Printf("cherry: admin listener unavailable on %s: %v", adminAddr, err)
		return
	}
	logger.Printf("cherry: admin grant listening addr=%s", adminAddr)
	srv := &http.Server{Handler: NewAdminMux(), ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = srv.Serve(ln) }()
}

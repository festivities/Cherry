package economy

import (
	"cmp"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"slices"
	"strings"
	"time"

	"cherry/internal/httpx"
	"cherry/internal/itemcode"
	"cherry/internal/store"
)

// Home menu Gift Shop (goSomewhere(giftShop)): buy an item for an accepted friend. Client side (menu audit
// 2026-10-28, rows-b-spec.md section 2): ParseGiftTable 0x2adbe48 reads DRESS and ROOM only; the send reply
// ResGiftShopSendGift 0x1b2be6c takes result.errorCode into NaGiftShopSendDXTableLayer::ShowSendFailedMessage,
// which has its own boxes for 2014, 2015 and 62100 only (signup, email and friend-age gates); every other code
// shows "Please check your network." (10075). So a refused gift is answered with 200 and result.errorCode, and
// its text cannot say more than that box does.

const giftMessageMax = 500 // staff mail's bound (mailbox.go handleAdminMail)

// giftDress reports whether a Fashion Shop tab sells the code (the M and A tab sets of handleShopList).
func giftDress(code string) bool {
	for _, g := range []string{"M", "A"} {
		for _, tab := range []string{"DHAIR", "DRESS", "TOPS", "BTTM", "SHOE", "ACC"} {
			prefix, slots := fashionSlots(g + tab)
			if code[:2] == prefix && slices.Contains(slots, code[2:4]) {
				return true
			}
		}
	}
	return false
}

// giftCatalogItem is the catalog item a gift may carry now: a clothing or furniture item the shops sell on sale
// now, priced in Gems. Landscapes (Cash), pets, faces and anything unpriced are refused.
func giftCatalogItem(code string, now time.Time) (shopItem, bool) {
	it := loadCatalog().byCode[code]
	if it == nil || !onSale(code, now) {
		return shopItem{}, false
	}
	switch _, kind := classify(code); kind {
	case kindFashion:
		if !giftDress(code) {
			return shopItem{}, false
		}
	case kindInterior:
	default:
		return shopItem{}, false
	}
	if _, cash, _, ok := ItemPrice(code); !ok || cash {
		return shopItem{}, false
	}
	return *it, true
}

// giftPrice is what a gift charges now: the daily deal price when the item is on deal, else the table price (buy's rule).
func giftPrice(code string, deals dealSet) int64 {
	if d, deal := deals[code]; deal {
		return d
	}
	price, _, _, _ := ItemPrice(code)
	return price
}

// WishlistMax is the most codes one wish list holds: NaWishListDataContainer::IsFull is true at four rows, and
// CNaGiftShopLayer::OnTryRegistNewWishListItem refuses a fifth before it sends anything.
const WishlistMax = 4

// WishlistOK reports whether a code may join a wish list now: the gift rule (giftCatalogItem), a clothing or
// furniture item the shops sell now, priced in Gems. web/wishlist.go checks only the codes a save adds.
func WishlistOK(code string) bool {
	_, ok := giftCatalogItem(code, NowFunc())
	return ok
}

// WishRows are the wish-list rows the client parses (ParseWishListJsonData reads itemCode, itemType, selling and
// specialEffects). selling is WishlistOK; a code the shops no longer list keeps its row with selling false.
func WishRows(codes []string) []map[string]any {
	now := NowFunc()
	rows := make([]map[string]any, 0, len(codes))
	for _, code := range codes {
		_, selling := giftCatalogItem(code, now)
		effects := ""
		if it := loadCatalog().byCode[code]; it != nil && it.Animated {
			effects = "ANIMATION"
		}
		rows = append(rows, map[string]any{"itemCode": code, "itemType": itemTypeOf(code), "selling": selling,
			"specialEffects": effects})
	}
	return rows
}

func giftRow(it shopItem, host string, deals dealSet, now time.Time) map[string]any {
	row := priceKeys(it, deals, now)
	_, _, grade, _ := ItemPrice(it.Code)
	for k, v := range map[string]any{"name": ItemName(it.Code), "itemID": it.ID, "itemType": itemTypeOf(it.Code),
		"representImageURL": itemImageURL(it.Code, host), "grade": grade, "existInven": false,
		"sellingStart": "", "discountStart": "", "recycleReward": 0, "vipMessageType": 0} {
		row[k] = v
	}
	return row
}

// GiftShopRows is prices.json gift_shop (Cherry-made): the Gift Shop rows of each tab after the day's deals.
type GiftShopRows struct {
	Best    int `json:"best"`    // the most-wished giftable items, grade BEST
	Premium int `json:"premium"` // a daily pick of animated items, grade PREMIUM
	Daily   int `json:"daily"`   // a daily pick of the other items
}

const giftShopRowsMax = 500 // per count: keeps the reply far below what timed out (19,385 rows, 9.2 MB)

// giftShopRows is the gift_shop counts in use; an override file written before they existed gets 20/40/120.
func giftShopRows() GiftShopRows {
	if g := ActivePrices().GiftShop; g != nil {
		return *g
	}
	return GiftShopRows{Best: 20, Premium: 40, Daily: 120}
}

// Gift Shop grades: CNaGiftShopLayer::SetTableViewData 0x2289f3c fills the PREMIUM and BEST tabs with the rows whose
// grade (sShopItem+88) is exactly that word; the ALL tab shows every row [S].
const (
	giftGradeBest    = "BEST"
	giftGradePremium = "PREMIUM"
	giftGradeNormal  = "N"
)

// giftShopSelection is one category's Gift Shop: the giftable deals of the day, the most-wished items (BEST), a daily
// pick of animated items (PREMIUM) and a daily pick of the rest, each item once. The whole catalog went out until
// 2026-11-28 (19,385 clothing rows, 9.2 MB, 1 to 8 s to build): phones timed out and the shop stayed empty. The client
// has no paging for this list (ReqGiftShopItemList 0x1b97f60 sends only the category and tab).
// ponytail: recomputed per request (a sha256 per catalog item, one pass over the accounts); cache per game day if it shows.
func giftShopSelection(list []shopItem, deals dealSet, now time.Time) ([]shopItem, map[string]string) {
	counts := giftShopRows()
	wished := wishCounts()
	day := AttendDayKey(now)
	var giftable []shopItem
	for _, it := range list {
		if _, ok := giftCatalogItem(it.Code, now); ok {
			giftable = append(giftable, it)
		}
	}
	grades := map[string]string{}
	var out []shopItem
	take := func(cands []shopItem, n int, grade func(shopItem) string) {
		for _, it := range cands {
			if n == 0 {
				return
			}
			if _, dup := grades[it.Code]; dup {
				continue
			}
			grades[it.Code] = grade(it)
			out = append(out, it)
			n--
		}
	}
	byAnimation := func(it shopItem) string {
		if it.Animated {
			return giftGradePremium
		}
		return giftGradeNormal
	}
	take(slices.DeleteFunc(slices.Clone(giftable), func(it shopItem) bool { _, d := deals[it.Code]; return !d }), len(deals), byAnimation)
	best := slices.DeleteFunc(slices.Clone(giftable), func(it shopItem) bool { return wished[it.Code] == 0 })
	slices.SortStableFunc(best, func(a, b shopItem) int { return cmp.Compare(wished[b.Code], wished[a.Code]) })
	take(best, counts.Best, func(shopItem) string { return giftGradeBest })
	rank := make(map[string]uint64, len(giftable))
	for _, it := range giftable {
		rank[it.Code] = dayRank(day, it.Code)
	}
	slices.SortFunc(giftable, func(a, b shopItem) int { return cmp.Compare(rank[a.Code], rank[b.Code]) })
	take(slices.DeleteFunc(slices.Clone(giftable), func(it shopItem) bool { return !it.Animated }), counts.Premium, byAnimation)
	take(slices.DeleteFunc(slices.Clone(giftable), func(it shopItem) bool { return it.Animated }), counts.Daily, byAnimation)
	return out, grades
}

// wishCounts counts every account's wish list codes (Account.Wishes, at most four each).
func wishCounts() map[string]int {
	store.AccountsMu.Lock()
	defer store.AccountsMu.Unlock()
	counts := map[string]int{}
	for _, acc := range store.AllAccountsLocked() {
		for _, code := range acc.Wishes {
			counts[code]++
		}
	}
	return counts
}

// HandleGiftShopList serves GET /v4/shop/gift/list/<CAT>/<SPECIAL>. DRESS and ROOM list giftShopSelection; FACE,
// GARDEN and any other category get an empty list (the client never parses them). SPECIAL is ALL, PREMIUM or
// BEST (GetSpecialCategoryName 0x222c3f8); every tab gets the same rows and the client filters them by grade.
func HandleGiftShopList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpx.ServeNotFound(w)
		return
	}
	cat, _, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, "/v4/shop/gift/list/"), "/")
	c := loadCatalog()
	var list []shopItem
	switch cat {
	case "DRESS":
		list = c.fashion
	case "ROOM":
		list = c.interior
	}
	now := NowFunc()
	deals := dealsOn(now)
	items, grades := giftShopSelection(list, deals, now)
	rows := make([]map[string]any, 0, len(items))
	for _, it := range items {
		row := giftRow(it, r.Host, deals, now)
		row["grade"] = grades[it.Code]
		rows = append(rows, row)
	}
	httpx.WriteJSON(w, http.StatusOK, itemsBody(rows))
}

// HandleGiftItemDetail serves GET /v4/shop/gift/item/detail/<code> (singleItemInfoURL 0x1adcd2c): the request behind
// a wish list's gift button (the Diary wish list cell, msg 10624, and the Gift Shop wish list popup, button 11). Its
// reply (ResSingleItemInfo 0x1c2db30 [S]: result.itemId, itemCode, representImagePath, price, itemName,
// sellingIconType, soldout, discount, selling, display, specialEffects, paymentType, recycleReward, discountedPrice,
// vipMessageType; top-level localeItems optional) posts 10767, which opens the gift send popup for the wish list's
// owner. It was 404 until 2026-11-28: the error path (10768) only hides the indicator, so the tap "went nowhere". An
// item that cannot be gifted (giftCatalogItem) stays 404: its wish row already says selling false.
func HandleGiftItemDetail(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpx.ServeNotFound(w)
		return
	}
	code := strings.Trim(strings.TrimPrefix(r.URL.Path, "/v4/shop/gift/item/detail/"), "/")
	now := NowFunc()
	it, ok := giftCatalogItem(code, now)
	if !ok {
		httpx.ServeNotFound(w)
		return
	}
	row := priceKeys(it, dealsOn(now), now)
	for k, v := range map[string]any{"itemId": it.ID, "itemName": ItemName(it.Code), "representImagePath": "",
		"recycleReward": 0, "vipMessageType": 0} {
		row[k] = v
	}
	httpx.WriteObj(w, map[string]any{"result": row})
}

type giftSendReq struct {
	ReceiveUser struct {
		Aid json.RawMessage `json:"aid"`
	} `json:"receiveUser"`
	CoinUnit    string `json:"coinUnit"`
	GiftMessage string `json:"giftMessage"`
	Items       []struct {
		ItemCode string          `json:"itemCode"`
		Price    json.RawMessage `json:"price"`
	} `json:"items"`
}

// giftError answers 200 with result.errorCode, the reply the send path reads (11243). A 4xx would post 11244, which
// shows nothing and leaves the system indicator up.
func giftError(w http.ResponseWriter, r *http.Request, code, msg string) {
	log.Printf("cherry: gift reject %s errorCode=%s (%s)", r.URL.Path, code, msg) // codes and text only
	httpx.WriteJSON(w, http.StatusOK, fmt.Sprintf(`{"result":{"errorCode":%q,"errorMessage":"cherry: %s"}}`, code, msg))
}

// HandleGiftSend serves POST /v4/gift/send. Gems only: the price must be the item's current price, the receiver an
// accepted friend with an account. The item goes to the receiver's mailbox as an item mail carrying the sender's
// name and gift message, flagged Gift: on claim an item the receiver already owns pays no sell-back Gems, and a new
// one is granted marked as a gift, so it sells for nothing later (grantItemLocked, deals.go soldPrice).
func HandleGiftSend(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpx.ServeNotFound(w)
		return
	}
	raw, err := httpx.ReadNativeBody(r)
	var req giftSendReq
	if err == nil {
		_, err = httpx.UnmarshalNativeJSON(raw, &req)
	}
	if err != nil || len(req.Items) == 0 || len(req.Items) > maxShopBatch {
		httpx.WriteJSON(w, http.StatusBadRequest, httpx.BadRequestBody)
		return
	}
	if strings.EqualFold(req.CoinUnit, "CASH") {
		giftError(w, r, errUnknown, "gifts are Gems only")
		return
	}
	if len(req.GiftMessage) > giftMessageMax {
		giftError(w, r, errUnknown, "message too long")
		return
	}
	aid := strings.Trim(strings.TrimSpace(string(req.ReceiveUser.Aid)), `"`)
	now := NowFunc()
	deals := dealsOn(now)
	codes := make([]string, 0, len(req.Items))
	var total int64
	for _, it := range req.Items {
		item, ok := giftCatalogItem(it.ItemCode, now)
		if !ok {
			giftError(w, r, errUnknown, "item cannot be gifted")
			return
		}
		want := giftPrice(item.Code, deals)
		if cp, ok := lenientInt(it.Price); !ok || cp != want {
			giftError(w, r, errUpdated, "shop updated")
			return
		}
		total += want
		codes = append(codes, item.Code)
	}

	store.AccountsMu.Lock()
	defer store.AccountsMu.Unlock()
	acc := store.SessionAccountLocked(r)
	if acc == nil {
		httpx.WriteJSON(w, http.StatusNotFound, httpx.UnknownSessionBody)
		return
	}
	recv := store.AccountByAidLocked(aid) // the synthetic friend (store.FriendAID) has no account: refused here
	if recv == nil || recv.Aid == acc.Aid {
		giftError(w, r, errUnknown, "no such friend")
		return
	}
	store.SocialMu.Lock()
	friends := store.FriendshipIndex(acc.Aid, recv.Aid, "accepted") >= 0
	store.SocialMu.Unlock()
	if !friends {
		giftError(w, r, errUnknown, "not a friend")
		return
	}
	if recv.Gender == "ANIMAL" { // an animal friend cannot wear hair or a human face: refused before any Gem moves
		for _, code := range codes {
			if itemcode.HumanOnly(code) {
				giftError(w, r, errUnknown, "receiver cannot wear item")
				return
			}
		}
	}

	prevSender, prevRecv := *acc, *recv
	lines, err := store.SpendLocked(acc, store.LedgerDeltas{Gems: -total}, "gift send to "+recv.Aid+" "+strings.Join(codes, ","))
	if err != nil {
		giftError(w, r, errNoMoney, "insufficient balance")
		return
	}
	for _, code := range codes {
		row := store.MailRow{Type: store.MailTypeItem, Item: code, Message: MaskBannedWords(req.GiftMessage), Sender: acc.Name, SenderAid: acc.Aid, Gift: true}
		if !addMailLocked(recv, row, ActiveRewards().Mail.DefaultDays, now) {
			*acc, *recv = prevSender, prevRecv
			giftError(w, r, errUnknown, "receiver mailbox is full")
			return
		}
	}
	if err := store.SaveLocked(lines, acc, recv); err != nil {
		*acc, *recv = prevSender, prevRecv
		httpx.WriteJSON(w, http.StatusInternalServerError, httpx.SaveFailedBody)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, fmt.Sprintf(`{"result":{"balance":%d,"balanceUnit":"GEM"}}`, acc.Gems))
}

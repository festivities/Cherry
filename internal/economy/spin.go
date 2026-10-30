package economy

import (
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"net/http"
	"slices"
	"strings"
	"time"

	"cherry/data"
	"cherry/internal/httpx"
	"cherry/internal/opdata"
	"cherry/internal/store"
)

// Lucky Spin and Jackpot Spin (spin-spec.md; the coordinator's brief overrides its section 17). Lucky is one Gem
// tier at 100 Gems with one free play a game day. Jackpot is tier 1 at 100 Gems and tier 2 at 10 earned Cash, with
// no free play. The server draws the slot (weighted, data/spin.json), pays the Gems or grants the item in the same save
// as the price, and replies with the slot the wheel stops on (historyList[0].showPriority). Names, prices, odds and
// prizes are data; the pictures are spinart.go.

// CherrySpinFirst and CherrySpinLast bound the spin ids and reward numbers Cherry makes (spin.json).
const (
	CherrySpinFirst int64 = 1920000000
	CherrySpinLast  int64 = 1920000999
)

// Spin is data/spin.json: the two spins. Strict JSON: unknown keys are an error.
//
//	origin, note  Cherry-made (every value in the file)
//	lucky         the Lucky Spin (one tier, GEM); free_daily gives one free play a game day (06:00 JST, AttendDayKey)
//	jackpot       the Jackpot Spin: tiers[0] is tier 1 (GEM), tiers[1] tier 2 (CASH, the client's "High" tier); no free play
type Spin struct {
	opdata.Provenance
	Lucky   SpinGame `json:"lucky"`
	Jackpot SpinGame `json:"jackpot"`
}

// SpinGame is one spin: its id (the spinSeq the client sends back), title, free play rule, tiers and locale texts.
type SpinGame struct {
	ID        int64        `json:"id"`
	Title     string       `json:"title"`
	FreeDaily bool         `json:"free_daily"`
	Tiers     []SpinTier   `json:"tiers"`
	Locales   []SpinLocale `json:"locales"`
}

// SpinTier is one price: its currency ("GEM" or "CASH"), its price per play and the eight wheel slots in order.
type SpinTier struct {
	Currency string     `json:"currency"`
	Price    int64      `json:"price"`
	Slots    []SpinSlot `json:"slots"`
}

// SpinSlot is one wheel slot: a Gem prize (gems above 0) or an item (item code), drawn with weight. Slot n is
// showPriority n.
type SpinSlot struct {
	Gems   int64  `json:"gems,omitempty"`
	Item   string `json:"item,omitempty"`
	Weight int64  `json:"weight"`
}

// SpinLocale is the text the client shows in one language (en is required; ja is sent too).
type SpinLocale struct {
	Language    string `json:"language"`
	Title       string `json:"title"`
	Description string `json:"description"`
}

var spinFile = opdata.New("spin.json", data.SpinJSON, checkSpin, verifySpin)

// ActiveSpin is the spin file in use.
func ActiveSpin() *Spin { return spinFile.Get() }

// spinNow is the clock the free play and the lists read. Tests replace it.
var spinNow = time.Now

// spinRoll returns a uniform value in [0, n). Tests replace it with a scripted source.
var spinRoll = func(n int64) int64 { return rand.Int64N(n) }

func checkSpin(s *Spin) error {
	if err := s.CheckOrigin("cherry"); err != nil {
		return err
	}
	if err := checkSpinGame("lucky", &s.Lucky, 1); err != nil {
		return err
	}
	if err := checkSpinGame("jackpot", &s.Jackpot, 2); err != nil {
		return err
	}
	switch {
	case s.Lucky.ID == s.Jackpot.ID:
		return errors.New("lucky and jackpot must have different ids")
	case s.Jackpot.FreeDaily:
		return errors.New("jackpot: free_daily must be false (no free Jackpot play)")
	}
	return nil
}

func checkSpinGame(name string, g *SpinGame, tiers int) error {
	switch {
	case !inCherryBlock(g.ID, CherrySpinFirst, CherrySpinLast):
		return fmt.Errorf("%s: id %d is outside the Cherry block %d-%d", name, g.ID, CherrySpinFirst, CherrySpinLast)
	case strings.TrimSpace(g.Title) == "":
		return fmt.Errorf("%s: title must not be empty", name)
	case len(g.Tiers) != tiers:
		return fmt.Errorf("%s: %d tiers, want %d", name, len(g.Tiers), tiers)
	case !slices.ContainsFunc(g.Locales, func(l SpinLocale) bool { return l.Language == "en" }):
		return fmt.Errorf("%s: locales must include en", name)
	}
	for _, l := range g.Locales {
		if strings.TrimSpace(l.Title) == "" {
			return fmt.Errorf("%s: the %s title must not be empty", name, l.Language)
		}
	}
	for i, t := range g.Tiers {
		want := "GEM"
		if i == 1 {
			want = "CASH"
		}
		if t.Currency != want {
			return fmt.Errorf("%s tier %d: currency must be %s", name, i+1, want)
		}
		if t.Price < 1 {
			return fmt.Errorf("%s tier %d: price must be at least 1", name, i+1)
		}
		if len(t.Slots) != 8 {
			return fmt.Errorf("%s tier %d: %d slots, want 8", name, i+1, len(t.Slots))
		}
		for j, s := range t.Slots {
			switch {
			case (s.Gems > 0) == (s.Item != ""):
				return fmt.Errorf("%s tier %d slot %d: give gems above 0 or an item code, not both or neither", name, i+1, j+1)
			case s.Gems > store.MaxBalance:
				return fmt.Errorf("%s tier %d slot %d: gems must be at most %d", name, i+1, j+1, store.MaxBalance)
			case s.Weight < 1:
				return fmt.Errorf("%s tier %d slot %d: weight must be at least 1", name, i+1, j+1)
			}
			if s.Item != "" {
				if _, kind := classify(s.Item); kind != kindFashion && kind != kindInterior {
					return fmt.Errorf("%s tier %d slot %d: %q is not a clothing or room item code", name, i+1, j+1, s.Item)
				}
			}
		}
		// An animal's draw needs weight somewhere (spinSlotsFor zeroes the human-only items); a Gem slot has no item.
		if !slices.ContainsFunc(t.Slots, func(s SpinSlot) bool { return !spinHumanOnly(s.Item) }) {
			return fmt.Errorf("%s tier %d: no slot an animal can win (every slot is a hair or face item)", name, i+1)
		}
	}
	return nil
}

// verifySpin runs at load (not for the embedded default alone). Every item must be sellable with art in the archive, and
// the expected Gem payout of each Gem-priced tier must stay below its price (spinPayout, sell-backs included).
func verifySpin(s *Spin) error {
	for _, g := range []struct {
		name string
		game *SpinGame
	}{{"lucky", &s.Lucky}, {"jackpot", &s.Jackpot}} {
		for i, t := range g.game.Tiers {
			for j, slot := range t.Slots {
				if slot.Item != "" && !itemAvailable(slot.Item) {
					return fmt.Errorf("%s tier %d slot %d: item %s has no art or price in the archive", g.name, i+1, j+1, slot.Item)
				}
			}
			if t.Currency != "GEM" {
				continue
			}
			sum, total := spinPayout(t)
			if sum >= t.Price*total {
				return fmt.Errorf("%s tier %d: expected Gem payout %.2f is not below the %d-Gem price", g.name, i+1, float64(sum)/float64(total), t.Price)
			}
		}
	}
	return nil
}

// spinPayout is the expected Gems one play of a tier pays: a Gem slot its amount, an item slot its sell-back (a duplicate
// pays it, and a new item is valued the same). It returns the weighted sum and the total weight, so the expectation is
// exactly sum/total.
func spinPayout(t SpinTier) (sum, total int64) {
	for _, s := range t.Slots {
		v := s.Gems
		if s.Item != "" {
			v = sellBackGems(s.Item)
		}
		sum += v * s.Weight
		total += s.Weight
	}
	return sum, total
}

// spinDraw returns the index of the slot the weighted draw lands on.
func spinDraw(slots []SpinSlot) int {
	var total int64
	for _, s := range slots {
		total += s.Weight
	}
	r := spinRoll(total)
	for i, s := range slots {
		if r < s.Weight {
			return i
		}
		r -= s.Weight
	}
	return len(slots) - 1
}

// spinHumanOnly reports whether an animal avatar cannot wear code: a hair item or a human face (CU HA, HE, EB, EY, NO,
// MO). isHumanFace is the Face Shop's human face check; the species rule itself is faceshop.go (CA for animals only).
func spinHumanOnly(code string) bool {
	return isHumanFace(code) || len(code) == 9 && strings.HasPrefix(code, "CUHA")
}

// spinSlotsFor is the wheel as the account can win it. An animal's copy gives each human-only item weight 0, so spinDraw
// never lands on one; the indexes stay the wheel's (showPriority). A human gets the wheel itself.
func spinSlotsFor(slots []SpinSlot, animal bool) []SpinSlot {
	if !animal {
		return slots
	}
	out := slices.Clone(slots)
	for i := range out {
		if spinHumanOnly(out[i].Item) {
			out[i].Weight = 0
		}
	}
	return out
}

// spinKey is the reward key of a tier: Lucky's tier is 1, the Jackpot's tiers 2 and 3.
func spinKey(jackpot bool, tier int) int64 {
	if !jackpot {
		return 1
	}
	return 2 + int64(tier)
}

// spinRewardNo numbers a slot's reward: CherrySpinFirst + 100*key + slot (slot 1 to 8; slot 0 names the group).
func spinRewardNo(key int64, slot int) int64 { return CherrySpinFirst + 100*key + int64(slot) }

// spinPicPrefix is where the client's relative picture paths point (served by HandleSpinImage).
const spinPicPrefix = "cherry/spin/"

// spinResources are the resourceList pictures in the client's slots (spin-spec 12.4): six for Lucky; seven for the Jackpot.
// For the Jackpot the wheel index follows the tier's switch mode (spin-spec 11.1: mode 0 loads [0], mode 1 loads [6]), so
// [0] is the GEM wheel (tier 1) and [6] the CASH wheel (tier 2). [I] 12.4 labels them the other way round; check on device.
func spinResources(jackpot bool) []spinRes {
	sp := ActiveSpin()
	names := []string{spinWheelName("lucky_wheel", sp.Lucky.Tiers[0]), "board.png", "deco.png", "list_bg.png", "detail_cover.png", "title_clear.png"}
	if jackpot {
		names = []string{spinWheelName("jackpot_gem_wheel", sp.Jackpot.Tiers[0]), "board.png", "deco.png", "list_bg.png", "detail_cover.png", "title_clear.png",
			spinWheelName("jackpot_cash_wheel", sp.Jackpot.Tiers[1])}
	}
	out := make([]spinRes, len(names))
	for i, n := range names {
		out[i] = spinRes{ResourceType: i, Language: "common", ImagePath: spinPicPrefix + n}
	}
	return out
}

// spinPrize is what a slot pays in the client's reward terms: rewardType 3 (Gems) or 1 (an item), its grade and picture.
type spinPrize struct {
	rewardType int
	grade      string
	picture    string
	value      int64
	item, name string
}

func spinPrizeOf(s SpinSlot) spinPrize {
	if s.Item != "" {
		return spinPrize{rewardType: 1, grade: "R", picture: spinPicPrefix + "item/" + s.Item + ".png", value: 1, item: s.Item, name: ItemName(s.Item)}
	}
	return spinPrize{rewardType: 3, grade: "N3", picture: spinPicPrefix + "gem.png", value: s.Gems}
}

// spinGroups is a tier's rewardList: one group per reward type (Gems, items), each slot a child whose showPriority is its
// slot. A group's probability is the sum of its weights.
func spinGroups(key int64, t SpinTier) []spinGroup {
	var gems, items spinGroup
	for i, s := range t.Slots {
		slot := i + 1
		p := spinPrizeOf(s)
		g := &gems
		if p.rewardType == 1 {
			g = &items
		}
		if len(g.DetailList) == 0 {
			*g = spinGroup{RewardNo: spinRewardNo(key, 0), RewardType: p.rewardType, RewardGradeType: p.grade, ShowPriority: slot, DetailList: []spinChild{}}
		}
		g.DetailList = append(g.DetailList, spinChild{RewardNo: spinRewardNo(key, slot), GetImagePath: p.picture,
			RewardValue: p.value, ItemCd: p.item, ShowPriority: slot, Probability: s.Weight})
		g.Probability += s.Weight
	}
	var out []spinGroup
	for _, g := range []spinGroup{gems, items} {
		if len(g.DetailList) > 0 {
			out = append(out, g)
		}
	}
	if out == nil {
		out = []spinGroup{}
	}
	return out
}

// ---- reply shapes (spin-spec section 4; every list is a JSON array, every number an int) ----

type spinEnvelope[T any] struct {
	Result T `json:"result"`
}

type spinRes struct {
	ResourceType int    `json:"resourceType"`
	Language     string `json:"language"`
	ImagePath    string `json:"imagePath"`
}

type spinLocaleRow struct {
	Language    string `json:"language"`
	Title       string `json:"title"`
	Color       string `json:"color"`
	Description string `json:"description"`
}

type spinFree struct {
	FreeChangeType int `json:"freeChangeType"`
}

type spinCoupon struct {
	SetNo     int64  `json:"setNo"`
	PlayCount int64  `json:"playCount"`
	PayItemCd string `json:"payItemCd"`
	Price     int64  `json:"price"`
}

type spinGroup struct {
	RewardNo        int64       `json:"rewardNo"`
	RewardType      int         `json:"rewardType"`
	RewardGradeType string      `json:"rewardGradeType"`
	Probability     int64       `json:"probability"`
	ShowPriority    int         `json:"showPriority"`
	DetailList      []spinChild `json:"detailList"`
}

type spinChild struct {
	RewardNo       int64  `json:"rewardNo"`
	GetImagePath   string `json:"getImagePath"`
	RewardDetailNo int    `json:"rewardDetailNo"`
	RewardValue    int64  `json:"rewardValue"`
	ItemCd         string `json:"itemCd"`
	SpecialEffects string `json:"specialEffects"`
	ShowPriority   int    `json:"showPriority"`
	Probability    int64  `json:"probability"`
}

// spinListBase is the row both lists share (sSpinData, spin-spec 4.1; the Jackpot row adds 4.2's High tier).
type spinListBase struct {
	SpinSeq            int64           `json:"spinSeq"`
	SpinNo             int             `json:"spinNo"`
	Title              string          `json:"title"`
	PayItemCd          string          `json:"payItemCd"`
	MapItemCd          string          `json:"mapItemCd"`
	AvatarType         string          `json:"avatarType"`
	PaymentType        string          `json:"paymentType"`
	ThumbnailImagePath string          `json:"thumbnailImagePath"`
	Price              int64           `json:"price"`
	DiscountPrice      int64           `json:"discountPrice"`
	DiscountRate       int64           `json:"discountRate"`
	DiscountedPrice    int64           `json:"discountedPrice"`
	FreeChanceList     []spinFree      `json:"freeChanceList"`
	SetPurchaseList    []any           `json:"setPurchaseList"`
	StartYmdt          string          `json:"startYmdt"`
	EndYmdt            string          `json:"endYmdt"`
	CouponList         []spinCoupon    `json:"couponList"`
	ResourceList       []spinRes       `json:"resourceList"`
	LocaleList         []spinLocaleRow `json:"localeList"`
}

type spinLuckyList struct {
	spinListBase
	RewardList []spinGroup `json:"rewardList"`
}

type spinJackpotList struct {
	spinListBase
	RewardList      []spinGroup `json:"rewardList"`
	PaymentTypeHigh string      `json:"paymentTypeHigh"`
	PriceHigh       int64       `json:"priceHigh"`
	PayItemCdHigh   string      `json:"payItemCdHigh"`
	RewardListHigh  []spinGroup `json:"rewardListHigh"`
}

// spinListBaseOf builds the shared row from a game's first tier. The coupon is the client's charged price (couponList[0]).
func spinListBaseOf(g SpinGame, jackpot bool, free int) spinListBase {
	t := g.Tiers[0]
	setNo := int64(1)
	if jackpot {
		setNo = 0
	}
	locales := make([]spinLocaleRow, len(g.Locales))
	for i, l := range g.Locales {
		locales[i] = spinLocaleRow{Language: l.Language, Title: l.Title, Description: l.Description}
	}
	res := spinResources(jackpot)
	return spinListBase{SpinSeq: g.ID, SpinNo: 1, Title: g.Title, PaymentType: t.Currency, ThumbnailImagePath: res[0].ImagePath,
		Price: t.Price, FreeChanceList: []spinFree{{FreeChangeType: free}}, SetPurchaseList: []any{}, StartYmdt: "0", EndYmdt: "0",
		CouponList: []spinCoupon{{SetNo: setNo, PlayCount: 1, Price: t.Price}}, ResourceList: res, LocaleList: locales}
}

type spinCoin struct {
	TotalCoinBalance int64 `json:"totalCoinBalance"`
}

// spinHistory is the awarded slot (sSpinChildItemInfo, spin-spec 4.9).
type spinHistory struct {
	RewardNo        int64  `json:"rewardNo"`
	GetImagePath    string `json:"getImagePath"`
	RewardType      int    `json:"rewardType"`
	RewardGradeType string `json:"rewardGradeType"`
	RewardDetailNo  int    `json:"rewardDetailNo"`
	RewardValue     int64  `json:"rewardValue"`
	ItemCode        string `json:"itemCode"`
	ItemName        string `json:"itemName"`
	SpecialEffects  string `json:"specialEffects"`
	ShowPriority    int    `json:"showPriority"`
	Probability     int64  `json:"probability"`
}

type spinPlayReply struct {
	GemBalance  spinCoin      `json:"gemBalance"`
	CashBalance spinCoin      `json:"cashBalance"`
	HistoryList []spinHistory `json:"historyList"`
}

// ---- routes ----

// spinFreeChance is freeChangeType for the requester: 1 while today's free Lucky Spin is unused, else 0 (also for no
// account and for a game without a free play).
func spinFreeChance(r *http.Request, g SpinGame) int {
	if !g.FreeDaily {
		return 0
	}
	store.AccountsMu.Lock()
	defer store.AccountsMu.Unlock()
	acc := store.Accounts[httpx.CookieValue(r, "AV_AUTH")]
	if acc == nil || acc.LuckySpinDay == AttendDayKey(spinNow()) {
		return 0
	}
	return 1
}

// handleSpinList serves luckyspin/list/inprogress and luckyspin/jackpot/list/inprogress: always one spin in progress, no end.
func handleSpinList(jackpot bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			httpx.ServeNotFound(w)
			return
		}
		sp := ActiveSpin()
		if !jackpot {
			g := sp.Lucky
			row := spinLuckyList{spinListBase: spinListBaseOf(g, false, spinFreeChance(r, g)), RewardList: spinGroups(spinKey(false, 0), g.Tiers[0])}
			httpx.WriteObj(w, spinEnvelope[[]spinLuckyList]{Result: []spinLuckyList{row}})
			return
		}
		g := sp.Jackpot
		row := spinJackpotList{spinListBase: spinListBaseOf(g, true, 0), RewardList: spinGroups(spinKey(true, 0), g.Tiers[0]),
			PaymentTypeHigh: g.Tiers[1].Currency, PriceHigh: g.Tiers[1].Price, RewardListHigh: spinGroups(spinKey(true, 1), g.Tiers[1])}
		httpx.WriteObj(w, spinEnvelope[[]spinJackpotList]{Result: []spinJackpotList{row}})
	}
}

type spinPlayReq struct {
	SpinSeq     json.RawMessage `json:"spinSeq"`
	CoinUnit    string          `json:"coinUnit"`
	Price       json.RawMessage `json:"price"`
	FreeChance  json.RawMessage `json:"freeChance"`
	SpinType    string          `json:"spinType"`
	IsHighPrice bool            `json:"isHighPrice"`
}

// handleSpinPlay serves luckyspin/purchasenplay for both spins; spinType JACKPOT selects the Jackpot and isHighPrice its
// tier 2. The spin id, coin unit and price must match the data (else 61003). A free play (freeChance above 0) costs nothing
// only while the account's free Lucky Spin of today is unused; any other free request is refused, never charged. Gems or
// Cash are spent, and the prize paid or granted, in one save before the reply. An animal account never draws a hair or
// human face item (spinSlotsFor).
func handleSpinPlay(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpx.ServeNotFound(w)
		return
	}
	raw, err := httpx.ReadNativeBody(r)
	var req spinPlayReq
	if err == nil {
		_, err = httpx.UnmarshalNativeJSON(raw, &req)
	}
	seq, okSeq := lenientInt(req.SpinSeq)
	price, okPrice := lenientInt(req.Price)
	free, okFree := lenientInt(req.FreeChance)
	if err != nil || !okSeq || !okPrice || !okFree {
		httpx.WriteJSON(w, http.StatusBadRequest, httpx.BadRequestBody)
		return
	}
	jackpot := req.SpinType == "JACKPOT"
	sp := ActiveSpin()
	game, tier := &sp.Lucky, 0
	if jackpot {
		game = &sp.Jackpot
		if req.IsHighPrice {
			tier = 1
		}
	}
	t := game.Tiers[tier]
	switch {
	case seq != game.ID || req.CoinUnit != t.Currency || price != t.Price:
		shopError(w, r, raw, errUpdated, "shop updated")
		return
	case free > 0 && !game.FreeDaily:
		shopError(w, r, raw, errUpdated, "no free play for this spin")
		return
	}

	store.AccountsMu.Lock()
	defer store.AccountsMu.Unlock()
	acc := store.Accounts[httpx.CookieValue(r, "AV_AUTH")]
	if acc == nil {
		httpx.WriteJSON(w, http.StatusNotFound, httpx.UnknownSessionBody)
		return
	}
	day := AttendDayKey(spinNow())
	useFree := free > 0 && acc.LuckySpinDay != day
	if free > 0 && !useFree {
		shopError(w, r, raw, errUpdated, "free play used today")
		return
	}
	var gemCost, cashCost int64
	if !useFree {
		if t.Currency == "CASH" {
			if acc.Cash < price {
				shopError(w, r, raw, errNoMoney, "insufficient balance")
				return
			}
			cashCost = price
		} else {
			if acc.Gems < price {
				shopError(w, r, raw, errNoMoney, "insufficient balance")
				return
			}
			gemCost = price
		}
	}

	idx := spinDraw(spinSlotsFor(t.Slots, acc.Gender == "ANIMAL"))
	slot := t.Slots[idx]
	previous := *acc
	reason := "spin:lucky"
	if jackpot {
		reason = "spin:jackpot"
	}
	// The price and the prize are separate ledger lines, so a 100-Gem play that wins 100 Gems still leaves a trace. An item
	// is granted through grantItemLocked: a duplicate pays its sell-back in its own line, a new item pays nothing.
	lines, err := store.SpendLocked(acc, store.LedgerDeltas{Gems: -gemCost, Cash: -cashCost}, reason)
	if err != nil {
		*acc = previous
		shopError(w, r, raw, errNoMoney, "insufficient balance")
		return
	}
	if slot.Item != "" {
		grantLines, err := grantItemLocked(acc, slot.Item, reason)
		if err != nil {
			*acc = previous
			httpx.WriteJSON(w, http.StatusInternalServerError, httpx.SaveFailedBody)
			return
		}
		lines = append(lines, grantLines...)
	} else {
		won, err := store.SpendLocked(acc, store.LedgerDeltas{Gems: slot.Gems}, reason+" prize")
		if err != nil {
			*acc = previous
			shopError(w, r, raw, errTooMany, "balance limit")
			return
		}
		lines = append(lines, won...)
	}
	if useFree {
		acc.LuckySpinDay = day
	}
	if err := store.SaveAccountsLocked(); err != nil {
		*acc = previous
		httpx.WriteJSON(w, http.StatusInternalServerError, httpx.SaveFailedBody)
		return
	}
	store.FlushLedgerLocked(lines)

	p := spinPrizeOf(slot)
	hist := spinHistory{RewardNo: spinRewardNo(spinKey(jackpot, tier), idx+1), GetImagePath: p.picture, RewardType: p.rewardType,
		RewardGradeType: p.grade, RewardValue: p.value, ItemCode: p.item, ItemName: p.name, ShowPriority: idx + 1, Probability: slot.Weight}
	httpx.WriteObj(w, spinEnvelope[spinPlayReply]{Result: spinPlayReply{GemBalance: spinCoin{acc.Gems},
		CashBalance: spinCoin{acc.Cash}, HistoryList: []spinHistory{hist}}})
}

// RegisterSpinRoutes serves the two spin lists and the play. The pictures are routed with the other Cherry pictures
// (HandleSpinImage, web/http.go). luckyspin/reward is not served: the client never calls it (spin-spec R4).
func RegisterSpinRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/v4/luckyspin/list/inprogress", handleSpinList(false))
	mux.HandleFunc("/v4/luckyspin/jackpot/list/inprogress", handleSpinList(true))
	mux.HandleFunc("/v4/luckyspin/purchasenplay", handleSpinPlay)
}

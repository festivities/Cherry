package economy

import (
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"

	"cherry/data"
	"cherry/internal/httpx"
	"cherry/internal/opdata"
	"cherry/internal/room"
	"cherry/internal/store"
)

// Gacha v1 (Phase C second pass): one normal Gem gacha over the shop's clothing and furniture.
// Protocol: .agents/PLAN.md "Economy", static spec economy-20261006/gacha-phaseC.md. Reply key
// types were checked against the native parsers in libgame.so (arm64, IDA, read-only): reward
// logs sDataGachaRewardLogs::ParseJson 0x259a744, rows GachaItemInfo::SetData 0x2607950,
// collection GachaCollectionInfo::SetData 0x260c7c8, lists sDataGachaShopList::SetData 0x2608bc0,
// detail sDataGachaShopCollectionList::SetData 0x260cd6c, replies ResGachaPurchaseCoin 0x1b437f4,
// ResGachaPurchaseBundle 0x1b4d1a4, ResGachaBonusInfo 0x1a57b94, ResGachaKeywords 0x1b4c43c (an
// array), ResGachaBannerList 0x1aac350 (result object), ResGachaCategroy 0x1b17224 (result array),
// ResGachaCategroy_Detail 0x1aab120 (list object), ResGachaLikeItemList 0x1b51bb8 (result array).

// Gacha is data/gacha.json: the one Gem gacha over the shop's clothing and furniture.
//
//	id, name          the gacha's id and title (the client's gachaId and banner text)
//	coin_price        Gems per pull; bundle_price buys bundle_size pulls (1 to 50)
//	rare_percent      RARE (animated) odds per pull, 0 to 100; a bundle always includes one RARE
//	refund_percent    a duplicate refunds this percent of its price in Gems, rounded up
//	collection_max    rows served on the detail page
//	kinds             the pool: "fashion" (clothing) and/or "interior" (furniture); pets and faces never
//	exclude           item codes never drawn (each must be a sellable code)
type Gacha struct {
	ID            int64    `json:"id"`
	Name          string   `json:"name"`
	CoinPrice     int64    `json:"coin_price"`
	BundlePrice   int64    `json:"bundle_price"`
	BundleSize    int      `json:"bundle_size"`
	RarePercent   int      `json:"rare_percent"`
	RefundPercent int64    `json:"refund_percent"`
	CollectionMax int      `json:"collection_max"`
	Kinds         []string `json:"kinds"`
	Exclude       []string `json:"exclude"`
}

var gachaFile = opdata.New("gacha.json", data.GachaJSON, checkGacha, nil)

// ActiveGacha is the gacha file in use.
func ActiveGacha() *Gacha { return gachaFile.Get() }

func checkGacha(g *Gacha) error {
	switch {
	case g.ID < 1:
		return errors.New("id must be positive")
	case g.Name == "":
		return errors.New("name must not be empty")
	case g.CoinPrice < 0 || g.BundlePrice < 0:
		return errors.New("coin_price and bundle_price must not be negative")
	case g.BundleSize < 1 || g.BundleSize > 50:
		return errors.New("bundle_size must be 1 to 50")
	case g.RarePercent < 0 || g.RarePercent > 100:
		return errors.New("rare_percent must be 0 to 100")
	case g.RefundPercent < 0 || g.RefundPercent > 100:
		return errors.New("refund_percent must be 0 to 100")
	case g.CollectionMax < 1:
		return errors.New("collection_max must be at least 1")
	case len(g.Kinds) == 0:
		return errors.New("kinds must name fashion or interior")
	}
	for i, k := range g.Kinds {
		if k != "fashion" && k != "interior" || slices.Contains(g.Kinds[:i], k) {
			return fmt.Errorf("kinds: %q must be fashion or interior, once each", k)
		}
	}
	for _, code := range g.Exclude {
		if _, kind := classify(code); kind == 0 {
			return fmt.Errorf("exclude: %q is not a sellable item code", code)
		}
	}
	return nil
}

// gachaRoll returns a uniform index in [0, n). Tests replace it with a seeded or scripted source.
var gachaRoll = func(n int) int { return rand.IntN(n) }

// gachaRewardSeq numbers reward logs. ponytail: process-local; a restart can reuse numbers
// (the client's use of rewardLogSeq is unverified).
var gachaRewardSeq atomic.Int64

type gachaEntry struct {
	code  string
	kind  byte // kindFashion or kindInterior
	price int64
	rare  bool // ItemPrice grade "R": animated
}

// gachaPool is the RARE and normal tiers of the shop catalog (fashion + interior; no pets, no faces).
type gachaPool struct{ normal, rare []gachaEntry }

func gachaPoolNow() gachaPool {
	var p gachaPool
	g := ActiveGacha()
	c := loadCatalog()
	for _, src := range []struct {
		items []shopItem
		kind  byte
		name  string
	}{{c.fashion, kindFashion, "fashion"}, {c.interior, kindInterior, "interior"}} {
		if !slices.Contains(g.Kinds, src.name) {
			continue
		}
		for _, it := range src.items {
			price, _, grade, ok := ItemPrice(it.Code)
			if !ok || slices.Contains(g.Exclude, it.Code) {
				continue
			}
			e := gachaEntry{code: it.Code, kind: src.kind, price: price, rare: grade == "R"}
			if e.rare {
				p.rare = append(p.rare, e)
			} else {
				p.normal = append(p.normal, e)
			}
		}
	}
	return p
}

// pick draws one item from the RARE tier when rare is set (the normal tier if the RARE tier is
// empty), otherwise from the normal tier. The caller guarantees the pool is not empty.
func (p gachaPool) pick(rare bool) gachaEntry {
	tier := p.normal
	if rare && len(p.rare) > 0 || len(p.normal) == 0 {
		tier = p.rare
	}
	return tier[gachaRoll(len(tier))]
}

// rollGacha draws n pulls: each is RARE with rare_percent odds, then uniform in its tier.
// guaranteed (the 10-pull) re-rolls the last pull from the RARE tier when no pull was RARE.
func rollGacha(p gachaPool, n int, guaranteed bool) []gachaEntry {
	out := make([]gachaEntry, n)
	anyRare := false
	rare := ActiveGacha().RarePercent
	for i := range out {
		out[i] = p.pick(gachaRoll(100) < rare)
		anyRare = anyRare || out[i].rare
	}
	if guaranteed && !anyRare && n > 0 {
		out[n-1] = p.pick(true)
	}
	return out
}

// gachaRefund is the Gem refund for a duplicate: refund_percent of the price, rounded up.
func gachaRefund(price int64) int64 {
	return (price*ActiveGacha().RefundPercent + 99) / 100
}

// gachaRowFor is the one gacha row: the thumbnail is the first RARE item (any item if none).
// GachaType "S" makes the client price the gacha in Gems: GachaItemInfo::SetData (0x2607950) sets
// the price type to (gachaType != "S") = Cash, read by NaGachaManager::GetPriceTypeWithBuyPopupType
// (0x25c0218), 0 = Gem. FirstBuyPrice equals the price: the client offers the first-purchase price
// (GachaPriceData kind 1, "First purchase") only when firstBuyPrice < price and the collection's
// gatheringCount is 0 (GetCurrentGachaPriceData 0x2618fb4, caller 0x261a890).
func gachaRowFor(p gachaPool) gachaRow {
	g := ActiveGacha()
	code := ""
	if len(p.rare) > 0 {
		code = p.rare[0].code
	} else if len(p.normal) > 0 {
		code = p.normal[0].code
	}
	coin := int(g.CoinPrice)
	return gachaRow{
		ItemCode: code, ItemID: int(g.ID), GachaID: int(g.ID), GachaName: g.Name,
		GachaDesc: fmt.Sprintf("%d%% normal / %d%% RARE", 100-g.RarePercent, g.RarePercent), GachaType: "S",
		Price: coin, DiscountedPrice: coin, FirstBuyPrice: coin, RetryPrice: coin,
		BundleGachaEnabled: true, BundleGachaQuantity: g.BundleSize, BundlePrice: int(g.BundlePrice),
		BundleDiscountedPrice: int(g.BundlePrice), BundleGachaGuarantee: 1,
		Selling: true, Display: true,
		GachaKindFlagNewTypes: []string{}, GachaKeywords: []string{},
	}
}

// gachaCollectionFor lists the RARE tier, then a normal sample, up to collection_max rows.
func gachaCollectionFor(p gachaPool) gachaCollection {
	g := ActiveGacha()
	items := []gachaCollectionItem{}
	for _, e := range p.rare {
		if len(items) < g.CollectionMax {
			items = append(items, gachaCollectionItem{ItemCode: e.code, Grade: "R"})
		}
	}
	for _, e := range p.normal {
		if len(items) < g.CollectionMax {
			items = append(items, gachaCollectionItem{ItemCode: e.code})
		}
	}
	return gachaCollection{CollectionNo: int(g.ID), CollectionName: g.Name, TotalLikesCount: "0", CollectionItems: items}
}

// ---- reply shapes (JSON key types as verified above) ----

type gachaResult[T any] struct {
	Result T `json:"result"`
}

// gachaRow is GachaItemInfo: one gacha in the list, and items[0] of the detail reply.
type gachaRow struct {
	ItemCode                        string   `json:"itemCode"`
	RepresentImageURL               string   `json:"representImageUrl"`
	Grade                           string   `json:"grade"`
	GachaDetailBannerImageURL       string   `json:"gachaDetailBannerImageUrl"`
	GachaMainBannerImageURL         string   `json:"gachaMainBannerImageUrl"`
	GachaPurchaseRewardBannerImgURL string   `json:"gachaPurchaseRewardBannerImgUrl"`
	VipGachaMainBannerImageURL      string   `json:"vipGachaMainBannerImageUrl"`
	GachaName                       string   `json:"gachaName"`
	GachaDesc                       string   `json:"gachaDesc"`
	GachaType                       string   `json:"gachaType"`
	GachaEffectType                 string   `json:"gachaEffectType"`
	GachaVipTitle                   string   `json:"gachaVipTitle"`
	VipGachaDesc                    string   `json:"vipGachaDesc"`
	VipGachaTarget                  string   `json:"vipGachaTarget"`
	SellingIconType                 string   `json:"sellingIconType"`
	SellingStart                    string   `json:"sellingStart"`
	SellingEnd                      string   `json:"sellingEnd"`
	DiscountStart                   string   `json:"discountStart"`
	DiscountEnd                     string   `json:"discountEnd"`
	SpecialEffects                  string   `json:"specialEffects"`
	LikesCount                      string   `json:"likesCount"`
	BottomMessage                   string   `json:"bottomMessage"`
	BotAvatarID1                    string   `json:"botAvatarId1"`
	BotAvatarID2                    string   `json:"botAvatarId2"`
	ItemID                          int      `json:"itemId"`
	GachaID                         int      `json:"gachaId"`
	Price                           int      `json:"price"`
	DiscountedPrice                 int      `json:"discountedPrice"`
	RetryPrice                      int      `json:"retryPrice"`
	FirstBuyPrice                   int      `json:"firstBuyPrice"`
	BuyCountForFree                 int      `json:"buyCountForFree"`
	CurrentBuyCount                 int      `json:"currentBuyCount"`
	BundleGachaQuantity             int      `json:"bundleGachaQuantity"`
	BundlePrice                     int      `json:"bundlePrice"`
	BundleDiscountedPrice           int      `json:"bundleDiscountedPrice"`
	BundleGachaGuarantee            int      `json:"bundleGachaGuarantee"`
	VipPrice                        int      `json:"vipPrice"`
	VipBundlePrice                  int      `json:"vipBundlePrice"`
	BundleGachaEnabled              bool     `json:"bundleGachaEnabled"`
	Selling                         bool     `json:"selling"`
	Display                         bool     `json:"display"`
	Soldout                         bool     `json:"soldout"`
	Discount                        bool     `json:"discount"`
	New                             bool     `json:"new"`
	Hidden                          bool     `json:"hidden"`
	NoDuplYn                        bool     `json:"noDuplYn"`
	GachaKindFlagNewTypes           []string `json:"gachaKindFlagNewTypes"`
	GachaKeywords                   []string `json:"gachaKeywords"`
}

type gachaListReply struct {
	NextCursor           string     `json:"nextCursor"`
	PreviousCursor       string     `json:"previousCursor"`
	TotalCount           string     `json:"totalCount"`
	VipGrade             string     `json:"vipGrade"`
	KeywordNm            string     `json:"keywordNm"`
	CategroyNm           string     `json:"categroyNm"`
	VipGachaDiscountRate int        `json:"vipGachaDiscountRate"`
	Items                []gachaRow `json:"items"`
	BannerList           []any      `json:"bannerList"`
}

// gachaCollection is GachaCollectionInfo; collectionItems entries are GachaItemInfo rows, of
// which only these keys are sent (missing keys take the parser's defaults).
type gachaCollection struct {
	GatheringCount  int                   `json:"gatheringCount"`
	CollectionNo    int                   `json:"collectionNo"`
	CollectionName  string                `json:"collectionName"`
	BannerImgPath   string                `json:"bannerImgPath"`
	Bg              string                `json:"bg"`
	BgColor         string                `json:"bgColor"`
	FontColor       string                `json:"fontColor"`
	TotalLikesCount string                `json:"totalLikesCount"`
	CollectionItems []gachaCollectionItem `json:"collectionItems"`
}

type gachaCollectionItem struct {
	ItemCode          string `json:"itemCode"`
	RepresentImageURL string `json:"representImageUrl"`
	Grade             string `json:"grade"`
	Gather            bool   `json:"gather"`
	Hidden            bool   `json:"hidden"`
	ArchiveItem       bool   `json:"archiveItem"`
}

// gachaDetailItem is items[0] of the detail reply: the row plus its collection.
type gachaDetailItem struct {
	gachaRow
	Collection gachaCollection `json:"collection"`
}

type gachaDetailReply struct {
	NextCursor            string            `json:"nextCursor"`
	PreviousCursor        string            `json:"previousCursor"`
	TotalCount            string            `json:"totalCount"`
	PreviewImageURL       string            `json:"previewImageUrl"`
	PreviewGardenImageURL string            `json:"previewGardenImageUrl"`
	Items                 []gachaDetailItem `json:"items"`
	NewUpgradeItems       []any             `json:"newUpgradeItems"`
	SimilarGachas         []any             `json:"similarGachas"`
	VipGachaDiscountRate  int               `json:"vipGachaDiscountRate"`
}

// gachaRewardLog is sDataGachaRewardLogs: the single-pull keys, and each gachaRewardLogs entry.
type gachaRewardLog struct {
	RewardLogSeq    string `json:"rewardLogSeq"`
	AvatarID        string `json:"avatarId"`
	GachaID         int    `json:"gachaId"`
	ItemCode        string `json:"itemCode"`
	RewardedCoin    string `json:"rewardedCoin"`
	Rewarded        bool   `json:"rewarded"`
	DisplayImageURL string `json:"displayImageUrl"`
	ItemName        string `json:"itemName"`
	SetItem         bool   `json:"setitem"`
	Grade           string `json:"grade"`
	SpecialEffects  string `json:"specialEffects"`
	DyeType         int    `json:"dyeType"`
}

type gachaSingleReply struct {
	Purchasable           bool  `json:"purchasable"`
	GainCirclePoint       int   `json:"gainCirclePoint"`
	Balance               int64 `json:"balance"`
	PurchaseRewardLogList []any `json:"purchaseRewardLogList"` // extra event rewards ("BONUS EVENT!" popup): none
	gachaRewardLog
}

type gachaBundleReply struct {
	Balance               int64            `json:"balance"`
	GainCirclePoint       int              `json:"gainCirclePoint"`
	GachaRewardLogs       []gachaRewardLog `json:"gachaRewardLogs"`
	PurchaseRewardLogList []any            `json:"purchaseRewardLogList"` // extra event rewards ("BONUS EVENT!" popup): none
}

// gachaOutcome is one pull: the item, whether it was a duplicate, and the refund for it.
type gachaOutcome struct {
	entry  gachaEntry
	dup    bool
	refund int64
}

func (o gachaOutcome) grade() string {
	if o.entry.rare {
		return "R"
	}
	return ""
}

func (o gachaOutcome) specialEffects() string {
	if o.entry.rare {
		return "ANIMATION"
	}
	return ""
}

func (o gachaOutcome) rewardLog(aid string, seq int64) gachaRewardLog {
	return gachaRewardLog{RewardLogSeq: strconv.FormatInt(seq, 10), AvatarID: aid, GachaID: int(ActiveGacha().ID),
		ItemCode: o.entry.code, RewardedCoin: strconv.FormatInt(o.refund, 10), Rewarded: true,
		ItemName: ItemName(o.entry.code), Grade: o.grade(), SpecialEffects: o.specialEffects()}
}

// grantRoomItemsLocked adds one owned room item per code with the next free seq (the interior
// branch of handleShopBuy uses the same rule).
func grantRoomItemsLocked(acc *store.Account, codes []string) {
	if len(codes) == 0 {
		return
	}
	items := slices.Clone(acc.RoomItems)
	seq := max(acc.NextRoomSeq, store.FirstRoomSeq)
	for _, it := range items {
		seq = max(seq, it.Seq+1)
	}
	for _, code := range codes {
		items = append(items, store.RoomItem{Seq: seq, Cd: code})
		seq++
	}
	acc.RoomItems, acc.NextRoomSeq = items, seq
}

// ---- routes ----

func handleGachaList(top bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			httpx.ServeNotFound(w)
			return
		}
		rep := gachaListReply{NextCursor: "0", PreviousCursor: "0", TotalCount: "0", VipGrade: "NONE",
			Items: []gachaRow{}, BannerList: []any{}}
		if top {
			rep.TotalCount, rep.Items = "1", []gachaRow{gachaRowFor(gachaPoolNow())}
		}
		httpx.WriteObj(w, gachaResult[gachaListReply]{rep})
	}
}

func handleGachaDetail(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpx.ServeNotFound(w)
		return
	}
	rep := gachaDetailReply{NextCursor: "0", PreviousCursor: "0", TotalCount: "0",
		Items: []gachaDetailItem{}, NewUpgradeItems: []any{}, SimilarGachas: []any{}}
	if strings.TrimPrefix(r.URL.Path, "/v4/shop/gacha/detail/target/") == strconv.FormatInt(ActiveGacha().ID, 10) {
		p := gachaPoolNow()
		rep.TotalCount = "1"
		rep.Items = []gachaDetailItem{{gachaRow: gachaRowFor(p), Collection: gachaCollectionFor(p)}}
	}
	httpx.WriteObj(w, gachaResult[gachaDetailReply]{rep})
}

// gachaOddsHTML is the odds page the thumbnail button opens in a webview.
func gachaOddsHTML(p gachaPool) string {
	g := ActiveGacha()
	share := func(n, pct int) string {
		if n == 0 {
			return "no items"
		}
		return fmt.Sprintf("%d items, each about %.2f%%", n, float64(pct)/float64(n))
	}
	return fmt.Sprintf(`<!doctype html>
<html lang="en">
<head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><title>Cherry Gacha odds</title></head>
<body>
<h1>%s</h1>
<p>Price: %d Gems per pull, or %d Gems for %d pulls. The %d-pull always includes at least one RARE item.</p>
<h2>Odds</h2>
<ul>
<li>Normal: %d%% in total, %s</li>
<li>RARE (animated): %d%% in total, %s</li>
</ul>
<h2>Duplicates</h2>
<p>An item you already own, or one that repeats within the same %d-pull, is not given again. You get %d%% of its price back in Gems (rounded up).</p>
</body>
</html>
`, g.Name, g.CoinPrice, g.BundlePrice, g.BundleSize, g.BundleSize,
		100-g.RarePercent, share(len(p.normal), 100-g.RarePercent),
		g.RarePercent, share(len(p.rare), g.RarePercent), g.BundleSize, g.RefundPercent)
}

func handleGachaOdds(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet || strings.TrimPrefix(r.URL.Path, "/web/probability/item/gacha/") != strconv.FormatInt(ActiveGacha().ID, 10) {
		httpx.ServeNotFound(w)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(gachaOddsHTML(gachaPoolNow())))
}

type gachaPurchaseReq struct {
	GachaID json.RawMessage `json:"gachaId"`
	Price   json.RawMessage `json:"price"`
}

// handleGachaPurchase serves purchase/gacha/coin (one pull) and purchase/gacha/multi (ten pulls).
// The gachaId and price are checked against this gacha's table; the Gem balance is the server's.
// timeMagicYn and adFreeYn are not offered and are ignored (the price stays the table price).
func handleGachaPurchase(bundle bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			httpx.ServeNotFound(w)
			return
		}
		raw, err := httpx.ReadNativeBody(r)
		var req gachaPurchaseReq
		if err == nil {
			_, err = httpx.UnmarshalNativeJSON(raw, &req)
		}
		if err != nil {
			httpx.WriteJSON(w, http.StatusBadRequest, httpx.BadRequestBody)
			return
		}
		g := ActiveGacha()
		cost, draws := g.CoinPrice, 1
		if bundle {
			cost, draws = g.BundlePrice, g.BundleSize
		}
		if id, ok := lenientInt(req.GachaID); !ok || id != g.ID {
			shopError(w, r, raw, errUpdated, "unknown gacha")
			return
		}
		if price, ok := lenientInt(req.Price); !ok || price != cost {
			shopError(w, r, raw, errUpdated, "shop updated")
			return
		}
		pool := gachaPoolNow()
		if len(pool.normal)+len(pool.rare) == 0 {
			shopError(w, r, raw, errUpdated, "shop updated")
			return
		}

		store.AccountsMu.Lock()
		defer store.AccountsMu.Unlock()
		acc := store.Accounts[httpx.CookieValue(r, "AV_AUTH")]
		if acc == nil {
			httpx.WriteJSON(w, http.StatusNotFound, httpx.UnknownSessionBody)
			return
		}
		if acc.Gems < cost {
			shopError(w, r, raw, errNoMoney, "insufficient balance")
			return
		}
		if err := room.EnsureRoomLocked(acc); err != nil { // furniture duplicates need the starter room
			httpx.WriteJSON(w, http.StatusInternalServerError, httpx.SaveFailedBody)
			return
		}
		previous := *acc

		// Ownership: clothing from AccountOwnedCodes, furniture from any RoomItem with the code.
		// A code rolled earlier in this pull counts as owned too.
		owned := map[string]bool{}
		for _, c := range store.AccountOwnedCodes(acc) {
			owned[c] = true
		}
		for _, it := range acc.RoomItems {
			owned[it.Cd] = true
		}
		picks := rollGacha(pool, draws, bundle)
		outcomes := make([]gachaOutcome, len(picks))
		var refund int64
		var codes, clothing, furniture []string
		for i, e := range picks {
			o := gachaOutcome{entry: e}
			codes = append(codes, e.code)
			if owned[e.code] {
				o.dup, o.refund = true, gachaRefund(e.price)
				refund += o.refund
			} else {
				owned[e.code] = true
				if e.kind == kindFashion {
					clothing = append(clothing, e.code)
				} else {
					furniture = append(furniture, e.code)
				}
			}
			outcomes[i] = o
		}

		lines, err := store.SpendLocked(acc, store.LedgerDeltas{Gems: refund - cost},
			"gacha:"+strconv.FormatInt(g.ID, 10)+":"+strings.Join(codes, ","))
		if err != nil { // only the balance ceiling can refuse here; nothing changed
			shopError(w, r, raw, errTooMany, "balance limit")
			return
		}
		if len(clothing) > 0 {
			acc.InventoryCodes = store.AppendUniqueItemCodes(store.AccountInventoryCodes(acc), clothing)
		}
		grantRoomItemsLocked(acc, furniture)
		if err := store.SaveAccountsLocked(); err != nil {
			*acc = previous
			httpx.WriteJSON(w, http.StatusInternalServerError, httpx.SaveFailedBody)
			return
		}
		store.FlushLedgerLocked(lines)

		logs := make([]gachaRewardLog, len(outcomes))
		for i, o := range outcomes {
			logs[i] = o.rewardLog(acc.Aid, gachaRewardSeq.Add(1))
		}
		if bundle {
			httpx.WriteObj(w, gachaResult[gachaBundleReply]{gachaBundleReply{Balance: acc.Gems,
				GainCirclePoint: 0, GachaRewardLogs: logs, PurchaseRewardLogList: []any{}}})
			return
		}
		httpx.WriteObj(w, gachaResult[gachaSingleReply]{gachaSingleReply{Purchasable: true,
			GainCirclePoint: 0, Balance: acc.Gems, PurchaseRewardLogList: []any{}, gachaRewardLog: logs[0]}})
	}
}

// handleGachaRetired answers the pull variants this gacha does not sell with "shop updated".
func handleGachaRetired(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpx.ServeNotFound(w)
		return
	}
	raw, _ := httpx.ReadNativeBody(r)
	shopError(w, r, raw, errUpdated, "shop updated")
}

func registerGachaRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/v4/shop/gachaList2/top/all", handleGachaList(true))
	// ReqGachaCategroy (catg/all/-1) parses result as an array (sDataGachaShopCategoryList::SetData);
	// ReqGachaCategroy_Detail (catg/all/<id>, incl. 0) parses the list object. No categories exist.
	mux.HandleFunc("/v4/shop/gachaList2/catg/all/-1", httpx.HandleJSONBody(`{"result":[]}`))
	mux.HandleFunc("/v4/shop/gachaList2/catg/all/", handleGachaList(false))
	mux.HandleFunc("/v4/shop/gachaList2/new/all", handleGachaList(false))
	// banner/all, banner/top and banner/promotion all parse as sDataGachaBannerAll (result object).
	mux.HandleFunc("/v4/shop/gachaList2/banner/", httpx.HandleJSONBody(`{"result":{"mainTopBannerList":[],"mainPromotionBannerList":[],"mainVipLeftList":[],"mainVipRightList":[],"moreVipCategoryId":0}}`))
	mux.HandleFunc("/v4/shop/gachaList2/keyword", httpx.HandleJSONBody(`{"result":[]}`))
	mux.HandleFunc("/v4/shop/gacha/detail/target/", handleGachaDetail)
	// ResGachaLikeItemList: result is an array of {categoryName, items} (walked with size()).
	mux.HandleFunc("/v4/likes/popular/item", httpx.HandleJSONBody(`{"result":[]}`))
	// Action 3/8 (home sub-list, .nhn URL) feeds the shop's Category section through ResGachaCategroy
	// (sDataGachaShopCategoryList walks result as an array). Runtime 2026-10-11: a list object here
	// crashed the client (SIGSEGV in SetData, Json::Value::operator[](unsigned)); a 404 showed
	// "Unable to load data.".
	mux.HandleFunc("/v4/home/sublist/gacha/", httpx.HandleJSONBody(`{"result":[]}`))
	// ResGachaBonusInfo parses (and may pop up) only when result has gachaNo: no bonus = empty result.
	mux.HandleFunc("/v4/gacha/bonus/info", httpx.HandleJSONBody(`{"result":{}}`))
	mux.HandleFunc("/v4/purchase/gacha/coin", handleGachaPurchase(false))
	mux.HandleFunc("/v4/purchase/gacha/multi", handleGachaPurchase(true))
	// The result screen always offers a retry button priced retryPrice (NaGachaBuyPopup::
	// SetResultUIState 0x261e8e4); a retry is an ordinary pull at the same price.
	mux.HandleFunc("/v4/purchase/gacha/coinretry", handleGachaPurchase(false))
	for _, v := range []string{"coinfirst", "coinfree", "ticket", "categoryticket", "clover"} {
		mux.HandleFunc("/v4/purchase/gacha/"+v, handleGachaRetired)
	}
	mux.HandleFunc("/web/probability/item/gacha/", handleGachaOdds)
}

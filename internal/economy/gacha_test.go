package economy

import (
	"encoding/json"
	"math"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"cherry/internal/room"
	"cherry/internal/store"
	"cherry/internal/testutil"
)

// gachaFixture points the catalog at a small item root and returns its pool.
// Normal: CUHA00001 200, CUTO00001 150, RUTD00005 150, RUWA000HR 100. RARE: CUON004TV 625, RUGO00002 375.
// CUHE00001 (face) and PUPE0000G (pet) are catalog-excluded.
func gachaFixture(t *testing.T) gachaPool {
	t.Helper()
	shopFixture(t, map[string][]string{
		"dress/225000001":    {"a.png"},
		"dress/225106259":    {"a.png", "x.aniproj"},
		"custom/224400001":   {"a.png"},
		"interior/121400639": {"a.png"},
		"interior/121700005": {"a.png"},
		"interior/122000002": {"a.aniproj"},
		"custom/223800001":   {"a.png"},
		"pet/326400016":      {"skeleton.xml"},
	})
	return gachaPoolNow()
}

// gachaAccount installs non-lab account "gtok" with gems, owned clothing and room items. A filler
// table always comes first, and a default-level layout exists, so EnsureRoomLocked neither reseeds
// the starter room nor saves (the store path stays empty unless a test sets it).
func gachaAccount(t *testing.T, gems int64, inventory []string, rooms ...string) *store.Account {
	t.Helper()
	acc := &store.Account{AccessToken: "gtok", SessionKey: "sk", Aid: "9601", Name: "Gacha", Gender: "FEMALE",
		Gems: gems, ItemCodes: inventory, InventoryCodes: inventory,
		Rooms: map[string]store.RoomLayout{room.DefaultLevel: {Placed: []store.RoomPlaced{}}}}
	for i, cd := range append([]string{"RUTA0004S"}, rooms...) {
		acc.RoomItems = append(acc.RoomItems, store.RoomItem{Seq: store.FirstRoomSeq + int64(i), Cd: cd})
	}
	acc.NextRoomSeq = store.FirstRoomSeq + int64(len(acc.RoomItems))
	testutil.InstallAccounts(t, map[string]*store.Account{"gtok": acc})
	testutil.SetLab(t, "9601", false)
	return acc
}

// gachaLedgerDir points the account store at a temp dir so saves and ledger lines are written.
func gachaLedgerDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	store.AccountsMu.Lock()
	store.AccountStorePath = filepath.Join(dir, "accounts.json")
	store.AccountsMu.Unlock()
	return dir
}

func gachaLedger(t *testing.T, dir string) []store.LedgerLine {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, "ledger.jsonl"))
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	var out []store.LedgerLine
	for _, ln := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		if ln == "" {
			continue
		}
		var l store.LedgerLine
		if err := json.Unmarshal([]byte(ln), &l); err != nil {
			t.Fatal(err)
		}
		out = append(out, l)
	}
	return out
}

func gachaGems(acc *store.Account) int64 {
	store.AccountsMu.Lock()
	defer store.AccountsMu.Unlock()
	return acc.Gems
}

// gachaCall serves one request through a fresh route table, as the HTTPS mux does.
func gachaCall(t *testing.T, method, target, body, token string) *httptest.ResponseRecorder {
	t.Helper()
	mux := http.NewServeMux()
	registerShopRoutes(mux)
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	if token != "" {
		req.Header.Set("Cookie", `AV_AUTH="`+token+`"`)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

// gachaBody is a client pull request, with the native trailing NUL.
func gachaBody(id, price string) string {
	return `{"language":"en","deviceType":"Android","gachaId":` + id + `,"price":` + price + `,"timeMagicYn":"N","adFreeYn":"N"}` + "\x00"
}

func gachaResultMap(t *testing.T, body []byte) map[string]any {
	t.Helper()
	var env struct {
		Result map[string]any `json:"result"`
	}
	if err := json.Unmarshal(body, &env); err != nil || env.Result == nil {
		t.Fatalf("want object result, got %s (%v)", body, err)
	}
	return env.Result
}

// gachaScript replaces gachaRoll with values in call order; running out or an out-of-range value fails.
func gachaScript(t *testing.T, vals ...int) {
	t.Helper()
	old := gachaRoll
	t.Cleanup(func() { gachaRoll = old })
	gachaRoll = func(n int) int {
		if len(vals) == 0 {
			t.Fatalf("roller exhausted at n=%d", n)
		}
		v := vals[0]
		vals = vals[1:]
		if v < 0 || v >= n {
			t.Fatalf("scripted %d not in [0,%d)", v, n)
		}
		return v
	}
}

// pullVals returns the roller values that land one pull on code: a tier roll (50 = normal, 5 = RARE), then the index.
func pullVals(t *testing.T, p gachaPool, code string) []int {
	t.Helper()
	if i := slices.IndexFunc(p.normal, func(e gachaEntry) bool { return e.code == code }); i >= 0 {
		return []int{50, i}
	}
	if i := slices.IndexFunc(p.rare, func(e gachaEntry) bool { return e.code == code }); i >= 0 {
		return []int{5, i}
	}
	t.Fatalf("%s not in pool", code)
	return nil
}

func gachaRareIndex(p gachaPool, code string) int {
	return slices.IndexFunc(p.rare, func(e gachaEntry) bool { return e.code == code })
}

func gachaCodes(es []gachaEntry) []string {
	out := make([]string, len(es))
	for i, e := range es {
		out[i] = e.code
	}
	return out
}

// gachaShape checks obj has exactly the keys in want, each of the JSON type named there.
func gachaShape(t *testing.T, label string, obj map[string]any, want map[string]string) {
	t.Helper()
	for k, typ := range want {
		v, ok := obj[k]
		if !ok {
			t.Errorf("%s: missing %q", label, k)
			continue
		}
		good := false
		switch typ {
		case "int":
			f, isNum := v.(float64)
			good = isNum && f == math.Trunc(f)
		case "string":
			_, good = v.(string)
		case "bool":
			_, good = v.(bool)
		case "array":
			_, good = v.([]any)
		case "object":
			_, good = v.(map[string]any)
		}
		if !good {
			t.Errorf("%s: %q = %v (%T), want %s", label, k, v, v, typ)
		}
	}
	for k := range obj {
		if _, ok := want[k]; !ok {
			t.Errorf("%s: unexpected key %q", label, k)
		}
	}
}

func shapeOf(typ, keys string) map[string]string {
	m := map[string]string{}
	for _, k := range strings.Fields(keys) {
		m[k] = typ
	}
	return m
}

func mergeShapes(parts ...map[string]string) map[string]string {
	m := map[string]string{}
	for _, p := range parts {
		for k, v := range p {
			m[k] = v
		}
	}
	return m
}

// Key types from the native parsers (IDA, 2026-10-11; see gacha.go).
var (
	gachaRowShape = mergeShapes(
		shapeOf("string", "itemCode representImageUrl grade gachaDetailBannerImageUrl gachaMainBannerImageUrl gachaPurchaseRewardBannerImgUrl vipGachaMainBannerImageUrl gachaName gachaDesc gachaType gachaEffectType gachaVipTitle vipGachaDesc vipGachaTarget sellingIconType sellingStart sellingEnd discountStart discountEnd specialEffects likesCount bottomMessage botAvatarId1 botAvatarId2"),
		shapeOf("int", "itemId gachaId price discountedPrice retryPrice firstBuyPrice buyCountForFree currentBuyCount bundleGachaQuantity bundlePrice bundleDiscountedPrice bundleGachaGuarantee vipPrice vipBundlePrice"),
		shapeOf("bool", "bundleGachaEnabled selling display soldout discount new hidden noDuplYn"),
		shapeOf("array", "gachaKindFlagNewTypes gachaKeywords"))
	gachaCollectionShape = mergeShapes(
		shapeOf("int", "gatheringCount collectionNo"),
		shapeOf("string", "collectionName bannerImgPath bg bgColor fontColor totalLikesCount"),
		shapeOf("array", "collectionItems"))
	gachaCollItemShape = mergeShapes(
		shapeOf("string", "itemCode representImageUrl grade"),
		shapeOf("bool", "gather hidden archiveItem"))
	gachaListShape = mergeShapes(
		shapeOf("string", "nextCursor previousCursor totalCount vipGrade keywordNm categroyNm"),
		shapeOf("int", "vipGachaDiscountRate"),
		shapeOf("array", "items bannerList"))
	gachaDetailShape = mergeShapes(
		shapeOf("string", "nextCursor previousCursor totalCount previewImageUrl previewGardenImageUrl"),
		shapeOf("int", "vipGachaDiscountRate"),
		shapeOf("array", "items newUpgradeItems similarGachas"))
	gachaDetailItemShape = mergeShapes(gachaRowShape, shapeOf("object", "collection"))
	gachaRewardShape     = mergeShapes(
		shapeOf("string", "rewardLogSeq avatarId itemCode rewardedCoin displayImageUrl itemName grade specialEffects"),
		shapeOf("int", "gachaId dyeType"),
		shapeOf("bool", "rewarded setitem"))
	gachaSingleShape = mergeShapes(
		shapeOf("bool", "purchasable"),
		shapeOf("int", "gainCirclePoint balance"),
		shapeOf("array", "purchaseRewardLogList"),
		gachaRewardShape)
	gachaBundleShape = mergeShapes(
		shapeOf("int", "balance gainCirclePoint"),
		shapeOf("array", "gachaRewardLogs purchaseRewardLogList"))
)

func TestGachaPoolTiers(t *testing.T) {
	p := gachaFixture(t)
	if got := gachaCodes(p.normal); !slices.Equal(got, []string{"CUHA00001", "CUTO00001", "RUTD00005", "RUWA000HR"}) {
		t.Errorf("normal tier = %v", got)
	}
	if got := gachaCodes(p.rare); !slices.Equal(got, []string{"CUON004TV", "RUGO00002"}) {
		t.Errorf("RARE tier = %v", got)
	}
	if p.rare[0].price != 625 || p.rare[1].price != 375 || p.normal[0].price != 200 || !p.rare[0].rare || p.normal[0].rare {
		t.Errorf("tier prices or flags wrong: %+v / %+v", p.rare, p.normal)
	}
}

func TestGachaOddsSeeded(t *testing.T) {
	p := gachaFixture(t)
	old := gachaRoll
	t.Cleanup(func() { gachaRoll = old })
	seed := func() {
		rng := rand.New(rand.NewPCG(20261011, 7))
		gachaRoll = func(n int) int { return rng.IntN(n) }
	}
	seed()
	const pulls = 20000
	rare := 0
	seen := map[string]bool{}
	for range pulls {
		e := rollGacha(p, 1, false)[0]
		if e.rare {
			rare++
		}
		seen[e.code] = true
	}
	if share := float64(rare) / pulls; share < 0.09 || share > 0.11 {
		t.Errorf("RARE share %.4f over %d pulls, want about 0.10", share, pulls)
	}
	if len(seen) != 6 {
		t.Errorf("drew %d distinct items, want all 6", len(seen))
	}
	seed()
	first := gachaCodes(rollGacha(p, 30, false))
	seed()
	if !slices.Equal(first, gachaCodes(rollGacha(p, 30, false))) {
		t.Error("same seed gave different pulls")
	}
}

func TestGachaTenPullGuarantee(t *testing.T) {
	p := gachaFixture(t)
	// Ten normal pulls (index 0 = CUHA00001); the guarantee re-rolls the last from RARE index 1.
	var vals []int
	for range gachaBundleSize {
		vals = append(vals, 50, 0)
	}
	gachaScript(t, append(vals, 1)...)
	got := rollGacha(p, gachaBundleSize, true)
	for i, e := range got[:9] {
		if e.rare {
			t.Errorf("pull %d RARE, want normal", i)
		}
	}
	if !got[9].rare || got[9].code != "RUGO00002" {
		t.Errorf("last pull = %+v, want RARE RUGO00002", got[9])
	}
	// A single pull is never re-rolled.
	gachaScript(t, 50, 0)
	if e := rollGacha(p, 1, false)[0]; e.rare || e.code != "CUHA00001" {
		t.Errorf("single pull = %+v, want normal CUHA00001", e)
	}
	// Any seed: every 10-pull has at least one RARE.
	rng := rand.New(rand.NewPCG(3, 9))
	old := gachaRoll
	t.Cleanup(func() { gachaRoll = old })
	gachaRoll = func(n int) int { return rng.IntN(n) }
	for range 3000 {
		if !slices.ContainsFunc(rollGacha(p, gachaBundleSize, true), func(e gachaEntry) bool { return e.rare }) {
			t.Fatal("a 10-pull came up with no RARE")
		}
	}
}

func TestGachaPurchaseChecks(t *testing.T) {
	gachaFixture(t)
	acc := gachaAccount(t, 1000, []string{"CUHA00001"}, "RUWA000HR")
	for _, c := range []struct {
		target, body string
		status       int
		code         string
	}{
		{"/v4/purchase/gacha/coin", gachaBody("1002", "100"), 400, "61003"},
		{"/v4/purchase/gacha/coin", gachaBody("1001", "150"), 400, "61003"},
		{"/v4/purchase/gacha/coin", gachaBody("1001", "900"), 400, "61003"},
		{"/v4/purchase/gacha/multi", gachaBody("1001", "100"), 400, "61003"},
		{"/v4/purchase/gacha/multi", gachaBody("9", "900"), 400, "61003"},
		{"/v4/purchase/gacha/coin", `{"gachaId":`, 400, "400"},
		{"/v4/purchase/gacha/coinfirst", gachaBody("1001", "100"), 400, "61003"},
		{"/v4/purchase/gacha/coinfree", gachaBody("1001", "100"), 400, "61003"},
		{"/v4/purchase/gacha/ticket", gachaBody("1001", "100"), 400, "61003"},
		{"/v4/purchase/gacha/categoryticket", gachaBody("1001", "100"), 400, "61003"},
		{"/v4/purchase/gacha/clover", gachaBody("1001", "100"), 400, "61003"},
		{"/v4/purchase/gacha/coin", gachaBody("1001", "100"), 404, "404"}, // unknown session
	} {
		token := "gtok"
		if c.status == 404 {
			token = "nobody"
		}
		rec := gachaCall(t, http.MethodPost, c.target, c.body, token)
		var out map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		if rec.Code != c.status || out["errorCode"] != c.code {
			t.Errorf("POST %s %q = %d %s, want %d errorCode %s", c.target, c.body, rec.Code, rec.Body, c.status, c.code)
		}
	}
	if rec := gachaCall(t, http.MethodGet, "/v4/purchase/gacha/coin", "", "gtok"); rec.Code != 404 {
		t.Errorf("GET coin = %d, want 404", rec.Code)
	}
	if gachaGems(acc) != 1000 || len(acc.RoomItems) != 2 || !slices.Equal(acc.InventoryCodes, []string{"CUHA00001"}) {
		t.Errorf("rejected pulls changed the account: gems %d rooms %d inventory %v", gachaGems(acc), len(acc.RoomItems), acc.InventoryCodes)
	}
}

func TestGachaShortBalance(t *testing.T) {
	gachaFixture(t)
	acc := gachaAccount(t, 99, nil)
	rec := gachaCall(t, http.MethodPost, "/v4/purchase/gacha/coin", gachaBody("1001", "100"), "gtok")
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if rec.Code != 400 || out["errorCode"] != "60001" {
		t.Fatalf("single with 99 gems = %d %s, want 400 60001", rec.Code, rec.Body)
	}
	store.AccountsMu.Lock()
	acc.Gems = 899
	store.AccountsMu.Unlock()
	rec = gachaCall(t, http.MethodPost, "/v4/purchase/gacha/multi", gachaBody("1001", "900"), "gtok")
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if rec.Code != 400 || out["errorCode"] != "60001" || gachaGems(acc) != 899 || len(acc.RoomItems) != 1 {
		t.Fatalf("bundle with 899 gems = %d %s (gems %d rooms %d)", rec.Code, rec.Body, gachaGems(acc), len(acc.RoomItems))
	}
}

func TestGachaSinglePullFurnitureAndLedger(t *testing.T) {
	p := gachaFixture(t)
	acc := gachaAccount(t, 500, []string{"CUHA00001"}, "RUWA000HR")
	dir := gachaLedgerDir(t)
	gachaScript(t, pullVals(t, p, "RUGO00002")...)
	rec := gachaCall(t, http.MethodPost, "/v4/purchase/gacha/coin", gachaBody("1001", "100"), "gtok")
	if rec.Code != 200 {
		t.Fatalf("single = %d %s", rec.Code, rec.Body)
	}
	var env struct {
		Result gachaSingleReply `json:"result"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatal(err)
	}
	r := env.Result
	if !r.Purchasable || r.Balance != 400 || r.ItemCode != "RUGO00002" || !r.Rewarded || r.RewardedCoin != "0" ||
		r.Grade != "R" || r.SpecialEffects != "ANIMATION" || r.AvatarID != "9601" || r.GachaID != gachaID ||
		r.ItemName != ItemName("RUGO00002") || r.RewardLogSeq == "" {
		t.Errorf("reply = %+v", r)
	}
	if len(r.PurchaseRewardLogList) != 0 { // extra event rewards: a row pops "BONUS EVENT!"
		t.Fatalf("purchaseRewardLogList = %+v, want []", r.PurchaseRewardLogList)
	}
	res := gachaResultMap(t, rec.Body.Bytes())
	gachaShape(t, "single", res, gachaSingleShape)
	if gachaGems(acc) != 400 {
		t.Errorf("gems = %d, want 400", gachaGems(acc))
	}
	last := acc.RoomItems[len(acc.RoomItems)-1]
	if last.Cd != "RUGO00002" || last.Seq != store.FirstRoomSeq+2 || acc.NextRoomSeq != store.FirstRoomSeq+3 {
		t.Errorf("room grant = %+v next %d", last, acc.NextRoomSeq)
	}
	lines := gachaLedger(t, dir)
	if len(lines) != 1 || lines[0].Currency != "gems" || lines[0].Delta != -100 || lines[0].Balance != 400 || lines[0].Reason != "gacha:1001:RUGO00002" {
		t.Errorf("ledger = %+v", lines)
	}
}

func TestGachaSingleDuplicateRefunds(t *testing.T) {
	p := gachaFixture(t)
	acc := gachaAccount(t, 500, []string{"CUTO00001"}, "RUWA000HR")
	dir := gachaLedgerDir(t)
	// Owned clothing CUTO00001 (150): refund 75.
	gachaScript(t, pullVals(t, p, "CUTO00001")...)
	rec := gachaCall(t, http.MethodPost, "/v4/purchase/gacha/coin", gachaBody("1001", "100"), "gtok")
	var env struct {
		Result gachaSingleReply `json:"result"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &env)
	if rec.Code != 200 || env.Result.RewardedCoin != "75" || env.Result.Balance != 475 ||
		env.Result.ItemCode != "CUTO00001" {
		t.Fatalf("clothing duplicate = %d %s", rec.Code, rec.Body)
	}
	// Owned furniture RUWA000HR (100): refund 50.
	gachaScript(t, pullVals(t, p, "RUWA000HR")...)
	rec = gachaCall(t, http.MethodPost, "/v4/purchase/gacha/coin", gachaBody("1001", "100"), "gtok")
	_ = json.Unmarshal(rec.Body.Bytes(), &env)
	if rec.Code != 200 || env.Result.RewardedCoin != "50" || env.Result.Balance != 425 || env.Result.ItemCode != "RUWA000HR" {
		t.Fatalf("furniture duplicate = %d %s", rec.Code, rec.Body)
	}
	if gachaGems(acc) != 425 || !slices.Equal(acc.InventoryCodes, []string{"CUTO00001"}) || len(acc.RoomItems) != 2 {
		t.Errorf("duplicates granted items: gems %d inventory %v rooms %d", gachaGems(acc), acc.InventoryCodes, len(acc.RoomItems))
	}
	lines := gachaLedger(t, dir)
	if len(lines) != 2 || lines[0].Delta != -25 || lines[0].Balance != 475 || lines[1].Delta != -50 || lines[1].Balance != 425 {
		t.Errorf("ledger = %+v", lines)
	}
}

func TestGachaBundleGuaranteeAndDuplicates(t *testing.T) {
	p := gachaFixture(t)
	acc := gachaAccount(t, 1000, []string{"CUHA00001"}, "RUWA000HR")
	dir := gachaLedgerDir(t)
	// Ten normal picks; the last is re-rolled as RARE CUON004TV by the guarantee.
	picks := []string{"CUTO00001", "CUTO00001", "CUTO00001", "CUHA00001", "RUWA000HR", "RUTD00005", "CUHA00001", "RUWA000HR", "RUTD00005", "CUTO00001"}
	var vals []int
	for _, c := range picks {
		vals = append(vals, pullVals(t, p, c)...)
	}
	vals = append(vals, gachaRareIndex(p, "CUON004TV"))
	gachaScript(t, vals...)
	rec := gachaCall(t, http.MethodPost, "/v4/purchase/gacha/multi", gachaBody("1001", "900"), "gtok")
	if rec.Code != 200 {
		t.Fatalf("bundle = %d %s", rec.Code, rec.Body)
	}
	bres := gachaResultMap(t, rec.Body.Bytes())
	gachaShape(t, "bundle", bres, gachaBundleShape)
	for _, lv := range bres["gachaRewardLogs"].([]any) {
		gachaShape(t, "bundle log", lv.(map[string]any), gachaRewardShape)
	}
	var env struct {
		Result gachaBundleReply `json:"result"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &env)
	b := env.Result
	wantCodes := []string{"CUTO00001", "CUTO00001", "CUTO00001", "CUHA00001", "RUWA000HR", "RUTD00005", "CUHA00001", "RUWA000HR", "RUTD00005", "CUON004TV"}
	wantRefund := []string{"0", "75", "75", "100", "50", "0", "100", "50", "75", "0"}
	if len(b.GachaRewardLogs) != 10 || len(b.PurchaseRewardLogList) != 0 {
		t.Fatalf("logs %d rows %d, want 10/0", len(b.GachaRewardLogs), len(b.PurchaseRewardLogList))
	}
	seqs := map[string]bool{}
	for i := range 10 {
		log := b.GachaRewardLogs[i]
		if log.ItemCode != wantCodes[i] || log.RewardedCoin != wantRefund[i] {
			t.Errorf("pull %d = %+v, want %s refund %s", i, log, wantCodes[i], wantRefund[i])
		}
		seqs[log.RewardLogSeq] = true
	}
	if len(seqs) != 10 {
		t.Errorf("reward log seqs not distinct: %v", seqs)
	}
	if b.Balance != 625 || gachaGems(acc) != 625 {
		t.Errorf("balance %d / account %d, want 625", b.Balance, gachaGems(acc))
	}
	if !slices.Equal(acc.InventoryCodes, []string{"CUHA00001", "CUTO00001", "CUON004TV"}) {
		t.Errorf("inventory = %v", acc.InventoryCodes)
	}
	if n := len(acc.RoomItems); n != 3 || acc.RoomItems[2].Cd != "RUTD00005" || acc.RoomItems[2].Seq != store.FirstRoomSeq+2 || acc.NextRoomSeq != store.FirstRoomSeq+3 {
		t.Errorf("room items = %+v next %d", acc.RoomItems, acc.NextRoomSeq)
	}
	lines := gachaLedger(t, dir)
	if len(lines) != 1 || lines[0].Delta != -375 || lines[0].Balance != 625 || lines[0].Reason != "gacha:1001:"+strings.Join(wantCodes, ",") {
		t.Errorf("ledger = %+v", lines)
	}
}

func TestGachaPurchaseRollsBackOnSaveFailure(t *testing.T) {
	p := gachaFixture(t)
	acc := gachaAccount(t, 500, []string{"CUHA00001"}, "RUWA000HR")
	blocker := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	store.AccountsMu.Lock()
	store.AccountStorePath = filepath.Join(blocker, "accounts.json") // parent is a file: the save fails
	store.AccountsMu.Unlock()
	gachaScript(t, pullVals(t, p, "RUGO00002")...)
	rec := gachaCall(t, http.MethodPost, "/v4/purchase/gacha/coin", gachaBody("1001", "100"), "gtok")
	if rec.Code != 500 {
		t.Fatalf("save failure = %d %s, want 500", rec.Code, rec.Body)
	}
	if gachaGems(acc) != 500 || !slices.Equal(acc.InventoryCodes, []string{"CUHA00001"}) || len(acc.RoomItems) != 2 || acc.NextRoomSeq != store.FirstRoomSeq+2 {
		t.Errorf("rollback left gems %d inventory %v rooms %+v next %d", gachaGems(acc), acc.InventoryCodes, acc.RoomItems, acc.NextRoomSeq)
	}
}

func TestGachaCatalogShapes(t *testing.T) {
	gachaFixture(t)
	gachaAccount(t, 0, nil)
	rec := gachaCall(t, http.MethodGet, "/v4/shop/gachaList2/top/all?deviceType=Android", "", "")
	if rec.Code != 200 {
		t.Fatalf("top = %d", rec.Code)
	}
	res := gachaResultMap(t, rec.Body.Bytes())
	gachaShape(t, "list", res, gachaListShape)
	var typed gachaResult[gachaListReply]
	if err := json.Unmarshal(rec.Body.Bytes(), &typed); err != nil || len(typed.Result.Items) != 1 {
		t.Fatalf("typed list decode: %v %+v", err, typed)
	}
	items := res["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("items = %v", items)
	}
	row := items[0].(map[string]any)
	gachaShape(t, "row", row, gachaRowShape)
	if row["itemCode"] != "CUON004TV" || row["gachaName"] != "Cherry Gacha" || row["gachaDesc"] != "90% normal / 10% RARE" ||
		row["price"] != 100.0 || row["bundlePrice"] != 900.0 || row["bundleGachaQuantity"] != 10.0 || row["bundleGachaGuarantee"] != 1.0 ||
		row["itemId"] != float64(gachaID) || row["gachaType"] != "S" || row["bundleGachaEnabled"] != true ||
		row["firstBuyPrice"] != 100.0 || row["discountedPrice"] != 100.0 || row["buyCountForFree"] != 0.0 || row["vipPrice"] != 0.0 {
		t.Errorf("row values = %v (gachaType S = Gem price; firstBuyPrice = price = no first-buy offer)", row)
	}

	for _, target := range []string{"/v4/shop/gachaList2/catg/all/1", "/v4/shop/gachaList2/new/all",
		"/v4/shop/gachaList2/catg/all/0"} {
		rec := gachaCall(t, http.MethodGet, target, "", "")
		gachaShape(t, target, gachaResultMap(t, rec.Body.Bytes()), gachaListShape)
	}
	// ReqGachaCategroy (catg/all/-1, home sub-list) and ResGachaLikeItemList walk their result as an array.
	for _, target := range []string{"/v4/shop/gachaList2/catg/all/-1", "/v4/home/sublist/gacha/10.1.0.0/Android.nhn?parentIconId=&isMain=true", "/v4/likes/popular/item"} {
		rec := gachaCall(t, http.MethodGet, target, "", "")
		var arr struct {
			Result []any `json:"result"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &arr); rec.Code != 200 || err != nil || arr.Result == nil || len(arr.Result) != 0 {
			t.Errorf("%s = %d %s, want an empty array result", target, rec.Code, rec.Body)
		}
	}
	for _, target := range []string{"/v4/shop/gachaList2/banner/all/ja/Android", "/v4/shop/gachaList2/banner/top/en/Android",
		"/v4/shop/gachaList2/banner/promotion/en/Android"} {
		rec = gachaCall(t, http.MethodGet, target, "", "")
		banner := gachaResultMap(t, rec.Body.Bytes())
		for _, k := range []string{"mainTopBannerList", "mainPromotionBannerList", "mainVipLeftList", "mainVipRightList"} {
			if _, ok := banner[k].([]any); !ok {
				t.Errorf("%s %s = %v, want array", target, k, banner[k])
			}
		}
	}
	rec = gachaCall(t, http.MethodGet, "/v4/shop/gachaList2/keyword", "", "")
	var kw struct {
		Result []any `json:"result"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &kw); err != nil || kw.Result == nil || len(kw.Result) != 0 {
		t.Errorf("keyword = %s, want an array result", rec.Body)
	}

	rec = gachaCall(t, http.MethodGet, "/v4/shop/gacha/detail/target/1001", "", "")
	det := gachaResultMap(t, rec.Body.Bytes())
	gachaShape(t, "detail", det, gachaDetailShape)
	var typedDet gachaResult[gachaDetailReply]
	if err := json.Unmarshal(rec.Body.Bytes(), &typedDet); err != nil || len(typedDet.Result.Items) != 1 {
		t.Fatalf("typed detail decode: %v", err)
	}
	di := det["items"].([]any)[0].(map[string]any)
	gachaShape(t, "detail item", di, gachaDetailItemShape)
	coll := di["collection"].(map[string]any)
	gachaShape(t, "collection", coll, gachaCollectionShape)
	if coll["collectionNo"] != float64(gachaID) || coll["gatheringCount"] != 0.0 {
		t.Errorf("collection = %v", coll)
	}
	cis := coll["collectionItems"].([]any)
	if len(cis) != 6 || cis[0].(map[string]any)["grade"] != "R" || cis[5].(map[string]any)["grade"] != "" {
		t.Errorf("collectionItems = %v, want 2 RARE then 4 normal", cis)
	}
	for _, ci := range cis {
		gachaShape(t, "collection item", ci.(map[string]any), gachaCollItemShape)
	}

	rec = gachaCall(t, http.MethodGet, "/v4/shop/gacha/detail/target/999", "", "")
	none := gachaResultMap(t, rec.Body.Bytes())
	gachaShape(t, "unknown detail", none, gachaDetailShape)
	if len(none["items"].([]any)) != 0 || none["totalCount"] != "0" {
		t.Errorf("unknown detail = %v", none)
	}

	rec = gachaCall(t, http.MethodGet, "/v4/gacha/bonus/info?language=en&gachaNo=1001", "", "")
	if bonus := gachaResultMap(t, rec.Body.Bytes()); len(bonus) != 0 { // no gachaNo: nothing parsed, no popup
		t.Errorf("bonus = %v, want {}", bonus)
	}
}

func TestGachaLenientTypes(t *testing.T) {
	gachaFixture(t)
	gachaAccount(t, 500, nil)
	// The client may send ids and prices quoted, as lenientInt accepts.
	rec := gachaCall(t, http.MethodPost, "/v4/purchase/gacha/coin", gachaBody(`"1001"`, `"100"`), "gtok")
	if rec.Code != 200 {
		t.Errorf("quoted ids = %d %s", rec.Code, rec.Body)
	}
}

func TestGachaOddsPage(t *testing.T) {
	gachaFixture(t)
	rec := gachaCall(t, http.MethodGet, "/web/probability/item/gacha/1001", "", "")
	body := rec.Body.String()
	if rec.Code != 200 || rec.Header().Get("Content-Type") != "text/html; charset=utf-8" {
		t.Fatalf("odds = %d %q", rec.Code, rec.Header().Get("Content-Type"))
	}
	for _, want := range []string{"100 Gems per pull", "900 Gems for 10 pulls", "Normal: 90% in total, 4 items, each about 22.50%",
		"RARE (animated): 10% in total, 2 items, each about 5.00%", "50% of its price back"} {
		if !strings.Contains(body, want) {
			t.Errorf("odds page lacks %q", want)
		}
	}
	if rec := gachaCall(t, http.MethodGet, "/web/probability/item/gacha/999", "", ""); rec.Code != 404 {
		t.Errorf("unknown odds id = %d, want 404", rec.Code)
	}
}

package economy

import (
	"bytes"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"maps"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"cherry/data"
	"cherry/internal/opdata"
	"cherry/internal/store"
	"cherry/internal/thumbs"
)

// spinItems points the catalog at a root holding the prize items (a real PNG as each dp.png, animated where the spin
// data is) and the picture cache at a temp dir.
func spinItems(t *testing.T) {
	t.Helper()
	shopFixture(t, map[string][]string{
		"dress/225000263":    {"dp.png"},
		"custom/224400001":   {"dp.png"},
		"interior/120700737": {"dp.png"},
		"dress/225503895":    {"dp.png", "x.aniproj"},
		"dress/225503903":    {"dp.png", "x.aniproj"},
		"interior/120701269": {"dp.png", "a.aniproj"},
	})
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewNRGBA(image.Rect(0, 0, 8, 8))); err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{"dress/225000263", "custom/224400001", "interior/120700737", "dress/225503895", "dress/225503903", "interior/120701269"} {
		if err := os.WriteFile(filepath.Join(thumbs.DpItemRoot, filepath.FromSlash(d), "dp.png"), buf.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// spinSetup installs the account "gtok" (Gems and Cash) and a ledger directory for the tests.
func spinSetup(t *testing.T, gems, cash int64) (*store.Account, string) {
	t.Helper()
	spinItems(t)
	acc := gachaAccount(t, gems, nil)
	dir := gachaLedgerDir(t) // after the account: InstallAccounts clears the store path
	store.AccountsMu.Lock()
	acc.Cash = cash
	store.AccountsMu.Unlock()
	return acc, dir
}

// spinPin pins the spin clock to at for one test.
func spinPin(t *testing.T, at time.Time) {
	t.Helper()
	old := spinNow
	spinNow = func() time.Time { return at }
	t.Cleanup(func() { spinNow = old })
}

// spinRollAt makes the next draw land on the weight r (0 is the first slot's first unit).
func spinRollAt(t *testing.T, r int64) {
	t.Helper()
	old := spinRoll
	spinRoll = func(n int64) int64 {
		if r >= n {
			t.Fatalf("roll %d out of range %d", r, n)
		}
		return r
	}
	t.Cleanup(func() { spinRoll = old })
}

// spinDefault decodes a fresh copy of the embedded spin file (a test may edit it).
func spinDefault(t *testing.T) *Spin {
	t.Helper()
	s, err := opdata.Decode(data.SpinJSON, checkSpin)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// spinCall posts one play through the spin routes.
func spinCall(t *testing.T, body, token string) *httptest.ResponseRecorder {
	t.Helper()
	mux := http.NewServeMux()
	RegisterSpinRoutes(mux)
	req := httptest.NewRequest(http.MethodPost, "/v4/luckyspin/purchasenplay", strings.NewReader(body))
	if token != "" {
		req.Header.Set("Cookie", `AV_AUTH="`+token+`"`)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

// spinLuckyBody is a Lucky play as the client sends it (the native trailing NUL included).
func spinLuckyBody(price, free int64) string {
	return `{"spinSeq":1920000001,"setNo":1,"coinUnit":"GEM","payItemCd":"","price":` + strconv.FormatInt(price, 10) +
		`,"freeChance":` + strconv.FormatInt(free, 10) + `,"language":"en","deviceType":"Android"}` + "\x00"
}

// spinJackpotBody is a Jackpot play: tier 2 is isHighPrice with coinUnit CASH.
func spinJackpotBody(high bool, coin string, price int64) string {
	h := "false"
	if high {
		h = "true"
	}
	return `{"spinSeq":1920000002,"setNo":0,"coinUnit":"` + coin + `","payItemCd":"","price":` + strconv.FormatInt(price, 10) +
		`,"freeChance":0,"spinType":"JACKPOT","isHighPrice":` + h + `,"language":"en","deviceType":"Android"}` + "\x00"
}

// spinBalances reads the Gem and Cash balances under the lock.
func spinBalances(acc *store.Account) (gems, cash int64) {
	store.AccountsMu.Lock()
	defer store.AccountsMu.Unlock()
	return acc.Gems, acc.Cash
}

// spinReply returns the play reply's result object.
func spinReply(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	return gachaResultMap(t, rec.Body.Bytes())
}

func TestSpinDefaultsAreValidAndPayLessThanPrice(t *testing.T) {
	// The archive decides item art and sell-backs, so this runs against the device archive (as the butler checks do).
	s := spinDefault(t)
	if err := verifySpin(s); err != nil {
		t.Fatalf("default spin file refused: %v", err)
	}
	for _, c := range []struct {
		name string
		tier SpinTier
	}{{"lucky", s.Lucky.Tiers[0]}, {"jackpot tier 1", s.Jackpot.Tiers[0]}, {"jackpot tier 2", s.Jackpot.Tiers[1]}} {
		sum, total := spinPayout(c.tier)
		ev := float64(sum) / float64(total)
		t.Logf("%s: expected Gem payout %.2f per play, price %d %s (CASH tiers are not Gem-priced)", c.name, ev, c.tier.Price, c.tier.Currency)
		if c.tier.Currency == "GEM" && sum >= c.tier.Price*total {
			t.Errorf("%s: expected payout %.2f is not below the %d-Gem price", c.name, ev, c.tier.Price)
		}
	}
}

func TestSpinVerifyRefusesAPayoutAbovePrice(t *testing.T) {
	s := spinDefault(t)
	s.Lucky.Tiers[0].Slots[4].Weight = 90 // the 200-Gem slot dominates: expected payout far above 100
	if err := verifySpin(s); err == nil || !strings.Contains(err.Error(), "not below the 100-Gem price") {
		t.Fatalf("verify err = %v, want the payout refused", err)
	}
	s = spinDefault(t)
	s.Lucky.Tiers[0].Slots[5].Item = "CUTO00ZZZ" // a clothing code with no art in the archive
	if err := verifySpin(s); err == nil || !strings.Contains(err.Error(), "no art or price") {
		t.Fatalf("verify err = %v, want the item refused", err)
	}
}

func TestSpinCheckRefusesBadData(t *testing.T) {
	for _, c := range []struct {
		name   string
		mutate func(*Spin)
		want   string
	}{
		{"origin not cherry", func(s *Spin) { s.Origin = "mixed" }, "origin"},
		{"lucky id outside the block", func(s *Spin) { s.Lucky.ID = 1920001000 }, "outside the Cherry block"},
		{"same id twice", func(s *Spin) { s.Jackpot.ID = s.Lucky.ID }, "different ids"},
		{"free jackpot", func(s *Spin) { s.Jackpot.FreeDaily = true }, "no free Jackpot play"},
		{"seven slots", func(s *Spin) { s.Lucky.Tiers[0].Slots = s.Lucky.Tiers[0].Slots[:7] }, "want 8"},
		{"gems and item in one slot", func(s *Spin) { s.Lucky.Tiers[0].Slots[0].Item = "CUTO0007B" }, "not both or neither"},
		{"zero weight", func(s *Spin) { s.Lucky.Tiers[0].Slots[2].Weight = 0 }, "weight must be at least 1"},
		{"tier 2 not CASH", func(s *Spin) { s.Jackpot.Tiers[1].Currency = "GEM" }, "currency must be CASH"},
		{"pet item", func(s *Spin) { s.Lucky.Tiers[0].Slots[5].Item = "PUPE0000G" }, "not a clothing or room item code"},
		{"no en locale", func(s *Spin) { s.Lucky.Locales = s.Lucky.Locales[1:] }, "locales must include en"},
		{"tier of hair only", func(s *Spin) {
			for j := range s.Lucky.Tiers[0].Slots {
				s.Lucky.Tiers[0].Slots[j] = SpinSlot{Item: "CUHA00001", Weight: 1}
			}
		}, "no slot an animal can win"},
	} {
		s := spinDefault(t)
		c.mutate(s)
		if err := checkSpin(s); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want %q", c.name, err, c.want)
		}
	}
}

func TestSpinWheelNameFollowsPrizes(t *testing.T) {
	tier := SpinTier{Slots: []SpinSlot{{Gems: 20, Weight: 1}}}
	before := spinWheelName("lucky_wheel", tier)
	tier.Slots = []SpinSlot{{Gems: 40, Weight: 1}}
	if after := spinWheelName("lucky_wheel", tier); after == before {
		t.Fatalf("changed prizes kept the file name %s, so devices would keep the old wheel", after)
	}
	if _, ok := spinPicture(before); !ok {
		t.Fatalf("%s is not served", before)
	}
}

func TestSpinWheelSlotMapping(t *testing.T) {
	for p := 1; p <= 8; p++ {
		centre := spinSlotCentreDeg(p)
		// The client's stop angle (9-p)*45-22.5 turns slot p's centre to the arrow at 12 o'clock (0 degrees).
		stop := float64((9-p)*45) - 22.5
		if got := math.Mod(centre+stop, 360); math.Abs(got) > 1e-9 {
			t.Errorf("slot %d: centre %.1f + stop %.1f = %.1f, want 0", p, centre, stop, got)
		}
		if got := spinSectorAt(centre); got != p {
			t.Errorf("slot %d: sector at its centre is %d", p, got)
		}
		if spinSectorAt(centre-22) != p || spinSectorAt(centre+22) != p {
			t.Errorf("slot %d: the sector does not span its 45 degrees", p)
		}
	}
	// Over the wheel the drawn colour of the sector at each slot centre follows the slot's parity.
	colours := [2]color.NRGBA{{R: 1, A: 255}, {G: 1, A: 255}}
	img := spinWheel(spinDefault(t).Lucky.Tiers[0].Slots, colours)
	c := float64(spinWheelSize) / 2
	for p := 1; p <= 8; p++ {
		rad := spinSlotCentreDeg(p) * math.Pi / 180
		x, y := int(c+150*math.Sin(rad)), int(c-150*math.Cos(rad))
		want := colours[0]
		if p%2 == 0 {
			want = colours[1]
		}
		if got := img.NRGBAAt(x, y); got != want {
			t.Errorf("slot %d: wheel colour at its centre %v, want %v", p, got, want)
		}
	}
}

func TestSpinDrawFollowsTheWeights(t *testing.T) {
	slots := spinDefault(t).Lucky.Tiers[0].Slots
	for _, c := range []struct {
		roll int64
		want int
	}{{0, 0}, {34, 0}, {35, 1}, {84, 4}, {85, 4}, {86, 5}, {99, 7}} {
		spinRollAt(t, c.roll)
		if got := spinDraw(slots); got != c.want {
			t.Errorf("roll %d: slot index %d, want %d", c.roll, got, c.want)
		}
	}
}

// TestSpinAnimalNeverDrawsAHumanOnlyItem: an animal's copy of a table gives hair no weight, so every draw lands on the
// Gem slot at its own index, while a human's draws reach the hair. The table keeps its weights.
func TestSpinAnimalNeverDrawsAHumanOnlyItem(t *testing.T) {
	table := []SpinSlot{{Item: "CUHA00001", Weight: 100}, {Gems: 20, Weight: 1}}
	old := spinRoll
	t.Cleanup(func() { spinRoll = old })
	draws := func(animal bool) (hair, gem int) {
		var r int64
		spinRoll = func(n int64) int64 { return r % n }
		for ; r < 1000; r++ {
			switch spinDraw(spinSlotsFor(table, animal)) {
			case 0:
				hair++
			case 1:
				gem++
			default:
				t.Fatal("the draw left the wheel")
			}
		}
		return hair, gem
	}
	if hair, gem := draws(true); hair != 0 || gem != 1000 {
		t.Fatalf("animal: hair %d gem %d, want 0 and 1000", hair, gem)
	}
	if hair, _ := draws(false); hair == 0 {
		t.Fatal("human never drew the hair")
	}
	if table[0].Weight != 100 {
		t.Fatalf("the table's hair weight became %d", table[0].Weight)
	}
}

// TestSpinAnimalPlayNeverWinsHair: on the default Lucky wheel roll 92 is hair for a human (slot 7). With the hair weight
// zeroed for an animal the same roll lands on slot 8 (RUBE000KH, 3 units), still at the wheel's own index.
func TestSpinAnimalPlayNeverWinsHair(t *testing.T) {
	acc, _ := spinSetup(t, 500, 0)
	store.AccountsMu.Lock()
	acc.Gender = "ANIMAL"
	store.AccountsMu.Unlock()
	spinRollAt(t, 92)
	rec := spinCall(t, spinLuckyBody(100, 0), "gtok")
	if rec.Code != http.StatusOK {
		t.Fatalf("animal play: %d %s", rec.Code, rec.Body)
	}
	hist := spinReply(t, rec)["historyList"].([]any)[0].(map[string]any)
	if hist["itemCode"] != "RUBE000KH" || hist["showPriority"] != float64(8) {
		t.Fatalf("animal history %v, want slot 8 RUBE000KH", hist)
	}
}

func TestSpinLuckyGemPlayCharges100AndPays(t *testing.T) {
	acc, dir := spinSetup(t, 500, 0)
	spinRollAt(t, 0) // slot 1: 20 Gems
	rec := spinCall(t, spinLuckyBody(100, 0), "gtok")
	if rec.Code != http.StatusOK {
		t.Fatalf("play: %d %s", rec.Code, rec.Body)
	}
	if g, c := spinBalances(acc); g != 420 || c != 0 {
		t.Fatalf("balances after 100 spent, 20 won: gems %d cash %d, want 420 0", g, c)
	}
	res := spinReply(t, rec)
	hist := res["historyList"].([]any)[0].(map[string]any)
	if hist["showPriority"] != float64(1) || hist["rewardType"] != float64(3) || hist["rewardValue"] != float64(20) {
		t.Fatalf("history %v", hist)
	}
	if res["gemBalance"].(map[string]any)["totalCoinBalance"] != float64(420) || res["cashBalance"].(map[string]any)["totalCoinBalance"] != float64(0) {
		t.Fatalf("both balances must be sent: %v", res)
	}
	if l := gachaLedger(t, dir); len(l) != 2 || l[0].Reason != "spin:lucky" || l[0].Delta != -100 || l[1].Reason != "spin:lucky prize" || l[1].Delta != 20 || l[1].Balance != 420 {
		t.Fatalf("ledger %+v, want spin:lucky -100 then spin:lucky prize +20", l)
	}
}

// TestSpinEvenPlayStillLeavesLedgerLines: a 100-Gem play that wins 100 Gems nets zero but must stay on the record.
func TestSpinEvenPlayStillLeavesLedgerLines(t *testing.T) {
	acc, dir := spinSetup(t, 500, 0)
	spinRollAt(t, 75) // lucky slot 4: 100 Gems
	if rec := spinCall(t, spinLuckyBody(100, 0), "gtok"); rec.Code != http.StatusOK {
		t.Fatalf("play: %d %s", rec.Code, rec.Body)
	}
	if g, _ := spinBalances(acc); g != 500 {
		t.Fatalf("gems %d, want 500", g)
	}
	if l := gachaLedger(t, dir); len(l) != 2 || l[0].Delta != -100 || l[1].Delta != 100 {
		t.Fatalf("ledger %+v, want -100 then +100", l)
	}
}

func TestSpinInsufficientBalanceChangesNothing(t *testing.T) {
	acc, dir := spinSetup(t, 50, 0)
	rec := spinCall(t, spinLuckyBody(100, 0), "gtok")
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), `"errorCode":"60001"`) {
		t.Fatalf("insufficient: %d %s, want 400 60001", rec.Code, rec.Body)
	}
	if g, _ := spinBalances(acc); g != 50 || len(gachaLedger(t, dir)) != 0 {
		t.Fatalf("a refused play changed the account: gems %d", g)
	}
}

func TestSpinNoAccountUsesTheShopPath(t *testing.T) {
	spinSetup(t, 500, 0)
	rec := spinCall(t, spinLuckyBody(100, 0), "unknown")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown account: %d %s", rec.Code, rec.Body)
	}
}

func TestSpinFreePlayOncePerGameDay(t *testing.T) {
	acc, dir := spinSetup(t, 500, 0)
	noon := time.Date(2026, 10, 30, 12, 0, 0, 0, jst)
	spinPin(t, noon)
	spinRollAt(t, 0)

	// The first free play is charged nothing: the 20-Gem win is the only change.
	if rec := spinCall(t, spinLuckyBody(100, 1), "gtok"); rec.Code != http.StatusOK {
		t.Fatalf("free play: %d %s", rec.Code, rec.Body)
	}
	if g, _ := spinBalances(acc); g != 520 {
		t.Fatalf("free play gems %d, want 520", g)
	}
	if acc.LuckySpinDay != AttendDayKey(noon) {
		t.Fatalf("counter %d, want today's game day %d", acc.LuckySpinDay, AttendDayKey(noon))
	}

	// A second free request the same game day is refused, never charged.
	rec := spinCall(t, spinLuckyBody(100, 1), "gtok")
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), `"errorCode":"61003"`) {
		t.Fatalf("second free play: %d %s, want 400 61003", rec.Code, rec.Body)
	}
	if g, _ := spinBalances(acc); g != 520 {
		t.Fatalf("a refused free play changed gems: %d", g)
	}

	// A paid play the same day is charged its price.
	if rec := spinCall(t, spinLuckyBody(100, 0), "gtok"); rec.Code != http.StatusOK {
		t.Fatalf("paid play: %d %s", rec.Code, rec.Body)
	}
	if g, _ := spinBalances(acc); g != 440 {
		t.Fatalf("paid play gems %d, want 440", g)
	}

	// The next game day (06:00 JST) brings a new free play.
	spinPin(t, noon.Add(24*time.Hour))
	if rec := spinCall(t, spinLuckyBody(100, 1), "gtok"); rec.Code != http.StatusOK {
		t.Fatalf("next day's free play: %d %s", rec.Code, rec.Body)
	}
	// Four lines: the free win, the paid play's price and win, the next day's free win.
	if l := gachaLedger(t, dir); len(l) != 4 {
		t.Fatalf("ledger lines %d, want 4", len(l))
	}
}

func TestSpinStalePriceAndCoinRefused(t *testing.T) {
	acc, _ := spinSetup(t, 500, 0)
	for _, body := range []string{spinLuckyBody(99, 0), `{"spinSeq":1920000001,"coinUnit":"CASH","price":100,"freeChance":0}`} {
		rec := spinCall(t, body, "gtok")
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), `"errorCode":"61003"`) {
			t.Fatalf("stale play: %d %s, want 400 61003", rec.Code, rec.Body)
		}
	}
	if g, _ := spinBalances(acc); g != 500 {
		t.Fatalf("stale plays changed gems: %d", g)
	}
}

func TestSpinJackpotTiersChargeTheirCurrency(t *testing.T) {
	acc, dir := spinSetup(t, 300, 25)
	spinRollAt(t, 0) // slot 1 of tier 2: 100 Gems

	// Tier 2 is 10 earned Cash, and its prize is Gems.
	if rec := spinCall(t, spinJackpotBody(true, "CASH", 10), "gtok"); rec.Code != http.StatusOK {
		t.Fatalf("tier 2: %d %s", rec.Code, rec.Body)
	}
	if g, c := spinBalances(acc); g != 400 || c != 15 {
		t.Fatalf("tier 2: gems %d cash %d, want 400 15", g, c)
	}
	// Tier 1 is 100 Gems and pays its own slot (tier 1 slot 1: 20 Gems).
	if rec := spinCall(t, spinJackpotBody(false, "GEM", 100), "gtok"); rec.Code != http.StatusOK {
		t.Fatalf("tier 1: %d %s", rec.Code, rec.Body)
	}
	if g, c := spinBalances(acc); g != 320 || c != 15 {
		t.Fatalf("tier 1: gems %d cash %d, want 320 15", g, c)
	}
	// Each play logs its price, then its Gem win.
	l := gachaLedger(t, dir)
	if len(l) != 4 || l[0].Currency != "cash" || l[0].Delta != -10 || l[1].Reason != "spin:jackpot prize" || l[1].Delta != 100 ||
		l[2].Currency != "gems" || l[2].Delta != -100 || l[3].Delta != 20 {
		t.Fatalf("ledger %+v", l)
	}

	// Short Cash is refused with no change; a free Jackpot is refused.
	acc.Cash = 5
	if rec := spinCall(t, spinJackpotBody(true, "CASH", 10), "gtok"); rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), `"errorCode":"60001"`) {
		t.Fatalf("short cash: %d %s", rec.Code, rec.Body)
	}
	if rec := spinCall(t, strings.Replace(spinJackpotBody(false, "GEM", 100), `"freeChance":0`, `"freeChance":1`, 1), "gtok"); rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), `"errorCode":"61003"`) {
		t.Fatalf("free jackpot: %d %s", rec.Code, rec.Body)
	}
	// Tier 2's coin and price must be the data's: GEM at 100 is not tier 2.
	if rec := spinCall(t, spinJackpotBody(true, "GEM", 100), "gtok"); rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), `"errorCode":"61003"`) {
		t.Fatalf("tier 2 as GEM: %d %s", rec.Code, rec.Body)
	}
}

func TestSpinItemPrizeGrantsThenSellsBack(t *testing.T) {
	acc, dir := spinSetup(t, 500, 0)
	spinRollAt(t, 86) // lucky slot 6: CUTO0007B, sells back for its 150-Gem price
	if rec := spinCall(t, spinLuckyBody(100, 0), "gtok"); rec.Code != http.StatusOK {
		t.Fatalf("first item: %d %s", rec.Code, rec.Body)
	}
	store.AccountsMu.Lock()
	owned := store.AccountOwnedCodes(acc)
	store.AccountsMu.Unlock()
	if !containsCode(owned, "CUTO0007B") {
		t.Fatalf("the item was not granted: %v", owned)
	}
	if g, _ := spinBalances(acc); g != 400 {
		t.Fatalf("first item gems %d, want 400 (no sell-back for a new item)", g)
	}
	hist := spinReply(t, spinCall(t, spinLuckyBody(100, 0), "gtok"))["historyList"].([]any)[0].(map[string]any)
	if hist["itemCode"] != "CUTO0007B" || hist["rewardType"] != float64(1) {
		t.Fatalf("history %v", hist)
	}
	// The play's 100 is spent, then the duplicate pays its sell-back (150 Gems, one line of its own from grantItemLocked).
	if g, _ := spinBalances(acc); g != 450 {
		t.Fatalf("duplicate gems %d, want 450", g)
	}
	l := gachaLedger(t, dir)
	if n := len(l); n < 2 || l[n-2].Reason != "spin:lucky" || l[n-2].Delta != -100 || l[n-1].Reason != "spin:lucky sell-back CUTO0007B" || l[n-1].Delta != 150 {
		t.Fatalf("last ledger lines %+v, want spin:lucky -100 then sell-back +150", l)
	}
}

// TestSpinPicturesAreServed: every picture a list names is a PNG of the right size, and only Cherry's names are served.
func TestSpinPicturesAreServed(t *testing.T) {
	spinSetup(t, 500, 0)
	mux := http.NewServeMux()
	mux.HandleFunc("/cherry/spin/", HandleSpinImage)
	want := map[string][2]int{
		"lucky_wheel.png": {320, 320}, "jackpot_gem_wheel.png": {320, 320}, "jackpot_cash_wheel.png": {320, 320},
		"board.png": {320, 690}, "deco.png": {320, 690}, "list_bg.png": {320, 660}, "detail_cover.png": {320, 660},
		"title_clear.png": {120, 20}, "item/CUTO0007B.png": {8, 8},
	}
	for name, size := range want {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/cherry/spin/"+name, nil))
		if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "image/png" {
			t.Fatalf("%s: status %d type %q", name, rec.Code, rec.Header().Get("Content-Type"))
		}
		img, err := png.Decode(bytes.NewReader(rec.Body.Bytes()))
		if err != nil {
			t.Fatalf("%s: not a PNG: %v", name, err)
		}
		if b := img.Bounds(); b.Dx() != size[0] || b.Dy() != size[1] {
			t.Fatalf("%s: %dx%d, want %dx%d", name, b.Dx(), b.Dy(), size[0], size[1])
		}
	}
	for _, name := range []string{"gem.png", "cash.png"} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/cherry/spin/"+name, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: status %d", name, rec.Code)
		}
	}
	for _, name := range []string{"unknown.png", "item/CUTO00ZZZ.png", "item/CUTO0007B.txt"} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/cherry/spin/"+name, nil))
		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s: status %d, want 404", name, rec.Code)
		}
	}
}

func containsCode(codes []string, code string) bool {
	for _, c := range codes {
		if c == code {
			return true
		}
	}
	return false
}

// spinListSlots maps each item code in a spin list's reward groups (rewardList or rewardListHigh) to the wheel slot the
// group child carries (showPriority), for the account token.
func spinListSlots(t *testing.T, target, token, key string) map[string]float64 {
	t.Helper()
	mux := http.NewServeMux()
	RegisterSpinRoutes(mux)
	req := httptest.NewRequest(http.MethodGet, target, nil)
	req.Header.Set("Cookie", `AV_AUTH="`+token+`"`)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	var env struct {
		Result []map[string]any `json:"result"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); rec.Code != http.StatusOK || err != nil || len(env.Result) != 1 {
		t.Fatalf("%s: %d %s", target, rec.Code, rec.Body)
	}
	out := map[string]float64{}
	groups, _ := env.Result[0][key].([]any)
	for _, g := range groups {
		children, _ := g.(map[string]any)["detailList"].([]any)
		for _, c := range children {
			child := c.(map[string]any)
			if code, _ := child["itemCd"].(string); code != "" {
				out[code] = child["showPriority"].(float64)
			}
		}
	}
	return out
}

// TestSpinListsHideHumanOnlyFromAnimal: both spin lists are built per request. An animal's reward lists drop the hair
// its draw can never win (CUHA00001 on Lucky and Jackpot tier 1), keep every other item at the slot a human sees, and
// leave the Jackpot's tier 2 list (no hair) as it is for a human.
func TestSpinListsHideHumanOnlyFromAnimal(t *testing.T) {
	acc, _ := spinSetup(t, 0, 0)
	for _, c := range []struct {
		name, target, key string
		hair              bool
	}{
		{"lucky", "/v4/luckyspin/list/inprogress", "rewardList", true},
		{"jackpot tier 1", "/v4/luckyspin/jackpot/list/inprogress", "rewardList", true},
		{"jackpot tier 2", "/v4/luckyspin/jackpot/list/inprogress", "rewardListHigh", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			setGender := func(g string) {
				store.AccountsMu.Lock()
				acc.Gender = g
				store.AccountsMu.Unlock()
			}
			setGender("FEMALE")
			human := spinListSlots(t, c.target, "gtok", c.key)
			if _, ok := human["CUHA00001"]; ok != c.hair {
				t.Fatalf("human list has hair %v, want %v: %v", ok, c.hair, human)
			}
			setGender("ANIMAL")
			animal := spinListSlots(t, c.target, "gtok", c.key)
			delete(human, "CUHA00001")
			if !maps.Equal(animal, human) {
				t.Errorf("animal list = %v, want the human list without hair = %v", animal, human)
			}
		})
	}
}

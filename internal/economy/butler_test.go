package economy

import (
	"bytes"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"cherry/internal/store"
	"cherry/internal/testutil"
	"cherry/internal/thumbs"
)

// butlerAccount installs aid with Cash and Gems, saving into a temp dir (ledger.jsonl sits next to it).
func butlerAccount(t *testing.T, aid string, cash, gems int64) string {
	t.Helper()
	testutil.InstallAccounts(t, map[string]*store.Account{"btok-" + aid: {AccessToken: "btok-" + aid, SessionKey: "sk", Aid: aid, Name: "Butler", Cash: cash, Gems: gems}})
	dir := t.TempDir()
	store.AccountsMu.Lock()
	store.AccountStorePath = filepath.Join(dir, "accounts.json")
	store.AccountsMu.Unlock()
	return dir
}

// butlerState returns aid's Cash, Gems, butler book and bag stacks.
func butlerState(t *testing.T, aid string) (cash, gems int64, book store.ButlerBook, bag []store.GardenBagStack) {
	t.Helper()
	store.AccountsMu.Lock()
	defer store.AccountsMu.Unlock()
	acc := store.AccountByAidLocked(aid)
	if acc == nil {
		t.Fatalf("account %s missing", aid)
	}
	return acc.Cash, acc.Gems, butlerBookOf(acc), acc.GardenBag
}

// ledgerLines returns the raw lines of ledger.jsonl in dir.
func ledgerLines(t *testing.T, dir string) []string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, "ledger.jsonl"))
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	var out []string
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		if line != "" {
			out = append(out, line)
		}
	}
	return out
}

// ledgerReasons returns the reason of each ledger line in dir.
func ledgerReasons(t *testing.T, dir string) []string {
	t.Helper()
	var out []string
	for _, line := range ledgerLines(t, dir) {
		i := strings.Index(line, `"reason":"`)
		out = append(out, strings.TrimSuffix(line[i+len(`"reason":"`):], `"}`))
	}
	return out
}

// TestHireChargesRefusesAndFreeOnce: a hire is priced by the server and charged in earned Cash through the ledger, a
// short Cash balance or an unknown period is refused with nothing spent, the account's free first hire (3 days,
// once per account, whichever butler) costs nothing, and a hire of a butler under contract extends it from its end.
func TestHireChargesRefusesAndFreeOnce(t *testing.T) {
	dir := butlerAccount(t, "3101", 45, 0)
	now := time.Date(2026, 10, 28, 3, 0, 0, 0, time.UTC)
	const hazel, wren = 1900000001, 1900000002
	if v := ButlerViewOf("3101", now); !v.FreeOpen() {
		t.Fatal("a new account has its free first hire open")
	}

	// The free first hire: hazel for 3 days, no Cash.
	if got, err := HireButler("3101", hazel, ButlerCoinCash, 3, true, now); err != nil || !got.Free || got.Price != 0 {
		t.Fatalf("free hire: %+v err=%v", got, err)
	}
	if cash, _, book, _ := butlerState(t, "3101"); cash != 45 || !book.FreeUsed || len(book.Hired) != 1 {
		t.Fatalf("after free hire: cash=%d book=%+v", cash, book)
	}
	if v := ButlerViewOf("3101", now); v.FreeOpen() {
		t.Fatal("the free hire of hazel did not close the account's free offer for every butler")
	}

	// Once per account: wren's free hire is refused, and 3 days is no paid period.
	if _, err := HireButler("3101", wren, ButlerCoinCash, 3, true, now); err == nil {
		t.Fatal("second free hire of the account accepted")
	}

	// Paid hires are open after the free one: 7 days for 10 Cash extends hazel's 3-day contract from its end.
	got, err := HireButler("3101", hazel, ButlerCoinCash, 7, false, now.Add(time.Hour))
	if err != nil || got.Price != 10 || got.Free {
		t.Fatalf("paid hire after the free one: %+v err=%v", got, err)
	}
	if cash, _, book, _ := butlerState(t, "3101"); cash != 35 || len(book.Hired) != 1 || book.Hired[0].End != now.UnixMilli()+10*dayMillis {
		t.Fatalf("after paid hire: cash=%d hired=%+v", cash, book.Hired)
	}

	// 30 days for 30 Cash with 35 Cash is affordable; with 25 it is refused and nothing moves.
	if _, err := HireButler("3101", wren, ButlerCoinCash, 30, false, now); err != nil {
		t.Fatalf("30-day hire with 35 Cash: %v", err)
	}
	store.AccountsMu.Lock()
	store.AccountByAidLocked("3101").Cash = 25
	store.AccountsMu.Unlock()
	if _, err := HireButler("3101", hazel, ButlerCoinCash, 30, false, now); err == nil {
		t.Fatal("short Cash balance accepted")
	}
	if cash, _, book, _ := butlerState(t, "3101"); cash != 25 || len(book.Hired) != 2 {
		t.Fatalf("short balance changed state: cash=%d book=%+v", cash, book)
	}

	// Only earned Cash is charged: any other cost type is refused.
	if _, err := HireButler("3101", hazel, 4, 7, false, now); err == nil {
		t.Fatal("cost type 4 (Gems) accepted for a hire")
	}
	// A butler outside the roster is refused.
	if _, err := HireButler("3101", 1900000009, ButlerCoinCash, 7, false, now); err == nil {
		t.Fatal("unknown butler accepted")
	}

	// Two paid hires (7 days, 30 days) are in the ledger as Cash debits; the free hire is not.
	lines := ledgerLines(t, dir)
	if len(lines) != 2 || !strings.Contains(lines[0], `"currency":"cash"`) || !strings.Contains(lines[0], `"delta":-10`) {
		t.Fatalf("ledger = %v, want the 7-day Cash debit then the 30-day one", lines)
	}
	if reasons := ledgerReasons(t, dir); reasons[0] != "butler:hire:1900000001:7" || reasons[1] != "butler:hire:1900000002:30" {
		t.Fatalf("ledger reasons = %v", reasons)
	}
}

// TestLegacyFreeListCountsAsUsed: the live store wrote the free hire per butler (ButlerBook.Free). A non-empty legacy
// list counts as the account's free hire being used, and the next write saves the flag without the list.
func TestLegacyFreeListCountsAsUsed(t *testing.T) {
	butlerAccount(t, "3103", 45, 0)
	store.AccountsMu.Lock()
	store.AccountByAidLocked("3103").Butlers = &store.ButlerBook{Free: []int64{1900000001}}
	store.AccountsMu.Unlock()
	now := time.Date(2026, 10, 28, 3, 0, 0, 0, time.UTC)

	if v := ButlerViewOf("3103", now); v.FreeOpen() {
		t.Fatal("a legacy free list left the account's free offer open")
	}
	if _, err := HireButler("3103", 1900000002, ButlerCoinCash, 3, true, now); err == nil {
		t.Fatal("free hire accepted after a legacy free hire")
	}
	if _, err := HireButler("3103", 1900000002, ButlerCoinCash, 7, false, now); err != nil {
		t.Fatalf("paid hire after a legacy free hire: %v", err)
	}
	store.AccountsMu.Lock()
	stored := store.AccountByAidLocked("3103").Butlers
	store.AccountsMu.Unlock()
	if stored == nil || !stored.FreeUsed || stored.Free != nil {
		t.Fatalf("stored book after the write = %+v, want the flag and no legacy list", stored)
	}
	if cash, _, _, _ := butlerState(t, "3103"); cash != 35 {
		t.Fatalf("paid hire cash = %d, want 35", cash)
	}
}

// TestGatherPaysCappedGems: a gather pays one drop of each configured kind and Gems up to gems_cap a game day, and
// gathers_per_day refuses further gathers that day. The next game day starts again.
func TestGatherPaysCappedGems(t *testing.T) {
	dir := butlerAccount(t, "3102", 0, 0)
	now := time.Date(2026, 10, 28, 3, 0, 0, 0, time.UTC)
	store.AccountsMu.Lock()
	acc := store.AccountByAidLocked("3102")
	acc.Butlers = &store.ButlerBook{Hired: []store.ButlerHire{{ID: 1900000001, Begin: now.UnixMilli(), End: now.UnixMilli() + 60*dayMillis}}}
	store.AccountsMu.Unlock()
	first := func(n int) int { return 0 } // the first drop of each kind

	var gems []int64
	for i := range 11 {
		got, err := GatherButler("3102", 1900000001, now.Add(time.Duration(i)*time.Minute), first)
		if i == 10 {
			if err == nil {
				t.Fatal("11th gather of the day accepted")
			}
			break
		}
		if err != nil || len(got.Drops) != 2 {
			t.Fatalf("gather %d: %+v err=%v", i+1, got, err)
		}
		gems = append(gems, got.Gems)
	}
	if want := []int64{3, 3, 3, 1, 0, 0, 0, 0, 0, 0}; !equalInt64(gems, want) {
		t.Fatalf("gems per gather = %v, want %v", gems, want)
	}
	if _, total, _, bag := butlerState(t, "3102"); total != 10 || len(bag) != 2 || bag[0].Count != 10 {
		t.Fatalf("after 10 gathers: gems=%d bag=%+v", total, bag)
	}

	// The next game day (06:00 JST boundary) starts a new cap.
	next := now.Add(24 * time.Hour)
	if got, err := GatherButler("3102", 1900000001, next, first); err != nil || got.Gems != 3 {
		t.Fatalf("next day gather: %+v err=%v", got, err)
	}
	if _, total, _, _ := butlerState(t, "3102"); total != 13 {
		t.Fatalf("next day gems = %d, want 13", total)
	}
	// One ledger line per gather that paid Gems: 3, 3, 3, 1 on the first day and 3 on the next.
	if reasons := ledgerReasons(t, dir); len(reasons) != 5 || !strings.HasPrefix(reasons[0], "butler:gather:") {
		t.Fatalf("gather ledger reasons = %v", reasons)
	}
}

// TestButlerConfigRefusesIdOutsideCherryBlock: the roster must use Cherry-block ids, and an operator file that
// does not is refused at load (the embedded default is valid).
func TestButlerConfigRefusesIdOutsideCherryBlock(t *testing.T) {
	t.Cleanup(butlersFile.Reset)
	if b := ActiveButlers(); b.Origin != "cherry" || len(b.Butlers) != 3 {
		t.Fatalf("embedded butlers: origin=%q n=%d", b.Origin, len(b.Butlers))
	}
	for _, d := range ActiveButlers().Butlers {
		if !IsCherryButler(d.ID) || !strings.HasPrefix(d.Skin, "cherry_") {
			t.Fatalf("embedded butler %+v is not Cherry-made", d)
		}
	}

	valid := func(id int64) *Butlers {
		return &Butlers{Origin: "cherry", Note: "n", FreeDays: 3, Hire: []ButlerPrice{{Days: 7, Cash: 10}},
			Gathering: ButlerGathering{Kinds: []string{"essence"}, Gems: 3, GemsCap: 10, GathersPerDay: 10},
			Butlers:   []ButlerDef{{ID: id, Name: "X", Gender: "FEMALE", Skin: "cherry_x", Outfit: []string{"CUON004BH"}}}}
	}
	if err := checkButlers(valid(CherryButlerFirst)); err != nil {
		t.Fatalf("first id of the block: %v", err)
	}
	if err := checkButlers(valid(CherryButlerLast + 1)); err == nil || !strings.Contains(err.Error(), "outside the Cherry block") {
		t.Fatalf("id after the block: err = %v", err)
	}
	if err := checkButlers(valid(428499001)); err == nil || !strings.Contains(err.Error(), "outside the Cherry block") {
		t.Fatalf("item-block id: err = %v", err)
	}

	bad := filepath.Join(t.TempDir(), "butlers.json")
	raw := `{"origin":"cherry","note":"n","free_days":3,"hire":[{"days":7,"cash":10}],` +
		`"gathering":{"kinds":["ore"],"gems":3,"gems_cap":10,"gathers_per_day":10},` +
		`"butlers":[{"id":5,"name":"Low","gender":"MALE","skin":"cherry_low"}]}`
	if err := os.WriteFile(bad, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := butlersFile.LoadFrom(bad); err == nil || !strings.Contains(err.Error(), bad) {
		t.Fatalf("operator file with id 5: err = %v, want the path named", err)
	}
}

// TestButlerOutfitAndPortrait: the shipped outfits pass the load check against the archive's shop catalog, an outfit
// with a code the catalog lacks or without hair is refused, and /cherry/butler/<id>.png serves the portrait (256 px,
// cached under a temp dir). The archive is read in place, so the test skips where it is absent.
func TestButlerOutfitAndPortrait(t *testing.T) {
	if _, err := os.Stat(thumbs.DpItemRoot); err != nil {
		t.Skip("item archive absent")
	}
	oldCache := thumbs.DpCacheDir
	thumbs.DpCacheDir = t.TempDir()
	t.Cleanup(func() { thumbs.DpCacheDir = oldCache })

	if err := verifyButlers(ActiveButlers()); err != nil {
		t.Fatalf("shipped outfits: %v", err)
	}
	bad := *ActiveButlers()
	bad.Butlers = slices.Clone(bad.Butlers)
	bad.Butlers[0].Outfit = []string{"CUON00000", "CUHA0003L", "CUSH0005G"}
	if err := verifyButlers(&bad); err == nil || !strings.Contains(err.Error(), "not a shop item with art") {
		t.Fatalf("unknown outfit code: err = %v", err)
	}
	bad.Butlers[0].Outfit = []string{"CUON004BH", "CUSH0005G"}
	if err := verifyButlers(&bad); err == nil || !strings.Contains(err.Error(), "needs hair") {
		t.Fatalf("outfit without hair: err = %v", err)
	}

	rec := httptest.NewRecorder()
	HandleButlerImage(rec, httptest.NewRequest(http.MethodGet, "/cherry/butler/1900000001.png", nil))
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "image/png" {
		t.Fatalf("portrait: status %d type %q", rec.Code, rec.Header().Get("Content-Type"))
	}
	img, err := png.Decode(bytes.NewReader(rec.Body.Bytes()))
	if err != nil || img.Bounds().Dx() != 256 || img.Bounds().Dy() != 256 {
		t.Fatalf("portrait is not a 256 px PNG: %v", err)
	}
	missing := httptest.NewRecorder()
	HandleButlerImage(missing, httptest.NewRequest(http.MethodGet, "/cherry/butler/1999999999.png", nil))
	if missing.Code != http.StatusNotFound {
		t.Fatalf("unknown butler: status %d, want 404", missing.Code)
	}
}

// TestHireMakesFirstHireChief: a hire made while no chief runs becomes the chief; a later hire does not replace it.
func TestHireMakesFirstHireChief(t *testing.T) {
	butlerAccount(t, "3104", 100, 0)
	now := time.Date(2026, 10, 28, 3, 0, 0, 0, time.UTC)
	const hazel, wren = 1900000001, 1900000002

	if _, err := HireButler("3104", hazel, ButlerCoinCash, 7, false, now); err != nil {
		t.Fatalf("first hire: %v", err)
	}
	if _, _, book, _ := butlerState(t, "3104"); book.Leader != hazel {
		t.Fatalf("first hire leader = %d, want %d", book.Leader, hazel)
	}
	if _, err := HireButler("3104", wren, ButlerCoinCash, 7, false, now.Add(time.Minute)); err != nil {
		t.Fatalf("second hire: %v", err)
	}
	if _, _, book, _ := butlerState(t, "3104"); book.Leader != hazel {
		t.Fatalf("second hire replaced the chief: leader = %d", book.Leader)
	}
	if d, _, ok := ButlerChief("3104", now); !ok || d.ID != hazel {
		t.Fatalf("ButlerChief = %+v ok=%v, want hazel", d, ok)
	}
}

// TestChiefFallbackAndPromotion: a book with running contracts and no running chief reads its earliest running
// contract as chief, without a write. When the chief ran out, expiry promotes the contract that ends last.
func TestChiefFallbackAndPromotion(t *testing.T) {
	butlerAccount(t, "3105", 0, 0)
	const hazel, wren, oswin = 1900000001, 1900000002, 1900000003
	t0 := time.Date(2026, 10, 28, 3, 0, 0, 0, time.UTC)
	day := func(d float64) int64 { return t0.UnixMilli() + int64(d*float64(dayMillis)) }
	store.AccountsMu.Lock()
	store.AccountByAidLocked("3105").Butlers = &store.ButlerBook{Hired: []store.ButlerHire{
		{ID: hazel, Begin: day(0), End: day(3)},
		{ID: wren, Begin: day(0.04), End: day(10)},
		{ID: oswin, Begin: day(0.08), End: day(5)},
	}}
	store.AccountsMu.Unlock()

	// No stored chief: the earliest running contract (hazel) is the chief.
	if id := ButlerViewOf("3105", t0.Add(time.Hour)).Chief(); id != hazel {
		t.Fatalf("view chief = %d, want hazel (earliest running)", id)
	}
	if d, _, ok := ButlerChief("3105", t0.Add(time.Hour)); !ok || d.ID != hazel {
		t.Fatalf("ButlerChief = %+v ok=%v, want hazel", d, ok)
	}

	// Hazel ran out at day 3: expiry removes it and the contract that ends last (wren, day 10) takes over.
	now := time.UnixMilli(day(4))
	if id := ButlerViewOf("3105", now).Chief(); id != wren {
		t.Fatalf("after hazel ran out, view chief = %d, want wren (earliest of the running ones)", id)
	}
	gone, err := ExpireButlers("3105", now)
	if err != nil || len(gone) != 1 || gone[0].ID != hazel {
		t.Fatalf("expire: gone=%+v err=%v, want hazel", gone, err)
	}
	if _, _, book, _ := butlerState(t, "3105"); book.Leader != wren || len(book.Hired) != 2 {
		t.Fatalf("after expiry: leader=%d hired=%+v, want wren as chief with two contracts", book.Leader, book.Hired)
	}
}

// TestExpiryPromotesLatestEndingChief: when the chief's contract runs out and others remain, the one that ends last
// becomes the chief (the stored leader is replaced at expiry).
func TestExpiryPromotesLatestEndingChief(t *testing.T) {
	butlerAccount(t, "3106", 0, 0)
	const hazel, wren, oswin = 1900000001, 1900000002, 1900000003
	t0 := time.Date(2026, 10, 28, 3, 0, 0, 0, time.UTC)
	day := func(d float64) int64 { return t0.UnixMilli() + int64(d*float64(dayMillis)) }
	store.AccountsMu.Lock()
	store.AccountByAidLocked("3106").Butlers = &store.ButlerBook{Leader: hazel, Hired: []store.ButlerHire{
		{ID: hazel, Begin: day(0), End: day(3)},
		{ID: oswin, Begin: day(0.5), End: day(5)},
		{ID: wren, Begin: day(1), End: day(10)},
	}}
	store.AccountsMu.Unlock()

	gone, err := ExpireButlers("3106", time.UnixMilli(day(4)))
	if err != nil || len(gone) != 1 || gone[0].ID != hazel {
		t.Fatalf("expire: gone=%+v err=%v, want hazel", gone, err)
	}
	if _, _, book, _ := butlerState(t, "3106"); book.Leader != wren {
		t.Fatalf("leader after the chief ran out = %d, want wren (ends last)", book.Leader)
	}
}

func equalInt64(a, b []int64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

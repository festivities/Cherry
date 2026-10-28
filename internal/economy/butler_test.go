package economy

import (
	"bytes"
	"fmt"
	"image/png"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"cherry/internal/store"
	"cherry/internal/testutil"
)

// The roster (data/butlers.json: b1 Ophelia to b7 Michel) and the first Cherry roster that left it (Hazel, Wren, Oswin).
const (
	ophelia, ian, merry, michel = 1900000004, 1900000005, 1900000006, 1900000010
	hazel, wren, oswin          = 1900000001, 1900000002, 1900000003
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

// writeLedger replaces ledger.jsonl in dir with lines.
func writeLedger(t *testing.T, dir string, lines ...string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "ledger.jsonl"), []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

// cashLedgerLine is one ledger line of aid's Cash change at at.
func cashLedgerLine(aid string, at time.Time, delta int64, reason string) string {
	return fmt.Sprintf(`{"time":%q,"aid":%q,"currency":"cash","delta":%d,"balance":0,"reason":%q}`, at.UTC().Format(time.RFC3339), aid, delta, reason)
}

// TestHireChargesRefusesAndFreeOnce: a hire is priced by the server and charged in earned Cash through the ledger, a
// short Cash balance or an unknown period is refused with nothing spent, the account's free first hire (3 days,
// once per account, whichever butler) costs nothing, and a hire of a butler under contract extends it from its end.
func TestHireChargesRefusesAndFreeOnce(t *testing.T) {
	dir := butlerAccount(t, "3101", 75, 0)
	now := time.Date(2026, 10, 28, 3, 0, 0, 0, time.UTC)
	if v := ButlerViewOf("3101", now); !v.FreeOpen() {
		t.Fatal("a new account has its free first hire open")
	}

	// The free first hire: ophelia for 3 days, no Cash.
	if got, err := HireButler("3101", ophelia, ButlerCoinCash, 3, true, now); err != nil || !got.Free || got.Price != 0 {
		t.Fatalf("free hire: %+v err=%v", got, err)
	}
	if cash, _, book, _ := butlerState(t, "3101"); cash != 75 || !book.FreeUsed || len(book.Hired) != 1 {
		t.Fatalf("after free hire: cash=%d book=%+v", cash, book)
	}
	if v := ButlerViewOf("3101", now); v.FreeOpen() {
		t.Fatal("the free hire of ophelia did not close the account's free offer for every butler")
	}

	// Once per account: ian's free hire is refused, and 3 days is no paid period.
	if _, err := HireButler("3101", ian, ButlerCoinCash, 3, true, now); err == nil {
		t.Fatal("second free hire of the account accepted")
	}

	// A paid 30-day hire after the free one extends ophelia's 3-day contract from its end, for 30 Cash.
	got, err := HireButler("3101", ophelia, ButlerCoinCash, 30, false, now.Add(time.Hour))
	if err != nil || got.Price != 30 || got.Free {
		t.Fatalf("paid hire after the free one: %+v err=%v", got, err)
	}
	if cash, _, book, _ := butlerState(t, "3101"); cash != 45 || len(book.Hired) != 1 || book.Hired[0].End != now.UnixMilli()+33*dayMillis {
		t.Fatalf("after paid hire: cash=%d hired=%+v", cash, book.Hired)
	}

	// ian for 30 days with 45 Cash is affordable; then, with 25 Cash, ophelia's extension is refused and nothing moves.
	if _, err := HireButler("3101", ian, ButlerCoinCash, 30, false, now); err != nil {
		t.Fatalf("30-day hire with 45 Cash: %v", err)
	}
	store.AccountsMu.Lock()
	store.AccountByAidLocked("3101").Cash = 25
	store.AccountsMu.Unlock()
	if _, err := HireButler("3101", ophelia, ButlerCoinCash, 30, false, now); err == nil {
		t.Fatal("short Cash balance accepted")
	}
	if cash, _, book, _ := butlerState(t, "3101"); cash != 25 || len(book.Hired) != 2 {
		t.Fatalf("short balance changed state: cash=%d book=%+v", cash, book)
	}

	// Only earned Cash is charged: any other cost type is refused. A butler outside the roster is refused.
	if _, err := HireButler("3101", ophelia, 4, 30, false, now); err == nil {
		t.Fatal("cost type 4 (Gems) accepted for a hire")
	}
	if _, err := HireButler("3101", 1900000050, ButlerCoinCash, 30, false, now); err == nil {
		t.Fatal("unknown butler accepted")
	}

	// Two paid 30-day hires are in the ledger as Cash debits; the free hire is not.
	lines := ledgerLines(t, dir)
	if len(lines) != 2 || !strings.Contains(lines[0], `"currency":"cash"`) || !strings.Contains(lines[0], `"delta":-30`) {
		t.Fatalf("ledger = %v, want two Cash debits of 30", lines)
	}
	if reasons := ledgerReasons(t, dir); reasons[0] != "butler:hire:1900000004:30" || reasons[1] != "butler:hire:1900000005:30" {
		t.Fatalf("ledger reasons = %v", reasons)
	}
}

// TestLegacyFreeListCountsAsUsed: the live store wrote the free hire per butler (ButlerBook.Free). A non-empty legacy
// list counts as the account's free hire being used, and the next write saves the flag without the list.
func TestLegacyFreeListCountsAsUsed(t *testing.T) {
	butlerAccount(t, "3103", 45, 0)
	store.AccountsMu.Lock()
	store.AccountByAidLocked("3103").Butlers = &store.ButlerBook{Free: []int64{ophelia}}
	store.AccountsMu.Unlock()
	now := time.Date(2026, 10, 28, 3, 0, 0, 0, time.UTC)

	if v := ButlerViewOf("3103", now); v.FreeOpen() {
		t.Fatal("a legacy free list left the account's free offer open")
	}
	if _, err := HireButler("3103", ian, ButlerCoinCash, 3, true, now); err == nil {
		t.Fatal("free hire accepted after a legacy free hire")
	}
	if _, err := HireButler("3103", ian, ButlerCoinCash, 30, false, now); err != nil {
		t.Fatalf("paid hire after a legacy free hire: %v", err)
	}
	store.AccountsMu.Lock()
	stored := store.AccountByAidLocked("3103").Butlers
	store.AccountsMu.Unlock()
	if stored == nil || !stored.FreeUsed || stored.Free != nil {
		t.Fatalf("stored book after the write = %+v, want the flag and no legacy list", stored)
	}
	if cash, _, _, _ := butlerState(t, "3103"); cash != 15 {
		t.Fatalf("paid hire cash = %d, want 15", cash)
	}
}

// TestGatherPaysCappedGems: a gather pays one drop of each of the butler's abilities (ophelia: fish, essence, ore) and
// Gems up to gems_cap a game day, and gathers_per_day refuses further gathers that day. The next game day starts again.
func TestGatherPaysCappedGems(t *testing.T) {
	dir := butlerAccount(t, "3102", 0, 0)
	now := time.Date(2026, 10, 28, 3, 0, 0, 0, time.UTC)
	store.AccountsMu.Lock()
	acc := store.AccountByAidLocked("3102")
	acc.Butlers = &store.ButlerBook{Hired: []store.ButlerHire{{ID: ophelia, Begin: now.UnixMilli(), End: now.UnixMilli() + 60*dayMillis}}}
	store.AccountsMu.Unlock()
	first := func(n int) int { return 0 } // the first drop of each kind

	var gems []int64
	for i := range 11 {
		got, err := GatherButler("3102", ophelia, now.Add(time.Duration(i)*time.Minute), first)
		if i == 10 {
			if err == nil {
				t.Fatal("11th gather of the day accepted")
			}
			break
		}
		if err != nil || len(got.Drops) != 3 {
			t.Fatalf("gather %d: %+v err=%v", i+1, got, err)
		}
		gems = append(gems, got.Gems)
	}
	if want := []int64{3, 3, 3, 1, 0, 0, 0, 0, 0, 0}; !slices.Equal(gems, want) {
		t.Fatalf("gems per gather = %v, want %v", gems, want)
	}
	if _, total, _, bag := butlerState(t, "3102"); total != 10 || len(bag) != 3 || bag[0].Count != 10 {
		t.Fatalf("after 10 gathers: gems=%d bag=%+v", total, bag)
	}

	// The next game day (06:00 JST boundary) starts a new cap.
	next := now.Add(24 * time.Hour)
	if got, err := GatherButler("3102", ophelia, next, first); err != nil || got.Gems != 3 {
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

// TestButlerConfigRefusesIdOutsideCherryBlock: the roster must use Cherry-block ids that were never retired, and an
// operator file that does not is refused at load (the embedded default is valid). The hire list must hold only the
// 30-day period. Abilities and the portrait slot are checked too.
func TestButlerConfigRefusesIdOutsideCherryBlock(t *testing.T) {
	t.Cleanup(butlersFile.Reset)
	if b := ActiveButlers(); b.Origin != "mixed" || len(b.Butlers) != 7 {
		t.Fatalf("embedded butlers: origin=%q n=%d", b.Origin, len(b.Butlers))
	}
	for _, d := range ActiveButlers().Butlers {
		if !IsCherryButler(d.ID) || !strings.HasPrefix(d.Skin, "cherry_") {
			t.Fatalf("embedded butler %+v has a non-Cherry id or skin", d)
		}
	}

	valid := func(id int64) *Butlers {
		return &Butlers{Origin: "mixed", Note: "n", FreeDays: 3, Hire: []ButlerPrice{{Days: 30, Cash: 30}},
			Gathering: ButlerGathering{Gems: 3, GemsCap: 10, GathersPerDay: 10},
			Butlers: []ButlerDef{{ID: id, Name: "X", Gender: "FEMALE", Skin: "cherry_x", Outfit: []string{"CUON004BH"},
				Abilities: []string{"essence"}, Art: "b1"}}}
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
	for _, id := range []int64{hazel, wren, oswin} {
		if err := checkButlers(valid(id)); err == nil || !strings.Contains(err.Error(), "retired") {
			t.Fatalf("retired id %d: err = %v", id, err)
		}
	}

	// Only the 30-day period: the client's contract picker offers nothing else (CButtonHandler 0x22cfb84).
	for _, hire := range [][]ButlerPrice{{{Days: 7, Cash: 10}}, {{Days: 30, Cash: 30}, {Days: 7, Cash: 10}}, {}} {
		b := valid(CherryButlerFirst)
		b.Hire = hire
		if err := checkButlers(b); err == nil || !strings.Contains(err.Error(), "30-day") || !strings.Contains(err.Error(), "CButtonHandler 0x22cfb84") {
			t.Fatalf("hire %+v: err = %v, want the 30-day client rule named", hire, err)
		}
	}

	// The gather abilities are the three gather kinds, once each; the portrait slot and the gender are named.
	for _, mutate := range []func(d *ButlerDef){
		func(d *ButlerDef) { d.Abilities = []string{"wood"} },
		func(d *ButlerDef) { d.Abilities = []string{"fish", "fish"} },
		func(d *ButlerDef) { d.Abilities = nil },
		func(d *ButlerDef) { d.Art = "" },
		func(d *ButlerDef) { d.Gender = "OTHER" },
	} {
		b := valid(CherryButlerFirst)
		mutate(&b.Butlers[0])
		if err := checkButlers(b); err == nil {
			t.Fatalf("butler %+v accepted", b.Butlers[0])
		}
	}
	animal := valid(CherryButlerFirst)
	animal.Butlers[0].Gender = "ANIMAL"
	if err := checkButlers(animal); err != nil {
		t.Fatalf("ANIMAL gender refused: %v", err)
	}

	bad := filepath.Join(t.TempDir(), "butlers.json")
	raw := `{"origin":"mixed","note":"n","free_days":3,"hire":[{"days":30,"cash":30}],` +
		`"gathering":{"gems":3,"gems_cap":10,"gathers_per_day":10},` +
		`"butlers":[{"id":5,"name":"Low","gender":"MALE","skin":"cherry_low","outfit":["CUON004BH"],"abilities":["ore"],"art":"b1"}]}`
	if err := os.WriteFile(bad, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := butlersFile.LoadFrom(bad); err == nil || !strings.Contains(err.Error(), bad) {
		t.Fatalf("operator file with id 5: err = %v, want the path named", err)
	}
}

// TestButlerRosterIsOriginal: the shipped roster is the seven original butlers in order (b1 Ophelia to b7 Michel), each
// with a Cherry-made id in the block, its original portrait slot and a profile; Merry is the ANIMAL bunny. The retired
// Cherry ids are not in it.
func TestButlerRosterIsOriginal(t *testing.T) {
	want := []struct {
		id     int64
		name   string
		art    string
		gender string
	}{
		{ophelia, "Ophelia", "b1", "FEMALE"}, {ian, "Ian", "b2", "MALE"}, {merry, "Merry", "b3", "ANIMAL"},
		{1900000007, "Kai", "b4", "MALE"}, {1900000008, "Celina", "b5", "FEMALE"}, {1900000009, "Elena", "b6", "FEMALE"},
		{michel, "Michel", "b7", "MALE"},
	}
	got := ActiveButlers().Butlers
	if len(got) != len(want) {
		t.Fatalf("roster has %d butlers, want %d", len(got), len(want))
	}
	for i, w := range want {
		d := got[i]
		if d.ID != w.id || d.Name != w.name || d.Art != w.art || d.Gender != w.gender || d.Profile.Quote == "" || d.Profile.BloodType == "" {
			t.Fatalf("roster slot %d = %+v, want %+v", i, d, w)
		}
	}
	for _, id := range retiredButlerIDs {
		if _, ok := ButlerByID(id); ok {
			t.Fatalf("retired butler %d is still in the roster", id)
		}
	}
}

// TestButlerOutfitAndPortrait: the shipped outfits pass the load check against the archive's shop catalog and every
// portrait is in the archive. An outfit with a code the catalog lacks, or a portrait slot the archive lacks, is refused.
// /cherry/butler/<id>.png serves each roster butler's original portrait as it is (474 px). The archive is read in
// place, so the test skips where it is absent.
func TestButlerOutfitAndPortrait(t *testing.T) {
	if _, err := fs.Stat(butlerArtFS, "."); err != nil {
		t.Skip("UI archive absent")
	}
	if err := verifyButlers(ActiveButlers()); err != nil {
		t.Fatalf("shipped outfits and portraits: %v", err)
	}
	bad := *ActiveButlers()
	bad.Butlers = slices.Clone(bad.Butlers)
	bad.Butlers[0].Outfit = []string{"CUON00000"}
	if err := verifyButlers(&bad); err == nil || !strings.Contains(err.Error(), "not a shop item with art") {
		t.Fatalf("unknown outfit code: err = %v", err)
	}
	bad.Butlers[0].Outfit = ActiveButlers().Butlers[0].Outfit
	bad.Butlers[0].Art = "b99"
	if err := verifyButlers(&bad); err == nil || !strings.Contains(err.Error(), "not in the archive") {
		t.Fatalf("missing portrait: err = %v", err)
	}

	for _, d := range ActiveButlers().Butlers {
		rec := httptest.NewRecorder()
		HandleButlerImage(rec, httptest.NewRequest(http.MethodGet, "/cherry/butler/"+strconv.FormatInt(d.ID, 10)+".png", nil))
		want, err := fs.ReadFile(butlerArtFS, butlerArtFile(d))
		if err != nil || rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "image/png" || !bytes.Equal(rec.Body.Bytes(), want) {
			t.Fatalf("%s portrait: status %d type %q, not the original file (%v)", d.Name, rec.Code, rec.Header().Get("Content-Type"), err)
		}
		img, err := png.Decode(bytes.NewReader(rec.Body.Bytes()))
		if err != nil || img.Bounds().Dx() != 474 || img.Bounds().Dy() != 474 {
			t.Fatalf("%s portrait is not a 474 px PNG: %v", d.Name, err)
		}
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

	if _, err := HireButler("3104", ophelia, ButlerCoinCash, 30, false, now); err != nil {
		t.Fatalf("first hire: %v", err)
	}
	if _, _, book, _ := butlerState(t, "3104"); book.Leader != ophelia {
		t.Fatalf("first hire leader = %d, want %d", book.Leader, ophelia)
	}
	if _, err := HireButler("3104", ian, ButlerCoinCash, 30, false, now.Add(time.Minute)); err != nil {
		t.Fatalf("second hire: %v", err)
	}
	if _, _, book, _ := butlerState(t, "3104"); book.Leader != ophelia {
		t.Fatalf("second hire replaced the chief: leader = %d", book.Leader)
	}
	if d, _, ok := ButlerChief("3104", now); !ok || d.ID != ophelia {
		t.Fatalf("ButlerChief = %+v ok=%v, want ophelia", d, ok)
	}
}

// TestChiefFallbackAndPromotion: a book with running contracts and no running chief reads its earliest running
// contract as chief, without a write. When the chief ran out, expiry promotes the contract that ends last.
func TestChiefFallbackAndPromotion(t *testing.T) {
	butlerAccount(t, "3105", 0, 0)
	t0 := time.Date(2026, 10, 28, 3, 0, 0, 0, time.UTC)
	day := func(d float64) int64 { return t0.UnixMilli() + int64(d*float64(dayMillis)) }
	store.AccountsMu.Lock()
	store.AccountByAidLocked("3105").Butlers = &store.ButlerBook{Hired: []store.ButlerHire{
		{ID: ophelia, Begin: day(0), End: day(3)},
		{ID: ian, Begin: day(0.04), End: day(10)},
		{ID: merry, Begin: day(0.08), End: day(5)},
	}}
	store.AccountsMu.Unlock()

	// No stored chief: the earliest running contract (ophelia) is the chief.
	if id := ButlerViewOf("3105", t0.Add(time.Hour)).Chief(); id != ophelia {
		t.Fatalf("view chief = %d, want ophelia (earliest running)", id)
	}
	if d, _, ok := ButlerChief("3105", t0.Add(time.Hour)); !ok || d.ID != ophelia {
		t.Fatalf("ButlerChief = %+v ok=%v, want ophelia", d, ok)
	}

	// Ophelia ran out at day 3: expiry removes it and the contract that ends last (ian, day 10) takes over.
	now := time.UnixMilli(day(4))
	if id := ButlerViewOf("3105", now).Chief(); id != ian {
		t.Fatalf("after ophelia ran out, view chief = %d, want ian (earliest of the running ones)", id)
	}
	gone, err := ExpireButlers("3105", now)
	if err != nil || len(gone) != 1 || gone[0].ID != ophelia {
		t.Fatalf("expire: gone=%+v err=%v, want ophelia", gone, err)
	}
	if _, _, book, _ := butlerState(t, "3105"); book.Leader != ian || len(book.Hired) != 2 {
		t.Fatalf("after expiry: leader=%d hired=%+v, want ian as chief with two contracts", book.Leader, book.Hired)
	}
}

// TestExpiryPromotesLatestEndingChief: when the chief's contract runs out and others remain, the one that ends last
// becomes the chief (the stored leader is replaced at expiry).
func TestExpiryPromotesLatestEndingChief(t *testing.T) {
	butlerAccount(t, "3106", 0, 0)
	t0 := time.Date(2026, 10, 28, 3, 0, 0, 0, time.UTC)
	day := func(d float64) int64 { return t0.UnixMilli() + int64(d*float64(dayMillis)) }
	store.AccountsMu.Lock()
	store.AccountByAidLocked("3106").Butlers = &store.ButlerBook{Leader: ophelia, Hired: []store.ButlerHire{
		{ID: ophelia, Begin: day(0), End: day(3)},
		{ID: merry, Begin: day(0.5), End: day(5)},
		{ID: ian, Begin: day(1), End: day(10)},
	}}
	store.AccountsMu.Unlock()

	gone, err := ExpireButlers("3106", time.UnixMilli(day(4)))
	if err != nil || len(gone) != 1 || gone[0].ID != ophelia {
		t.Fatalf("expire: gone=%+v err=%v, want ophelia", gone, err)
	}
	if _, _, book, _ := butlerState(t, "3106"); book.Leader != ian {
		t.Fatalf("leader after the chief ran out = %d, want ian (ends last)", book.Leader)
	}
}

// TestMigrateButlerRosterRefundsOnce: the retired butlers' contracts leave every book once. An unexpired paid contract
// is refunded what the ledger shows its hires charged (an earlier contract of the same butler is not counted); the free
// hire and an expired contract refund nothing; the chief moves to the roster contract left; the free-hire flag stays
// used; a second run changes nothing.
func TestMigrateButlerRosterRefundsOnce(t *testing.T) {
	const aid = "3110"
	dir := butlerAccount(t, aid, 30, 0)
	now := time.Date(2026, 10, 28, 3, 0, 0, 0, time.UTC)
	wrenBegin := now.Add(-24 * time.Hour)
	writeLedger(t, dir,
		cashLedgerLine(aid, now.Add(-40*24*time.Hour), -10, "butler:hire:1900000002:7"), // wren's earlier, expired contract
		cashLedgerLine(aid, wrenBegin, -10, "butler:hire:1900000002:7"),                 // wren's current contract
		cashLedgerLine(aid, now.Add(-2*time.Hour), -30, "butler:hire:1900000004:30"),    // ophelia: in the roster
	)
	store.AccountsMu.Lock()
	store.AccountByAidLocked(aid).Butlers = &store.ButlerBook{Leader: wren, FreeUsed: true, Hired: []store.ButlerHire{
		{ID: hazel, Begin: now.Add(-time.Hour).UnixMilli(), End: now.Add(48 * time.Hour).UnixMilli()},
		{ID: wren, Begin: wrenBegin.UnixMilli(), End: now.Add(6 * 24 * time.Hour).UnixMilli()},
		{ID: oswin, Begin: now.Add(-20 * 24 * time.Hour).UnixMilli(), End: now.Add(-time.Hour).UnixMilli()},
		{ID: ophelia, Begin: now.Add(-2 * time.Hour).UnixMilli(), End: now.Add(30 * 24 * time.Hour).UnixMilli()},
	}}
	store.AccountsMu.Unlock()

	if err := MigrateButlerRoster(now); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if cash, _, book, _ := butlerState(t, aid); cash != 40 {
		t.Fatalf("cash after the migration = %d, want 30 + 10 (wren's current paid contract)", cash)
	} else if len(book.Hired) != 1 || book.Hired[0].ID != ophelia || book.Leader != ophelia || !book.FreeUsed || book.Free != nil {
		t.Fatalf("book after the migration = %+v", book)
	}
	reasons := ledgerReasons(t, dir)
	if len(reasons) != 4 || reasons[3] != "butler:refund:1900000002" {
		t.Fatalf("ledger reasons = %v, want the refund of wren as the last line", reasons)
	}
	if v := ButlerViewOf(aid, now); v.Chief() != ophelia {
		t.Fatalf("chief after the migration = %d, want ophelia", v.Chief())
	}

	// Second run: nothing left to retire, nothing refunded.
	if err := MigrateButlerRoster(now); err != nil {
		t.Fatalf("second migrate: %v", err)
	}
	if cash, _, _, _ := butlerState(t, aid); cash != 40 || len(ledgerReasons(t, dir)) != 4 {
		t.Fatalf("second run changed the account: cash=%d ledger=%v", cash, ledgerReasons(t, dir))
	}
}

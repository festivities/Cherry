package economy

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"cherry/data"
)

// resetOperatorData drops every loaded operator file, so reads see the embedded defaults again.
func resetOperatorData() {
	pricesFile.Reset()
	gachaFile.Reset()
	gatheringFile.Reset()
	fishingFile.Reset()
	calendarsFile.Reset()
	rewardsFile.Reset()
	profilesFile.Reset()
	atelierFile.Reset()
}

// operatorDir points the operator config directory at an empty temp dir (os.UserCacheDir reads
// %LOCALAPPDATA% on Windows and XDG_CACHE_HOME elsewhere), resets the files when the test ends, and
// returns the directory.
func operatorDir(t *testing.T) string {
	t.Helper()
	base := t.TempDir()
	t.Setenv("LOCALAPPDATA", base)
	t.Setenv("XDG_CACHE_HOME", base)
	dir := filepath.Join(base, "Cherry", "config")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(resetOperatorData)
	return dir
}

// mutateDefault returns the embedded default raw with each old/new pair replaced once, in order. Every
// old text must occur, or the test would pass on the untouched default.
func mutateDefault(t *testing.T, raw []byte, pairs ...string) string {
	t.Helper()
	out := raw
	for i := 0; i < len(pairs); i += 2 {
		if !bytes.Contains(out, []byte(pairs[i])) {
			t.Fatalf("default has no %q to mutate", pairs[i])
		}
		out = bytes.Replace(out, []byte(pairs[i]), []byte(pairs[i+1]), 1)
	}
	return string(out)
}

// refuseFile writes text as the operator file name and runs load on it: the load must fail, name the
// file and the bad entry (want).
func refuseFile(t *testing.T, load func(string) error, name, text, want string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	err := load(path)
	if err == nil || !strings.Contains(err.Error(), want) || !strings.Contains(err.Error(), path) {
		t.Fatalf("%s: err = %v, want %q naming %s", name, err, want, path)
	}
}

// TestOperatorDefaultsLoad: every embedded default passes the startup checks (archive included), with no
// operator files present.
func TestOperatorDefaultsLoad(t *testing.T) {
	requireArchive(t)
	operatorDir(t)
	if err := LoadOperatorData(); err != nil {
		t.Fatalf("defaults: %v", err)
	}
	if g := ActiveGacha(); g.ID != 1001 || g.Name != "Cherry Gacha" {
		t.Fatalf("gacha default = %+v", g)
	}
}

func TestPricesFileRefusals(t *testing.T) {
	t.Cleanup(resetOperatorData)
	raw := data.PricesJSON
	cases := []struct{ text, want string }{
		{mutateDefault(t, raw, `"clothing_prices"`, `"clothing_pricez"`), "unknown field"},
		{mutateDefault(t, raw, `"pet_price": 1200`, `"pet_price": -1`), "pet_price must not be negative"},
		{mutateDefault(t, raw, `"sell_back_percent": 100`, `"sell_back_percent": 150`), "sell_back_percent must be 0 to 100"},
		{mutateDefault(t, raw, `"HA": 200`, `"ha": 200`), "must be two capital letters"},
		{mutateDefault(t, raw, `"code": "FSV5"`, `"code": "FSV1"`), "code must be unique"},
	}
	for _, c := range cases {
		refuseFile(t, pricesFile.LoadFrom, "prices.json", c.text, c.want)
	}
}

func TestGachaFileRefusals(t *testing.T) {
	t.Cleanup(resetOperatorData)
	raw := data.GachaJSON
	cases := []struct{ text, want string }{
		{mutateDefault(t, raw, `"rare_percent": 10`, `"rare_percent": 101`), "rare_percent must be 0 to 100"},
		{mutateDefault(t, raw, `"kinds": ["fashion", "interior"]`, `"kinds": ["clothes"]`), `kinds: "clothes" must be fashion or interior`},
		{mutateDefault(t, raw, `"exclude": []`, `"exclude": ["XXXX"]`), `exclude: "XXXX" is not a sellable item code`},
		{mutateDefault(t, raw, `"id": 1001`, `"id": 0`), "id must be positive"},
	}
	for _, c := range cases {
		refuseFile(t, gachaFile.LoadFrom, "gacha.json", c.text, c.want)
	}
}

func TestGatheringFileRefusals(t *testing.T) {
	t.Cleanup(resetOperatorData)
	raw := data.GatheringJSON
	zeroEssence := mutateDefault(t, raw,
		`"item": 210000, "name": "Dew Essence", "weight": 60`, `"item": 210000, "name": "Dew Essence", "weight": 0`,
		`"item": 220000, "name": "Bloom Essence", "weight": 30`, `"item": 220000, "name": "Bloom Essence", "weight": 0`,
		`"item": 230000, "name": "Star Essence", "weight": 10`, `"item": 230000, "name": "Star Essence", "weight": 0`)
	cases := []struct{ text, want string }{
		{mutateDefault(t, raw, `"own_cap": 20`, `"own_cap": -1`), "own_cap must not be negative"},
		{zeroEssence, "drops essence: weights must sum above 0"},
		{mutateDefault(t, raw, `"essence": [`, `"essences": [`), `kind "essences" is not fish, essence or ore`},
		{mutateDefault(t, raw, `"item": 321000`, `"item": 310000`), "item 310000 must be unique"},
		{mutateDefault(t, raw, `"fish_length_max": 70`, `"fish_length_max": 5`), "fish_length_max at least fish_length_min"},
	}
	for _, c := range cases {
		refuseFile(t, gatheringFile.LoadFrom, "gathering.json", c.text, c.want)
	}
}

func TestFishingFileRefusals(t *testing.T) {
	t.Cleanup(resetOperatorData)
	raw := data.FishingJSON
	noRare := mutateDefault(t, raw,
		`"rare": true, "tint": "#ffcd1e"`, `"rare": false, "tint": "#ffcd1e"`,
		`"rare": true, "tint": "#78e1ff"`, `"rare": false, "tint": "#78e1ff"`)
	cases := []struct{ text, want string }{
		{noRare, "needs at least one normal and one rare fish"},
		{mutateDefault(t, raw, `"tint": "#f08228"`, `"tint": "red"`), "must be #rrggbb"},
		{mutateDefault(t, raw, `"kind": "bait", "price": 40`, `"kind": "boat", "price": 40`), `kind must be rod or bait, not "boat"`},
		{mutateDefault(t, raw, `"daily_cap": 150`, `"daily_cap": -5`), "daily_cap must not be negative"},
	}
	for _, c := range cases {
		refuseFile(t, fishingFile.LoadFrom, "fishing.json", c.text, c.want)
	}
}

func TestCalendarsFileRefusals(t *testing.T) {
	t.Cleanup(resetOperatorData)
	requireArchive(t)
	raw := data.CalendarsJSON
	cases := []struct{ text, want string }{
		{mutateDefault(t, raw, `"ink": "#606094"`, `"ink": "606094"`), `normal: colour "606094" must be #rrggbb`},
		{mutateDefault(t, raw, `"start": "01-01"`, `"start": "02-30"`), "must be a date MM-DD"},
		{mutateDefault(t, raw, `{"gems": 450, "cash": 15, "tickets": 3, "item": "RUCH004RC"}`,
			`{"gems": 450, "cash": 15, "tickets": 3, "item": "RUCH004RC"}, {"gems": 1}`), "cycle must have 7 rows, not 8"},
		// A season item day the archive cannot serve (no art) is refused at startup.
		{mutateDefault(t, raw, `"item": "CUAE00737"`, `"item": "CUAE0000X"`), "season winter day 3: item CUAE0000X has no art"},
		{mutateDefault(t, raw, `"CAAE007BS"`, `"CUAE0072Z", "CAAE007BS"`), "limited CUAE0072Z is also listed in season winter"},
	}
	for _, c := range cases {
		refuseFile(t, calendarsFile.LoadFrom, "calendars.json", c.text, c.want)
	}
}

// TestLimitedItemWithoutArtStillStarts: a limited code without art is logged, not refused (the archive may
// be missing assets), and the file loads.
func TestLimitedItemWithoutArtStillStarts(t *testing.T) {
	t.Cleanup(resetOperatorData)
	requireArchive(t)
	path := filepath.Join(t.TempDir(), "calendars.json")
	text := mutateDefault(t, data.CalendarsJSON, `"CUAE009EA"`, `"CUAE0000X"`)
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := calendarsFile.LoadFrom(path); err != nil {
		t.Fatalf("limited without art: %v", err)
	}
}

func TestRewardsFileRefusals(t *testing.T) {
	t.Cleanup(resetOperatorData)
	raw := data.RewardsJSON
	cases := []struct{ text, want string }{
		{mutateDefault(t, raw, `"display": true}`, `"display": true}, {"id": 9006, "key": "", "name": "Second", "desc": "", "display": true}`), "quests need exactly one hearts row"},
		{mutateDefault(t, raw, `"key": "square"`, `"key": "diary"`), `quest 9004: key "diary" must be unique`},
		{mutateDefault(t, raw, `"display": true}`, `"display": true, "gems": 5}`), "the hearts row pays hearts.reward_gems"},
		{mutateDefault(t, raw, `"max_rows": 100`, `"max_rows": 0`), "mail.max_rows must be at least 1"},
	}
	for _, c := range cases {
		refuseFile(t, rewardsFile.LoadFrom, "rewards.json", c.text, c.want)
	}
}

func TestProfilesFileRefusals(t *testing.T) {
	t.Cleanup(resetOperatorData)
	raw := data.ProfilesJSON
	cases := []struct{ text, want string }{
		{mutateDefault(t, raw, `"1151262555912366100": "MALE"`, `"1151262555912366100": "ROBOT"`), "type must be MALE, FEMALE or ANIMAL"},
		{mutateDefault(t, raw, `"1151262590612422506"`, `"11512x590612422506"`), "id must be digits"},
	}
	for _, c := range cases {
		refuseFile(t, profilesFile.LoadFrom, "profiles.json", c.text, c.want)
	}
}

// TestOperatorOverridesReplaceDefaults: an operator file in the config dir replaces the default through
// the startup path, and the values it changes are the ones the game reads.
func TestOperatorOverridesReplaceDefaults(t *testing.T) {
	requireArchive(t)
	dir := operatorDir(t)
	defaultID := NormalScheduleID()
	write := func(name, text string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("gacha.json", mutateDefault(t, data.GachaJSON, `"name": "Cherry Gacha"`, `"name": "Operator Gacha"`, `"exclude": []`, `"exclude": ["CUON004TV"]`))
	write("prices.json", mutateDefault(t, data.PricesJSON, `"HA": 200`, `"HA": 999`))
	write("calendars.json", mutateDefault(t, data.CalendarsJSON, `{"gems": 90},`, `{"gems": 91},`))
	if err := LoadOperatorData(); err != nil {
		t.Fatalf("overrides: %v", err)
	}
	if g := ActiveGacha(); g.Name != "Operator Gacha" {
		t.Fatalf("gacha name = %q, want the operator's", g.Name)
	}
	if base, _ := classify("CUHA0036Z"); base != 999 {
		t.Fatalf("clothing HA price = %d, want the operator's 999", base)
	}
	if NormalScheduleID() == defaultID {
		t.Fatal("a changed normal cycle must change the calendar's skin version")
	}
	for _, it := range gachaPoolNow().normal {
		if it.code == "CUON004TV" {
			t.Fatal("an excluded gacha item is still in the pool")
		}
	}
	for _, it := range gachaPoolNow().rare {
		if it.code == "CUON004TV" {
			t.Fatal("an excluded gacha item is still in the RARE tier")
		}
	}
}

// TestOperatorBadFileStopsStartup: LoadOperatorData refuses a bad operator file and names it.
func TestOperatorBadFileStopsStartup(t *testing.T) {
	dir := operatorDir(t)
	bad := filepath.Join(dir, "fishing.json")
	if err := os.WriteFile(bad, []byte(`{"daily_cap": 1, "fish": []}`), 0o600); err != nil {
		t.Fatal(err)
	}
	err := LoadOperatorData()
	if err == nil || !strings.Contains(err.Error(), bad) {
		t.Fatalf("err = %v, want the bad file %s named", err, bad)
	}
}

// TestSellBackPercentApplies: an owned item pays sell_back_percent of its price, and a deal item what it
// was bought for (the default 100 keeps the table price).
func TestSellBackPercentApplies(t *testing.T) {
	requireArchive(t)
	t.Cleanup(resetOperatorData)
	price, _, _, _ := ItemPrice("CUHA0036Z")
	if got := soldPrice("CUHA0036Z", 0); got != price {
		t.Fatalf("default sell-back = %d, want the table price %d", got, price)
	}
	dir := operatorDir(t)
	if err := os.WriteFile(filepath.Join(dir, "prices.json"), []byte(mutateDefault(t, data.PricesJSON, `"sell_back_percent": 100`, `"sell_back_percent": 50`)), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := LoadOperatorData(); err != nil {
		t.Fatal(err)
	}
	if got := soldPrice("CUHA0036Z", 0); got != price/2 {
		t.Fatalf("sell-back at 50%% = %d, want %d", got, price/2)
	}
	if got := soldPrice("CUHA0036Z", price-10); got != (price-10)/2 {
		t.Fatalf("deal item sell-back = %d, want half its paid price %d", got, (price-10)/2)
	}
}

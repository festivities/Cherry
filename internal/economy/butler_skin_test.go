package economy

import (
	"bytes"
	"slices"
	"strings"
	"testing"
	"time"

	"cherry/internal/store"
	"cherry/internal/testutil"
)

// merrySilver is Merry's first further skin (Silver Beret, butler-robo-spec 2.5: skin_1604557276).
const merrySilver = 1900001601

// butlersWith is a one-butler config for checkButlers: the default skin of d plus extra.
func butlersWith(d ButlerDef) *Butlers {
	return &Butlers{Origin: "mixed", Note: "n", FreeDays: 3, Hire: []ButlerPrice{{Days: 30, Cash: 30}},
		Gathering: ButlerGathering{Gems: 3, GemsCap: 10, GathersPerDay: 10, DurationMinutes: 60},
		Butlers:   []ButlerDef{d}}
}

// validMerry is a roster-shaped butler with a default skin and one further skin, for the skin checks.
func validMerry(extra ButlerSkin) ButlerDef {
	return ButlerDef{ID: merry, Name: "Merry", Gender: "ANIMAL", Abilities: []string{"ore"}, Art: "b3",
		Skins: []ButlerSkin{{ID: DefaultSkinID(merry), Code: "cherry_merry", Name: "Merry", Outfit: []string{"CUON009CV"}}, extra}}
}

// TestBuyButlerSkinChargesOnceAndPersists: a further skin costs its Cash price once, through the ledger, only for a butler
// under contract; the default and a bought skin are refused again (nothing charged); a short balance is refused with
// nothing spent; the ownership is saved in the account's row.
func TestBuyButlerSkinChargesOnceAndPersists(t *testing.T) {
	dir := butlerAccount(t, "3201", 100, 0)
	now := time.Date(2026, 11, 16, 3, 0, 0, 0, time.UTC)
	if err := BuyButlerSkin("3201", merry, merrySilver, now); err == nil {
		t.Fatal("a skin of a butler not under contract was bought")
	}
	if _, err := HireButler("3201", merry, ButlerCoinCash, 30, false, now); err != nil {
		t.Fatalf("hire merry: %v", err)
	}
	for _, skin := range []int64{DefaultSkinID(merry), ophelia, 1900001699} {
		if err := BuyButlerSkin("3201", merry, skin, now); err == nil {
			t.Fatalf("skin %d bought: the default is owned and an unknown or other butler's skin is refused", skin)
		}
	}
	if err := BuyButlerSkin("3201", merry, merrySilver, now); err != nil {
		t.Fatalf("buy the silver beret: %v", err)
	}
	if cash, _, _, _ := butlerState(t, "3201"); cash != 70-30 {
		t.Fatalf("cash after the buy = %d, want 40", cash)
	}
	if err := BuyButlerSkin("3201", merry, merrySilver, now); err == nil {
		t.Fatal("a skin bought twice")
	}
	if cash, _, _, _ := butlerState(t, "3201"); cash != 40 {
		t.Fatalf("a refused second buy charged: cash = %d", cash)
	}
	if reasons := ledgerReasons(t, dir); len(reasons) != 2 || reasons[1] != "butler:skin:1900000006:1900001601" {
		t.Fatalf("ledger reasons = %v, want the hire then one skin debit", reasons)
	}
	v := ButlerViewOf("3201", now)
	d, _ := ButlerByID(merry)
	if !v.Owns(d, merrySilver) || v.Worn(d).ID != DefaultSkinID(merry) {
		t.Fatalf("after the buy: owned=%v worn=%d, want owned and still the default worn", v.Owns(d, merrySilver), v.Worn(d).ID)
	}
	if row := testutil.AccountRowText(t, "3201"); !bytes.Contains(row, []byte(`"skins":[{"butler":1900000006,"skin":1900001601}]`)) {
		t.Fatalf("the skin is not in the saved account row: %s", row)
	}

	// A short balance: the next skin costs 30 and the account has 10.
	store.AccountsMu.Lock()
	store.AccountByAidLocked("3201").Cash = 10
	store.AccountsMu.Unlock()
	if err := BuyButlerSkin("3201", merry, merrySilver+1, now); err == nil {
		t.Fatal("a skin bought with a short balance")
	}
	if cash, _, book, _ := butlerState(t, "3201"); cash != 10 || len(book.Skins) != 1 {
		t.Fatalf("a short balance changed state: cash=%d skins=%+v", cash, book.Skins)
	}
}

// TestEquipButlerSkinWearsOwnedOnly: only an owned skin (the default always is) is worn, of a butler under contract. One
// skin is worn per butler at a time; the default takes the wear back and the bought skin stays owned.
func TestEquipButlerSkinWearsOwnedOnly(t *testing.T) {
	butlerAccount(t, "3202", 100, 0)
	now := time.Date(2026, 11, 16, 3, 0, 0, 0, time.UTC)
	d, _ := ButlerByID(merry)
	worn := func() int64 { return ButlerViewOf("3202", now).Worn(d).ID }
	if _, err := HireButler("3202", merry, ButlerCoinCash, 30, false, now); err != nil {
		t.Fatalf("hire merry: %v", err)
	}
	if err := EquipButlerSkin("3202", merry, merrySilver, now); err == nil {
		t.Fatal("an unowned skin was worn")
	}
	if err := BuyButlerSkin("3202", merry, merrySilver, now); err != nil {
		t.Fatalf("buy: %v", err)
	}
	if err := EquipButlerSkin("3202", merry, merrySilver, now); err != nil {
		t.Fatalf("wear the bought skin: %v", err)
	}
	if worn() != merrySilver {
		t.Fatalf("worn = %d, want the silver beret", worn())
	}
	if err := EquipButlerSkin("3202", merry, ophelia, now); err == nil {
		t.Fatal("another butler's skin id was worn")
	}
	if err := EquipButlerSkin("3202", merry, DefaultSkinID(merry), now); err != nil {
		t.Fatalf("wear the default: %v", err)
	}
	if worn() != DefaultSkinID(merry) || !ButlerViewOf("3202", now).Owns(d, merrySilver) {
		t.Fatalf("after the default is worn: worn=%d, the bought skin owned=%v", worn(), ButlerViewOf("3202", now).Owns(d, merrySilver))
	}

	// A butler whose contract ran out cannot change its skin.
	later := now.Add(31 * 24 * time.Hour)
	if err := EquipButlerSkin("3202", merry, merrySilver, later); err == nil {
		t.Fatal("a skin worn for a butler whose contract ran out")
	}
	if err := BuyButlerSkin("3202", merry, merrySilver+1, later); err == nil {
		t.Fatal("a skin bought for a butler whose contract ran out")
	}
}

// TestButlerSkinShapeAndChecks: Merry's shipped skins (the default and fourteen further with look-alike outfits, Cherry-made
// prices and ids in her sub-block), and checkButlers refuses a further skin with no price, an id outside its butler's sub-block, a default with a
// price, an id listed twice and a further skin with no picture.
func TestButlerSkinShapeAndChecks(t *testing.T) {
	m, ok := ButlerByID(merry)
	if !ok || len(m.Skins) != 15 || m.Skins[0].ID != DefaultSkinID(merry) || m.Skins[0].Price != 0 {
		t.Fatalf("merry's skins: %d, default id %d", len(m.Skins), m.Skins[0].ID)
	}
	for i, s := range m.Skins[1:] {
		// The outfits are Cherry-picked look-alikes (all listed as such) that keep Merry's face, ears and body.
		if s.ID != merrySilver+int64(i) || s.Price != 30 || s.SkinImage == "" || !strings.HasPrefix(s.Name, "Merry: ") ||
			!slices.Equal(s.Lookalikes, s.Outfit) || !slices.Contains(s.Outfit, "CAFA0000T") || slices.Equal(s.Outfit, m.Skins[0].Outfit) {
			t.Fatalf("further skin %d = %+v", i+1, s)
		}
	}
	base := ButlerSkin{ID: merrySilver, Code: "cherry_merry_x", Name: "Merry: X", Outfit: []string{"CUON009CV"}, SkinImage: "cache/x.png", Price: 30}
	if err := checkButlers(butlersWith(validMerry(base))); err != nil {
		t.Fatalf("a valid further skin: %v", err)
	}
	mutations := map[string]func(d *ButlerDef){
		"no price":              func(d *ButlerDef) { d.Skins[1].Price = 0 },
		"outside the sub-block": func(d *ButlerDef) { d.Skins[1].ID = merrySilver + 100 },
		"no picture":            func(d *ButlerDef) { d.Skins[1].SkinImage = "" },
		"default with a price":  func(d *ButlerDef) { d.Skins[0].Price = 5 },
		"skin id listed twice":  func(d *ButlerDef) { d.Skins[1].ID = DefaultSkinID(merry) },
		"no outfit":             func(d *ButlerDef) { d.Skins[1].Outfit = nil },
		"code without cherry_":  func(d *ButlerDef) { d.Skins[1].Code = "merry_x" },
	}
	for name, mutate := range mutations {
		d := validMerry(base)
		mutate(&d)
		if err := checkButlers(butlersWith(d)); err == nil {
			t.Fatalf("%s: accepted", name)
		}
	}
	// A second butler may not reuse a skin id of the first.
	first := validMerry(base)
	second := ButlerDef{ID: ophelia, Name: "Ophelia", Gender: "FEMALE", Abilities: []string{"ore"}, Art: "b1",
		Skins: []ButlerSkin{{ID: DefaultSkinID(ophelia), Code: "cherry_ophelia", Name: "Ophelia", Outfit: []string{"CUON009D6"}}, base}}
	both := butlersWith(first)
	both.Butlers = append(both.Butlers, second)
	if err := checkButlers(both); err == nil {
		t.Fatal("a skin id shared by two butlers was accepted")
	}
}

// TestButlerVoiceAndSkinPicturesAreInTheArchive: the shipped voice lines and skin pictures are in the archive (the load
// check reads them), and a voice line the archive lacks is refused.
func TestButlerVoiceAndSkinPicturesAreInTheArchive(t *testing.T) {
	testutil.UseArchive(t)
	if err := verifyButlers(ActiveButlers()); err != nil {
		t.Fatalf("shipped voices and skins: %v", err)
	}
	bad := *ActiveButlers()
	bad.Butlers = slices.Clone(bad.Butlers)
	voices := *bad.Butlers[2].Voices
	voices.Lines = append(slices.Clone(voices.Lines), 36) // 036 is absent from the donation
	bad.Butlers[2].Voices = &voices
	if err := verifyButlers(&bad); err == nil || !strings.Contains(err.Error(), "voice_036.ogg") {
		t.Fatalf("absent voice line: err = %v", err)
	}
}

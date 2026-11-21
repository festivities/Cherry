package economy

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"cherry/internal/store"
	"cherry/internal/testutil"
)

// fishBook returns aid's stored catch counts.
func fishBook(t *testing.T, aid string) map[int64]int64 {
	t.Helper()
	store.AccountsMu.Lock()
	defer store.AccountsMu.Unlock()
	acc := store.AccountByAidLocked(aid)
	if acc == nil {
		t.Fatalf("no account %s", aid)
	}
	return acc.FishCaught
}

func TestFishCatalogOrder(t *testing.T) {
	cat := FishCatalog()
	if len(cat) != 81 || cat[0].ID != 4101 || cat[0].Name != "Mackerel" ||
		cat[8].ID != 4109 || cat[25].ID != 4126 || !cat[25].Rare ||
		cat[71].ID != 4172 || cat[71].Name != "Porcupinefish" || cat[71].Image != "" ||
		cat[80].ID != 4181 || cat[80].Name != "Seahorse" || !cat[80].Rare {
		t.Fatalf("catalog unexpected: len=%d first=%+v p8last=%+v last=%+v", len(cat), cat[0], cat[71], cat[80])
	}
	if FishBookPageCount() != 9 {
		t.Fatalf("page count = %d, want 9", FishBookPageCount())
	}
	cat[0].ID = 1 // a copy: the table itself is unchanged
	if FishCatalog()[0].ID != 4101 {
		t.Fatal("FishCatalog shares the fish table")
	}
}

// Every hooked catch enters the book, including once the daily cap pays 0 (no ledger line then).
func TestFishCatchRecordsBookAtCap(t *testing.T) {
	now := jstAt(11, 12, 0)
	fishAccount(t, "9911", 0, &store.FishingDay{Day: AttendDayKey(now), Gems: ActiveFishing().DailyCap})
	if credit, ok := FishCatch("9911", ActiveFishing().normal[6], now); !ok || credit != 0 {
		t.Fatalf("capped catch = (%d, %v), want (0, true)", credit, ok)
	}
	if credit, ok := FishCatch("9911", ActiveFishing().rare[1], now); !ok || credit != 0 {
		t.Fatalf("second capped catch = (%d, %v), want (0, true)", credit, ok)
	}
	if book := fishBook(t, "9911"); book[4107] != 1 || book[4127] != 1 || len(book) != 2 {
		t.Fatalf("book = %v", book)
	}
	counts, total := FishCollection("9911")
	if total != 2 || counts[4107] != 1 || counts[4101] != 0 {
		t.Fatalf("collection = %v total %d", counts, total)
	}
}

// The book is saved with the account, keyed by fish id.
func TestFishCatchBookPersists(t *testing.T) {
	fishAccount(t, "9912", 0, nil)
	for i := 0; i < 2; i++ {
		if _, ok := FishCatch("9912", ActiveFishing().normal[0], jstAt(11, 12, i)); !ok {
			t.Fatal("catch failed")
		}
	}
	b := testutil.AccountRowText(t, "9912")
	if !regexp.MustCompile(`"fishCaught":\{"4101":2\}`).Match(b) {
		t.Fatalf("saved book missing: %s", b)
	}
}

// A failed save rolls the book back with the balance, as it does for the Gems.
func TestFishCatchBookRollsBackWithSave(t *testing.T) {
	fishAccount(t, "9913", 0, nil)
	blocker := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(blocker, []byte("file, not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	testutil.FailSaves(t)
	if _, ok := FishCatch("9913", ActiveFishing().normal[0], jstAt(11, 12, 0)); ok {
		t.Fatal("failed save reported ok")
	}
	if book := fishBook(t, "9913"); book != nil {
		t.Fatalf("rolled back book = %v, want none", book)
	}
}

func TestFishBookPageAndClaim(t *testing.T) {
	now := jstAt(11, 12, 0)
	fishAccount(t, "9914", 50, nil)
	cat := FishCatalog()
	// Catch 8 of 9 species on page 1.
	for i := 0; i < 8; i++ {
		if _, ok := FishCatch("9914", cat[i], now); !ok {
			t.Fatalf("catch %d failed", i)
		}
	}
	page1, counts, total, possible, already, rewardGems := FishBookPage("9914", 1)
	if len(page1) != 9 || total != 8 || counts[4101] != 1 || possible || already || rewardGems != 100 {
		t.Fatalf("incomplete page1 = len=%d total=%d possible=%v already=%v reward=%d", len(page1), total, possible, already, rewardGems)
	}
	if reward, ok := ClaimFishBookPage("9914", 1); ok || reward != 0 {
		t.Fatalf("incomplete claim = (%d, %v), want (0, false)", reward, ok)
	}

	// Catch the 9th species on page 1.
	if _, ok := FishCatch("9914", cat[8], now); !ok {
		t.Fatal("catch 9 failed")
	}
	_, _, total, possible, already, _ = FishBookPage("9914", 1)
	if total != 9 || !possible || already {
		t.Fatalf("complete page1 = total=%d possible=%v already=%v", total, possible, already)
	}
	beforeGems, _ := fishGems(t, "9914")
	if reward, ok := ClaimFishBookPage("9914", 1); !ok || reward != 100 {
		t.Fatalf("claim = (%d, %v), want (100, true)", reward, ok)
	}
	afterGems, _ := fishGems(t, "9914")
	if afterGems != beforeGems+100 {
		t.Fatalf("gems after claim = %d, want %d", afterGems, beforeGems+100)
	}
	b, err := testutil.LedgerText(t)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"reason":"fishbook:page:1"`) || !strings.Contains(string(b), `"delta":100`) {
		t.Fatalf("ledger missing page claim: %s", b)
	}

	// Already claimed: cannot claim twice, PossibleComplete false, CompleteAlready true.
	_, _, _, possible, already, _ = FishBookPage("9914", 1)
	if possible || !already {
		t.Fatalf("after claim: possible=%v already=%v, want false, true", possible, already)
	}
	if reward, ok := ClaimFishBookPage("9914", 1); ok || reward != 0 {
		t.Fatalf("duplicate claim = (%d, %v), want (0, false)", reward, ok)
	}
	if bal, _ := fishGems(t, "9914"); bal != afterGems {
		t.Fatalf("balance changed on duplicate claim: %d", bal)
	}

	// Out-of-range page claim is refused.
	if reward, ok := ClaimFishBookPage("9914", 0); ok || reward != 0 {
		t.Fatalf("page 0 claim = (%d, %v)", reward, ok)
	}
	if reward, ok := ClaimFishBookPage("9914", 10); ok || reward != 0 {
		t.Fatalf("page 10 claim = (%d, %v)", reward, ok)
	}
}

func TestFishBookClaimSaveFailureRollsBack(t *testing.T) {
	now := jstAt(11, 12, 0)
	fishAccount(t, "9915", 0, nil)
	cat := FishCatalog()
	for i := 0; i < 9; i++ {
		if _, ok := FishCatch("9915", cat[i], now); !ok {
			t.Fatalf("catch %d failed", i)
		}
	}
	beforeGems, _ := fishGems(t, "9915")
	testutil.FailSaves(t)
	if reward, ok := ClaimFishBookPage("9915", 1); ok || reward != 0 {
		t.Fatalf("failed save claim = (%d, %v), want (0, false)", reward, ok)
	}
	if bal, _ := fishGems(t, "9915"); bal != beforeGems {
		t.Fatalf("gems rolled back = %d, want %d", bal, beforeGems)
	}
	_, _, _, possible, already, _ := FishBookPage("9915", 1)
	if !possible || already {
		t.Fatalf("after rollback: possible=%v already=%v, want true, false", possible, already)
	}
}

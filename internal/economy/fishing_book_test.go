package economy

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"cherry/internal/store"
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
	if len(cat) != 8 || cat[0].ID != 4101 || cat[5].ID != 4106 || cat[6].ID != 4201 || cat[7].ID != 4202 || !cat[6].Rare {
		t.Fatalf("catalog = %+v", cat)
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
	if credit, ok := FishCatch("9911", ActiveFishing().normal[5], now); !ok || credit != 0 {
		t.Fatalf("capped catch = (%d, %v), want (0, true)", credit, ok)
	}
	if credit, ok := FishCatch("9911", ActiveFishing().rare[1], now); !ok || credit != 0 {
		t.Fatalf("second capped catch = (%d, %v), want (0, true)", credit, ok)
	}
	if book := fishBook(t, "9911"); book[4106] != 1 || book[4202] != 1 || len(book) != 2 {
		t.Fatalf("book = %v", book)
	}
	counts, total := FishCollection("9911")
	if total != 2 || counts[4106] != 1 || counts[4101] != 0 {
		t.Fatalf("collection = %v total %d", counts, total)
	}
}

// The book is saved with the account, keyed by fish id.
func TestFishCatchBookPersists(t *testing.T) {
	dir := fishAccount(t, "9912", 0, nil)
	for i := 0; i < 2; i++ {
		if _, ok := FishCatch("9912", ActiveFishing().normal[0], jstAt(11, 12, i)); !ok {
			t.Fatal("catch failed")
		}
	}
	b, err := os.ReadFile(filepath.Join(dir, "accounts.json"))
	if err != nil {
		t.Fatal(err)
	}
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
	store.AccountsMu.Lock()
	store.AccountStorePath = filepath.Join(blocker, "accounts.json")
	store.AccountsMu.Unlock()
	if _, ok := FishCatch("9913", ActiveFishing().normal[0], jstAt(11, 12, 0)); ok {
		t.Fatal("failed save reported ok")
	}
	if book := fishBook(t, "9913"); book != nil {
		t.Fatalf("rolled back book = %v, want none", book)
	}
}

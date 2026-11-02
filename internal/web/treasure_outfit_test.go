package web

import (
	"net/http"
	"testing"
	"time"

	"cherry/internal/economy"
	"cherry/internal/store"
	"cherry/internal/testutil"
)

// TestTreasureBuffFollowsClosetSave: the buff follows the saved outfit through the real Closet handler (save/v2). A save that wears
// the pool item starts the buff, and a save that takes it off ends it.
func TestTreasureBuffFollowsClosetSave(t *testing.T) {
	testutil.InstallAccounts(t, map[string]*store.Account{"tbuff": {AccessToken: "tbuff", SessionKey: "sk", Aid: "9301", Name: "Buffer",
		Gender: "FEMALE", InventoryCodes: []string{"CUTO0006W", "CUAH006FG"}}})
	testutil.SetLab(t, "9301", false)
	store.ResetTreasure()
	t.Cleanup(store.ResetTreasure)
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.FixedZone("JST", 9*60*60))
	if _, err := store.UpdateTreasure("9301", func(r *store.TreasureRecord) {
		*r = store.TreasureRecord{Season: economy.ActiveTreasureData().Season.ID, Tickets: 10, Charged: now.UnixMilli()}
	}); err != nil {
		t.Fatal(err)
	}
	percent := func() int64 { return economy.TreasurePeek("9301", now).BuffPercent }
	save := func(body string) {
		t.Helper()
		if rec := socialTestRequestAs(t, "tbuff", http.MethodPut, "/v4/avatar/save/v2", body); rec.Code != http.StatusOK {
			t.Fatalf("save/v2 %s = %d %s", body, rec.Code, rec.Body.String())
		}
	}
	save(`[{"itemCode":"CUTO0006W"},{"itemCode":"CUAH006FG"}]`)
	if got := percent(); got != 10 {
		t.Fatalf("after the Closet wear = %d%%, want 10", got)
	}
	save(`[{"itemCode":"CUTO0006W"}]`)
	if got := percent(); got != 0 {
		t.Fatalf("after the Closet take-off = %d%%, want 0", got)
	}
}

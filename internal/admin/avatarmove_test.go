package admin

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"cherry/internal/config"
	"cherry/internal/store"
)

// TestAvatarMoveOwnerOnlyAndReviewed: the move's review shows the plan and the refusals; the confirm needs the avatar id typed back;
// a staff member who is not the owner gets 403 on the review and on the confirm, and nothing changes.
func TestAvatarMoveOwnerOnlyAndReviewed(t *testing.T) {
	activateIdentity(t)
	fx := newFixture(t, devConfigWithStaff(t))
	id := seedPlayer(t, "dev-mover", "7201", "Mover", 10)
	seedPlayer(t, "dev-busy", "7202", "Busy", 0)
	seedPlayer(t, "dev-dst", "0", "", 0) // an empty row: the move removes it
	path := playerURL(id) + "/avatar/move"

	st := fx.client()
	st.signInDev("staff")
	expect(t, st.get(path+"?to=dev-dst"), http.StatusForbidden)
	expect(t, st.post(path, url.Values{"csrf": {st.csrf()}, "to": {"dev-dst"}, "confirm": {"7201"}}), http.StatusForbidden)

	owner := fx.client()
	owner.signInDev("owner")
	if r := owner.get(path + "?to=dev-dst"); r.StatusCode != http.StatusOK || !strings.Contains(r.body, "Type the avatar id 7201") ||
		!strings.Contains(r.body, "holds no avatar, so that empty row is removed") {
		t.Fatalf("the move's review: status %d, the plan is missing", r.StatusCode)
	}
	if r := owner.get(path + "?to=dev-busy"); r.StatusCode != http.StatusOK || !strings.Contains(r.body, "already has avatar 7202") ||
		strings.Contains(r.body, "Type the avatar id 7201") {
		t.Fatalf("a refused move's review: status %d, the refusal is missing or the confirm form shows", r.StatusCode)
	}
	expect(t, owner.post(path, url.Values{"csrf": {owner.csrf()}, "to": {"dev-dst"}, "confirm": {"7299"}}), http.StatusBadRequest)
	expect(t, owner.post(path, url.Values{"csrf": {owner.csrf()}, "to": {"dev-busy"}, "confirm": {"7201"}}), http.StatusConflict)

	moved := owner.post(path, url.Values{"csrf": {owner.csrf()}, "to": {"dev-dst"}, "confirm": {"7201"}})
	expect(t, moved, http.StatusSeeOther)
	if loc := moved.Header.Get("Location"); loc != playerURL(id)+"?notice=avatar-moved" {
		t.Fatalf("move redirect = %q", loc)
	}
	p, ok := store.PlayerByID(id)
	if !ok || p.Identity != "dev-dst" || p.Aid != "7201" {
		t.Fatalf("after the move the row is %+v", p)
	}
	found := false
	for _, row := range auditRows(t) {
		found = found || row.Action == "avatar-move"
	}
	if !found {
		t.Fatal("the move wrote no avatar-move audit row")
	}
}

// TestAvatarRestoreFromTheRecord: the restore's review, the typed confirm, the record marked restored and its link to the player, the
// second restore refused, and the owner only rule on the review and the confirm.
func TestAvatarRestoreFromTheRecord(t *testing.T) {
	activateIdentity(t)
	fx := newFixture(t, devConfigWithStaff(t))
	rec := store.AvatarRecord{Time: "2026-11-10T08:00:00Z", Account: store.StoredAccount{Aid: "7301", Name: "Gone", Gems: 40, Identity: "dev-gone"}}
	recID := seedDeleted(t, "7301", "dev-gone", "Gone", rec)
	rid := strconv.FormatInt(recID, 10)
	path := "/players/deleted/" + rid + "/restore"

	st := fx.client()
	st.signInDev("staff")
	expect(t, st.get(path), http.StatusForbidden)
	expect(t, st.post(path, url.Values{"csrf": {st.csrf()}, "to": {"dev-gone"}, "confirm": {"7301"}}), http.StatusForbidden)

	owner := fx.client()
	owner.signInDev("owner")
	if r := owner.get(path); r.StatusCode != http.StatusOK || !strings.Contains(r.body, "Type the avatar id 7301") {
		t.Fatalf("the restore review: status %d", r.StatusCode)
	}
	expect(t, owner.post(path, url.Values{"csrf": {owner.csrf()}, "to": {"dev-gone"}, "confirm": {"7399"}}), http.StatusBadRequest)
	restored := owner.post(path, url.Values{"csrf": {owner.csrf()}, "to": {"dev-gone"}, "confirm": {"7301"}})
	expect(t, restored, http.StatusSeeOther)
	if loc := restored.Header.Get("Location"); loc != "/players/deleted/"+rid+"?notice=avatar-restored" {
		t.Fatalf("restore redirect = %q", loc)
	}
	players, _ := store.FindPlayers("dev-gone")
	if len(players) != 1 {
		t.Fatalf("the restore left %d rows for dev-gone, want 1", len(players))
	}
	page := owner.get("/players/deleted/" + rid)
	if !strings.Contains(page.body, "Restored on ") || !strings.Contains(page.body, `href="/players/`+strconv.FormatInt(players[0].ID, 10)+`"`) {
		t.Fatal("the restored record does not show its restore and the player link")
	}
	if list := owner.get("/players/deleted"); !strings.Contains(list.body, "· restored ") {
		t.Fatal("the deleted list does not mark the restored record")
	}

	again := owner.get(path)
	if again.StatusCode != http.StatusOK || !strings.Contains(again.body, "was restored at") || strings.Contains(again.body, "Type the avatar id") {
		t.Fatalf("a second restore's review: status %d", again.StatusCode)
	}
	expect(t, owner.post(path, url.Values{"csrf": {owner.csrf()}, "to": {"dev-gone"}, "confirm": {"7301"}}), http.StatusConflict)
}

// activateIdentity makes the store's mode the identity mode the staff fixture runs in (cmd/cherry activates the configuration at
// start; the fixture does not), and resets it when the test ends.
func activateIdentity(t *testing.T) {
	t.Helper()
	config.Activate(config.Config{AuthMode: config.AuthDevFake})
	t.Cleanup(func() { config.Activate(config.Config{}) })
}

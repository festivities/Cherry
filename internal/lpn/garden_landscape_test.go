package lpn

import (
	"path/filepath"
	"testing"

	"cherry/internal/economy"
	"cherry/internal/store"
	"cherry/internal/testutil"
)

// landscapeAccount installs an account that owns its Garden, with its saves in a temp file so that a
// change is persisted (reloaded by the test).
func landscapeAccount(t *testing.T, aid string, bought []string) {
	t.Helper()
	testutil.InstallAccounts(t, map[string]*store.Account{"ls-" + aid: {AccessToken: "ls-" + aid, SessionKey: "sk", Aid: aid, Name: "Landscape", Landscapes: bought}})
	testutil.SetLab(t, aid, false)
	store.AccountsMu.Lock()
	store.AccountStorePath, store.NextAvatarID = filepath.Join(t.TempDir(), "accounts.json"), 9999
	store.AccountsMu.Unlock()
}

// landscapeCode is a preview request body: itemCode#1.
func landscapeCode(code string) []byte {
	return pbLen(nil, 1, []byte(code))
}

// TestGardenLandscapeList: the list holds one MyGardenItem per owned landscape: the default map's and the
// bought ones. seq is the numeric item id and itemCode the GULS code.
func TestGardenLandscapeList(t *testing.T) {
	landscapeAccount(t, "9701", nil)
	logger := quietLogger()
	msg := gardenLandscapeReply(4, 9701, 9701, nil, &gardenLandscape{}, logger)
	list := pbFieldsOf(t, msg.body)
	if msg.id != 5 || list.vars[1] != 0 || len(list.msgs[2]) != 1 {
		t.Fatalf("list = id %d vars %v items %d, want one item", msg.id, list.vars, len(list.msgs[2]))
	}
	item := pbFieldsOf(t, list.msgs[2][0])
	if item.vars[1] != 428400003 || string(item.msgs[3][0]) != "GULS00003" {
		t.Fatalf("default item = %v %q", item.vars, item.msgs[3])
	}

	landscapeAccount(t, "9702", []string{"GULS0255R"})
	list = pbFieldsOf(t, gardenLandscapeReply(4, 9702, 9702, nil, &gardenLandscape{}, logger).body)
	if len(list.msgs[2]) != 2 {
		t.Fatalf("items with a bought landscape = %d, want 2", len(list.msgs[2]))
	}
	bought := pbFieldsOf(t, list.msgs[2][1])
	if bought.vars[1] != 428499999 || string(bought.msgs[3][0]) != "GULS0255R" {
		t.Fatalf("bought item = %v %q", bought.vars, bought.msgs[3])
	}
}

// TestGardenLandscapeChangeFlow: an unowned preview is refused, a change without a preview is refused,
// a preview of an owned landscape names its map and spots, and the change makes that map the Garden's
// (saved, so the next room enter shows it). The preview is used once.
func TestGardenLandscapeChangeFlow(t *testing.T) {
	landscapeAccount(t, "9702", []string{"GULS0255R"})
	logger := quietLogger()
	st := &gardenLandscape{}

	refused := func(msg gardenMsg, id uint16) {
		t.Helper()
		body := pbFieldsOf(t, msg.body)
		if msg.id != id || body.vars[1] != gatherRefused || len(body.msgs[2]) != 0 || len(body.msgs[3]) != 0 {
			t.Fatalf("reply %d = %v, want result %d only", msg.id, body.vars, gatherRefused)
		}
	}
	refused(gardenLandscapeReply(6, 9702, 9702, landscapeCode("GULS0005U"), st, logger), 7)
	refused(gardenLandscapeReply(5, 9702, 9702, nil, st, logger), 6)

	preview := pbFieldsOf(t, gardenLandscapeReply(6, 9702, 9702, landscapeCode("GULS0255R"), st, logger).body)
	if preview.vars[1] != 0 || string(preview.msgs[2][0]) != "1908231804" || len(preview.msgs[3][0]) == 0 {
		t.Fatalf("preview = %v map %q spots %d", preview.vars, preview.msgs[2], len(preview.msgs[3]))
	}
	if economy.GardenMap("9702") != economy.DefaultGardenMap() {
		t.Fatalf("a preview changed the map to %s", economy.GardenMap("9702"))
	}

	change := pbFieldsOf(t, gardenLandscapeReply(5, 9702, 9702, nil, st, logger).body)
	if change.vars[1] != 0 || string(change.msgs[3][0]) != "1908231804" || change.vars[4] != 0 {
		t.Fatalf("change = %v map %q", change.vars, change.msgs[3])
	}
	if economy.GardenMap("9702") != "1908231804" {
		t.Fatalf("Garden map after the change = %s", economy.GardenMap("9702"))
	}
	store.AccountsMu.Lock()
	path := store.AccountStorePath
	store.AccountsMu.Unlock()
	if err := store.LoadAccountsFrom(path); err != nil || economy.GardenMap("9702") != "1908231804" {
		t.Fatalf("saved map after reload: %v, %s", err, economy.GardenMap("9702"))
	}
	refused(gardenLandscapeReply(5, 9702, 9702, nil, st, logger), 6) // the preview was used up
}

// TestGardenLandscapeOnlyTheOwnerChanges: a Garden's owner is the only one who can change it, and a
// failed preview leaves no map for a later change to apply.
func TestGardenLandscapeOnlyTheOwnerChanges(t *testing.T) {
	landscapeAccount(t, "9703", []string{"GULS0255R"})
	logger := quietLogger()
	st := &gardenLandscape{}
	gardenLandscapeReply(6, 9703, 9703, landscapeCode("GULS0255R"), st, logger)
	if msg := gardenLandscapeReply(5, 9703, 9704, nil, st, logger); pbFieldsOf(t, msg.body).vars[1] != gatherRefused {
		t.Fatal("a change in someone else's Garden was accepted")
	}
	if economy.GardenMap("9703") != economy.DefaultGardenMap() {
		t.Fatalf("map changed by a visitor: %s", economy.GardenMap("9703"))
	}
	gardenLandscapeReply(6, 9703, 9703, landscapeCode("GULS0255R"), st, logger)
	gardenLandscapeReply(6, 9703, 9703, landscapeCode("GULS0005U"), st, logger) // unowned: clears the preview
	if msg := gardenLandscapeReply(5, 9703, 9703, nil, st, logger); pbFieldsOf(t, msg.body).vars[1] != gatherRefused {
		t.Fatal("a change after a failed preview was accepted")
	}
}

// TestGardenLandscapeMessages: the editor's requests are 4, 5 and 6 and no other GARDEN request.
func TestGardenLandscapeMessages(t *testing.T) {
	for msgid := uint16(0); msgid < 50; msgid++ {
		want := msgid == 4 || msgid == 5 || msgid == 6
		if got := gardenLandscapeMsg(msgid); got != want {
			t.Errorf("gardenLandscapeMsg(%d) = %v", msgid, got)
		}
	}
}

package lpn

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"cherry/internal/economy"
	"cherry/internal/store"
	"cherry/internal/testutil"
	"cherry/internal/thumbs"
)

// racingArchive points the item catalog at a fixture: a Riding Pet (animated, so the catalog files it as
// URIDE) and a dog, both with art.
func racingArchive(t *testing.T) {
	t.Helper()
	root := t.TempDir()
	oldZip, oldCache := thumbs.DpZipPath, thumbs.DpCacheDir
	thumbs.DpZipPath, thumbs.DpCacheDir = filepath.Join(t.TempDir(), "none.zip"), t.TempDir()
	t.Cleanup(func() { thumbs.DpZipPath, thumbs.DpCacheDir = oldZip, oldCache })
	files := map[string][]string{
		"pet/326400259": {"skeleton.xml", "r.aniproj", "dp.png"}, // PUPE00077: a Riding Pet
		"pet/326400016": {"skeleton.xml", "dp.png"},              // PUPE0000G: a dog
	}
	for dir, names := range files {
		d := filepath.Join(root, filepath.FromSlash(dir))
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
		for _, n := range names {
			if err := os.WriteFile(filepath.Join(d, n), []byte("x"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	old := thumbs.DpItemRoot
	thumbs.DpItemRoot = root
	t.Cleanup(func() { thumbs.DpItemRoot = old })
}

// racingConfig returns the live racing file so a test can shorten its waits; the values come back at the end.
// Call it before racingAccount, so the restore runs after the test's races have stopped.
func racingConfig(t *testing.T) *economy.Racing {
	t.Helper()
	r := economy.ActiveRacing()
	saved := *r
	t.Cleanup(func() { *r = saved })
	return r
}

// racingAccount joins the Square with an account that owns pets, and stops its race when the test ends.
func racingAccount(t *testing.T, aid string, pets []store.PetItem) *sqClient {
	t.Helper()
	racingArchive(t)
	testutil.InstallAccounts(t, map[string]*store.Account{"race-" + aid: {AccessToken: "race-" + aid, SessionKey: "sk", Aid: aid, Name: "Racer", Pets: pets}})
	store.AccountsMu.Lock()
	store.AccountStorePath = filepath.Join(t.TempDir(), "accounts.json")
	store.AccountsMu.Unlock()
	c := newSqClient(t, &squareRoom{}, 1)
	n, err := strconv.ParseUint(aid, 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	c.join(n)
	t.Cleanup(c.sess.stopRacing)
	return c
}

var (
	racingRidePet = store.PetItem{ID: 2, Cd: "PUPE00077", Name: "Minipet 2"} // numeric 326400259
	racingDog     = store.PetItem{ID: 1, Cd: "PUPE0000G", Name: "Minipet 1"} // numeric 326400016
)

// racingMoveReq is cr94's PurposeMoveTheme (the echo of rc84's).
func racingMoveReq() []byte {
	return pbLen(nil, 1, pbVar(pbVar(nil, 1, squareRacingMoveReason), 2, squareRacingTheme))
}

// pbAll returns every length-delimited value of field, in order.
func pbAll(t *testing.T, data []byte, field uint64) [][]byte {
	t.Helper()
	var out [][]byte
	if !pbScan(data, func(f, _ uint64, d []byte) {
		if f == field && d != nil {
			out = append(out, d)
		}
	}) {
		t.Fatalf("malformed message %x", data)
	}
	return out
}

// racingSendCreate sends cr66 for a new room and checks rc84 (created, not refused).
func racingSendCreate(t *testing.T, c *sqClient) {
	t.Helper()
	c.send(66, pbVar(nil, 1, 1)) // IsCreate1
	if id, body := c.read(); id != 84 || squareUint(t, body, 1) != 0 || squareUint(t, body, 3) != 1 {
		t.Fatalf("create = %d/%x, want rc84 created", id, body)
	}
}

// racingCheckRoster checks an rc110 push: the request type, and the one member, the master, with its ready flag.
func racingCheckRoster(t *testing.T, body []byte, reqType uint64, ready bool, aid uint64) {
	t.Helper()
	if squareUint(t, body, 1) != reqType || squareUint(t, body, 2) != aid || squareUint(t, body, 3) != aid {
		t.Fatalf("roster reqtype/target/master = %x, want %d %d %d", body, reqType, aid, aid)
	}
	room := squareNested(t, body, 4)
	if squareUint(t, room, 6) != 1 { // MinUserCount6: the master starts alone
		t.Fatalf("room info = %x, want MinUserCount 1", room)
	}
	member := squareNested(t, body, 5)
	if squareUint(t, member, 1) != aid || squareUint(t, member, 2) != 1 || squareUint(t, member, 3) != squareBool(ready) {
		t.Fatalf("member = %x, want master aid %d ready %v", member, aid, ready)
	}
}

// racingToRoster creates the room, moves into the racing world (rc6) and reads the roster push after the delay.
func racingToRoster(t *testing.T, c *sqClient, aid uint64) {
	t.Helper()
	racingSendCreate(t, c)
	started := time.Now()
	c.send(94, racingMoveReq())
	id, enter := c.read()
	if id != 6 || squareUint(t, enter, 1) != squareEnterResult || squareUint(t, enter, 4) != squareRacingTheme {
		t.Fatalf("racing enter = %d/%x, want rc6 theme %d", id, enter, squareRacingTheme)
	}
	if name, _, _ := pbBytesField(enter, 6); string(name) != squareRacingMap {
		t.Fatalf("racing map = %q", name)
	}
	id, roster := c.read()
	if id != 110 {
		t.Fatalf("roster = %d/%x, want rc110", id, roster)
	}
	if elapsed := time.Since(started); elapsed < time.Duration(economy.ActiveRacing().RosterDelayMs)*time.Millisecond {
		t.Fatalf("roster came after %v, before the %dms delay", elapsed, economy.ActiveRacing().RosterDelayMs)
	}
	racingCheckRoster(t, roster, 1, false, aid)
}

// racingToRace goes from the roster to the start: ready (cr86), start (cr71 -> rc91), the client's
// ready-play (cr72 -> rc92) and the start signal (rc97) after the countdown.
func racingToRace(t *testing.T, c *sqClient, aid uint64) {
	t.Helper()
	racingToRoster(t, c, aid)
	c.send(86, pbVar(pbVar(nil, 1, squareRacingGame), 2, squareRacingReady))
	if id, body := c.read(); id != 110 {
		t.Fatalf("ready = %d/%x, want rc110", id, body)
	} else {
		racingCheckRoster(t, body, squareRacingReady, true, aid)
	}
	c.send(71, nil)
	id, loading := c.read()
	if id != 91 || squareUint(t, loading, 1) != 0 {
		t.Fatalf("start = %d/%x, want rc91 result 0", id, loading)
	}
	racer := squareNested(t, loading, 2)
	pet := squareNested(t, racer, 5)
	if squareUint(t, racer, 2) != aid || squareUint(t, pet, 1) != 326400259 {
		t.Fatalf("racer = %x, want aid %d and pet 326400259", racer, aid)
	}
	if code, _, _ := pbBytesField(pet, 3); string(code) != "PUPE00077" {
		t.Fatalf("pet skincode = %q", code)
	}
	if n := countVarintField(t, loading, 4); n != economy.ActiveRacing().PosKeyCount {
		t.Fatalf("position keys = %d, want %d", n, economy.ActiveRacing().PosKeyCount)
	}
	if squareUint(t, loading, 7) != uint64(economy.ActiveRacing().MaxRaceSec) {
		t.Fatalf("race time = %d", squareUint(t, loading, 7))
	}
	c.send(72, nil) // the client's ready-play, ~3 s after rc91 in the client
	if id, cd := c.read(); id != 92 || squareUint(t, cd, 1) != uint64(economy.ActiveRacing().CountdownSec) {
		t.Fatalf("countdown = %d/%x, want rc92", id, cd)
	}
	if id, _ := c.read(); id != 97 {
		t.Fatalf("start signal = %d, want rc97", id)
	}
}

// countVarintField counts the varint values of field (unpacked repeated int32).
func countVarintField(t *testing.T, data []byte, field uint64) int {
	t.Helper()
	n := 0
	if !pbScan(data, func(f, _ uint64, d []byte) {
		if f == field && d == nil {
			n++
		}
	}) {
		t.Fatalf("malformed message %x", data)
	}
	return n
}

// TestRacingEntryListsRidePets: the entry (cr63) lists only the owned Riding Pets and selects one; the game
// list (cr64) and the room list (cr65) answer.
func TestRacingEntryListsRidePets(t *testing.T) {
	racingConfig(t)
	c := racingAccount(t, "9901", []store.PetItem{racingDog, racingRidePet})
	c.send(63, pbVar(nil, 1, 0))
	id, info := c.read()
	if id != 82 || squareUint(t, info, 1) != 0 || squareUint(t, info, 2) != 326400259 {
		t.Fatalf("entry = %d/%x, want rc82 result 0 with the pet selected", id, info)
	}
	rows := pbAll(t, info, 3)
	if len(rows) != 1 || squareUint(t, rows[0], 1) != 326400259 {
		t.Fatalf("pet picker rows = %d, want the one Riding Pet", len(rows))
	}
	if tickets := squareNested(t, info, 5, 2); squareUint(t, tickets, 1) != uint64(economy.ActiveRacing().MaxTickets) {
		t.Fatalf("tickets = %x", tickets)
	}
	c.send(64, nil)
	if id, games := c.read(); id != 83 || squareUint(t, games, 1) != 0 {
		t.Fatalf("games = %d/%x, want rc83", id, games)
	} else if game := squareNested(t, games, 5); squareUint(t, game, 1) != squareRacingGame {
		t.Fatalf("game = %x, want MinigameId 1", game)
	}
	c.send(65, pbVar(nil, 1, 1))
	if id, rooms := c.read(); id != 107 || squareUint(t, rooms, 1) != 0 {
		t.Fatalf("rooms = %d/%x, want rc107 result 0", id, rooms)
	}
	c.send(95, nil) // cr95 has no reply: its index is another message's
	c.none()
}

// TestRacingWithoutRidePetIsRefused: an account with no Riding Pet gets no pet in the picker (-1) and a
// refused create (rc84 0x300004, box 7666) that grants nothing.
func TestRacingWithoutRidePetIsRefused(t *testing.T) {
	racingConfig(t)
	c := racingAccount(t, "9902", []store.PetItem{racingDog})
	c.send(63, pbVar(nil, 1, 0))
	id, info := c.read()
	if id != 82 || squareUint(t, info, 2) != ^uint64(0) || len(pbAll(t, info, 3)) != 0 {
		t.Fatalf("entry = %d/%x, want no pet (-1) and no picker rows", id, info)
	}
	c.send(66, pbVar(nil, 1, 1))
	if id, body := c.read(); id != 84 || squareUint(t, body, 1) != squareRacingRefused {
		t.Fatalf("create = %d/%x, want refused 0x300004", id, body)
	}
	c.none()
}

// TestRacingRosterAfterWorldLoadNotBefore: rc6 puts the racer into the racing world; the roster (rc110)
// follows after the delay, never in the same burst, and the racer is the ready-less master.
func TestRacingRosterAfterWorldLoadNotBefore(t *testing.T) {
	cfg := racingConfig(t)
	cfg.RosterDelayMs = 300
	c := racingAccount(t, "9903", []store.PetItem{racingRidePet})
	racingToRoster(t, c, 9903)
	c.none() // nothing else follows the roster
}

// TestRacingReadyToggleAndStart: the master readies (cr86 -> rc110), the start needs the ready flag, and the
// start pushes the racer and the countdown; a ticket is spent at the start.
func TestRacingReadyToggleAndStart(t *testing.T) {
	cfg := racingConfig(t)
	cfg.RosterDelayMs, cfg.CountdownSec = 300, 1
	c := racingAccount(t, "9904", []store.PetItem{racingRidePet})
	racingToRoster(t, c, 9904)
	c.send(71, nil) // not ready yet: the start button would be disabled
	c.none()
	c.send(86, pbVar(pbVar(nil, 1, squareRacingGame), 2, squareRacingReady))
	if id, body := c.read(); id != 110 {
		t.Fatalf("ready = %d/%x", id, body)
	} else {
		racingCheckRoster(t, body, squareRacingReady, true, 9904)
	}
	if st := economy.RaceStatusOf("9904", time.Now()); st.Tickets != cfg.MaxTickets {
		t.Fatalf("tickets before the start = %d", st.Tickets)
	}
	c.send(71, nil)
	if id, _ := c.read(); id != 91 {
		t.Fatalf("start = %d, want rc91", id)
	}
	if st := economy.RaceStatusOf("9904", time.Now()); st.Tickets != cfg.MaxTickets-1 {
		t.Fatalf("tickets after the start = %d, want one spent", st.Tickets)
	}
	c.send(72, nil)
	if id, _ := c.read(); id != 92 {
		t.Fatalf("countdown = %d, want rc92", id)
	}
	if id, _ := c.read(); id != 97 {
		t.Fatalf("start signal = %d, want rc97", id)
	}
}

// TestRacingArrivalPaysOnce: a position short of the finish area does nothing; the finish area sends rc101
// (rank 1) and rc102 after the finish countdown, with the reward paid once.
func TestRacingArrivalPaysOnce(t *testing.T) {
	cfg := racingConfig(t)
	cfg.RosterDelayMs, cfg.CountdownSec, cfg.FinishCountdownSec = 300, 1, 1
	c := racingAccount(t, "9905", []store.PetItem{racingRidePet})
	racingToRace(t, c, 9905)
	base := fishingBalance(t, "9905")
	finish := cfg.Track.Finish
	mid := uint64((finish.BeginX + finish.EndX) / 2)

	c.send(76, pbVar(nil, 1, 0)) // a position at the start
	c.none()
	pos := func(x uint64) []byte { return pbLen(nil, 2, pbVar(nil, 1, x)) } // cr76 TilePos2
	c.send(76, pos(mid))
	id, arrive := c.read()
	if id != 101 || squareUint(t, arrive, 1) != 9905 || squareUint(t, arrive, 2) != 1 || squareUint(t, arrive, 3) != uint64(cfg.FinishCountdownSec) {
		t.Fatalf("arrival = %d/%x, want rc101 rank 1", id, arrive)
	}
	id, result := c.read()
	if id != 102 {
		t.Fatalf("result = %d/%x, want rc102", id, result)
	}
	race := squareNested(t, result, 1)
	if squareUint(t, race, 2) != 1 || squareUint(t, result, 5) != uint64(economy.ActiveRacing().Reward.StorePoints) {
		t.Fatalf("result = %x, want rank 1 and the store points", result)
	}
	if got := fishingBalance(t, "9905"); got != base+economy.ActiveRacing().Reward.Gems {
		t.Fatalf("Gems after the arrival = %d, want %d", got, base+economy.ActiveRacing().Reward.Gems)
	}
	c.send(76, pos(mid+5)) // a second arrival pays nothing and answers nothing
	c.none()
	if got := fishingBalance(t, "9905"); got != base+economy.ActiveRacing().Reward.Gems {
		t.Fatalf("Gems after a repeated position = %d, want %d", got, base+economy.ActiveRacing().Reward.Gems)
	}
}

// TestRacingTimeOutEndsUnranked: a race without an arrival ends at the race time with rc102 rank 0 and no
// reward; cr79 (play again) then goes back to the move, and an empty account is refused.
func TestRacingTimeOutEndsUnranked(t *testing.T) {
	cfg := racingConfig(t)
	cfg.RosterDelayMs, cfg.CountdownSec, cfg.MaxRaceSec = 300, 1, 1
	c := racingAccount(t, "9906", []store.PetItem{racingRidePet})
	racingToRace(t, c, 9906)
	base := fishingBalance(t, "9906")
	id, result := c.read()
	if id != 102 || squareUint(t, squareNested(t, result, 1), 2) != 0 {
		t.Fatalf("time-out = %d/%x, want rc102 unranked", id, result)
	}
	if got := fishingBalance(t, "9906"); got != base {
		t.Fatalf("Gems after a time-out = %d, want %d", got, base)
	}
	c.send(79, pbVar(nil, 1, 1))
	if id, again := c.read(); id != 106 || squareUint(t, again, 1) != 0 || squareUint(t, squareNested(t, again, 2), 2) != squareRacingTheme {
		t.Fatalf("play again = %d/%x, want rc106 theme %d", id, again, squareRacingTheme)
	}
	c.none()
}

// TestRacingExitClearsTimers: leaving the racing world (cr2 back to the Square) stops the pending roster
// push, so nothing arrives after the delay.
func TestRacingExitClearsTimers(t *testing.T) {
	cfg := racingConfig(t)
	cfg.RosterDelayMs = 300
	c := racingAccount(t, "9907", []store.PetItem{racingRidePet})
	racingSendCreate(t, c)
	c.send(94, racingMoveReq())
	if id, _ := c.read(); id != 6 {
		t.Fatalf("move = %d, want rc6", id)
	}
	c.send(2, pbVar(nil, 1, 1)) // back to the Square: rc6 theme 6
	if id, enter := c.read(); id != 6 || squareUint(t, enter, 4) != squareThemeID {
		t.Fatalf("re-enter = %d/%x, want theme %d", id, enter, squareThemeID)
	}
	time.Sleep(400 * time.Millisecond) // past the roster delay
	c.none()
	c.send(71, nil) // the race is gone: nothing answers the start
	c.none()
}

// TestRacingTicketsSpentAndRefused: with one ticket a day, the start spends it and the next create is refused.
func TestRacingTicketsSpentAndRefused(t *testing.T) {
	cfg := racingConfig(t)
	cfg.RosterDelayMs, cfg.CountdownSec, cfg.MaxTickets = 300, 1, 1
	c := racingAccount(t, "9908", []store.PetItem{racingRidePet})
	racingToRace(t, c, 9908)
	c.send(66, pbVar(nil, 1, 1))
	if id, body := c.read(); id != 84 || squareUint(t, body, 1) != squareRacingRefused {
		t.Fatalf("create with no tickets = %d/%x, want refused 0x300004", id, body)
	}
	c.none()
}

// TestRacingPickPet: the picker (cr82) remembers an owned Riding Pet and refuses a pet the account does not own.
func TestRacingPickPet(t *testing.T) {
	racingConfig(t)
	c := racingAccount(t, "9909", []store.PetItem{racingDog, racingRidePet})
	c.send(82, pbVar(nil, 1, 326400016)) // a dog
	if id, body := c.read(); id != 109 || squareUint(t, body, 1) != 1 {
		t.Fatalf("pick a dog = %d/%x, want refused", id, body)
	}
	c.send(82, pbVar(nil, 1, 326400259))
	id, body := c.read()
	if id != 109 || squareUint(t, body, 1) != 0 || squareUint(t, squareNested(t, body, 2), 1) != 326400259 {
		t.Fatalf("pick the Riding Pet = %d/%x", id, body)
	}
}

// TestRaceArrivedAlongEitherAxis: arrival is measured along the course's main axis and direction, one tile
// short counts (the client truncates its tile read-back), and the start tile never counts.
func TestRaceArrivedAlongEitherAxis(t *testing.T) {
	area := func(x, y int64) economy.RacingArea { return economy.RacingArea{BeginX: x, BeginY: y, EndX: x, EndY: y} }
	cases := []struct {
		start, finish economy.RacingArea
		x, y          int64
		want          bool
	}{
		{area(75, 69), area(112, 69), 75, 69, false},
		{area(75, 69), area(112, 69), 111, 69, true},
		{area(73, 67), area(73, 122), 73, 67, false}, // the north-south course: the start x equals the finish x
		{area(73, 67), area(73, 120), 73, 119, true},
		{area(73, 122), area(73, 67), 73, 121, false}, // reversed
		{area(73, 122), area(73, 67), 73, 68, true},
	}
	for _, c := range cases {
		if got := reachedFinish(c.start, c.finish, c.x, c.y); got != c.want {
			t.Errorf("start %v finish %v at (%d,%d) = %v, want %v", c.start, c.finish, c.x, c.y, got, c.want)
		}
	}
}

// TestRacingRosterResentAfterRelayStart: the racing world's relay start (cr5) after the timed roster gets the
// roster again a moment later, for a client whose world load outlasted the first push (it showed no READY).
func TestRacingRosterResentAfterRelayStart(t *testing.T) {
	cfg := racingConfig(t)
	cfg.RosterDelayMs = 300
	c := racingAccount(t, "9909", []store.PetItem{racingRidePet})
	racingToRoster(t, c, 9909)
	c.send(5, nil)
	if id, _ := c.read(); id != 8 {
		t.Fatalf("relaystart reply = %d, want rc8", id)
	}
	id, roster := c.read()
	if id != 110 {
		t.Fatalf("after relaystart = %d/%x, want the roster again", id, roster)
	}
	racingCheckRoster(t, roster, 1, false, 9909)
	c.none()
}

package lpn

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"cherry/internal/economy"
	"cherry/internal/store"
	"cherry/internal/testutil"
)

// bossRoomClients installs one account per aid and logs each one into the Square room sq, in order. The frames the
// earlier clients get from a later join (the Square's adduser) are drained, so every test reads only its own pushes.
func bossRoomClients(t *testing.T, sq *squareRoom, aids ...string) []*sqClient {
	t.Helper()
	entries := map[string]*store.Account{}
	for _, aid := range aids {
		entries["boss-"+aid] = &store.Account{AccessToken: "boss-" + aid, SessionKey: "sk", Aid: aid, Name: "Diver " + aid}
	}
	testutil.InstallAccounts(t, entries)
	store.AccountsMu.Lock()
	store.AccountStorePath = filepath.Join(t.TempDir(), "accounts.json")
	store.AccountsMu.Unlock()
	var clients []*sqClient
	for i, aid := range aids {
		c := newSqClient(t, sq, byte(i+1))
		n, err := strconv.ParseUint(aid, 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		c.join(n)
		for _, o := range clients {
			drainBoss(o)
		}
		clients = append(clients, c)
	}
	return clients
}

// drainBoss discards the frames already queued for c. The server writes them before the call that caused them returns.
func drainBoss(c *sqClient) {
	c.t.Helper()
	for {
		_ = c.peer.SetReadDeadline(time.Now().Add(20 * time.Millisecond))
		var pre [4]byte
		if _, err := io.ReadFull(c.peer, pre[:]); err != nil {
			return
		}
		f := make([]byte, binary.BigEndian.Uint32(pre[:]))
		if _, err := io.ReadFull(c.peer, f); err != nil {
			c.t.Fatalf("drain: %v", err)
		}
	}
}

// bossSeat is one FishBossUserInLobby: who, whether the master, and the State3 (1 not ready, 2 ready).
type bossSeat struct{ aid, master, state uint64 }

func bossSeatOf(t *testing.T, u []byte) bossSeat {
	t.Helper()
	return bossSeat{aid: squareUint(t, u, 1), master: squareUint(t, u, 2), state: squareUint(t, u, 3)}
}

// bossLobby is cr46 for c: RoomState and the members of rc59 Users3, in order.
func bossLobby(t *testing.T, c *sqClient) (uint64, []bossSeat) {
	t.Helper()
	c.send(46, nil)
	id, body := c.read()
	if id != 59 {
		t.Fatalf("lobby = %d/%x, want 59", id, body)
	}
	var users []bossSeat
	for _, u := range pbAll(t, body, 3) {
		users = append(users, bossSeatOf(t, u))
	}
	return squareUint(t, body, 1), users
}

// bossRoster70 reads one rc70 from c: the LogoutUser (0 = none) and the User2 entry, when there is one.
func bossRoster70(t *testing.T, c *sqClient) (logout uint64, user *bossSeat) {
	t.Helper()
	id, body := c.read()
	if id != 70 {
		t.Fatalf("roster push = %d/%x, want 70", id, body)
	}
	logout, _ = protobufUintField(body, 1)
	if d, found, _ := pbBytesField(body, 2); found {
		u := bossSeatOf(t, d)
		user = &u
	}
	return logout, user
}

// bossHit sends cr50 rows {TouchResult, NoteIndex, TouchCount} from c and returns the rc64 the sender got.
func bossHit(t *testing.T, c *sqClient, rows ...[3]uint64) []byte {
	t.Helper()
	var body []byte
	for _, r := range rows {
		body = pbLen(body, 2, pbVar(pbVar(pbVar(nil, 1, r[0]), 3, r[1]), 4, r[2]))
	}
	c.send(50, body)
	id, reply := c.read()
	if id != 64 {
		t.Fatalf("touch reply = %d/%x, want 64", id, reply)
	}
	return reply
}

// bossFinishFrom sends cr58 from c and returns the rc65 body, failing unless it is one.
func bossFinishFrom(t *testing.T, c *sqClient) []byte {
	t.Helper()
	c.send(58, nil)
	id, body := c.read()
	if id != 65 {
		t.Fatalf("finish = %d/%x, want 65", id, body)
	}
	return body
}

// bossTries is the boss tries the account spent on the current game day.
func bossTries(t *testing.T, aid string) int64 {
	t.Helper()
	store.AccountsMu.Lock()
	defer store.AccountsMu.Unlock()
	acc := store.AccountByAidLocked(aid)
	if acc == nil {
		t.Fatalf("no account %s", aid)
	}
	if acc.BossTries == nil {
		return 0
	}
	return acc.BossTries.Used
}

// bossLedger returns the ledger lines of aid whose reason starts with prefix (the boss pays through the ledger).
func bossLedger(t *testing.T, aid, prefix string) []store.LedgerLine {
	t.Helper()
	store.AccountsMu.Lock()
	path := filepath.Join(filepath.Dir(store.AccountStorePath), "ledger.jsonl")
	store.AccountsMu.Unlock()
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	var out []store.LedgerLine
	for _, raw := range bytes.Split(b, []byte("\n")) {
		if len(raw) == 0 {
			continue
		}
		var l store.LedgerLine
		if err := json.Unmarshal(raw, &l); err != nil {
			t.Fatalf("ledger line %q: %v", raw, err)
		}
		if l.Aid == aid && strings.HasPrefix(l.Reason, prefix) {
			out = append(out, l)
		}
	}
	return out
}

// bossLobbies counts the lobbies of a Square.
func bossLobbies(sq *squareRoom) int {
	sq.mu.Lock()
	defer sq.mu.Unlock()
	return len(sq.bosses)
}

// bossSeats returns the members of the Square's index-th lobby, in join order.
func bossSeats(sq *squareRoom, index int) []uint64 {
	sq.mu.Lock()
	defer sq.mu.Unlock()
	var out []uint64
	for _, m := range sq.bosses[index].members {
		out = append(out, m.aid())
	}
	return out
}

// TestSquareBossLobbyPlacement: the banner joins the oldest open lobby of the Square, every member already there is
// told (rc70), the lobby roster shows the master first, and each banner spends one try.
func TestSquareBossLobbyPlacement(t *testing.T) {
	room := &squareRoom{}
	cs := bossRoomClients(t, room, "9901", "9902", "9903")
	a, b, d := cs[0], cs[1], cs[2]
	bossStart(t, a) // opens the lobby: A is the master
	bossStart(t, b) // joins A's lobby
	if logout, user := bossRoster70(t, a); logout != 0 || user == nil || user.aid != 9902 || user.master != 0 || user.state != squareBossNotReady {
		t.Fatalf("A told of B = logout %d user %+v", logout, user)
	}
	bossStart(t, d)
	if _, user := bossRoster70(t, a); user == nil || user.aid != 9903 {
		t.Fatalf("A told of D = %+v", user)
	}
	if _, user := bossRoster70(t, b); user == nil || user.aid != 9903 {
		t.Fatalf("B told of D = %+v", user)
	}
	if n := bossLobbies(room); n != 1 {
		t.Fatalf("lobbies = %d, want one shared lobby", n)
	}
	state, users := bossLobby(t, d)
	want := []bossSeat{{9901, 1, squareBossReady}, {9902, 0, squareBossNotReady}, {9903, 0, squareBossNotReady}}
	if state != squareBossRoomState || len(users) != len(want) {
		t.Fatalf("lobby state %d users %+v, want %v", state, users, want)
	}
	for i := range want {
		if users[i] != want[i] {
			t.Fatalf("lobby member %d = %+v, want %+v", i, users[i], want[i])
		}
	}
	for _, aid := range []string{"9901", "9902", "9903"} {
		if got := bossTries(t, aid); got != 1 {
			t.Fatalf("tries spent by %s = %d, want 1", aid, got)
		}
	}
}

// TestSquareBossFullLobbyOpensAnother: a full lobby (max_players) takes no one; the next banner opens a new lobby
// and later banners fill the oldest lobby that has room.
func TestSquareBossFullLobbyOpensAnother(t *testing.T) {
	boss := economy.ActiveFishBoss()
	boss.MaxPlayers = 2
	t.Cleanup(func() { boss.MaxPlayers = economy.BossMaxSeats })
	room := &squareRoom{}
	cs := bossRoomClients(t, room, "9911", "9912", "9913", "9914")
	a, b, c, d := cs[0], cs[1], cs[2], cs[3]
	bossStart(t, a)
	bossStart(t, b) // lobby 1 is full now
	drainBoss(a)
	bossStart(t, c) // lobby 2: C is its master
	if n := bossLobbies(room); n != 2 {
		t.Fatalf("lobbies = %d, want 2", n)
	}
	if _, users := bossLobby(t, c); len(users) != 1 || users[0].aid != 9913 || users[0].master != 1 {
		t.Fatalf("new lobby = %+v, want C alone as master", users)
	}
	bossStart(t, d) // lobby 2 has room: the oldest lobby with room is 2, not 1
	if got := bossSeats(room, 0); len(got) != 2 {
		t.Fatalf("lobby 1 members = %v, want its two", got)
	}
	if got := bossSeats(room, 1); len(got) != 2 || got[1] != 9914 {
		t.Fatalf("lobby 2 members = %v, want C then D", got)
	}
}

// TestSquareBossReadyAndStart: a non-master's ready toggles are pushed to the lobby, only the master starts, and the
// start needs every non-master ready. rc71 goes to every member with the whole AidList.
func TestSquareBossReadyAndStart(t *testing.T) {
	room := &squareRoom{}
	cs := bossRoomClients(t, room, "9921", "9922")
	a, b := cs[0], cs[1]
	bossStart(t, a)
	bossStart(t, b)
	drainBoss(a)

	b.send(47, pbVar(nil, 1, squareBossStateStart)) // not the master: nothing
	b.none()
	a.send(47, pbVar(nil, 1, squareBossStateStart)) // B is not ready: refused
	a.none()
	b.none()

	b.send(47, pbVar(nil, 1, squareBossReadyReq)) // B is ready: both are told
	if logout, user := bossRoster70(t, a); logout != 0 || user == nil || user.aid != 9922 || user.state != squareBossReady {
		t.Fatalf("A told B ready = %d %+v", logout, user)
	}
	if logout, user := bossRoster70(t, b); logout != 0 || user == nil || user.aid != 9922 || user.state != squareBossReady {
		t.Fatalf("B told B ready = %d %+v", logout, user)
	}
	a.send(47, pbVar(nil, 1, squareBossReadyReq)) // the master's own 1 is not a ready state: nothing
	a.none()

	a.send(47, pbVar(nil, 1, squareBossStateStart))
	for _, c := range []*sqClient{a, b} {
		id, battle := c.read()
		if id != 71 {
			t.Fatalf("battle start = %d/%x, want 71", id, battle)
		}
		var aids []uint64
		if !pbScanAids(battle, &aids) || len(aids) != 2 || aids[0] != 9921 || aids[1] != 9922 {
			t.Fatalf("AidList = %v, want [9921 9922]", aids)
		}
	}
	b.send(47, pbVar(nil, 1, squareBossUnready)) // the battle started: no more ready changes
	b.none()
}

// pbScanAids reads the AidList (field 2) of rc71.
func pbScanAids(body []byte, out *[]uint64) bool {
	return pbScan(body, func(f, v uint64, _ []byte) {
		if f == 2 {
			*out = append(*out, v)
		}
	})
}

// TestSquareBossMasterHandover: the master's leave makes the earliest remaining member the master in one push, and
// that member starts the battle.
func TestSquareBossMasterHandover(t *testing.T) {
	room := &squareRoom{}
	cs := bossRoomClients(t, room, "9931", "9932", "9933")
	a, b, d := cs[0], cs[1], cs[2]
	bossStart(t, a)
	bossStart(t, b)
	bossStart(t, d)
	drainBoss(b)
	drainBoss(d)
	drainBoss(a)

	a.send(2, pbVar(nil, 1, squareThemeID)) // the master leaves for the Square
	if id, enter := a.read(); id != 6 || squareUint(t, enter, 4) != squareThemeID {
		t.Fatalf("leave = %d/%x, want rc6 theme %d", id, enter, squareThemeID)
	}
	for _, c := range []*sqClient{b, d} {
		id, body := c.read()
		if id != 70 || squareUint(t, body, 1) != 9931 {
			t.Fatalf("handover push = %d/%x, want LogoutUser 9931", id, body)
		}
		user := bossSeatOf(t, squareNested(t, body, 2))
		if user != (bossSeat{9932, 1, squareBossReady}) {
			t.Fatalf("new master = %+v, want B as master", user)
		}
	}
	b.send(47, pbVar(nil, 1, squareBossStateStart)) // D is not ready yet: the new master cannot start
	b.none()
	d.send(47, pbVar(nil, 1, squareBossReadyReq))
	bossRoster70(t, b)
	bossRoster70(t, d)
	b.send(47, pbVar(nil, 1, squareBossStateStart))
	for _, c := range []*sqClient{b, d} {
		if id, battle := c.read(); id != 71 {
			t.Fatalf("battle = %d/%x, want 71", id, battle)
		}
	}
}

// TestSquareBossDisconnectLeavesLobby: a dropped session leaves its lobby like cr2 does.
func TestSquareBossDisconnectLeavesLobby(t *testing.T) {
	room := &squareRoom{}
	cs := bossRoomClients(t, room, "9941", "9942")
	a, b := cs[0], cs[1]
	bossStart(t, a)
	bossStart(t, b)
	drainBoss(a)
	a.sess.close()
	if logout, user := bossRoster70(t, b); logout != 9941 || user == nil || user.aid != 9942 || user.master != 1 {
		t.Fatalf("after A dropped = logout %d user %+v", logout, user)
	}
	if n := bossLobbies(room); n != 1 {
		t.Fatalf("lobbies = %d, want the lobby kept for B", n)
	}
}

// TestSquareBossKick: the master removes a member before the battle (rc61 to the master, rc70 to all, the member
// included). A non-master, the master itself and a kick in the battle are refused with Result 1.
func TestSquareBossKick(t *testing.T) {
	room := &squareRoom{}
	cs := bossRoomClients(t, room, "9951", "9952", "9953")
	a, b, d := cs[0], cs[1], cs[2]
	bossStart(t, a)
	bossStart(t, b)
	bossStart(t, d)
	drainBoss(a)
	drainBoss(b)
	drainBoss(d)

	b.send(48, pbVar(nil, 1, 9953)) // not the master: refused
	if id, body := b.read(); id != 61 || squareUint(t, body, 1) != 1 || squareUint(t, body, 2) != 9953 {
		t.Fatalf("non-master kick = %d/%x, want refused", id, body)
	}
	a.send(48, pbVar(nil, 1, 9951)) // the master cannot kick itself
	if id, body := a.read(); id != 61 || squareUint(t, body, 1) != 1 {
		t.Fatalf("self kick = %d/%x, want refused", id, body)
	}
	drainBoss(a)

	a.send(48, pbVar(nil, 1, 9953))
	if id, body := a.read(); id != 61 || squareUint(t, body, 1) != 0 || squareUint(t, body, 2) != 9953 {
		t.Fatalf("kick = %d/%x, want Result 0", id, body)
	}
	for _, c := range []*sqClient{a, b, d} {
		if logout, user := bossRoster70(t, c); logout != 9953 || user != nil {
			t.Fatalf("roster after the kick = logout %d user %+v", logout, user)
		}
	}
	if _, users := bossLobby(t, a); len(users) != 2 {
		t.Fatalf("lobby after the kick = %+v, want two", users)
	}
	d.send(2, pbVar(nil, 1, squareThemeID)) // the kicked member leaves: nothing else changes
	d.read()
	a.none()
	b.none()
}

// TestSquareBossEmptyLobbyDeleted: a lobby whose last member leaves is deleted, and the next banner opens a new one.
func TestSquareBossEmptyLobbyDeleted(t *testing.T) {
	room := &squareRoom{}
	cs := bossRoomClients(t, room, "9961", "9962")
	a, b := cs[0], cs[1]
	bossStart(t, a)
	a.send(2, pbVar(nil, 1, squareThemeID))
	a.read()
	if n := bossLobbies(room); n != 0 {
		t.Fatalf("lobbies after the last member left = %d, want 0", n)
	}
	bossStart(t, b)
	if _, users := bossLobby(t, b); len(users) != 1 || users[0].master != 1 {
		t.Fatalf("new lobby = %+v, want B as master", users)
	}
}

// TestSquareBossInviteAccept: the master's invitation lets the invitee in (rc6 theme 28, the lobby follows); an
// uninvited session and a full lobby are refused with an rc6 that has no Result. Only the invitee's try is spent.
func TestSquareBossInviteAccept(t *testing.T) {
	boss := economy.ActiveFishBoss()
	boss.MaxPlayers = 2
	t.Cleanup(func() { boss.MaxPlayers = economy.BossMaxSeats })
	room := &squareRoom{}
	cs := bossRoomClients(t, room, "9971", "9972", "9973")
	a, b, c := cs[0], cs[1], cs[2]
	bossStart(t, a)
	drainBoss(a)

	a.send(35, pbVar(nil, 1, 9972)) // only B is invited; the invitation gets no reply
	a.none()

	c.send(36, pbVar(nil, 1, 9971)) // C was not invited
	if id, body := c.read(); id != 6 || squareUint(t, body, 1) != 0 {
		t.Fatalf("uninvited accept = %d/%x, want rc6 without a Result", id, body)
	}
	if got := bossTries(t, "9973"); got != 0 {
		t.Fatalf("a refused accept spent %d tries", got)
	}

	b.send(36, pbVar(nil, 1, 9971)) // B was invited: rc6 theme 28 into A's lobby
	if id, body := b.read(); id != 6 || squareUint(t, body, 4) != squareBossThemeID {
		t.Fatalf("accept = %d/%x, want rc6 theme %d", id, body, squareBossThemeID)
	}
	if logout, user := bossRoster70(t, a); logout != 0 || user == nil || user.aid != 9972 {
		t.Fatalf("A told of B = %d %+v", logout, user)
	}
	if _, users := bossLobby(t, b); len(users) != 2 {
		t.Fatalf("lobby after the accept = %+v, want two", users)
	}
	if got := bossTries(t, "9972"); got != 1 {
		t.Fatalf("tries spent by B = %d, want 1", got)
	}

	a.send(35, pbVar(nil, 1, 9973)) // the lobby is full now (max_players 2)
	c.send(36, pbVar(nil, 1, 9971))
	if id, body := c.read(); id != 6 || squareUint(t, body, 1) != 0 {
		t.Fatalf("full-lobby accept = %d/%x, want refused", id, body)
	}
}

// TestSquareBossSharedHp: a battle starts for two members; each member's cr55 gets its own rc73; every touch
// request pushes the same rc64 to both (the shared HP and the sender's cumulative DamageSum). Each member's
// buttons count on their own, so both can hit note 0.
func TestSquareBossSharedHp(t *testing.T) {
	room := &squareRoom{}
	cs := bossRoomClients(t, room, "9981", "9982")
	a, b := cs[0], cs[1]
	bossStart(t, a)
	bossStart(t, b)
	drainBoss(a)
	b.send(47, pbVar(nil, 1, squareBossReadyReq))
	bossRoster70(t, a)
	bossRoster70(t, b)
	a.send(47, pbVar(nil, 1, squareBossStateStart))
	a.read()
	b.read()

	a.send(55, nil)
	if id, body := a.read(); id != 73 {
		t.Fatalf("A loaded = %d/%x, want rc73", id, body)
	}
	b.none()
	b.send(55, nil)
	if id, _ := b.read(); id != 73 {
		t.Fatalf("B loaded = %d, want 73", id)
	}
	a.none()

	boss := economy.ActiveFishBoss()
	dmg := uint64(boss.DamagePerHit)
	maxHp := uint64(boss.Boss.MaxHp)
	push := bossHit(t, a, [3]uint64{1, 0, 1})
	if got := squareUint(t, squareNested(t, push, 2), 1); got != maxHp-dmg {
		t.Fatalf("shared HP after A = %d, want %d", got, maxHp-dmg)
	}
	if got := squareUint(t, squareNested(t, push, 3), 2); got != dmg {
		t.Fatalf("A DamageSum = %d, want %d", got, dmg)
	}
	if id, body := b.read(); id != 64 || !bytes.Equal(body, push) {
		t.Fatalf("B's rc64 = %d/%x, want A's %x", id, body, push)
	}

	second := bossHit(t, b, [3]uint64{1, 0, 1}) // B's own note 0: a new budget, not blocked by A's hit
	if got := squareUint(t, squareNested(t, second, 2), 1); got != maxHp-2*dmg {
		t.Fatalf("shared HP after B = %d, want %d", got, maxHp-2*dmg)
	}
	if id, body := a.read(); id != 64 || !bytes.Equal(body, second) {
		t.Fatalf("A's rc64 for B = %d/%x, want %x", id, body, second)
	}
}

// TestSquareBossCatchSplitsTheResult: the members hit the boss to 0 HP; each member finishes once and gets the same
// caught result, ranked by damage (ties by join order), and is paid the catch. A repeated finish is not paid again.
func TestSquareBossCatchSplitsTheResult(t *testing.T) {
	room := &squareRoom{}
	cs := bossRoomClients(t, room, "9991", "9992")
	a, b := cs[0], cs[1]
	baseA, baseB := fishingBalance(t, "9991"), fishingBalance(t, "9992")
	bossStart(t, a)
	bossStart(t, b)
	drainBoss(a)
	b.send(47, pbVar(nil, 1, squareBossReadyReq))
	bossRoster70(t, a)
	bossRoster70(t, b)
	a.send(47, pbVar(nil, 1, squareBossStateStart))
	a.read()
	b.read()

	boss := economy.ActiveFishBoss()
	need := int(boss.Boss.MaxHp / boss.DamagePerHit) // ten notes each hit once: 20 hits
	half := need / 2
	for i := range half { // A hits notes 0..9 and B notes 0..9, one hit each per note
		bossHit(t, a, [3]uint64{1, uint64(i), 1})
		drainBoss(b)
		bossHit(t, b, [3]uint64{1, uint64(i), 1})
		drainBoss(a)
	}
	// Both have equal damage: A joined first, so A ranks first; B is shown second.
	end := bossFinishFrom(t, a)
	users := pbAll(t, end, 2)
	if squareUint(t, end, 1) != 1 || len(users) != 2 || squareUint(t, users[0], 1) != 9991 || squareUint(t, users[0], 2) != 1 ||
		squareUint(t, users[1], 1) != 9992 || squareUint(t, users[1], 2) != 2 {
		t.Fatalf("A's result = %x, want caught, A rank 1, B rank 2", end)
	}
	gems := boss.Reward.GemsParticipation + boss.Reward.GemsCatch
	coins := boss.Reward.CoinsParticipation + boss.Reward.CoinsCatch
	if got := fishingBalance(t, "9991"); got != baseA+gems {
		t.Fatalf("A Gems = %d, want %d", got, baseA+gems)
	}
	if lines := bossLedger(t, "9991", "fishboss:4901:"); len(lines) != 1 || lines[0].Delta != gems {
		t.Fatalf("A ledger = %+v, want one line of %d Gems", lines, gems)
	}
	if c, caught := bossRecord(t, "9991"); c != coins || caught[boss.Boss.ID] != 1 {
		t.Fatalf("A record = coins %d caught %v", c, caught)
	}

	if other := bossFinishFrom(t, b); !bytes.Equal(other, end) {
		t.Fatalf("B's result = %x, want the same as A's %x", other, end)
	}
	if got := fishingBalance(t, "9992"); got != baseB+gems {
		t.Fatalf("B Gems = %d, want %d", got, baseB+gems)
	}
	if lines := bossLedger(t, "9992", "fishboss:4901:"); len(lines) != 1 || lines[0].Delta != gems {
		t.Fatalf("B ledger = %+v, want one line of %d Gems", lines, gems)
	}
	a.send(58, nil) // a repeated finish: the same result, not paid again
	if again, body := a.read(); again != 65 || !bytes.Equal(body, end) {
		t.Fatalf("repeated finish = %d/%x", again, body)
	}
	if got := fishingBalance(t, "9991"); got != baseA+gems {
		t.Fatalf("A Gems after a repeat = %d, want %d", got, baseA+gems)
	}
	if lines := bossLedger(t, "9991", "fishboss:4901:"); len(lines) != 1 {
		t.Fatalf("A ledger after a repeat = %d lines, want 1", len(lines))
	}
}

// TestSquareBossTimeEndsForEveryone: the first finish without HP 0 ends the battle: each member is paid the
// participation only, the ranks follow the damage, and a touch after the end is refused.
func TestSquareBossTimeEndsForEveryone(t *testing.T) {
	room := &squareRoom{}
	cs := bossRoomClients(t, room, "9995", "9996")
	a, b := cs[0], cs[1]
	baseA, baseB := fishingBalance(t, "9995"), fishingBalance(t, "9996")
	bossStart(t, a)
	bossStart(t, b)
	drainBoss(a)
	b.send(47, pbVar(nil, 1, squareBossReadyReq))
	bossRoster70(t, a)
	bossRoster70(t, b)
	a.send(47, pbVar(nil, 1, squareBossStateStart))
	a.read()
	b.read()

	bossHit(t, a, [3]uint64{1, 0, 1})
	drainBoss(b)
	bossHit(t, b, [3]uint64{1, 0, 1}, [3]uint64{1, 1, 1}) // B deals more: B ranks first
	drainBoss(a)

	end := bossFinishFrom(t, a)
	users := pbAll(t, end, 2)
	if squareUint(t, end, 1) != 0 || len(users) != 2 || squareUint(t, users[0], 1) != 9996 || squareUint(t, users[0], 2) != 1 {
		t.Fatalf("result = %x, want not caught and B ranked first", end)
	}
	participation := economy.ActiveFishBoss().Reward.GemsParticipation
	if got := fishingBalance(t, "9995"); got != baseA+participation {
		t.Fatalf("A Gems = %d, want the participation %d on top of %d", got, participation, baseA)
	}
	if got, caught := bossRecord(t, "9995"); got != economy.ActiveFishBoss().Reward.CoinsParticipation || len(caught) != 0 {
		t.Fatalf("A coins = %d caught %v, want the participation coins and no catch", got, caught)
	}

	b.send(50, pbLen(nil, 2, pbVar(pbVar(pbVar(nil, 1, 1), 3, 2), 4, 1))) // the battle is over: no touch
	b.none()
	if other := bossFinishFrom(t, b); !bytes.Equal(other, end) {
		t.Fatalf("B's result = %x, want the same", other)
	}
	if lines := bossLedger(t, "9996", "fishboss:4901:"); len(lines) != 1 || lines[0].Delta != participation {
		t.Fatalf("B ledger = %+v, want one participation line of %d Gems", lines, participation)
	}
	if got := fishingBalance(t, "9996"); got != baseB+participation {
		t.Fatalf("B Gems = %d, want the participation %d on top of %d", got, participation, baseB)
	}
}

// TestSquareBossLeaveMidBattleForfeits: a member who leaves in the battle is removed for the rest (rc70), keeps the
// damage it dealt in the shared HP, and gets no result or reward.
func TestSquareBossLeaveMidBattleForfeits(t *testing.T) {
	room := &squareRoom{}
	cs := bossRoomClients(t, room, "9985", "9986")
	a, b := cs[0], cs[1]
	baseB := fishingBalance(t, "9986")
	bossStart(t, a)
	bossStart(t, b)
	drainBoss(a)
	b.send(47, pbVar(nil, 1, squareBossReadyReq))
	bossRoster70(t, a)
	bossRoster70(t, b)
	a.send(47, pbVar(nil, 1, squareBossStateStart))
	a.read()
	b.read()

	boss := economy.ActiveFishBoss()
	bossHit(t, b, [3]uint64{1, 0, 1})
	drainBoss(a)
	b.send(2, pbVar(nil, 1, squareThemeID))
	if id, _ := b.read(); id != 6 {
		t.Fatalf("B leaves = %d, want rc6", id)
	}
	if logout, user := bossRoster70(t, a); logout != 9986 || user != nil {
		t.Fatalf("A told of B's leave = %d %+v", logout, user)
	}

	reply := bossHit(t, a, [3]uint64{1, 1, 1})
	if got := squareUint(t, squareNested(t, reply, 2), 1); got != uint64(boss.Boss.MaxHp-2*boss.DamagePerHit) {
		t.Fatalf("shared HP after B left = %d, want B's damage kept", got)
	}
	end := bossFinishFrom(t, a)
	if users := pbAll(t, end, 2); len(users) != 1 || squareUint(t, users[0], 1) != 9985 {
		t.Fatalf("result after the leave = %x, want A alone", end)
	}
	b.send(58, nil) // B left the battle: nothing to answer
	b.none()
	if got := fishingBalance(t, "9986"); got != baseB {
		t.Fatalf("B Gems after leaving = %d, want %d", got, baseB)
	}
}

// TestSquareBossSticker: a lobby sticker reaches every member, the sender included (rc79).
func TestSquareBossSticker(t *testing.T) {
	room := &squareRoom{}
	cs := bossRoomClients(t, room, "9901", "9902")
	a, b := cs[0], cs[1]
	bossStart(t, a)
	bossStart(t, b)
	drainBoss(a)
	b.send(60, pbVar(nil, 2, 7))
	want := pbVar(pbVar(pbVar(nil, 1, 9902), 2, 0), 3, 7)
	for _, c := range []*sqClient{a, b} {
		c.expect(79, want)
	}
}

// TestSquareBossLobbiesPerSquare: a lobby belongs to one Square; the same boss banner in another Square opens its own.
func TestSquareBossLobbiesPerSquare(t *testing.T) {
	rooms := &squareRooms{}
	a := newSqClientIn(t, rooms, 1)
	b := newSqClientIn(t, rooms, 2)
	testutil.InstallAccounts(t, map[string]*store.Account{
		"boss-9901": {AccessToken: "boss-9901", SessionKey: "sk", Aid: "9901", Name: "Diver 1"},
		"boss-9902": {AccessToken: "boss-9902", SessionKey: "sk", Aid: "9902", Name: "Diver 2"},
	})
	store.AccountsMu.Lock()
	store.AccountStorePath = filepath.Join(t.TempDir(), "accounts.json")
	store.AccountsMu.Unlock()
	a.joinTheme(9901, squareThemeID)
	b.joinTheme(9902, 32)
	bossStart(t, a)
	bossStart(t, b)
	if _, users := bossLobby(t, b); len(users) != 1 || users[0].aid != 9902 {
		t.Fatalf("Square 32 lobby = %+v, want B alone", users)
	}
	if _, users := bossLobby(t, a); len(users) != 1 || users[0].aid != 9901 {
		t.Fatalf("Square 6 lobby = %+v, want A alone", users)
	}
}

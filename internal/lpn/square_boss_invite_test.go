package lpn

import (
	"bytes"
	"encoding/json"
	"log"
	"net"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"cherry/internal/store"
	"cherry/internal/testutil"
)

// inviteAccounts installs the accounts of one invite test. Each has its own session key: sendInit binds a :10123
// session by its key, so a key shared by two accounts would bind to either of them.
func inviteAccounts(t *testing.T, entries map[string]*store.Account) {
	t.Helper()
	testutil.InstallAccounts(t, entries)
	store.AccountsMu.Lock()
	store.AccountStorePath = filepath.Join(t.TempDir(), "accounts.json")
	store.AccountsMu.Unlock()
}

// inviteeSession opens a :10123 session for the account with session key key, the way the client does: banner, hello,
// then sendInit. The reply comes after the bind, so the session can take pushes once inviteeSession returns.
func inviteeSession(t *testing.T, key string) net.Conn {
	t.Helper()
	conn, cleanup := startSessionServer(t)
	t.Cleanup(cleanup)
	readFullDeadline(t, conn, len(sessionBanner))
	if _, err := conn.Write(sessionHello); err != nil {
		t.Fatalf("write hello: %v", err)
	}
	body, err := json.Marshal(map[string]string{"sessionId": "1", "sessionKey": key, "country": "JP", "os": "android", "lpVer": "10.1.0.0"})
	if err != nil {
		t.Fatal(err)
	}
	writeSessionFrame(t, conn, buildLOFrame(loOpSendInit, 1, body)[4:])
	if reply := readSessionFrame(t, conn); !bytes.Equal(reply, buildLOFrame(loOpConfigure, 0, configurePayload)) {
		t.Fatalf("sendInit reply = %x", reply)
	}
	return conn
}

// readLOPush reads one session frame, which must be an LO frame, and returns its op, txn and payload.
func readLOPush(t *testing.T, conn net.Conn) (byte, uint64, []byte) {
	t.Helper()
	frame := readSessionFrame(t, conn)
	op, txn, payload, ok := parseLOFrame(frame[4:])
	if !ok {
		t.Fatalf("session frame %x is not an LO frame", frame)
	}
	return op, txn, payload
}

// TestSquareBossInvitePushesToSession: the master's cr35 pushes the invite to the invitee's :10123 session as one LO
// frame (op 0x1B, txn 0) whose JSON names the master (boss-mp-addendum 1e and 1j). The master gets no reply.
func TestSquareBossInvitePushesToSession(t *testing.T) {
	inviteAccounts(t, map[string]*store.Account{
		"boss-9961": {AccessToken: "boss-9961", SessionKey: "sk-9961", Aid: "9961", Name: "Master"},
		"boss-9962": {AccessToken: "boss-9962", SessionKey: "sk-9962", Aid: "9962", Name: "Invitee"},
	})
	master := newSqClient(t, &squareRoom{}, 1)
	master.join(9961)
	bossStart(t, master)
	drainBoss(master)
	invitee := inviteeSession(t, "sk-9962")

	master.send(35, pbVar(nil, 1, 9962))
	master.none()

	op, txn, payload := readLOPush(t, invitee)
	if op != loOpPush || txn != 0 {
		t.Fatalf("push op=%#02x txn=%d, want op 0x1b txn 0", op, txn)
	}
	var got map[string]any
	if err := json.Unmarshal(payload, &got); err != nil {
		t.Fatalf("push payload is not JSON: %v", err)
	}
	if id, _ := got["msgid"].(string); id == "" {
		t.Fatalf("push msgid empty: %s", payload)
	}
	delete(got, "msgid")
	want := map[string]any{
		"catg":     "square",
		"sender":   "9961",
		"content":  "Master invites you to a Boss Raid!",
		"params":   map[string]any{"pType": "SIR"},
		"mapParam": map[string]any{"catg": "square", "aid": "9961"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("push = %v, want %v", got, want)
	}
}

// TestSquareBossInviteWithoutSessionLogged: an invitee with no :10123 session gets no push. The cr35 neither panics
// nor replies, and the log line says sent=false.
func TestSquareBossInviteWithoutSessionLogged(t *testing.T) {
	inviteAccounts(t, map[string]*store.Account{
		"boss-9963": {AccessToken: "boss-9963", SessionKey: "sk-9963", Aid: "9963", Name: "Master"},
	})
	master := newSqClient(t, &squareRoom{}, 1)
	master.join(9963)
	bossStart(t, master)
	drainBoss(master)
	var logs bytes.Buffer
	master.sess.logger = log.New(&logs, "", 0)

	master.send(35, pbVar(nil, 1, 9964)) // 9964 has no session
	master.none()

	if want := "SQUARE fishing boss invite push aid=9963 to=9964 sent=false"; !strings.Contains(logs.String(), want) {
		t.Fatalf("log = %q, want a line %q", logs.String(), want)
	}
}

// TestSquareBossAcceptFromAnotherSquare: the master's lobby is in LINE City (6). One invitee accepts from Square 32, and
// one from no Square at all (logged in only, as after a Garden entry). The lobby is found by the master's aid, and the
// invitation by the invitee's aid, so both enter (rc6 theme 28).
func TestSquareBossAcceptFromAnotherSquare(t *testing.T) {
	inviteAccounts(t, map[string]*store.Account{
		"boss-9965": {AccessToken: "boss-9965", SessionKey: "sk-9965", Aid: "9965", Name: "Master"},
		"boss-9966": {AccessToken: "boss-9966", SessionKey: "sk-9966", Aid: "9966", Name: "Square"},
		"boss-9967": {AccessToken: "boss-9967", SessionKey: "sk-9967", Aid: "9967", Name: "Garden"},
	})
	rooms := &squareRooms{}
	master := newSqClientIn(t, rooms, 1)
	inSquare := newSqClientIn(t, rooms, 2)
	inSquare.joinTheme(9966, 32)
	outside := newSqClientIn(t, rooms, 3)
	outside.send(0, loginBody(9967))
	outside.expect(0, []byte{0x08, 0x81, 0x80, 0x08}) // login result 131073
	master.join(9965)
	bossStart(t, master)
	drainBoss(master)

	master.send(35, pbVar(pbVar(nil, 1, 9966), 1, 9967))
	master.none()

	for _, c := range []*sqClient{inSquare, outside} {
		c.send(36, pbVar(nil, 1, 9965))
		if id, body := c.read(); id != 6 || squareUint(t, body, 4) != squareBossThemeID {
			t.Fatalf("accept from aid %d = %d/%x, want rc6 theme %d", c.sess.aid, id, body, squareBossThemeID)
		}
	}
	drainBoss(master) // the rc70 of each accept
	if _, users := bossLobby(t, master); len(users) != 3 {
		t.Fatalf("lobby after two accepts = %+v, want three", users)
	}
	// The accept counts as the Square enter: the Garden invitee's relaystart (it sends no cr2) is answered.
	outside.send(5, nil)
	if id, _ := outside.read(); id != 8 {
		t.Fatalf("relaystart after the accept = rc%d, want rc8", id)
	}
}

// TestSquareBossRosterRodAndPicture: every lobby entry carries RodInfo4 (the rod in use, or the shop's first rod) with
// an absolute picture URL, and Profile5 is never empty (boss-mp-addendum 3).
func TestSquareBossRosterRodAndPicture(t *testing.T) {
	inviteAccounts(t, map[string]*store.Account{
		"boss-9991": {AccessToken: "boss-9991", SessionKey: "sk-9991", Aid: "9991", Name: "Master",
			ProfileImage: store.ProfileImage{Image: "/lineplay/pr/9991_1700000000"}},
		"boss-9992": {AccessToken: "boss-9992", SessionKey: "sk-9992", Aid: "9992", Name: "Member",
			Gear: &store.FishingGear{Owned: []int64{9002}, Rod: 9002}},
	})
	room := &squareRoom{}
	master := newSqClient(t, room, 1)
	member := newSqClient(t, room, 2)
	master.join(9991)
	bossStart(t, master)
	member.join(9992)
	bossStart(t, member)
	drainBoss(master)

	master.send(46, nil)
	id, body := master.read()
	if id != 59 {
		t.Fatalf("roster reply = %d, want rc59", id)
	}
	const static = "https://play-static.line-scdn.net"
	want := map[uint64]struct {
		rod     uint64
		profile string
	}{
		9991: {9001, static + "/lineplay/pr/9991_1700000000"}, // no rod in use: the shop's first rod
		9992: {9002, static + "/cherry/menu/user-star.png"},   // the rod in use; no picture: the placeholder
	}
	seen := 0
	ok := pbScan(body, func(f, _ uint64, d []byte) {
		if f != 3 || d == nil {
			return
		}
		aid := squareUint(t, d, 1)
		exp, known := want[aid]
		if !known {
			t.Fatalf("unexpected roster entry aid=%d", aid)
		}
		seen++
		rod, found, ok := pbBytesField(d, 4)
		if !found || !ok {
			t.Fatalf("aid %d: RodInfo4 missing", aid)
		}
		itemID, _ := protobufUintField(rod, 1)
		itemType, _ := protobufUintField(rod, 8)
		if itemID != exp.rod || itemType != 2 {
			t.Fatalf("aid %d: rod ItemId=%d ItemType=%d, want rod %d (ItemType 2)", aid, itemID, itemType, exp.rod)
		}
		url, _, _ := pbBytesField(rod, 5)
		playing, _, _ := pbBytesField(rod, 6)
		wantURL := static + "/cherry/fishing/" + strconv.FormatUint(exp.rod, 10) + ".png"
		if string(url) != wantURL || len(playing) != 0 {
			t.Fatalf("aid %d: rod ImageUrl %q, PlayingImageUrl %q; want %q and empty", aid, url, playing, wantURL)
		}
		if profile, _, _ := pbBytesField(d, 5); string(profile) != exp.profile {
			t.Fatalf("aid %d: Profile5 = %q, want %q", aid, profile, exp.profile)
		}
	})
	if !ok || seen != 2 {
		t.Fatalf("roster ok=%v with %d entries checked, want 2", ok, seen)
	}
}

// TestSquareBossInvitedList: the invite picker's cr37 gets rc46 with the aids the lobby already invited (empty before
// any invite), which the picker waits for before it lists friends.
func TestSquareBossInvitedList(t *testing.T) {
	inviteAccounts(t, map[string]*store.Account{
		"boss-9961": {AccessToken: "boss-9961", SessionKey: "sk-9961", Aid: "9961", Name: "Master"},
	})
	master := newSqClient(t, &squareRoom{}, 1)
	master.join(9961)
	bossStart(t, master)
	drainBoss(master)

	master.send(37, nil)
	master.expect(46, nil)
	master.send(35, pbVar(pbVar(nil, 1, 9972), 1, 9970))
	master.none()
	master.send(37, nil)
	master.expect(46, pbVar(pbVar(nil, 1, 9970), 1, 9972))
}

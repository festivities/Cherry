package lpn

import (
	"bytes"
	"encoding/binary"
	"log"
	"net"
	"slices"
	"strings"
	"testing"
	"time"

	"cherry/internal/economy"
	"cherry/internal/store"
	"cherry/internal/testutil"
)

// tlupParamData is one ParamData: Name #1 string, Intvalue #2 varint.
func tlupParamData(name string, value uint64) []byte {
	return pbVar(pbLen(nil, 1, []byte(name)), 2, value)
}

// tlupEventData is one EventRequestData: ServiceId #1, Eventid #2, Param #3 (repeated).
func tlupEventData(serviceID, eventID uint64, params ...[]byte) []byte {
	b := pbVar(pbVar(nil, 1, serviceID), 2, eventID)
	for _, p := range params {
		b = pbLen(b, 3, p)
	}
	return b
}

// tlupClientEvent is a numbered ct_clientevent_req: ClientSession #1 (sessionKey #1, aid #2),
// packetNo #2, then the events (#3).
func tlupClientEvent(aid, packetNo uint64, events ...[]byte) []byte {
	session := pbVar(pbLen(nil, 1, []byte("K")), 2, aid)
	b := pbVar(pbLen(nil, 1, session), 2, packetNo)
	for _, e := range events {
		b = pbLen(b, 3, e)
	}
	return b
}

// heartEvent is the stock avatar heart to target (Garden, profile and ranking buttons).
func heartEvent(target uint64) []byte {
	return tlupEventData(102, 12, tlupParamData("youraid", target))
}

// TestTLUPWireLayout pins the bytes NaQuestManager::ReqSendHeart (0x22b32ac) serializes for a
// profile heart to aid 1001: ServiceId 102 (field 1), Eventid 12 (field 2), and youraid as an
// Intvalue (ParamData field 2), not a string.
func TestTLUPWireLayout(t *testing.T) {
	want := []byte{0x08, 0x66, 0x10, 0x0c, 0x1a, 0x0c, 0x0a, 0x07, 'y', 'o', 'u', 'r', 'a', 'i', 'd', 0x10, 0xe9, 0x07}
	if got := heartEvent(1001); !bytes.Equal(got, want) {
		t.Fatalf("heart event = %x, want %x", got, want)
	}
}

// TestTLUPClientEvents: the sender is the ClientSession aid, and only ServiceId 102 with Eventid
// 12 (avatar) or 13 (pet) and a youraid target counts as a heart.
func TestTLUPClientEvents(t *testing.T) {
	petHeart := func(target uint64) []byte { return tlupEventData(102, 13, tlupParamData("youraid", target)) }
	for _, c := range []struct {
		name       string
		data       []byte
		wantSender uint64
		wantHearts []uint64
	}{
		{"avatar heart", tlupClientEvent(9701, 7, heartEvent(9702)), 9701, []uint64{9702}},
		{"pet heart", tlupClientEvent(9701, 7, petHeart(9702)), 9701, []uint64{9702}},
		{"two hearts", tlupClientEvent(9701, 7, heartEvent(9702), petHeart(9703)), 9701, []uint64{9702, 9703}},
		{"other eventid", tlupClientEvent(9701, 7, tlupEventData(102, 14, tlupParamData("youraid", 9702))), 9701, nil},
		{"diary heart", tlupClientEvent(9701, 7, tlupEventData(106, 15, tlupParamData("youraid", 9702))), 9701, nil},
		{"ids swapped", tlupClientEvent(9701, 7, tlupEventData(12, 102, tlupParamData("youraid", 9702))), 9701, nil},
		{"no youraid", tlupClientEvent(9701, 7, tlupEventData(102, 12, tlupParamData("cell_idx", 9702))), 9701, nil},
		{"zero target", tlupClientEvent(9701, 7, heartEvent(0)), 9701, nil},
		{"session only", tlupClientEvent(9701, 0), 9701, nil},
		{"no session", pbVar(nil, 2, 7), 0, nil},
		{"zero sender", tlupClientEvent(0, 7, heartEvent(9702)), 0, nil},
		{"truncated", append(tlupClientEvent(9701, 7, heartEvent(9702)), 0x1a), 0, nil},
	} {
		sender, events, ok := tlupClientEvents(c.data)
		if (c.wantSender != 0) != ok || sender != c.wantSender {
			t.Errorf("%s: sender %d ok %v, want sender %d", c.name, sender, ok, c.wantSender)
			continue
		}
		var hearts []uint64
		for _, ev := range events {
			if ev.isHeart() {
				hearts = append(hearts, ev.target)
			}
		}
		if !slices.Equal(hearts, c.wantHearts) {
			t.Errorf("%s: hearts %v, want %v", c.name, hearts, c.wantHearts)
		}
	}
}

// TestRecordTLUPEvents: five counted hearts pay the sender once through the economy and queue one
// hearts notice; a sixth heart counts nothing. Each event logs its ids and ParamData names only.
func TestRecordTLUPEvents(t *testing.T) {
	testutil.InstallAccounts(t, map[string]*store.Account{
		"rtok-9711": {AccessToken: "rtok-9711", SessionKey: "sk", Aid: "9711", Name: "S"},
		"rtok-9712": {AccessToken: "rtok-9712", SessionKey: "sk", Aid: "9712", Name: "T"},
	})
	testutil.SetLab(t, "9711", false)
	testutil.SetLab(t, "9712", false)
	now := time.Date(2026, time.October, 22, 12, 0, 0, 0, time.UTC)
	old := economy.NowFunc
	economy.NowFunc = func() time.Time { return now }
	t.Cleanup(func() { economy.NowFunc = old })
	var logs bytes.Buffer
	logger := log.New(&logs, "", 0)

	for range economy.HeartBase() {
		recordTLUPEvents(logger, tlupClientEvent(9711, 3, heartEvent(9712)))
	}
	recordTLUPEvents(logger, tlupClientEvent(9711, 4, heartEvent(9712))) // refused: cap
	if got := economy.HeartsReceived("9712"); got != economy.HeartBase() {
		t.Fatalf("received = %d, want %d", got, economy.HeartBase())
	}
	store.AccountsMu.Lock()
	gems := store.AccountByAidLocked("9711").Gems
	store.AccountsMu.Unlock()
	if gems != economy.HeartRewardGems() {
		t.Fatalf("sender gems = %d, want %d", gems, economy.HeartRewardGems())
	}
	if notes := takeQuestNotices(9711); len(notes) != 1 || notes[0].ID != economy.HeartQuest().ID {
		t.Fatalf("notices = %+v, want one hearts notice", notes)
	}
	if got := strings.Count(logs.String(), "cherry: tlup event aid=9711 "); got != int(economy.HeartBase())+1 {
		t.Fatalf("tlup event lines = %d, want %d", got, economy.HeartBase()+1)
	}
	if line := `cherry: tlup event aid=9711 service=102 event=12 params=["youraid"]` + "\n"; !strings.Contains(logs.String(), line) {
		t.Fatalf("log lacks %q in %q", line, logs.String())
	}
}

// TestGatewayTLUPHeartEventAcked: a numbered TLUP heart request through the gateway still gets the
// unchanged packetNo reply (24 bytes, same layout as the quest-request test), and the heart counts.
func TestGatewayTLUPHeartEventAcked(t *testing.T) {
	testutil.InstallAccounts(t, map[string]*store.Account{
		"gtok-9713": {AccessToken: "gtok-9713", SessionKey: "sk", Aid: "9713", Name: "S"},
		"gtok-9714": {AccessToken: "gtok-9714", SessionKey: "sk", Aid: "9714", Name: "T"},
	})
	testutil.SetLab(t, "9713", false)
	testutil.SetLab(t, "9714", false)
	server, client := net.Pipe()
	cleared := make(chan struct{}, 1)
	var output bytes.Buffer
	done := make(chan struct{})
	go func() {
		observeConn(deadlineRecordingConn{server, cleared}, GatewayAddr, log.New(&output, "", 0))
		close(done)
	}()
	defer func() {
		client.Close()
		<-done
	}()
	if banner := readFullDeadline(t, client, len(sessionBanner)); !bytes.Equal(banner, sessionBanner) {
		t.Fatalf("gateway banner = %x", banner)
	}
	body := tlupClientEvent(9713, 0x2a, heartEvent(9714))
	frame := []byte{'D', 0, 0, 1, 0, 1, 0, 0x6f, 0, 0, 5, 0, 0, 0, 0x42, 0, 0, 1}
	frame = append(frame, body...)
	wire := binary.BigEndian.AppendUint32(nil, uint32(len(frame)))
	if _, err := client.Write(append(wire, frame...)); err != nil {
		t.Fatalf("tlup write: %v", err)
	}
	reply := readFullDeadline(t, client, 24)
	want := []byte{0, 0, 0, 20, 'D', 0, 0, 1, 0, 1, 0, 0x6f, 0, 0, 5, 0, 0, 0, 0x42, 0, 0, 1, 0x08, 0x2a}
	if !bytes.Equal(reply, want) {
		t.Fatalf("tlup reply = %x, want %x", reply, want)
	}
	if got := economy.HeartsReceived("9714"); got != 1 {
		t.Fatalf("received after gateway heart = %d, want 1", got)
	}
}

package lpn

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"log"
	"net"
	"slices"
	"strconv"
	"testing"
	"time"

	"cherry/internal/economy"
	"cherry/internal/store"
	"cherry/internal/testutil"
)

func decodeHighFiveTargets(t *testing.T, body []byte) []uint64 {
	t.Helper()
	var targets []uint64
	if !pbScan(body, func(f, v uint64, _ []byte) {
		if f == 1 {
			targets = append(targets, v)
		}
	}) {
		t.Fatalf("malformed rc_highfive_target_list_res: %x", body)
	}
	return targets
}

type highFiveAchieveMsg struct {
	result, myAid, targetAid, count uint64
	hasResult, hasSerial            bool
}

func decodeHighFiveAchieve(t *testing.T, body []byte) highFiveAchieveMsg {
	t.Helper()
	var msg highFiveAchieveMsg
	if !pbScan(body, func(f, v uint64, _ []byte) {
		switch f {
		case 1:
			msg.hasResult = true
			msg.result = v
		case 2:
			msg.myAid = v
		case 3:
			msg.targetAid = v
		case 4:
			msg.count = v
		case 5:
			msg.hasSerial = true
		}
	}) {
		t.Fatalf("malformed rc_highfive_achieve_res: %x", body)
	}
	return msg
}

func highFiveJoin(t *testing.T, c *sqClient, aid, theme uint64) {
	t.Helper()
	c.send(0, loginBody(aid))
	c.expect(0, []byte{0x08, 0x81, 0x80, 0x08})
	c.send(2, pbVar(nil, 1, theme))
	if id, _ := c.read(); id != 6 {
		t.Fatalf("room enter msgid = %d, want 6", id)
	}
	c.send(5, nil)
	if id, _ := c.read(); id != 8 {
		t.Fatalf("relaystart msgid = %d, want 8", id)
	}
}

func pinHighFiveClock(t *testing.T, now time.Time) {
	t.Helper()
	prev := economy.NowFunc
	economy.NowFunc = func() time.Time { return now }
	t.Cleanup(func() { economy.NowFunc = prev })
}

func TestSquareHighFiveJoinAndAchieve(t *testing.T) {
	pinHighFiveClock(t, time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC))
	testutil.InstallAccounts(t, withLoginAccounts(nil, 101, 202))

	room := &squareRoom{}
	p1 := newSqClient(t, room, 1)
	p2 := newSqClient(t, room, 2)

	// First player joins alone: no msgid 26 push.
	highFiveJoin(t, p1, 101, squareThemeID)
	p1.none()

	// Second player joins: p2 gets [101], p1 gets adduser (2) then [202].
	highFiveJoin(t, p2, 202, squareThemeID)
	if id, body := p2.read(); id != squareRcHighFiveTargetList || !slices.Equal(decodeHighFiveTargets(t, body), []uint64{101}) {
		t.Fatalf("p2 push = msgid %d targets %v, want [101]", id, decodeHighFiveTargets(t, body))
	}
	p2.none()

	if id, _ := p1.read(); id != 2 {
		t.Fatalf("p1 adduser msgid = %d, want 2", id)
	}
	if id, body := p1.read(); id != squareRcHighFiveTargetList || !slices.Equal(decodeHighFiveTargets(t, body), []uint64{202}) {
		t.Fatalf("p1 push = msgid %d targets %v, want [202]", id, decodeHighFiveTargets(t, body))
	}
	p1.none()

	// p1 -> p2 succeeds: both receive msgid 27 with result=1, my_aid=101, target_aid=202, count=1, no field 5.
	p1.send(squareCrHighFiveAchieve, pbVar(nil, 1, 202))
	for _, tc := range []struct {
		name string
		c    *sqClient
	}{{"p1", p1}, {"p2", p2}} {
		id, body := tc.c.read()
		if id != squareRcHighFiveAchieve {
			t.Fatalf("%s achieve msgid = %d, want %d", tc.name, id, squareRcHighFiveAchieve)
		}
		got := decodeHighFiveAchieve(t, body)
		if !got.hasResult || got.result != 1 || got.myAid != 101 || got.targetAid != 202 || got.count != 1 || got.hasSerial {
			t.Fatalf("%s achieve = %+v, want result=1 my=101 target=202 count=1 serial=false", tc.name, got)
		}
	}

	// Repeat p1 -> p2 on the same game day fails: p1 gets result=0 count=1, p2 gets nothing.
	p1.send(squareCrHighFiveAchieve, pbVar(nil, 1, 202))
	if id, body := p1.read(); id != squareRcHighFiveAchieve {
		t.Fatalf("repeat p1 msgid = %d, want %d", id, squareRcHighFiveAchieve)
	} else {
		got := decodeHighFiveAchieve(t, body)
		if !got.hasResult || got.result != 0 || got.myAid != 101 || got.targetAid != 202 || got.count != 1 || got.hasSerial {
			t.Fatalf("repeat p1 achieve = %+v, want result=0 my=101 target=202 count=1 serial=false", got)
		}
	}
	p2.none()

	// Symmetric dedup: p2 -> p1 on the same game day ALSO fails (p2 count=0), p1 gets nothing.
	p2.send(squareCrHighFiveAchieve, pbVar(nil, 1, 101))
	if id, body := p2.read(); id != squareRcHighFiveAchieve {
		t.Fatalf("reverse p2 msgid = %d, want %d", id, squareRcHighFiveAchieve)
	} else {
		got := decodeHighFiveAchieve(t, body)
		if !got.hasResult || got.result != 0 || got.myAid != 202 || got.targetAid != 101 || got.count != 0 || got.hasSerial {
			t.Fatalf("reverse p2 achieve = %+v, want result=0 my=202 target=101 count=0 serial=false", got)
		}
	}
	p1.none()

	// Self-target and absent target fail with result=0 and do not change count.
	for _, bad := range []uint64{0, 101, 999} {
		p1.send(squareCrHighFiveAchieve, pbVar(nil, 1, bad))
		id, body := p1.read()
		if id != squareRcHighFiveAchieve {
			t.Fatalf("bad target %d msgid = %d, want %d", bad, id, squareRcHighFiveAchieve)
		}
		got := decodeHighFiveAchieve(t, body)
		if !got.hasResult || got.result != 0 || got.myAid != 101 || got.targetAid != bad || got.count != 1 || got.hasSerial {
			t.Fatalf("bad target %d achieve = %+v, want result=0 my=101 target=%d count=1 serial=false", bad, got, bad)
		}
		p2.none()
	}

	// Rejoin by p1 after high-fiving p2: p2 is NOT pushed in msgid 26 to p1 (and p2 gets nothing).
	p1.send(2, pbVar(nil, 1, squareThemeID))
	if id, _ := p1.read(); id != 6 {
		t.Fatalf("rejoin enter msgid = %d, want 6", id)
	}
	p1.send(5, nil)
	if id, _ := p1.read(); id != 8 {
		t.Fatalf("rejoin relaystart msgid = %d, want 8", id)
	}
	p1.none()
	p2.none()

	// Rejoin by p2 after p1 -> p2: p1 is NOT pushed in msgid 26 to p2 either (symmetric).
	p2.send(2, pbVar(nil, 1, squareThemeID))
	if id, _ := p2.read(); id != 6 {
		t.Fatalf("p2 rejoin enter msgid = %d, want 6", id)
	}
	p2.send(5, nil)
	if id, _ := p2.read(); id != 8 {
		t.Fatalf("p2 rejoin relaystart msgid = %d, want 8", id)
	}
	p2.none()
	p1.none()
}

func TestSquareHighFiveRejoinFiltersDoneOnlyAndResetsOnNewDay(t *testing.T) {
	day1 := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)
	prev := economy.NowFunc
	now := day1
	economy.NowFunc = func() time.Time { return now }
	t.Cleanup(func() { economy.NowFunc = prev })

	testutil.InstallAccounts(t, withLoginAccounts(nil, 101, 202, 303))
	room := &squareRoom{}
	p1 := newSqClient(t, room, 1)
	p2 := newSqClient(t, room, 2)
	p3 := newSqClient(t, room, 3)

	highFiveJoin(t, p1, 101, squareThemeID)
	highFiveJoin(t, p2, 202, squareThemeID)
	p2.read() // 26 [101]
	p1.read() // 2 adduser
	p1.read() // 26 [202]

	// p1 high-fives p2.
	p1.send(squareCrHighFiveAchieve, pbVar(nil, 1, 202))
	p1.read()
	p2.read()

	// p3 joins: p3 gets [101, 202], p1 and p2 each get [303].
	highFiveJoin(t, p3, 303, squareThemeID)
	if id, body := p3.read(); id != squareRcHighFiveTargetList || !slices.Equal(decodeHighFiveTargets(t, body), []uint64{101, 202}) {
		t.Fatalf("p3 push = msgid %d targets %v, want [101 202]", id, decodeHighFiveTargets(t, body))
	}
	p1.read() // 2 adduser
	if id, body := p1.read(); id != squareRcHighFiveTargetList || !slices.Equal(decodeHighFiveTargets(t, body), []uint64{303}) {
		t.Fatalf("p1 push = msgid %d targets %v, want [303]", id, decodeHighFiveTargets(t, body))
	}
	p2.read() // 2 adduser
	if id, body := p2.read(); id != squareRcHighFiveTargetList || !slices.Equal(decodeHighFiveTargets(t, body), []uint64{303}) {
		t.Fatalf("p2 push = msgid %d targets %v, want [303]", id, decodeHighFiveTargets(t, body))
	}

	// p1 rejoins: p1 gets only [303] (p2 filtered out); p2 and p3 get nothing.
	p1.send(2, pbVar(nil, 1, squareThemeID))
	p1.read() // 6
	p1.send(5, nil)
	p1.read() // 8
	if id, body := p1.read(); id != squareRcHighFiveTargetList || !slices.Equal(decodeHighFiveTargets(t, body), []uint64{303}) {
		t.Fatalf("p1 rejoin push = msgid %d targets %v, want [303]", id, decodeHighFiveTargets(t, body))
	}
	p1.none()
	p2.none()
	p3.none()

	// Next game day resets daily state so p1 -> p2 succeeds again with count=1.
	now = day1.Add(24 * time.Hour)
	p1.send(squareCrHighFiveAchieve, pbVar(nil, 1, 202))
	if id, body := p1.read(); id != squareRcHighFiveAchieve {
		t.Fatalf("next day p1 msgid = %d, want %d", id, squareRcHighFiveAchieve)
	} else {
		got := decodeHighFiveAchieve(t, body)
		if !got.hasResult || got.result != 1 || got.count != 1 || got.hasSerial {
			t.Fatalf("next day p1 achieve = %+v, want result=1 count=1 serial=false", got)
		}
	}
	p2.read()
}

func TestSquareHighFiveGoalStopsRc26Push(t *testing.T) {
	now := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)
	pinHighFiveClock(t, now)

	aids := []uint64{101, 202, 303}
	for i := 1; i <= economy.HighFiveDailyGoal; i++ {
		aids = append(aids, uint64(500+i))
	}
	testutil.InstallAccounts(t, withLoginAccounts(nil, aids...))

	// Pre-fill p1 (101) to 20 high-fives today via RecordHighFive.
	for i := 1; i <= economy.HighFiveDailyGoal; i++ {
		if _, _, ok := economy.RecordHighFive("101", strconv.Itoa(500+i)); !ok {
			t.Fatalf("prefill highfive %d failed", i)
		}
	}

	room := &squareRoom{}
	p1 := newSqClient(t, room, 1)
	p2 := newSqClient(t, room, 2)
	p3 := newSqClient(t, room, 3)

	// p2 joins first, then p1 (already at 20/20):
	// p1 should get NO rc26 (at goal), while p2 gets adduser + rc26 [101] (p2 is at 0/20).
	highFiveJoin(t, p2, 202, squareThemeID)
	p2.none()

	highFiveJoin(t, p1, 101, squareThemeID)
	p1.none()
	if id, _ := p2.read(); id != 2 {
		t.Fatalf("p2 adduser msgid = %d, want 2", id)
	}
	if id, body := p2.read(); id != squareRcHighFiveTargetList || !slices.Equal(decodeHighFiveTargets(t, body), []uint64{101}) {
		t.Fatalf("p2 push = msgid %d targets %v, want [101]", id, decodeHighFiveTargets(t, body))
	}
	p2.none()

	// p3 joins: p3 gets [202, 101], p2 gets adduser + [303], p1 gets ONLY adduser (NO rc26!).
	highFiveJoin(t, p3, 303, squareThemeID)
	if id, body := p3.read(); id != squareRcHighFiveTargetList {
		t.Fatalf("p3 push msgid = %d, want %d", id, squareRcHighFiveTargetList)
	} else if got := decodeHighFiveTargets(t, body); len(got) != 2 || !slices.Contains(got, 101) || !slices.Contains(got, 202) {
		t.Fatalf("p3 push targets = %v, want [101 202]", got)
	}
	p2.read() // adduser
	if id, body := p2.read(); id != squareRcHighFiveTargetList || !slices.Equal(decodeHighFiveTargets(t, body), []uint64{303}) {
		t.Fatalf("p2 push = msgid %d targets %v, want [303]", id, decodeHighFiveTargets(t, body))
	}
	if id, _ := p1.read(); id != 2 {
		t.Fatalf("p1 adduser msgid = %d, want 2", id)
	}
	p1.none()
}

func TestSquareHighFiveSkippedInBossAndRacing(t *testing.T) {
	testutil.InstallAccounts(t, withLoginAccounts(nil, 101, 202))

	for _, tc := range []struct {
		name  string
		setup func(s *squareSession)
	}{
		{name: "boss_session", setup: func(s *squareSession) { s.boss = &squareBossSeat{} }},
		{name: "racing_session", setup: func(s *squareSession) { s.racing = &squareRace{} }},
		{name: "boss_theme", setup: func(s *squareSession) { s.room.theme = squareBossThemeID }},
		{name: "racing_theme", setup: func(s *squareSession) { s.room.theme = squareRacingTheme }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			room := &squareRoom{theme: squareThemeID, number: 1}
			rooms := &squareRooms{rooms: map[uint64][]*squareRoom{squareThemeID: {room}}}
			p1 := newSqClientIn(t, rooms, 1)
			p2 := newSqClientIn(t, rooms, 2)
			highFiveJoin(t, p1, 101, squareThemeID)
			p1.none()

			p2.send(0, loginBody(202))
			p2.expect(0, []byte{0x08, 0x81, 0x80, 0x08})
			p2.send(2, pbVar(nil, 1, squareThemeID))
			if id, _ := p2.read(); id != 6 {
				t.Fatalf("enter msgid = %d, want 6", id)
			}
			p2.sess.room = room
			tc.setup(p2.sess)
			p2.send(5, nil)
			if id, _ := p2.read(); id != 8 {
				t.Fatalf("relaystart msgid = %d, want 8", id)
			}
			p2.none()
			if id, _ := p1.read(); id != 2 {
				t.Fatalf("p1 adduser msgid = %d, want 2", id)
			}
			p1.none()
			p2.sess.boss = nil
			p2.sess.stopRacing()
		})
	}
}

func TestTLUPQuestListBodyAndGatewaySeed(t *testing.T) {
	now := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)
	pinHighFiveClock(t, now)

	day := economy.AttendDayKey(now)
	entries := withLoginAccounts(nil, 101, 202, 303)
	entries["lt-101"].HighFive = store.HighFiveDay{Day: day, Count: 5, Targets: []string{"1", "2", "3", "4", "5"}}
	entries["lt-202"].HighFive = store.HighFiveDay{Day: day, Count: 25, Targets: make([]string, 25)}
	entries["lt-303"].HighFive = store.HighFiveDay{Day: day - 1, Count: 12, Targets: make([]string, 12)}
	testutil.InstallAccounts(t, entries)

	for _, tc := range []struct {
		aid      uint64
		wantUser uint64
	}{
		{aid: 101, wantUser: 5},
		{aid: 202, wantUser: 20}, // clamped to BaseCount=20
		{aid: 303, wantUser: 0},  // previous game day -> 0
	} {
		raw := tlupQuestListBody(tc.aid)
		var ts uint64
		var questBytes []byte
		if !pbScan(raw, func(f, v uint64, d []byte) {
			if f == 1 {
				ts = v
			} else if f == 2 {
				questBytes = d
			}
		}) {
			t.Fatalf("aid %d: malformed tc_questlist_res", tc.aid)
		}
		if ts != uint64(now.Unix()) {
			t.Fatalf("aid %d: server_timestamp = %d, want %d", tc.aid, ts, now.Unix())
		}

		var qid uint64
		var masterBytes, userBytes []byte
		if !pbScan(questBytes, func(f, v uint64, d []byte) {
			switch f {
			case 1:
				qid = v
			case 2:
				masterBytes = d
			case 3:
				userBytes = d
			}
		}) {
			t.Fatalf("aid %d: malformed QuestInfo", tc.aid)
		}
		if qid != 7 {
			t.Fatalf("aid %d: QuestInfo.questid = %d, want 7", tc.aid, qid)
		}

		var mQid, mTarget uint64
		var mParams string
		var hasGroupID bool
		if !pbScan(masterBytes, func(f, v uint64, d []byte) {
			switch f {
			case 1:
				mQid = v
			case 13:
				mTarget = v
			case 21:
				mParams = string(d)
			case 23:
				hasGroupID = true
			}
		}) {
			t.Fatalf("aid %d: malformed MasterQuestInfo", tc.aid)
		}
		if mQid != 7 || mTarget != 20 || hasGroupID {
			t.Fatalf("aid %d: master qid=%d target=%d hasGroupID=%v, want 7, 20, false", tc.aid, mQid, mTarget, hasGroupID)
		}
		var parsed struct {
			DisplayVer   int   `json:"display_ver"`
			CompletedGem int64 `json:"completed_gem"`
			ResetHour    int   `json:"reset_hour"`
		}
		if err := json.Unmarshal([]byte(mParams), &parsed); err != nil {
			t.Fatalf("aid %d: params JSON %q err=%v", tc.aid, mParams, err)
		}
		if parsed.DisplayVer != 0 || parsed.CompletedGem != 100 || parsed.ResetHour != 0 {
			t.Fatalf("aid %d: parsed params = %+v, want display_ver=0 completed_gem=100 reset_hour=0", tc.aid, parsed)
		}

		var uQid, uTarget uint64
		if !pbScan(userBytes, func(f, v uint64, _ []byte) {
			switch f {
			case 1:
				uQid = v
			case 8:
				uTarget = v
			}
		}) {
			t.Fatalf("aid %d: malformed UserQuestInfo", tc.aid)
		}
		if uQid != 7 || uTarget != tc.wantUser {
			t.Fatalf("aid %d: user qid=%d targetcount=%d, want 7, %d", tc.aid, uQid, uTarget, tc.wantUser)
		}
	}

	// End-to-end via observeConn: Square login binds viewer=101, then TLUP msgid 0 returns seeded tc_questlist_res.
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

	// Login on Square (agent 7 -> b1 = (7-1)<<3 | 0 = 0x30), msgid 0.
	sqPayload := loginBody(101)
	sqFrame := make([]byte, 12+len(sqPayload))
	sqFrame[0], sqFrame[1] = 'G', (squareAgent-1)<<3
	copy(sqFrame[2:10], []byte{1, 2, 3, 4, 5, 6, 7, 8})
	binary.BigEndian.PutUint16(sqFrame[10:12], 0)
	copy(sqFrame[12:], sqPayload)
	sqWire := make([]byte, 4+len(sqFrame))
	binary.BigEndian.PutUint32(sqWire[:4], uint32(len(sqFrame)))
	copy(sqWire[4:], sqFrame)
	if _, err := client.Write(sqWire); err != nil {
		t.Fatalf("square login write: %v", err)
	}
	var hdr [4]byte
	if _, err := client.Read(hdr[:]); err != nil {
		t.Fatalf("square login reply hdr: %v", err)
	}
	sqLen := binary.BigEndian.Uint32(hdr[:])
	_ = readFullDeadline(t, client, int(sqLen))

	// Send TLUP ct_questlist_req (agent 5, msgid 0).
	reqBody := []byte{0x10, 0x00, 0x18, 0x00}
	tlupFrame := []byte{'D', 0, 0, 1, 0, 1, 0, 0x6f, 0, 0, 5, 0, 0, 0, 0x42, 0, 0, 0}
	tlupFrame = append(tlupFrame, reqBody...)
	tlupWire := append([]byte{0, 0, 0, byte(len(tlupFrame))}, tlupFrame...)
	if _, err := client.Write(tlupWire); err != nil {
		t.Fatalf("questlist write: %v", err)
	}
	if _, err := client.Read(hdr[:]); err != nil {
		t.Fatalf("questlist reply hdr: %v", err)
	}
	replyLen := binary.BigEndian.Uint32(hdr[:])
	reply := readFullDeadline(t, client, int(replyLen))
	if reply[0] != 'D' || reply[10] != 5 || binary.BigEndian.Uint16(reply[16:18]) != 0 {
		t.Fatalf("unexpected TLUP header: %x", reply[:18])
	}
	if got, want := reply[18:], tlupQuestListBody(101); !bytes.Equal(got, want) {
		t.Fatalf("gateway tc_questlist_res body = %x, want %x", got, want)
	}
}

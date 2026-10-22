package lpn

import (
	"bytes"
	"testing"
	"time"

	"cherry/internal/economy"
	"cherry/internal/store"
)

// bowRoom puts host (aid 11) and guests into one room on floor 1 and drains the enter and spawn
// frames, so the tests start from a quiet room.
func bowRoom(host *pc, guests ...*pc) {
	host.enterHost(11)
	host.start(nil)
	host.read() // idx29 self
	for _, g := range guests {
		g.enter(11)
		g.read()
		g.start(nil)
	}
	for _, p := range append([]*pc{host}, guests...) {
		for p.drain() {
		}
	}
}

// TestBowBody pins the idx21 body for a bow from aid 12 to aid 11 with count 3: Success 1, both
// aids, the count, and RewardCoin and CompleteQuest sent as zero.
func TestBowBody(t *testing.T) {
	want := mustHex(t, `08 01 10 0c 18 0b 20 03 28 00 30 00`)
	if got := bowBody(12, 11, 3); !bytes.Equal(got, want) {
		t.Fatalf("idx21 body = %x, want %x", got, want)
	}
}

// TestBowIgnored: a sender outside any room, a target outside the sender's room and a self-bow get
// no reply and count nothing.
func TestBowIgnored(t *testing.T) {
	h := partySetup(t)
	a := partyLogin(t, h, 11, 1)
	b := partyLogin(t, h, 12, 2)
	bowRoom(a, b)
	out := partyLogin(t, h, 13, 3)
	out.send(17, pbVar(nil, 1, 11)) // not in a room
	out.none()
	b.send(17, pbVar(nil, 1, 13)) // 13 is not in this room
	b.none()
	b.send(17, pbVar(nil, 1, 12)) // self
	b.none()
	a.none()
	if len(h.bows) != 0 {
		t.Fatalf("ignored bows were counted: %v", h.bows)
	}
}

// TestBowReplies: a guest's bow to the host gets idx21 on both sides, then the heart show again on the
// guest's connection. Counts run per source per game day. A bow to another guest pushes no show, and
// a bow with no TLUP session seen on the connection gets only the idx21 pair.
func TestBowReplies(t *testing.T) {
	h := partySetup(t)
	store.SocialMu.Lock()
	store.Friendships = []store.Friendship{{A: "11", B: "12", State: "accepted"}, {A: "11", B: "13", State: "accepted"}}
	store.SocialMu.Unlock()
	a := partyLogin(t, h, 11, 1)
	b := partyLogin(t, h, 12, 2)
	c := partyLogin(t, h, 13, 3)
	now := time.Date(2026, time.October, 22, 12, 0, 0, 0, time.UTC)
	old := economy.NowFunc
	economy.NowFunc = func() time.Time { return now }
	t.Cleanup(func() { economy.NowFunc = old })
	tlup := heartTestSession
	b.s.tlup = &tlup
	bowRoom(a, b, c)
	show := tlupDFrame(heartTestSession, tlupQuestEventMsg, tlupQuestActionBody(heartButtonShowID))

	// guest b bows the host: idx21 to both, then the heart show on b's connection after its reply
	b.send(17, pbVar(nil, 1, 11))
	b.expect(21, bowBody(12, 11, 1))
	a.expect(21, bowBody(12, 11, 1))
	if got := readFullDeadline(t, b.peer, len(show)); !bytes.Equal(got, show) {
		t.Fatalf("heart show = %x, want %x", got, show)
	}
	b.send(17, pbVar(nil, 1, 11)) // second bow of the day
	b.expect(21, bowBody(12, 11, 2))
	a.expect(21, bowBody(12, 11, 2))
	readFullDeadline(t, b.peer, len(show))
	now = now.Add(24 * time.Hour) // next game day starts the count again
	b.send(17, pbVar(nil, 1, 11))
	b.expect(21, bowBody(12, 11, 1))
	a.expect(21, bowBody(12, 11, 1))
	readFullDeadline(t, b.peer, len(show))

	// a bow to another guest: idx21 pair only, no show
	c.send(17, pbVar(nil, 1, 12))
	c.expect(21, bowBody(13, 12, 1))
	b.expect(21, bowBody(13, 12, 1))
	c.none()
	b.none()
	a.none()

	// no TLUP session seen on b's connection: idx21 only
	b.s.tlup = nil
	b.send(17, pbVar(nil, 1, 11))
	b.expect(21, bowBody(12, 11, 2))
	a.expect(21, bowBody(12, 11, 2))
	b.none()
}

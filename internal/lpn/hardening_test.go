package lpn

import (
	"testing"

	"cherry/internal/store"
	"cherry/internal/testutil"
)

// PLAN 8.6b part 4, target checks: a Garden visit counts only for an owner with an avatar, and a Room Party invitation
// cancel changes only the host's own room.

func TestCreditGardenVisitNeedsAnAvatar(t *testing.T) {
	testutil.InstallAccounts(t, map[string]*store.Account{
		"viewer": {AccessToken: "viewer", Aid: "21", Name: "Viv"},
		"owner":  {AccessToken: "owner", Aid: "22", Name: "Owe"},
	})
	testutil.UseDatabase(t)
	if creditGardenVisit(21, 999) {
		t.Fatal("a visit to an aid with no account counted")
	}
	if !creditGardenVisit(21, 22) {
		t.Fatal("a visit to an existing avatar did not count")
	}
	// Repeat visit on same game day is deduplicated; self-visit does not add footprints.
	if !creditGardenVisit(21, 22) {
		t.Fatal("repeat visit returned false")
	}
	if !creditGardenVisit(22, 22) {
		t.Fatal("self visit returned false")
	}
	snap, ok := store.AccountByAvatarID("22")
	if !ok || snap.GardenStats.Footprints != 1 || snap.GardenStats.TodayVisits != 1 {
		t.Fatalf("owner stats after creditGardenVisit = %+v (ok=%v), want footprints=1 todayVisits=1", snap.GardenStats, ok)
	}
}

// TestPartyInviteCancelNeedsHost: a member who joined another room cannot drop the host's invitations; the host can.
func TestPartyInviteCancelNeedsHost(t *testing.T) {
	h := partySetup(t)
	host := partyLogin(t, h, 11, 1)
	member := partyLogin(t, h, 14, 2)
	h.mu.Lock()
	h.rooms = map[uint64]*partyRoom{11: {host: 11, invited: map[uint64]bool{12: true}}}
	h.mu.Unlock()
	cancel := pbLen(nil, 1, []byte{12})

	// The member's session is bound to the host's room (s.host = 11): its cancel is answered but changes nothing.
	member.s.host = 11
	frame := member.req(18, cancel)
	done := make(chan error, 1)
	go func() { done <- member.s.inviteCancel(frame, cancel) }()
	member.read()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	h.mu.Lock()
	stillInvited := h.rooms[11].invited[12]
	h.mu.Unlock()
	if !stillInvited {
		t.Fatal("a member of another room cancelled the host's invitation")
	}

	// The host's own cancel (s.host 0: its own room) drops the invitation.
	host.s.host = 0
	frame = host.req(18, cancel)
	go func() { done <- host.s.inviteCancel(frame, cancel) }()
	host.read()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	h.mu.Lock()
	gone := !h.rooms[11].invited[12]
	h.mu.Unlock()
	if !gone {
		t.Fatal("the host's cancel did not drop the invitation")
	}
}

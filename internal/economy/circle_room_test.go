package economy

import "testing"

// circRoleCircle makes a circle led by 1101 with 1102, 1103 and 1104 as plain members (open join), and returns its id.
func circRoleCircle(t *testing.T) int64 {
	t.Helper()
	circFixture(t, map[string]int64{"1101": 6000, "1102": 0, "1103": 0, "1104": 0, "1105": 0})
	c, err := CreateCircle("ctok-1101", circValidCreate("Roles"), circNow)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	for _, aid := range []string{"1102", "1103", "1104"} {
		if err := circleJoin("ctok-"+aid, c.ID, circNow); err != nil {
			t.Fatalf("join %s: %v", aid, err)
		}
	}
	return c.ID
}

func TestCirclePlayerTypeCodes(t *testing.T) {
	if CirclePlayerType("leader") != 2 || CirclePlayerType("vice") != 1 || CirclePlayerType("member") != 0 {
		t.Fatalf("PlayerType = %d/%d/%d, want 2/1/0", CirclePlayerType("leader"), CirclePlayerType("vice"), CirclePlayerType("member"))
	}
}

// TestCircleChangeAuthLeaderOnly: only the leader makes vice-leaders or demotes them; the leader is never changed; a
// target outside the circle is not found; repeated targets change once.
func TestCircleChangeAuthLeaderOnly(t *testing.T) {
	id := circRoleCircle(t)
	if _, err := CircleChangeAuth("1102", id, []string{"1103"}, true); !isRefusal(err, circleErrRights) {
		t.Fatalf("member's vice promotion: err = %v", err)
	}
	changed, err := CircleChangeAuth("1101", id, []string{"1102", "1102"}, true)
	if err != nil || len(changed) != 1 || changed[0].Aid != "1102" || changed[0].Role != roleVice {
		t.Fatalf("promote 1102: changed %+v err %v", changed, err)
	}
	if _, err := CircleChangeAuth("1102", id, []string{"1103"}, true); !isRefusal(err, circleErrRights) {
		t.Fatalf("vice's promotion: err = %v", err)
	}
	if _, err := CircleChangeAuth("1101", id, []string{"1101"}, false); !isRefusal(err, circleErrRights) {
		t.Fatalf("leader demoted itself: err = %v", err)
	}
	if _, err := CircleChangeAuth("1101", id, []string{"1105"}, false); !isRefusal(err, circleErrNotFound) {
		t.Fatalf("stranger: err = %v", err)
	}
	if _, err := CircleChangeAuth("1101", id, []string{"1102"}, false); err != nil {
		t.Fatalf("demote 1102: %v", err)
	}
	members, master, ok := CircleMembersOf(id)
	role1102 := ""
	for _, m := range members {
		if m.Aid == "1102" {
			role1102 = m.Role
		}
	}
	if !ok || master != "1101" || len(members) != 4 || role1102 != roleMember {
		t.Fatalf("after demotion: master %s members %+v ok %v", master, members, ok)
	}
}

// TestCircleFireRules: the leader removes members and vice-leaders, a vice-leader removes plain members only, nobody
// removes the leader, a plain member removes nobody, and a batch with one bad target changes nothing.
func TestCircleFireRules(t *testing.T) {
	id := circRoleCircle(t)
	if _, err := CircleChangeAuth("1101", id, []string{"1102", "1104"}, true); err != nil {
		t.Fatalf("promote: %v", err)
	}
	if _, err := CircleFire("1103", id, []string{"1104"}); !isRefusal(err, circleErrRights) {
		t.Fatalf("plain member fired: err = %v", err)
	}
	for _, target := range []string{"1101", "1102", "1104"} {
		if _, err := CircleFire("1102", id, []string{target}); !isRefusal(err, circleErrRights) {
			t.Fatalf("vice fired %s: err = %v", target, err)
		}
	}
	if _, err := CircleFire("1101", id, []string{"1102", "1105"}); !isRefusal(err, circleErrNotFound) {
		t.Fatalf("batch with a stranger: err = %v", err)
	}
	if _, _, ok := CircleOf("1102"); !ok {
		t.Fatal("a refused batch removed a member")
	}
	removed, err := CircleFire("1102", id, []string{"1103", "1103"})
	if err != nil || len(removed) != 1 || removed[0] != "1103" {
		t.Fatalf("vice fired a plain member: removed %v err %v", removed, err)
	}
	if _, _, ok := CircleOf("1103"); ok {
		t.Fatal("removed member still in the circle")
	}
	if removed, err := CircleFire("1101", id, []string{"1104"}); err != nil || len(removed) != 1 {
		t.Fatalf("leader fired a vice-leader: removed %v err %v", removed, err)
	}
	if _, err := CircleFire("1101", id, []string{"1101"}); !isRefusal(err, circleErrRights) {
		t.Fatalf("leader fired itself: err = %v", err)
	}
}

// TestCircleTransferMasterHandsOver: the leader hands over to a member and becomes a plain member, which may leave;
// the new leader may not leave; the old leader has no rights any more.
func TestCircleTransferMasterHandsOver(t *testing.T) {
	id := circRoleCircle(t)
	changed, err := CircleTransferMaster("1101", id, "1103")
	if err != nil || len(changed) != 2 || changed[0].Aid != "1103" || changed[0].Role != roleLeader || changed[1].Aid != "1101" || changed[1].Role != roleMember {
		t.Fatalf("transfer: changed %+v err %v", changed, err)
	}
	if _, master, _ := CircleMembersOf(id); master != "1103" {
		t.Fatalf("master = %s, want 1103", master)
	}
	if err := LeaveCircle("ctok-1101", id, circNow); err != nil {
		t.Fatalf("old leader (now a member) could not leave: %v", err)
	}
	if err := LeaveCircle("ctok-1103", id, circNow); err == nil {
		t.Fatal("new leader left the circle")
	}
	if _, err := CircleTransferMaster("1101", id, "1102"); !isRefusal(err, circleErrRights) {
		t.Fatalf("old leader transferred again: err = %v", err)
	}
	if _, err := CircleTransferMaster("1103", id, "1103"); !isRefusal(err, circleErrBadRequest) {
		t.Fatalf("transfer to self: err = %v", err)
	}
}

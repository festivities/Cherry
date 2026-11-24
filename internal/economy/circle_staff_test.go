package economy

import (
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"cherry/internal/store"
)

func TestStaffEditCircle(t *testing.T) {
	circFixture(t, map[string]int64{"1101": 6000, "1102": 6000})
	c1, err := CreateCircle("ctok-1101", circValidCreate("Alpha"), circNow)
	if err != nil {
		t.Fatalf("create c1: %v", err)
	}
	if _, err := CreateCircle("ctok-1102", circValidCreate("Beta"), circNow); err != nil {
		t.Fatalf("create c2: %v", err)
	}

	later := circNow.Add(time.Hour)
	if err := StaffEditCircle(c1.ID, "  Gamma  ", "  new bio  ", "fix name", "discord:1", later); err != nil {
		t.Fatalf("edit: %v", err)
	}
	store.CirclesMu.Lock()
	got := store.CircleState.Circles[0]
	store.CirclesMu.Unlock()
	if got.Name != "Gamma" || got.Intro != "new bio" || got.Modified != later.UnixMilli() {
		t.Fatalf("got circle = %+v", got)
	}

	// Clearing intro and keeping the same name (case-insensitively) succeeds.
	later2 := later.Add(time.Minute)
	if err := StaffEditCircle(c1.ID, "gamma", "", "clear abusive bio", "discord:1", later2); err != nil {
		t.Fatalf("clear intro: %v", err)
	}
	store.CirclesMu.Lock()
	got = store.CircleState.Circles[0]
	store.CirclesMu.Unlock()
	if got.Name != "gamma" || got.Intro != "" || got.Modified != later2.UnixMilli() {
		t.Fatalf("after clear = %+v", got)
	}

	// Validation errors and missing circle.
	cases := []struct {
		name   string
		id     int64
		cName  string
		intro  string
		reason string
		want   error
	}{
		{"missing circle", 1940000999, "Delta", "ok", "reason", store.ErrNoItem},
		{"empty name", c1.ID, "   ", "ok", "reason", store.ErrBadInput},
		{"name too long", c1.ID, strings.Repeat("a", circleNameMax+1), "ok", "reason", store.ErrBadInput},
		{"intro too long", c1.ID, "Gamma", strings.Repeat("b", circleIntroMax+1), "reason", store.ErrBadInput},
		{"control char in name", c1.ID, "Bad\nName", "ok", "reason", store.ErrBadInput},
		{"control char in intro", c1.ID, "Gamma", "bad\tintro", "reason", store.ErrBadInput},
		{"del char in intro", c1.ID, "Gamma", "bad\x7fintro", "reason", store.ErrBadInput},
		{"empty reason", c1.ID, "Gamma", "ok", " ", store.ErrBadInput},
		{"duplicate name", c1.ID, "beta", "ok", "reason", store.ErrBadInput},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := StaffEditCircle(tc.id, tc.cName, tc.intro, tc.reason, "discord:1", later2); !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
		})
	}

	// Audit row was written.
	rows, err := store.AuditRows(0, 20)
	if err != nil {
		t.Fatal(err)
	}
	target := fmt.Sprintf("circle:%d", c1.ID)
	var found bool
	for _, r := range rows {
		if r.Actor == "discord:1" && r.Action == "circle-edit" && r.Target == target &&
			strings.Contains(r.Detail, `"prevName":"Alpha"`) && strings.Contains(r.Detail, `"reason":"fix name"`) {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("missing circle-edit audit row in %+v", rows)
	}
}

func TestStaffEditCirclePersistsAcrossDB(t *testing.T) {
	circFixture(t, map[string]int64{"1101": 6000})
	c, err := CreateCircle("ctok-1101", circValidCreate("Before"), circNow)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	later := circNow.Add(2 * time.Hour)
	if err := StaffEditCircle(c.ID, "After", "clean intro", "moderation", "discord:2", later); err != nil {
		t.Fatalf("staff edit: %v", err)
	}
	store.ResetCircles()
	if err := store.ReloadStores(); err != nil {
		t.Fatalf("reload: %v", err)
	}
	store.CirclesMu.Lock()
	defer store.CirclesMu.Unlock()
	if len(store.CircleState.Circles) != 1 || store.CircleState.Circles[0].Name != "After" || store.CircleState.Circles[0].Intro != "clean intro" {
		t.Fatalf("reloaded circles = %+v", store.CircleState.Circles)
	}
}

func TestStaffRemoveCircleMember(t *testing.T) {
	id := circRoleCircle(t)
	var events []CircleRoomEvent
	oldHook := CircleRoomHook
	CircleRoomHook = func(ev CircleRoomEvent) {
		// Verify CirclesMu is released before the hook fires.
		_, _, _ = CircleMembersOf(ev.Circle)
		events = append(events, ev)
	}
	t.Cleanup(func() { CircleRoomHook = oldHook })

	if err := StaffRemoveCircleMember(id, "1101", "cannot kick leader", "discord:1", circNow); !errors.Is(err, store.ErrBadInput) {
		t.Fatalf("kick leader err = %v, want ErrBadInput", err)
	}
	if err := StaffRemoveCircleMember(id, "1199", "not a member", "discord:1", circNow); !errors.Is(err, store.ErrNoItem) {
		t.Fatalf("kick non-member err = %v, want ErrNoItem", err)
	}
	if err := StaffRemoveCircleMember(1940000999, "1102", "missing circle", "discord:1", circNow); !errors.Is(err, store.ErrNoItem) {
		t.Fatalf("kick missing circle err = %v, want ErrNoItem", err)
	}
	if err := StaffRemoveCircleMember(id, "1102", " ", "discord:1", circNow); !errors.Is(err, store.ErrBadInput) {
		t.Fatalf("blank reason err = %v, want ErrBadInput", err)
	}

	kickAt := circNow.Add(30 * time.Minute)
	if err := StaffRemoveCircleMember(id, "1102", "harassment", "discord:1", kickAt); err != nil {
		t.Fatalf("kick member: %v", err)
	}
	members, _, ok := CircleMembersOf(id)
	if !ok || len(members) != 3 {
		t.Fatalf("after kick: ok=%t members=%+v", ok, members)
	}
	for _, m := range members {
		if m.Aid == "1102" {
			t.Fatal("1102 is still a member")
		}
	}
	if len(events) != 1 || !reflect.DeepEqual(events[0], CircleRoomEvent{Circle: id, Left: []string{"1102"}}) {
		t.Fatalf("hook events = %+v", events)
	}

	rows, err := store.AuditRows(0, 20)
	if err != nil {
		t.Fatal(err)
	}
	target := fmt.Sprintf("circle:%d", id)
	var audited bool
	for _, r := range rows {
		if r.Actor == "discord:1" && r.Action == "circle-kick" && r.Target == target &&
			strings.Contains(r.Detail, `"aid":"1102"`) && strings.Contains(r.Detail, `"reason":"harassment"`) {
			audited = true
			break
		}
	}
	if !audited {
		t.Fatalf("missing circle-kick audit row in %+v", rows)
	}
}

func TestStaffDisbandCircle(t *testing.T) {
	id := circRoleCircle(t)
	var events []CircleRoomEvent
	oldHook := CircleRoomHook
	CircleRoomHook = func(ev CircleRoomEvent) {
		// Verify CirclesMu is released before the hook fires.
		_, _, _ = CircleMembersOf(ev.Circle)
		events = append(events, ev)
	}
	t.Cleanup(func() { CircleRoomHook = oldHook })

	if err := StaffDisbandCircle(1940000999, "missing", "discord:1"); !errors.Is(err, store.ErrNoItem) {
		t.Fatalf("missing circle err = %v, want ErrNoItem", err)
	}
	if err := StaffDisbandCircle(id, " ", "discord:1"); !errors.Is(err, store.ErrBadInput) {
		t.Fatalf("blank reason err = %v, want ErrBadInput", err)
	}

	if err := StaffDisbandCircle(id, "policy violation", "discord:1"); err != nil {
		t.Fatalf("disband: %v", err)
	}
	if _, _, ok := CircleMembersOf(id); ok {
		t.Fatal("disbanded circle still exists")
	}
	if len(events) != 1 || events[0].Circle != id || !events[0].Closed || !slices.Equal(events[0].Left, []string{"1101", "1102", "1103", "1104"}) {
		t.Fatalf("hook events = %+v", events)
	}

	rows, err := store.AuditRows(0, 20)
	if err != nil {
		t.Fatal(err)
	}
	target := fmt.Sprintf("circle:%d", id)
	var audited bool
	for _, r := range rows {
		if r.Actor == "discord:1" && r.Action == "circle-disband" && r.Target == target &&
			strings.Contains(r.Detail, `"name":"Roles"`) && strings.Contains(r.Detail, `"members":4`) && strings.Contains(r.Detail, `"reason":"policy violation"`) {
			audited = true
			break
		}
	}
	if !audited {
		t.Fatalf("missing circle-disband audit row in %+v", rows)
	}
}

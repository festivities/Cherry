package store

import (
	"os"
	"path/filepath"
	"testing"
)

// TestCircleStoreBackCompatAndRoundTrip: a missing circles.json is an empty book; a saved book loads back unchanged; a
// file with another version is refused.
func TestCircleStoreBackCompatAndRoundTrip(t *testing.T) {
	t.Cleanup(ResetCircles)
	dir := t.TempDir()
	path := filepath.Join(dir, "circles.json")
	if err := LoadCirclesFrom(path); err != nil {
		t.Fatalf("absent file: %v", err)
	}
	if len(CircleState.Circles) != 0 || CirclesStorePath != path {
		t.Fatalf("absent file: state = %+v path = %q", CircleState, CirclesStorePath)
	}

	book := CircleBook{
		NextID: 1940000002,
		Circles: []Circle{{
			ID: 1940000002, Code: "000002", Name: "Sushi", Intro: "hi", JoinType: "OPEN", LogoID: 1940001001, LogoBgID: 1940001101,
			Interests: []int64{101}, Created: 1, Modified: 1,
			Members: []CircleMember{{Aid: "1101", Role: "leader", Joined: 1}},
		}},
		Leaves: map[string]int64{"1102": 5},
	}
	CirclesMu.Lock()
	if err := SaveCirclesLocked(book); err != nil {
		CirclesMu.Unlock()
		t.Fatalf("save: %v", err)
	}
	CirclesMu.Unlock()
	if err := LoadCirclesFrom(path); err != nil {
		t.Fatalf("reload: %v", err)
	}
	if CircleState.NextID != 1940000002 || len(CircleState.Circles) != 1 || CircleState.Circles[0].Members[0].Aid != "1101" || CircleState.Leaves["1102"] != 5 {
		t.Fatalf("reloaded = %+v", CircleState)
	}

	if err := os.WriteFile(path, []byte(`{"version":2,"nextId":0,"circles":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := LoadCirclesFrom(path); err == nil {
		t.Fatal("version 2 accepted")
	}
}

// TestCircleCloneIsDeep: a clone shares no slice or map with the book it came from.
func TestCircleCloneIsDeep(t *testing.T) {
	orig := CircleBook{NextID: 1, Circles: []Circle{{ID: 1, Members: []CircleMember{{Aid: "1101", Role: "leader"}}}}, Leaves: map[string]int64{"x": 1}}
	cp := orig.Clone()
	cp.Circles[0].Members[0].Role = "member"
	cp.Circles[0].Members = append(cp.Circles[0].Members, CircleMember{Aid: "1102"})
	cp.Leaves["y"] = 2
	if orig.Circles[0].Members[0].Role != "leader" || len(orig.Circles[0].Members) != 1 || len(orig.Leaves) != 1 {
		t.Fatalf("original changed: %+v", orig)
	}
}

// TestCircleCloneCopiesSeasons: the season records (member records, their maps and their steps) are copied too, so a
// write on a copy never reaches the book it was copied from.
func TestCircleCloneCopiesSeasons(t *testing.T) {
	ms := &CircleMemberSeason{Points: 5, DayKinds: map[string]int64{"chat": 1}, Progress: map[string]int64{"garden": 2}, Steps: []int64{1}}
	orig := CircleBook{NextID: 1, Circles: []Circle{{ID: 1, Seasons: []CircleSeason{{Season: 1, Total: 5, Members: map[string]*CircleMemberSeason{"1101": ms}}}}}}
	cp := orig.Clone()
	got := cp.Circles[0].Seasons[0].Members["1101"]
	got.Points, got.DayKinds["chat"], got.Progress["garden"], got.Steps[0] = 9, 9, 9, 9
	cp.Circles[0].Seasons[0].Total = 9
	cp.Circles[0].Seasons[0].Members["1102"] = &CircleMemberSeason{}
	if ms.Points != 5 || ms.DayKinds["chat"] != 1 || ms.Progress["garden"] != 2 || ms.Steps[0] != 1 || orig.Circles[0].Seasons[0].Total != 5 || len(orig.Circles[0].Seasons[0].Members) != 1 {
		t.Fatalf("original season changed: %+v", orig.Circles[0].Seasons[0])
	}
}

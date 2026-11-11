package store

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// TestCircleRowsRoundTrip: a circle created and a circle closed in two saves (with a leaf changed and one added) come back
// from the database after a restart as the book the saves installed.
func TestCircleRowsRoundTrip(t *testing.T) {
	path := swapStore(t)
	t.Cleanup(ResetCircles)
	first := CircleBook{
		NextID: 1940000002,
		Circles: []Circle{{
			ID: 1940000002, Code: "000002", Name: "Sushi", Intro: "hi", JoinType: "OPEN", LogoID: 1940001001, LogoBgID: 1940001101,
			Interests: []int64{101}, Created: 1, Modified: 1,
			Members: []CircleMember{{Aid: "1101", Role: "leader", Joined: 1}},
		}},
		Leaves: map[string]int64{"1102": 5},
	}
	second := CircleBook{
		NextID: 1940000003,
		Circles: []Circle{{
			ID: 1940000003, Code: "000003", Name: "Ramen", JoinType: "INVITE", Created: 2, Modified: 3,
			Members: []CircleMember{{Aid: "1103", Role: "leader", Joined: 2}},
		}},
		Leaves: map[string]int64{"1102": 6, "1104": 7},
	}
	CirclesMu.Lock()
	for _, next := range []CircleBook{first, second} {
		if err := SaveCirclesLocked(CircleState, next); err != nil {
			CirclesMu.Unlock()
			t.Fatalf("save: %v", err)
		}
		CircleState = next
	}
	CirclesMu.Unlock()

	if err := CloseStore(); err != nil {
		t.Fatal(err)
	}
	if err := OpenStore(path); err != nil {
		t.Fatalf("reopen: %v", err)
	}
	t.Cleanup(func() { CloseStore() })
	CirclesMu.Lock()
	defer CirclesMu.Unlock()
	if !reflect.DeepEqual(CircleState, second) {
		t.Fatalf("reloaded = %+v, want %+v", CircleState, second)
	}
}

// TestCircleFileParser: the importer's reader of circles.json: a missing file is an empty book, and a file with another
// version is refused.
func TestCircleFileParser(t *testing.T) {
	path := filepath.Join(t.TempDir(), "circles.json")
	book, err := parseCirclesFile(path)
	if err != nil || len(book.Circles) != 0 {
		t.Fatalf("absent file: book %+v err %v", book, err)
	}
	if err := os.WriteFile(path, []byte(`{"version":2,"nextId":0,"circles":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := parseCirclesFile(path); err == nil {
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

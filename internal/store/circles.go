package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"sync"

	"cherry/internal/config"
)

// Circles (player clubs, economy/circle.go): the circles, their members and their pending applications and invites,
// in %LOCALAPPDATA%\Cherry\circles.json beside accounts.json. Absence means no circles. The rules live in economy;
// this file only holds the book and saves it.

// CircleMember is one member of a circle. Role is "leader", "vice" or "member": memberLevel MASTER, MANAGER and
// MEMBER over HTTP, and PlayerType 2, 1 and 0 over LPN (economy.CirclePlayerType, decoded 2026-11-01).
type CircleMember struct {
	Aid    string `json:"aid"`
	Role   string `json:"role"`
	Joined int64  `json:"joined"` // ms
}

// CircleApply is a join application waiting for a leader's answer.
type CircleApply struct {
	Aid  string `json:"aid"`
	Date int64  `json:"date"` // ms
}

// CircleInvite is an invitation waiting for the invitee's answer; By is the inviting member.
type CircleInvite struct {
	Aid  string `json:"aid"`
	By   string `json:"by"`
	Date int64  `json:"date"` // ms
}

// CircleSeason is one circle's points in one season (economy/circle_points.go): the circle's total and each member's
// record. A season with no points is absent.
type CircleSeason struct {
	Season  int64                          `json:"season"`
	Total   int64                          `json:"total"`
	Members map[string]*CircleMemberSeason `json:"members,omitempty"`
}

// CircleMemberSeason is one member's season: the points earned, the game day and the per-kind credits that the daily
// caps read, the old mission progress per trigger kind (no longer written), the point steps claimed and the number of missions claimed.
type CircleMemberSeason struct {
	Points   int64            `json:"points"`
	Day      int64            `json:"day,omitempty"`       // AttendDayKey of the day the counters below belong to
	DayPts   int64            `json:"dayPoints,omitempty"` // points credited on Day
	DayKinds map[string]int64 `json:"dayKinds,omitempty"`  // credits per trigger kind on Day
	Progress map[string]int64 `json:"progress,omitempty"`  // credits per trigger kind this season (missions)
	Steps    []int64          `json:"steps,omitempty"`     // point steps claimed (1-based seqs)
	Missions int64            `json:"missions,omitempty"`  // missions claimed, in order
}

// Circle is one club. ID is in Cherry's circle block and is never reused. Name and Code are fixed at creation.
type Circle struct {
	ID         int64          `json:"id"`
	Code       string         `json:"code"`
	Name       string         `json:"name"`
	Intro      string         `json:"intro"`
	JoinType   string         `json:"joinType"` // "OPEN" or "INVITE"
	LogoID     int64          `json:"logoId"`
	LogoBgID   int64          `json:"logoBgId"`
	Interests  []int64        `json:"interests,omitempty"`
	Created    int64          `json:"created"`  // ms
	Modified   int64          `json:"modified"` // ms
	Members    []CircleMember `json:"members"`
	Applies    []CircleApply  `json:"applies,omitempty"`
	Invites    []CircleInvite `json:"invites,omitempty"`
	MaxMembers int            `json:"maxMembers,omitempty"` // member cap after upgrades (0: the data's base cap)
	Seasons    []CircleSeason `json:"seasons,omitempty"`
}

// CircleBook is the whole circle state. Leaves holds, per aid, when that avatar last left a circle (the 24 h
// cooldown before another join or creation).
type CircleBook struct {
	NextID  int64            `json:"nextId"`
	Circles []Circle         `json:"circles"`
	Leaves  map[string]int64 `json:"leaves,omitempty"`
}

type savedCircles struct {
	Version int `json:"version"`
	CircleBook
}

var (
	// CirclesMu guards CircleState. Lock order: AccountsMu before CirclesMu before SocialMu.
	CirclesMu sync.Mutex
	// CirclesStorePath is circles.json ("" in tests: nothing is written).
	CirclesStorePath string
	// CircleState is the book in use. Writers copy it (Clone), change the copy and swap it in after the save.
	CircleState CircleBook
)

// LoadCircles reads %LOCALAPPDATA%\Cherry\circles.json (none is not an error).
func LoadCircles() error {
	dir, err := config.DataDir()
	if err != nil {
		return err
	}
	return LoadCirclesFrom(filepath.Join(dir, "circles.json"))
}

// LoadCirclesFrom loads the book at path; a missing file is an empty book.
func LoadCirclesFrom(path string) error {
	data, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	book := CircleBook{NextID: 0, Circles: []Circle{}}
	if err == nil {
		var saved savedCircles
		if err := json.Unmarshal(data, &saved); err != nil {
			return fmt.Errorf("invalid circle store: %w", err)
		}
		if saved.Version != 1 {
			return errors.New("invalid circle store version")
		}
		book = saved.CircleBook
	}
	if book.Circles == nil {
		book.Circles = []Circle{}
	}
	seen := map[int64]bool{}
	members := map[string]bool{}
	for _, c := range book.Circles {
		if c.ID <= 0 || seen[c.ID] || c.ID > book.NextID || (c.JoinType != "OPEN" && c.JoinType != "INVITE") || len(c.Members) == 0 {
			return fmt.Errorf("invalid circle record %d", c.ID)
		}
		seen[c.ID] = true
		for _, m := range c.Members {
			if members[m.Aid] {
				return fmt.Errorf("circle %d: avatar %s is a member twice", c.ID, m.Aid)
			}
			members[m.Aid] = true
		}
	}
	CirclesMu.Lock()
	CirclesStorePath = path
	CircleState = book
	CirclesMu.Unlock()
	return nil
}

// ResetCircles empties the book and forgets its path (tests).
func ResetCircles() {
	CirclesMu.Lock()
	CirclesStorePath = ""
	CircleState = CircleBook{}
	CirclesMu.Unlock()
}

// Clone returns a deep copy of b: the slices and the maps are not shared with the original.
func (b CircleBook) Clone() CircleBook {
	out := CircleBook{NextID: b.NextID, Circles: make([]Circle, len(b.Circles))}
	for i, c := range b.Circles {
		c.Interests = slices.Clone(c.Interests)
		c.Members = slices.Clone(c.Members)
		c.Applies = slices.Clone(c.Applies)
		c.Invites = slices.Clone(c.Invites)
		c.Seasons = cloneCircleSeasons(c.Seasons)
		out.Circles[i] = c
	}
	if b.Leaves != nil {
		out.Leaves = maps.Clone(b.Leaves)
	}
	return out
}

// cloneCircleSeasons deep-copies the seasons: each member record is a pointer and each map is copied.
func cloneCircleSeasons(in []CircleSeason) []CircleSeason {
	if in == nil {
		return nil
	}
	out := make([]CircleSeason, len(in))
	for i, s := range in {
		if s.Members != nil {
			s.Members = make(map[string]*CircleMemberSeason, len(in[i].Members))
			for aid, ms := range in[i].Members {
				cp := *ms
				cp.DayKinds = maps.Clone(ms.DayKinds)
				cp.Progress = maps.Clone(ms.Progress)
				cp.Steps = slices.Clone(ms.Steps)
				s.Members[aid] = &cp
			}
		}
		out[i] = s
	}
	return out
}

// SaveCirclesLocked writes b to circles.json (temp file, then rename). Caller holds CirclesMu. Nothing is written when
// CirclesStorePath is "".
func SaveCirclesLocked(b CircleBook) error {
	if CirclesStorePath == "" {
		return nil
	}
	data, err := json.Marshal(savedCircles{Version: 1, CircleBook: b})
	if err != nil {
		return err
	}
	dir := filepath.Dir(CirclesStorePath)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".circles-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err = tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err = tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), CirclesStorePath)
}

// AcceptedFriends reports whether a and b are accepted friends. It takes SocialMu alone, so a caller may hold
// AccountsMu or CirclesMu around it.
func AcceptedFriends(a, b string) bool {
	SocialMu.Lock()
	defer SocialMu.Unlock()
	return FriendshipIndex(a, b, "accepted") >= 0
}

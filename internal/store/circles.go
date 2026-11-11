package store

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"slices"
	"strconv"
	"sync"
	"testing"
	"time"

	"cherry/internal/metrics"
)

// Circles (player clubs, economy/circle.go): the circles, their members and their pending applications and invites. The
// book lives in the cherry.db tables circles, circle_leaves and meta (circles.nextId); circles.json is read only by
// cmd/cherry-import. The rules live in economy; this file only holds the book and saves it.

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
	// CircleState is the book in use. Writers copy it (Clone), change the copy and swap it in after the save.
	CircleState CircleBook
)

// loadCirclesFromDB reads the circle book of d into memory (OpenStore, ReloadStores). A nil d empties the book.
func loadCirclesFromDB(d *DB) error {
	var b CircleBook
	if d != nil {
		if err := d.View(func(q Querier) error {
			var err error
			b, err = readCircles(q)
			return err
		}); err != nil {
			return err
		}
	}
	CirclesMu.Lock()
	defer CirclesMu.Unlock()
	CircleState = b
	return nil
}

// parseCirclesFile reads a circles.json the way the server used to load it, for cmd/cherry-import and the startup guard.
// A missing file is an empty book.
func parseCirclesFile(path string) (CircleBook, error) {
	data, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return CircleBook{}, err
	}
	book := CircleBook{NextID: 0, Circles: []Circle{}}
	if err == nil {
		var saved savedCircles
		if err := json.Unmarshal(data, &saved); err != nil {
			return book, fmt.Errorf("invalid circle store: %w", err)
		}
		if saved.Version != 1 {
			return book, errors.New("invalid circle store version")
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
			return book, fmt.Errorf("invalid circle record %d", c.ID)
		}
		seen[c.ID] = true
		for _, m := range c.Members {
			if members[m.Aid] {
				return book, fmt.Errorf("circle %d: avatar %s is a member twice", c.ID, m.Aid)
			}
			members[m.Aid] = true
		}
	}
	return book, nil
}

// ResetCircles empties the book in memory (tests). It does not touch an attached database.
func ResetCircles() {
	CirclesMu.Lock()
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

// SaveCirclesLocked writes the change from prev, the book the database holds, to next, in ONE transaction: the circle rows
// whose value differs, the circles that are gone, the leaves that changed and the next id. The rows are found by comparing
// the two books, not named by each operation: a circle operation changes the copy it is given (economy/circle.go), so the
// comparison is what names the change, and the safety net checks the rows written. The new circle at the end of the list
// goes to the end of the table. Without an attached database nothing is written. Caller holds CirclesMu.
func SaveCirclesLocked(prev, next CircleBook) error {
	defer metrics.SaveSince(time.Now())
	d := attachedDB()
	if d == nil {
		return nil
	}
	site := callerSite()
	if err := d.Update(func(tx *sql.Tx) error { return saveCirclesRowsTx(tx, prev, next) }); err != nil {
		return err
	}
	if testing.Testing() {
		netCheckCircles(d, next, site)
	}
	return nil
}

// saveCirclesRowsTx writes the difference between prev and next (see SaveCirclesLocked).
func saveCirclesRowsTx(tx *sql.Tx, prev, next CircleBook) error {
	before := make(map[int64][]byte, len(prev.Circles))
	for _, c := range prev.Circles {
		b, err := json.Marshal(c)
		if err != nil {
			return err
		}
		before[c.ID] = b
	}
	present := make(map[int64]bool, len(next.Circles))
	for _, c := range next.Circles {
		present[c.ID] = true
	}
	for _, c := range prev.Circles {
		if !present[c.ID] {
			if err := deleteRow(tx, "circles", "circle_id = ?", c.ID); err != nil {
				return err
			}
		}
	}
	for i, c := range next.Circles {
		data, err := json.Marshal(c)
		if err != nil {
			return err
		}
		if old, ok := before[c.ID]; ok && bytes.Equal(old, data) {
			continue
		}
		if err := upsertRow(tx, "circles", slotOf(i, len(next.Circles)), []string{"circle_id", "data"},
			[]any{c.ID, string(data)}, "circle_id = ?", c.ID); err != nil {
			return err
		}
	}
	for aid := range prev.Leaves {
		if _, ok := next.Leaves[aid]; !ok {
			if err := deleteRow(tx, "circle_leaves", "aid = ?", aid); err != nil {
				return err
			}
		}
	}
	for aid, v := range next.Leaves {
		if old, ok := prev.Leaves[aid]; ok && old == v {
			continue
		}
		if err := upsertRow(tx, "circle_leaves", slotBack, []string{"aid", "value"}, []any{aid, v}, "aid = ?", aid); err != nil {
			return err
		}
	}
	return setMetaTx(tx, "circles.nextId", strconv.FormatInt(next.NextID, 10))
}

// AcceptedFriends reports whether a and b are accepted friends. It takes SocialMu alone, so a caller may hold
// AccountsMu or CirclesMu around it.
func AcceptedFriends(a, b string) bool {
	SocialMu.Lock()
	defer SocialMu.Unlock()
	return FriendshipIndex(a, b, "accepted") >= 0
}

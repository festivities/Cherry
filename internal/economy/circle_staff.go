package economy

import (
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"cherry/internal/store"
)

// CircleMaxMembers returns the effective member cap of c for staff pages.
func CircleMaxMembers(c store.Circle) int {
	return circleCapOf(&c)
}

func hasControlOrInvalidUTF8(s string) bool {
	return !utf8.ValidString(s) || slices.ContainsFunc([]rune(s), unicode.IsControl)
}

// StaffEditCircle renames a circle and updates its intro for the staff panel. The circle book change and its audit row
// ("circle-edit") are saved in one transaction; nothing changes on error.
func StaffEditCircle(id int64, name, intro, reason, actor string, now time.Time) error {
	reason, err := store.CleanStaffReason(reason)
	if err != nil {
		return err
	}
	name = strings.TrimSpace(name)
	intro = strings.TrimSpace(intro)
	if hasControlOrInvalidUTF8(name) {
		return fmt.Errorf("%w: the circle name must not contain control characters", store.ErrBadInput)
	}
	if hasControlOrInvalidUTF8(intro) {
		return fmt.Errorf("%w: the circle intro must not contain control characters", store.ErrBadInput)
	}
	if n := utf8.RuneCountInString(name); n < 1 || n > circleNameMax {
		return fmt.Errorf("%w: the circle name must be 1 to %d characters", store.ErrBadInput, circleNameMax)
	}
	if utf8.RuneCountInString(intro) > circleIntroMax {
		return fmt.Errorf("%w: the circle intro must be at most %d characters", store.ErrBadInput, circleIntroMax)
	}

	store.CirclesMu.Lock()
	ci := circleIndex(&store.CircleState, id)
	if ci < 0 {
		store.CirclesMu.Unlock()
		return store.ErrNoItem
	}
	for _, other := range store.CircleState.Circles {
		if other.ID != id && strings.EqualFold(other.Name, name) {
			store.CirclesMu.Unlock()
			return fmt.Errorf("%w: another circle already has that name", store.ErrBadInput)
		}
	}
	prev := store.CircleState
	next := prev.Clone()
	prevName := next.Circles[ci].Name
	next.Circles[ci].Name = name
	next.Circles[ci].Intro = intro
	next.Circles[ci].Modified = now.UnixMilli()
	detail := map[string]any{"name": name, "prevName": prevName, "intro": intro, "reason": reason}
	if err := store.SaveCirclesAuditedLocked(prev, next, actor, "circle-edit", fmt.Sprintf("circle:%d", id), detail); err != nil {
		store.CirclesMu.Unlock()
		return err
	}
	store.CircleState = next
	store.CirclesMu.Unlock()
	return nil
}

// StaffRemoveCircleMember removes a non-leader member from a circle for the staff panel, writes the "circle-kick"
// audit row in the same transaction, and notifies the live circle room after releasing CirclesMu.
func StaffRemoveCircleMember(id int64, aid, reason, actor string, now time.Time) error {
	reason, err := store.CleanStaffReason(reason)
	if err != nil {
		return err
	}

	store.CirclesMu.Lock()
	ci := circleIndex(&store.CircleState, id)
	if ci < 0 {
		store.CirclesMu.Unlock()
		return store.ErrNoItem
	}
	mi := memberIndex(&store.CircleState.Circles[ci], aid)
	if mi < 0 {
		store.CirclesMu.Unlock()
		return store.ErrNoItem
	}
	if store.CircleState.Circles[ci].Members[mi].Role == roleLeader {
		store.CirclesMu.Unlock()
		return fmt.Errorf("%w: the circle leader cannot be removed; disband the circle instead", store.ErrBadInput)
	}
	prev := store.CircleState
	next := prev.Clone()
	next.Circles[ci].Members = slices.Delete(next.Circles[ci].Members, mi, mi+1)
	next.Circles[ci].Modified = now.UnixMilli()
	detail := map[string]any{"aid": aid, "reason": reason}
	if err := store.SaveCirclesAuditedLocked(prev, next, actor, "circle-kick", fmt.Sprintf("circle:%d", id), detail); err != nil {
		store.CirclesMu.Unlock()
		return err
	}
	store.CircleState = next
	store.CirclesMu.Unlock()

	notifyCircleRoom(CircleRoomEvent{Circle: id, Left: []string{aid}})
	return nil
}

// StaffDisbandCircle disbands a circle for the staff panel, writes the "circle-disband" audit row in the same
// transaction, and notifies the live circle room after releasing CirclesMu.
func StaffDisbandCircle(id int64, reason, actor string) error {
	reason, err := store.CleanStaffReason(reason)
	if err != nil {
		return err
	}

	store.CirclesMu.Lock()
	ci := circleIndex(&store.CircleState, id)
	if ci < 0 {
		store.CirclesMu.Unlock()
		return store.ErrNoItem
	}
	prev := store.CircleState
	prevName := prev.Circles[ci].Name
	former := make([]string, len(prev.Circles[ci].Members))
	for i, m := range prev.Circles[ci].Members {
		former[i] = m.Aid
	}
	next := prev.Clone()
	next.Circles = slices.Delete(next.Circles, ci, ci+1)
	detail := map[string]any{"name": prevName, "members": len(former), "reason": reason}
	if err := store.SaveCirclesAuditedLocked(prev, next, actor, "circle-disband", fmt.Sprintf("circle:%d", id), detail); err != nil {
		store.CirclesMu.Unlock()
		return err
	}
	store.CircleState = next
	store.CirclesMu.Unlock()

	notifyCircleRoom(CircleRoomEvent{Circle: id, Left: former, Closed: true})
	return nil
}

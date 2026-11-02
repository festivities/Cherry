package economy

import (
	"time"

	"cherry/internal/store"
)

// Treasure World quests and missions (cells.md section 2; Help Center): a quest set of five pays tickets, a mission set
// of four pays Jewels. Progress counts the board's own events (rolls, Jewels won, laps, exchanges) on the posted tasks
// only, as the Help Center says requirements met before a task was posted do not count. A set is replaced when every task
// in it is claimed, and a quest set can be refreshed refresh_hours after it went up.

// TreasureTaskRow is one posted task as the wire shows it: Seq (a quest's slot, 1 to 5, or a mission's sequence number:
// set * 100 + order), the picture type, progress, the goal, the reward (tickets for a quest, Jewels for a mission) and the
// state (0 in progress, 1 complete, 2 claimed). Text and Item are a quest's own; missions carry neither.
type TreasureTaskRow struct {
	Seq, Type, Count, Goal, Reward, State int64
	Text, Item                            string
}

// TreasureTaskLists are the posted quests and missions, and when the free quest refresh opens (FreeRefresh now, ResetMs
// the Unix millisecond it opens).
type TreasureTaskLists struct {
	Quests, Missions []TreasureTaskRow
	FreeRefresh      bool
	ResetMs          int64
}

// TreasureClaim is one claim: what it paid, the task after it, the posted lists and the board.
type TreasureClaim struct {
	Paid  int64
	Row   TreasureTaskRow
	Lists TreasureTaskLists
	View  TreasureView
}

// treasureProgress counts one board event of kind n times on the posted quests and missions.
func treasureProgress(r *store.TreasureRecord, d *TreasureData, kind string, n int64) {
	treasureCount(&r.Quests, d.Quests.Pool, kind, n)
	treasureCount(&r.Missions, d.Missions.Pool, kind, n)
}

// treasureCount adds n to each task of the list that counts kind and is still in progress; a task that reaches its goal
// is complete (claimable).
func treasureCount(l *store.TreasureList, pool []TreasureTask, kind string, n int64) {
	for i := range l.Slots {
		s := &l.Slots[i]
		task := pool[s.Task]
		if s.State != 0 || task.Kind != kind {
			continue
		}
		s.Count = min(s.Count+n, task.Goal)
		if s.Count >= task.Goal {
			s.State = 1
		}
	}
}

// treasureNewSet posts the next set: SetSize tasks from the pool, rotated so that consecutive sets differ.
func treasureNewSet(l *store.TreasureList, t TreasureTasks, nowMs int64) {
	l.Set++
	l.Posted = nowMs
	n := int64(len(t.Pool))
	start := (l.Set - 1) * t.SetSize % n
	l.Slots = make([]store.TreasureSlot, t.SetSize)
	for i := range l.Slots {
		l.Slots[i].Task = (start + int64(i)) % n
	}
}

// treasureSetDone reports whether every task of the posted set is claimed, or the set names a task the pool no longer has
// (a data change: the set is replaced).
func treasureSetDone(l store.TreasureList, t TreasureTasks) bool {
	for _, s := range l.Slots {
		if s.Task >= int64(len(t.Pool)) {
			return true
		}
		if s.State != 2 {
			return false
		}
	}
	return true
}

// treasureEnsure posts the first set, or a new one once the posted set is done.
func treasureEnsure(l *store.TreasureList, t TreasureTasks, nowMs int64) {
	if l.Set == 0 || treasureSetDone(*l, t) {
		treasureNewSet(l, t, nowMs)
	}
}

// treasureRows lists a set's tasks as the wire shows them (a mission's Seq is set * 100 + its order).
func treasureRows(l store.TreasureList, pool []TreasureTask, missions bool) []TreasureTaskRow {
	rows := make([]TreasureTaskRow, 0, len(l.Slots))
	for i, s := range l.Slots {
		task := pool[s.Task]
		row := TreasureTaskRow{Seq: int64(i + 1), Type: task.Type, Count: s.Count, Goal: task.Goal, Reward: task.Reward, State: s.State}
		if missions {
			row.Seq = l.Set*100 + int64(i+1)
		} else {
			row.Text, row.Item = task.Text, task.Item
		}
		rows = append(rows, row)
	}
	return rows
}

// treasureLists is both lists as they stand at nowMs.
func treasureLists(r store.TreasureRecord, d *TreasureData, nowMs int64) TreasureTaskLists {
	resetMs := r.Quests.Posted + d.Quests.RefreshHours*3600*1000
	return TreasureTaskLists{
		Quests:      treasureRows(r.Quests, d.Quests.Pool, false),
		Missions:    treasureRows(r.Missions, d.Missions.Pool, true),
		FreeRefresh: nowMs >= resetMs,
		ResetMs:     resetMs,
	}
}

// TreasureTaskList posts the sets a board has none of (or whose set is done) and returns both lists. The client asks on
// every board entry.
func TreasureTaskList(aid string, now time.Time) (TreasureTaskLists, error) {
	var out TreasureTaskLists
	_, err := treasureTx(aid, now, true, func(_ *store.Account, r *store.TreasureRecord, d *TreasureData) ([]store.LedgerLine, error) {
		nowMs := now.UnixMilli()
		treasureEnsure(&r.Quests, d.Quests, nowMs)
		treasureEnsure(&r.Missions, d.Missions, nowMs)
		out = treasureLists(*r, d, nowMs)
		return nil, nil
	})
	return out, err
}

// TreasureQuestRefresh posts a new quest set. It is refused until refresh_hours after the posted set went up.
func TreasureQuestRefresh(aid string, now time.Time) (TreasureTaskLists, error) {
	var out TreasureTaskLists
	_, err := treasureTx(aid, now, true, func(_ *store.Account, r *store.TreasureRecord, d *TreasureData) ([]store.LedgerLine, error) {
		nowMs := now.UnixMilli()
		if r.Quests.Set > 0 && nowMs < r.Quests.Posted+d.Quests.RefreshHours*3600*1000 {
			return nil, ErrTreasureRefused
		}
		treasureNewSet(&r.Quests, d.Quests, nowMs)
		treasureEnsure(&r.Missions, d.Missions, nowMs)
		out = treasureLists(*r, d, nowMs)
		return nil, nil
	})
	return out, err
}

// TreasureQuestClaim pays the tickets of quest slot (1 to 5) once it is complete.
func TreasureQuestClaim(aid string, slot int64, now time.Time) (TreasureClaim, error) {
	var out TreasureClaim
	view, err := treasureTx(aid, now, true, func(_ *store.Account, r *store.TreasureRecord, d *TreasureData) ([]store.LedgerLine, error) {
		if slot < 1 || slot > int64(len(r.Quests.Slots)) || r.Quests.Slots[slot-1].State != 1 {
			return nil, ErrTreasureRefused
		}
		s := &r.Quests.Slots[slot-1]
		s.State = 2
		out.Paid = d.Quests.Pool[s.Task].Reward
		r.Tickets += out.Paid
		out.Row = treasureRows(r.Quests, d.Quests.Pool, false)[slot-1]
		out.Lists = treasureLists(*r, d, now.UnixMilli())
		return nil, nil
	})
	out.View = view
	return out, err
}

// TreasureMissionClaim pays the Jewels of the mission seq (set * 100 + order) once it is complete.
func TreasureMissionClaim(aid string, seq int64, now time.Time) (TreasureClaim, error) {
	var out TreasureClaim
	view, err := treasureTx(aid, now, true, func(_ *store.Account, r *store.TreasureRecord, d *TreasureData) ([]store.LedgerLine, error) {
		order := seq % 100
		if seq/100 != r.Missions.Set || order < 1 || order > int64(len(r.Missions.Slots)) || r.Missions.Slots[order-1].State != 1 {
			return nil, ErrTreasureRefused
		}
		s := &r.Missions.Slots[order-1]
		s.State = 2
		out.Paid = treasureBoost(d.Missions.Pool[s.Task].Reward, treasureBuffPercent(*r, d))
		r.Jewels += out.Paid
		out.Row = treasureRows(r.Missions, d.Missions.Pool, true)[order-1]
		out.Lists = treasureLists(*r, d, now.UnixMilli())
		return nil, nil
	})
	out.View = view
	return out, err
}

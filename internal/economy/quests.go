package economy

import (
	"log"
	"slices"
	"strconv"
	"time"

	"cherry/internal/store"
)

// Economy Phase D slice 1: four Garden daily quests paying Gems (user decision 2026-10-11;
// 150 Gems per game day in total). Progress is server-side: the Garden, Square and diary
// handlers call MarkQuest, and the client only lists, reads and claims. Game days start at
// 06:00 JST like the login calendar. Wire notes: %TEMP%\opencode\cherry\phaseD-20261011\quests.md section 10.

// QuestDef is one daily quest. ID is the QuestId the client sends back. It is Cherry-chosen
// and sits above the client's compiled eQUEST_CODE values.
type QuestDef struct {
	ID   int64
	Key  string // progress key passed to MarkQuest
	Name string
	Desc string
	Gems int64
}

// QuestCatalog is the daily list in display order.
var QuestCatalog = []QuestDef{
	{9001, "garden-own", "Visit your Garden", "Enter your own Garden today.", 30},
	{9002, "garden-visit", "Visit a friend's Garden", "Enter a friend's Garden today.", 40},
	{9003, "square", "Hang out in Square", "Enter Square today.", 40},
	{9004, "diary", "Write in your Diary", "Post a diary entry today.", 40},
}

// questErrRejected is the error_code for every refused claim (unknown id, not done, already
// claimed, or a failed save). 1 shows the client's generic error text.
const questErrRejected = 1

var jst = time.FixedZone("JST", 9*3600)

// QuestByID returns the catalog entry for id.
func QuestByID(id int64) (QuestDef, bool) {
	for _, def := range QuestCatalog {
		if def.ID == id {
			return def, true
		}
	}
	return QuestDef{}, false
}

func questByKey(key string) (QuestDef, bool) {
	for _, def := range QuestCatalog {
		if def.Key == key {
			return def, true
		}
	}
	return QuestDef{}, false
}

// questRecord returns acc's progress for day. A record from another day counts as empty.
func questRecord(acc *store.Account, day int64) store.QuestDay {
	if acc.Quests == nil || acc.Quests.Day != day {
		return store.QuestDay{Day: day}
	}
	return *acc.Quests
}

// QuestProgress returns a copy of aid's progress for now's game day (empty when unknown).
func QuestProgress(aid string, now time.Time) store.QuestDay {
	store.AccountsMu.Lock()
	defer store.AccountsMu.Unlock()
	day := AttendDayKey(now)
	acc := store.AccountByAidLocked(aid)
	if acc == nil {
		return store.QuestDay{Day: day}
	}
	return questRecord(acc, day)
}

// QuestResetMillis is the next game-day start (06:00 JST) in epoch milliseconds.
func QuestResetMillis(now time.Time) int64 {
	return ((AttendDayKey(now)+1)*86400 - attendBoundaryShift) * 1000
}

// MarkQuest records progress key for aid today and completes the quest. The client's daily
// normal row has no claim control (quests.md section 10), so the Gems are credited here, once
// per game day, at the first completion. Unknown keys and accounts change nothing, and a
// repeat the same day writes nothing. It reports the quest when this call completed it. A
// failed save or ledger change rolls back.
func MarkQuest(aid, key string, now time.Time) (QuestDef, bool) {
	def, ok := questByKey(key)
	if !ok {
		return QuestDef{}, false
	}
	store.AccountsMu.Lock()
	defer store.AccountsMu.Unlock()
	acc := store.AccountByAidLocked(aid)
	if acc == nil {
		return QuestDef{}, false
	}
	day := AttendDayKey(now)
	rec := questRecord(acc, day)
	if slices.Contains(rec.Claimed, def.ID) {
		return QuestDef{}, false
	}
	done := rec.Done
	if !slices.Contains(done, def.ID) {
		done = append(slices.Clone(done), def.ID)
	}
	prev := *acc
	lines, err := store.SpendLocked(acc, store.LedgerDeltas{Gems: def.Gems}, "quest:"+strconv.FormatInt(def.ID, 10)+":"+questDate(day))
	if err != nil {
		*acc = prev
		log.Printf("cherry: garden-quest credit failed aid=%s id=%d err=%v", aid, def.ID, err)
		return QuestDef{}, false
	}
	acc.Quests = &store.QuestDay{Day: day, Done: done, Claimed: append(slices.Clone(rec.Claimed), def.ID)}
	if err := store.SaveAccountsLocked(); err != nil {
		*acc = prev
		log.Printf("cherry: garden-quest save failed aid=%s id=%d err=%v", aid, def.ID, err)
		return QuestDef{}, false
	}
	store.FlushLedgerLocked(lines)
	log.Printf("cherry: garden-quest complete aid=%s id=%d gems=%d", aid, def.ID, def.Gems)
	return def, true
}

// ClaimQuest credits the Gems of quest id when it is done today and not yet claimed. It
// returns (gems, 0) on credit and (0, questErrRejected) otherwise, with no ledger line. The
// claim is saved before the reply; a failed save rolls back.
func ClaimQuest(aid string, id int64, now time.Time) (int64, int) {
	def, ok := QuestByID(id)
	store.AccountsMu.Lock()
	defer store.AccountsMu.Unlock()
	acc := store.AccountByAidLocked(aid)
	if !ok || acc == nil {
		return 0, questErrRejected
	}
	day := AttendDayKey(now)
	rec := questRecord(acc, day)
	if !slices.Contains(rec.Done, id) || slices.Contains(rec.Claimed, id) {
		return 0, questErrRejected
	}
	prev := *acc
	reason := "quest:" + strconv.FormatInt(id, 10) + ":" + questDate(day)
	lines, err := store.SpendLocked(acc, store.LedgerDeltas{Gems: def.Gems}, reason)
	if err != nil {
		*acc = prev
		return 0, questErrRejected
	}
	acc.Quests = &store.QuestDay{Day: day, Done: rec.Done, Claimed: append(slices.Clone(rec.Claimed), id)}
	if err := store.SaveAccountsLocked(); err != nil {
		*acc = prev
		log.Printf("cherry: garden-quest claim save failed aid=%s id=%d err=%v", aid, id, err)
		return 0, questErrRejected
	}
	store.FlushLedgerLocked(lines)
	return def.Gems, 0
}

// questDate is the calendar date (JST) on which game day day starts at 06:00.
func questDate(day int64) string {
	return time.Unix(day*86400-attendBoundaryShift, 0).In(jst).Format("20060102")
}

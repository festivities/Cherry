package economy

import (
	"fmt"
	"log"
	"slices"

	"cherry/internal/store"
)

const (
	// HighFiveDailyGoal is the number of unique player-to-player Square high-fives
	// required per 06:00 JST game day to complete TLUP QuestId 7 (eQUEST_CODE 106).
	HighFiveDailyGoal = 20

	// HighFiveRewardGems is the daily completion reward credited once per game day
	// when HighFiveDailyGoal is reached.
	HighFiveRewardGems int64 = 100
)

// HighFiveProgress returns aid's Square high-five progress for the current 06:00 JST
// game day, along with the daily goal and Gem reward. If aid is missing or its stored
// record is from an earlier game day, count is 0 and targets is nil.
func HighFiveProgress(aid string) (count int, targets []string, goal int, reward int64) {
	snap, ok := store.AccountByAvatarID(aid)
	if !ok || snap.HighFive.Day != AttendDayKey(NowFunc()) {
		return 0, nil, HighFiveDailyGoal, HighFiveRewardGems
	}
	return snap.HighFive.Count, snap.HighFive.Targets, HighFiveDailyGoal, HighFiveRewardGems
}

// RecordHighFive records a one-way player-to-player Square high-five from aid to
// targetAid on the current 06:00 JST game day. Each initiator can high-five a given
// target once per 06:00 JST game day (A -> B is deduplicated for A, while B -> A
// remains allowed for B's own quest). Reaching HighFiveDailyGoal credits
// HighFiveRewardGems once per game day via an atomic ledger+account save. There is
// no hard cap on additional unique high-fives after the goal is reached.
func RecordHighFive(aid, targetAid string) (count int, paidGems int64, ok bool) {
	if aid == "" || targetAid == "" || aid == targetAid {
		return 0, 0, false
	}
	store.AccountsMu.Lock()
	defer store.AccountsMu.Unlock()

	acc := store.AccountByAidLocked(aid)
	target := store.AccountByAidLocked(targetAid)
	if acc == nil || target == nil {
		return 0, 0, false
	}

	day := AttendDayKey(NowFunc())
	curAcc := acc.HighFive
	if curAcc.Day != day {
		curAcc = store.HighFiveDay{Day: day}
	}

	if slices.Contains(curAcc.Targets, targetAid) {
		return curAcc.Count, 0, false
	}

	prevAcc := *acc
	prevCount := curAcc.Count

	curAcc.Targets = append(slices.Clone(curAcc.Targets), targetAid)
	curAcc.Count++
	acc.HighFive = curAcc

	var lines []store.LedgerLine
	if acc.HighFive.Count >= HighFiveDailyGoal && !acc.HighFive.Claimed {
		acc.HighFive.Claimed = true
		var err error
		lines, err = store.SpendLocked(acc, store.LedgerDeltas{Gems: HighFiveRewardGems}, fmt.Sprintf("highfive:%d", day))
		if err != nil {
			*acc = prevAcc
			log.Printf("cherry: highfive reward failed aid=%s err=%v", aid, err)
			return prevCount, 0, false
		}
		paidGems = HighFiveRewardGems
	}

	if err := store.SaveLocked(lines, acc); err != nil {
		*acc = prevAcc
		log.Printf("cherry: highfive save failed aid=%s target=%s err=%v", aid, targetAid, err)
		return prevCount, 0, false
	}
	return acc.HighFive.Count, paidGems, true
}

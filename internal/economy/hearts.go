package economy

import (
	"log"
	"slices"
	"time"

	"cherry/internal/store"
)

// Hearts (Economy Phase D leftover). An avatar sends hearts over TLUP event 102 (the Garden
// heart button, profile, diary and ranking buttons). A sender may send hearts.base hearts on one
// game day (06:00 JST, like the quests); past that each extra heart it holds (Extra, staff grants)
// is spent for one more. The hearts.quest-th heart of the day completes the hearts quest (row 9005)
// and credits hearts.reward_gems and hearts.reward_cash once (rewards.json). Repeats to one target
// count. Wire notes: %TEMP%\opencode\cherry\phaseDE-20261022\hearts.md.

// HeartBase is the hearts a sender may send per game day without extra hearts.
func HeartBase() int64 { return ActiveRewards().Hearts.Base }

// HeartQuestCount is the hearts sent in one game day that complete the hearts quest (checkRewards keeps
// it from 1 to HeartBase).
func HeartQuestCount() int64 { return ActiveRewards().Hearts.Quest }

// HeartRewardGems is the Gems credited once a day with the HeartQuestCount-th heart.
func HeartRewardGems() int64 { return ActiveRewards().Hearts.RewardGems }

// HeartRewardCash is the Cash credited once a day with the HeartQuestCount-th heart.
func HeartRewardCash() int64 { return ActiveRewards().Hearts.RewardCash }

// heartRecord returns acc's heart record for day. A record from another day keeps only the
// lifetime Received and Extra counts.
func heartRecord(acc *store.Account, day int64) store.Hearts {
	if acc.Hearts == nil {
		return store.Hearts{Day: day}
	}
	if acc.Hearts.Day != day {
		return store.Hearts{Day: day, Received: acc.Hearts.Received, Extra: acc.Hearts.Extra}
	}
	return *acc.Hearts
}

// heartExtra is acc's extra hearts to send (a lifetime count, 0 for none).
func heartExtra(acc *store.Account) int64 {
	if acc.Hearts == nil {
		return 0
	}
	return acc.Hearts.Extra
}

// heartTargetLocked finds a heart target: an existing account (nil account for the synthetic
// Friend, which has no record) and whether the aid is valid at all. Caller holds accountsMu.
func heartTargetLocked(aid string) (*store.Account, bool) {
	if acc := store.AccountByAidLocked(aid); acc != nil {
		return acc, true
	}
	return nil, aid == store.FriendAID
}

// HeartSend counts one heart from sender to target at now. sent is whether it counted; questDone is
// whether it was the day's HeartQuestCount-th heart, which completes the hearts quest. It returns
// false, false and changes nothing for an unknown sender, an unknown target, a self-target, or a
// sender with no daily heart and no extra heart left today. Past HeartBase a heart spends one extra
// heart. A counted heart bumps the target's received total and sender list (real accounts only). The
// HeartQuestCount-th heart of the day credits the reward once. A failed save or ledger change rolls
// both accounts back.
func HeartSend(sender, target string, now time.Time) (sent, questDone bool) {
	store.AccountsMu.Lock()
	defer store.AccountsMu.Unlock()
	acc := store.AccountByAidLocked(sender)
	tgt, valid := heartTargetLocked(target)
	if acc == nil || !valid || sender == target {
		return false, false
	}
	day := AttendDayKey(now)
	rec := heartRecord(acc, day)
	if rec.Sent >= HeartBase() {
		if rec.Extra == 0 {
			return false, false
		}
		rec.Extra--
	}
	prevAcc := *acc
	var prevTgt store.Account
	if tgt != nil {
		prevTgt = *tgt
		th := heartRecord(tgt, day)
		th.Received++
		th.From = append(slices.Clone(th.From), sender)
		if len(th.From) > ActiveRewards().Hearts.FromLimit {
			th.From = th.From[len(th.From)-ActiveRewards().Hearts.FromLimit:]
		}
		tgt.Hearts = &th
		store.AppendNewsAlertLocked(tgt, store.NewsAlert{
			Type:      "HEART_RECEIVE",
			At:        now.UnixMilli(),
			Actor:     sender,
			ActorName: acc.Name,
		})
	}
	restore := func() {
		*acc = prevAcc
		if tgt != nil {
			*tgt = prevTgt
		}
	}
	rec.Sent++
	acc.Hearts = &rec
	questDone = rec.Sent == HeartQuestCount()
	var lines []store.LedgerLine
	if questDone && (HeartRewardGems() != 0 || HeartRewardCash() != 0) {
		var err error
		lines, err = store.SpendLocked(acc, store.LedgerDeltas{Gems: HeartRewardGems(), Cash: HeartRewardCash()}, "heart:"+questDate(day))
		if err != nil {
			restore()
			log.Printf("cherry: heart reward failed aid=%s err=%v", sender, err)
			return false, false
		}
	}
	named := []*store.Account{acc}
	if tgt != nil {
		named = append(named, tgt)
	}
	if err := store.SaveLocked(lines, named...); err != nil {
		restore()
		log.Printf("cherry: heart save failed aid=%s to=%s err=%v", sender, target, err)
		return false, false
	}
	log.Printf("cherry: heart sent aid=%s to=%s sent=%d", sender, target, rec.Sent)
	return true, questDone
}

// HeartsLeft returns how many hearts aid may still send today: the daily allowance left (HeartBase
// minus sent, at least 0) plus its extra hearts. ok is false for an unknown account.
func HeartsLeft(aid string, now time.Time) (int64, bool) {
	store.AccountsMu.Lock()
	defer store.AccountsMu.Unlock()
	acc := store.AccountByAidLocked(aid)
	if acc == nil {
		return 0, false
	}
	rec := heartRecord(acc, AttendDayKey(now))
	return max(HeartBase()-rec.Sent, 0) + rec.Extra, true
}

// HeartsSentToday returns how many hearts aid has sent today, daily and extra. ok is false for an
// unknown account (0 sent).
func HeartsSentToday(aid string, now time.Time) (int64, bool) {
	store.AccountsMu.Lock()
	defer store.AccountsMu.Unlock()
	acc := store.AccountByAidLocked(aid)
	if acc == nil {
		return 0, false
	}
	return heartRecord(acc, AttendDayKey(now)).Sent, true
}

// HeartsReceived returns aid's lifetime received-heart total (0 for an unknown account).
func HeartsReceived(aid string) int64 {
	store.AccountsMu.Lock()
	defer store.AccountsMu.Unlock()
	if acc := store.AccountByAidLocked(aid); acc != nil && acc.Hearts != nil {
		return acc.Hearts.Received
	}
	return 0
}

// HeartSenders returns the sender aids that hearted aid today, oldest first (at most heartFromLimit).
func HeartSenders(aid string, now time.Time) []string {
	store.AccountsMu.Lock()
	defer store.AccountsMu.Unlock()
	acc := store.AccountByAidLocked(aid)
	if acc == nil {
		return nil
	}
	return slices.Clone(heartRecord(acc, AttendDayKey(now)).From)
}

// HaveHeart is the heart/haveHeart answer: viewer may still send hearts today (a daily or an extra one).
func HaveHeart(viewer string, now time.Time) bool {
	left, ok := HeartsLeft(viewer, now)
	return ok && left > 0
}

// HeartReceivable reports whether viewer may send a heart to target: viewer is a known account,
// target is a known account or the synthetic Friend, and they differ.
func HeartReceivable(viewer, target string) bool {
	store.AccountsMu.Lock()
	defer store.AccountsMu.Unlock()
	if store.AccountByAidLocked(viewer) == nil || viewer == target {
		return false
	}
	_, valid := heartTargetLocked(target)
	return valid
}

// HeartResetSeconds is the whole seconds from now to the next 06:00 JST game-day start, as the
// quest/status toExpire field counts them.
func HeartResetSeconds(now time.Time) int64 {
	return QuestResetMillis(now)/1000 - now.Unix()
}

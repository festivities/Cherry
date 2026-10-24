package economy

import (
	"log"
	"slices"
	"time"

	"cherry/internal/store"
)

// Hearts quest (Economy Phase D leftover). An avatar sends hearts over TLUP event 102 (the Garden
// heart button, profile, diary and ranking buttons). The first hearts.base hearts a sender sends on
// one game day (06:00 JST, like the quests) count, and hearts.reward_gems is credited once, with the
// base-th heart (rewards.json). Repeats to one target count. Wire notes:
// %TEMP%\opencode\cherry\phaseDE-20261022\hearts.md.

// HeartBase is hearts per game day that count toward the quest.
func HeartBase() int64 { return ActiveRewards().Hearts.Base }

// HeartRewardGems is the Gems credited once a day with the HeartBase-th heart.
func HeartRewardGems() int64 { return ActiveRewards().Hearts.RewardGems }

// heartRecord returns acc's heart record for day. A record from another day keeps only the
// lifetime Received count.
func heartRecord(acc *store.Account, day int64) store.Hearts {
	if acc.Hearts == nil {
		return store.Hearts{Day: day}
	}
	if acc.Hearts.Day != day {
		return store.Hearts{Day: day, Received: acc.Hearts.Received}
	}
	return *acc.Hearts
}

// heartTargetLocked finds a heart target: an existing account (nil account for the synthetic
// Friend, which has no record) and whether the aid is valid at all. Caller holds accountsMu.
func heartTargetLocked(aid string) (*store.Account, bool) {
	if acc := store.AccountByAidLocked(aid); acc != nil {
		return acc, true
	}
	return nil, aid == store.FriendAID
}

// HeartSend counts one heart from sender to target at now. It returns false and changes nothing
// for an unknown sender, an unknown target, a self-target, or a sender already at HeartBase today.
// A counted heart bumps the target's received total and sender list (real accounts only). The
// HeartBase-th heart of the day credits HeartRewardGems once. A failed save or ledger change rolls
// both accounts back.
func HeartSend(sender, target string, now time.Time) bool {
	store.AccountsMu.Lock()
	defer store.AccountsMu.Unlock()
	acc := store.AccountByAidLocked(sender)
	tgt, valid := heartTargetLocked(target)
	if acc == nil || !valid || sender == target {
		return false
	}
	day := AttendDayKey(now)
	rec := heartRecord(acc, day)
	if rec.Sent >= HeartBase() {
		return false
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
	}
	restore := func() {
		*acc = prevAcc
		if tgt != nil {
			*tgt = prevTgt
		}
	}
	rec.Sent++
	acc.Hearts = &rec
	var lines []store.LedgerLine
	if rec.Sent == HeartBase() {
		var err error
		lines, err = store.SpendLocked(acc, store.LedgerDeltas{Gems: HeartRewardGems()}, "heart:"+questDate(day))
		if err != nil {
			restore()
			log.Printf("cherry: heart reward failed aid=%s err=%v", sender, err)
			return false
		}
	}
	if err := store.SaveAccountsLocked(); err != nil {
		restore()
		log.Printf("cherry: heart save failed aid=%s to=%s err=%v", sender, target, err)
		return false
	}
	store.FlushLedgerLocked(lines)
	log.Printf("cherry: heart sent aid=%s to=%s sent=%d", sender, target, rec.Sent)
	return true
}

// HeartsLeft returns how many hearts aid may still send today (HeartBase minus sent, at least 0).
// ok is false for an unknown account.
func HeartsLeft(aid string, now time.Time) (int64, bool) {
	store.AccountsMu.Lock()
	defer store.AccountsMu.Unlock()
	acc := store.AccountByAidLocked(aid)
	if acc == nil {
		return 0, false
	}
	return max(HeartBase()-heartRecord(acc, AttendDayKey(now)).Sent, 0), true
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

// HaveHeart is the heart/haveHeart answer: viewer may still send hearts today (under HeartBase).
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

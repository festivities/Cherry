package economy

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"cherry/data"
	"cherry/internal/store"
	"cherry/internal/testutil"
)

// heartAccounts installs non-lab accounts aids for the hearts tests.
func heartAccounts(t *testing.T, aids ...string) {
	t.Helper()
	entries := map[string]*store.Account{}
	for _, aid := range aids {
		entries["htok-"+aid] = &store.Account{AccessToken: "htok-" + aid, SessionKey: "sk", Aid: aid, Name: "H" + aid}
	}
	testutil.InstallAccounts(t, entries)
	for _, aid := range aids {
		testutil.SetLab(t, aid, false)
	}
}

func heartAccount(t *testing.T, aid string) store.Account {
	t.Helper()
	store.AccountsMu.Lock()
	defer store.AccountsMu.Unlock()
	acc := store.AccountByAidLocked(aid)
	if acc == nil {
		t.Fatalf("no account %s", aid)
	}
	return *acc
}

func heartTargetIDs(prefix, count int) []string {
	out := make([]string, count)
	for i := range count {
		out[i] = strconv.Itoa(prefix + i)
	}
	return out
}

func sendHeartsToTargets(t *testing.T, from string, targets []string, now time.Time) {
	t.Helper()
	for i, to := range targets {
		if sent, _ := HeartSend(from, to, now); !sent {
			t.Fatalf("heart %d from %s to %s refused", i+1, from, to)
		}
	}
}

// TestHeartSendCapsAtBase: hearts to distinct targets count up to the daily allowance, the heart past
// the daily allowance is refused and changes nothing, and each target's received total follows counted hearts.
func TestHeartSendCapsAtBase(t *testing.T) {
	targets := heartTargetIDs(9802, int(HeartBase())+1)
	heartAccounts(t, append([]string{"9801"}, targets...)...)
	now := jstAt(22, 12, 0)
	sendHeartsToTargets(t, "9801", targets[:HeartBase()], now)
	if sent, _ := HeartSend("9801", targets[HeartBase()], now); sent {
		t.Fatal("heart past the daily allowance counted")
	}
	if left, _ := HeartsLeft("9801", now); left != 0 {
		t.Fatalf("left = %d, want 0", left)
	}
	if got := HeartsReceived(targets[0]); got != 1 {
		t.Fatalf("received[0] = %d, want 1", got)
	}
	if got := HeartsReceived(targets[HeartBase()]); got != 0 {
		t.Fatalf("received[over-cap] = %d, want 0", got)
	}
	if got := heartAccount(t, "9801").Hearts.Sent; got != HeartBase() {
		t.Fatalf("sent = %d, want %d", got, HeartBase())
	}
}

// TestHeartRewardOncePerDay: the HeartQuestCount()-th heart credits HeartRewardCash() once, and the
// hearts after it up to the daily allowance do not pay again; the next game day can earn it again.
func TestHeartRewardOncePerDay(t *testing.T) {
	targets := heartTargetIDs(98100, int(HeartBase()))
	heartAccounts(t, append([]string{"9803"}, targets...)...)
	day1 := jstAt(22, 12, 0)
	sendHeartsToTargets(t, "9803", targets[:HeartQuestCount()-1], day1)
	if acc := heartAccount(t, "9803"); acc.Cash != 0 || acc.Gems != 0 {
		t.Fatalf("balances before the quest heart = cash %d gems %d, want 0/0", acc.Cash, acc.Gems)
	}
	sendHeartsToTargets(t, "9803", targets[HeartQuestCount()-1:HeartQuestCount()], day1)
	if acc := heartAccount(t, "9803"); acc.Cash != HeartRewardCash() || acc.Gems != 0 {
		t.Fatalf("balances after quest heart = cash %d gems %d, want %d/0", acc.Cash, acc.Gems, HeartRewardCash())
	}
	sendHeartsToTargets(t, "9803", targets[HeartQuestCount():HeartBase()], day1)
	if acc := heartAccount(t, "9803"); acc.Cash != HeartRewardCash() || acc.Gems != 0 {
		t.Fatalf("balances after the rest of the allowance = cash %d gems %d, want %d/0", acc.Cash, acc.Gems, HeartRewardCash())
	}
	day2 := jstAt(23, 12, 0)
	sendHeartsToTargets(t, "9803", targets[:HeartQuestCount()], day2)
	if acc := heartAccount(t, "9803"); acc.Cash != 2*HeartRewardCash() || acc.Gems != 0 {
		t.Fatalf("balances after day 2 = cash %d gems %d, want %d/0", acc.Cash, acc.Gems, 2*HeartRewardCash())
	}
}

// TestHeartExtraHearts: the quest heart pays once; the heart past the daily allowance is refused without extra
// hearts; with three extra hearts (a staff grant) the next two are sent and one is left; the left-over extra
// heart survives the game day while the daily count resets, and the last one is spent after the allowance.
func TestHeartExtraHearts(t *testing.T) {
	targets := heartTargetIDs(98200, int(HeartBase())+4)
	heartAccounts(t, append([]string{"9860"}, targets...)...)
	if HeartQuestCount() >= HeartBase() {
		t.Fatal("this test needs hearts.quest below hearts.base")
	}
	day1, day2 := jstAt(22, 12, 0), jstAt(23, 12, 0)
	sendHeartsToTargets(t, "9860", targets[:HeartBase()], day1)
	if sent, _ := HeartSend("9860", targets[HeartBase()], day1); sent {
		t.Fatal("heart past the allowance counted without extra hearts")
	}
	grantExtra(t, "9860", 3, day1)
	if left, _ := HeartsLeft("9860", day1); left != 3 {
		t.Fatalf("left after the grant = %d, want 3", left)
	}
	sendHeartsToTargets(t, "9860", targets[HeartBase():HeartBase()+2], day1)
	if acc := heartAccount(t, "9860"); acc.Hearts.Extra != 1 || acc.Hearts.Sent != HeartBase()+2 || acc.Cash != HeartRewardCash() {
		t.Fatalf("after the extras: hearts %+v cash %d, want extra 1, sent %d, cash %d", *acc.Hearts, acc.Cash, HeartBase()+2, HeartRewardCash())
	}
	if left, _ := HeartsLeft("9860", day2); left != HeartBase()+1 {
		t.Fatalf("day 2 left = %d, want the allowance and the extra heart (%d)", left, HeartBase()+1)
	}
	sendHeartsToTargets(t, "9860", targets[:HeartBase()+1], day2)
	if sent, _ := HeartSend("9860", targets[HeartBase()+1], day2); sent {
		t.Fatal("heart past the extra hearts counted")
	}
	if acc := heartAccount(t, "9860"); acc.Hearts.Sent != HeartBase()+1 || acc.Hearts.Extra != 0 || acc.Cash != 2*HeartRewardCash() {
		t.Fatalf("day 2 hearts %+v cash %d, want sent %d, extra 0, cash %d", *acc.Hearts, acc.Cash, HeartBase()+1, 2*HeartRewardCash())
	}
}

// TestHeartQuestRules: hearts.quest is from 1 to hearts.base, and an override that leaves it out means hearts.base;
// hearts.cooldown_minutes is 0..10080 (omitted defaults to 1440, 0 disables the per-target cooldown).
func TestHeartQuestRules(t *testing.T) {
	t.Cleanup(resetOperatorData)
	raw := data.RewardsJSON
	refuseFile(t, rewardsFile.LoadFrom, "rewards.json", mutateDefault(t, raw, `"quest": 20`, `"quest": 31`), "hearts.quest must be from 1 to hearts.base")
	refuseFile(t, rewardsFile.LoadFrom, "rewards.json", mutateDefault(t, raw, `"quest": 20`, `"quest": -1`), "hearts.quest must be from 1 to hearts.base")
	refuseFile(t, rewardsFile.LoadFrom, "rewards.json", mutateDefault(t, raw, `"cooldown_minutes": 1440`, `"cooldown_minutes": -1`), "hearts.cooldown_minutes must be from 0 to 10080")
	refuseFile(t, rewardsFile.LoadFrom, "rewards.json", mutateDefault(t, raw, `"cooldown_minutes": 1440`, `"cooldown_minutes": 10081`), "hearts.cooldown_minutes must be from 0 to 10080")
	path := filepath.Join(t.TempDir(), "rewards.json")
	if err := os.WriteFile(path, []byte(mutateDefault(t, raw, `"quest": 20, `, ``)), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := rewardsFile.LoadFrom(path); err != nil || HeartQuestCount() != HeartBase() {
		t.Fatalf("override without hearts.quest: err %v, quest %d, base %d", err, HeartQuestCount(), HeartBase())
	}
	// Override without cooldown_minutes falls back to defaultHeartCooldownMinutes (1440).
	noCD := filepath.Join(t.TempDir(), "rewards-nocd.json")
	if err := os.WriteFile(noCD, []byte(mutateDefault(t, raw, `, "cooldown_minutes": 1440`, ``)), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := rewardsFile.LoadFrom(noCD); err != nil || HeartCooldownMinutes() != defaultHeartCooldownMinutes {
		t.Fatalf("override without cooldown_minutes: err %v, got %d, want %d", err, HeartCooldownMinutes(), defaultHeartCooldownMinutes)
	}
	// Explicit cooldown_minutes: 0 disables per-target cooldown.
	zeroCD := filepath.Join(t.TempDir(), "rewards-zerocd.json")
	if err := os.WriteFile(zeroCD, []byte(mutateDefault(t, raw, `"cooldown_minutes": 1440`, `"cooldown_minutes": 0`)), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := rewardsFile.LoadFrom(zeroCD); err != nil || HeartCooldownMinutes() != 0 {
		t.Fatalf("override with cooldown_minutes=0: err %v, got %d", err, HeartCooldownMinutes())
	}
	heartAccounts(t, "9871", "9872")
	now := jstAt(22, 12, 0)
	if sent, _ := HeartSend("9871", "9872", now); !sent {
		t.Fatal("first heart with cooldown=0 refused")
	}
	if !HeartReceivableAt("9871", "9872", now) {
		t.Fatal("HeartReceivableAt false with cooldown=0")
	}
	if sent, _ := HeartSend("9871", "9872", now); !sent {
		t.Fatal("repeat heart with cooldown=0 refused")
	}
}

// grantExtra gives aid n extra hearts through the staff grant path (GrantLocked) at now.
func grantExtra(t *testing.T, aid string, n int64, now time.Time) {
	t.Helper()
	store.AccountsMu.Lock()
	defer store.AccountsMu.Unlock()
	acc := store.AccountByAidLocked(aid)
	if _, err := GrantLocked(acc, StaffGrant{ExtraHearts: n}, "test", now); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveLocked(nil, acc); err != nil {
		t.Fatal(err)
	}
}

// TestHeartSelfUnknownAndSynthetic: a self-target, an unknown target or an unknown sender counts
// nothing; the synthetic Friend is a valid target with no record to update.
func TestHeartSelfUnknownAndSynthetic(t *testing.T) {
	heartAccounts(t, "9805")
	now := jstAt(22, 12, 0)
	for _, c := range []struct{ from, to string }{
		{"9805", "9805"}, {"9805", "9999"}, {"9999", "9805"},
	} {
		if sent, _ := HeartSend(c.from, c.to, now); sent {
			t.Errorf("heart %s -> %s counted", c.from, c.to)
		}
	}
	if sent, _ := HeartSend("9805", store.FriendAID, now); !sent {
		t.Fatal("heart to synthetic Friend refused")
	}
	if left, _ := HeartsLeft("9805", now); left != HeartBase()-1 {
		t.Fatalf("left after Friend heart = %d, want %d", left, HeartBase()-1)
	}
	if HeartsReceived(store.FriendAID) != 0 || HeartsReceived("9999") != 0 {
		t.Fatal("received counted for a non-account")
	}
	if got := heartAccount(t, "9805").Hearts.Sent; got != 1 {
		t.Fatalf("sent = %d, want 1", got)
	}
}

// TestHeartDayRollover: a new game day restarts the sender's count and today's sender list, but
// the lifetime received total carries over.
func TestHeartDayRollover(t *testing.T) {
	targets := heartTargetIDs(98300, int(HeartBase()))
	heartAccounts(t, append([]string{"9806"}, targets...)...)
	day1, day2Early, day2 := jstAt(22, 12, 0), jstAt(23, 7, 0), jstAt(23, 12, 0)
	sendHeartsToTargets(t, "9806", targets, day1)
	if senders := HeartSenders(targets[0], day1); len(senders) != 1 || senders[0] != "9806" {
		t.Fatalf("day 1 senders = %v, want [9806]", senders)
	}
	if !HaveHeart("9806", day2Early) {
		t.Fatal("haveHeart false on the new day")
	}
	if HaveHeart("9806", day1) {
		t.Fatal("haveHeart true at the cap")
	}
	if senders := HeartSenders(targets[0], day2Early); len(senders) != 0 {
		t.Fatalf("day 2 senders = %v, want none", senders)
	}
	if got := HeartsReceived(targets[0]); got != 1 {
		t.Fatalf("received after rollover = %d, want 1", got)
	}
	sendHeartsToTargets(t, "9806", targets[:1], day2)
	if got := HeartsReceived(targets[0]); got != 2 {
		t.Fatalf("received after day 2 heart = %d, want 2", got)
	}
}

// TestHeartSendersBounded: the receiver keeps the newest ActiveRewards().Hearts.FromLimit sender aids of the day.
func TestHeartSendersBounded(t *testing.T) {
	sendersIn := heartTargetIDs(98400, ActiveRewards().Hearts.FromLimit+5)
	heartAccounts(t, append(sendersIn, "9822")...)
	now := jstAt(22, 12, 0)
	for _, from := range sendersIn {
		if sent, _ := HeartSend(from, "9822", now); !sent {
			t.Fatalf("heart from %s to 9822 refused", from)
		}
	}
	senders := HeartSenders("9822", now)
	if len(senders) != ActiveRewards().Hearts.FromLimit {
		t.Fatalf("senders = %d, want %d", len(senders), ActiveRewards().Hearts.FromLimit)
	}
	if senders[0] != sendersIn[5] || senders[len(senders)-1] != sendersIn[len(sendersIn)-1] {
		t.Fatalf("senders first %q last %q, want %s and %s", senders[0], senders[len(senders)-1], sendersIn[5], sendersIn[len(sendersIn)-1])
	}
	if got := HeartsReceived("9822"); got != int64(len(sendersIn)) {
		t.Fatalf("received = %d, want %d", got, len(sendersIn))
	}
}

// TestHeartLedgerLine: the reward writes one cash line with reason heart:<yyyymmdd>.
func TestHeartLedgerLine(t *testing.T) {
	targets := heartTargetIDs(98500, int(HeartQuestCount()))
	heartAccounts(t, append([]string{"9830"}, targets...)...)
	testutil.UseDatabase(t)
	sendHeartsToTargets(t, "9830", targets, jstAt(22, 12, 0))
	lines, err := store.LedgerLines()
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != 1 || lines[0].Aid != "9830" || lines[0].Currency != "cash" || lines[0].Delta != HeartRewardCash() || lines[0].Reason != "heart:20261022" {
		t.Fatalf("ledger = %+v, want one cash +%d heart:20261022 line", lines, HeartRewardCash())
	}
}

// TestHeartPerTargetCooldown verifies that a sender cannot re-heart the same target until
// HeartCooldown() has elapsed, while other targets and reverse sends remain open, cooldowns
// survive the 06:00 JST game-day rollover, and expired timestamps are pruned on later sends.
func TestHeartPerTargetCooldown(t *testing.T) {
	heartAccounts(t, "9880", "9881", "9882")
	cd := HeartCooldown()
	if cd != 24*time.Hour {
		t.Fatalf("default HeartCooldown = %v, want 24h", cd)
	}
	t0 := time.Date(2026, time.October, 22, 5, 50, 0, 0, jst) // 10 min before 06:00 JST rollover

	if !HeartReceivableAt("9880", "9881", t0) {
		t.Fatal("initial HeartReceivableAt false")
	}
	if sent, _ := HeartSend("9880", "9881", t0); !sent {
		t.Fatal("initial HeartSend refused")
	}
	// Immediately and 1 second before expiry: blocked, no extra count or target bump.
	for _, at := range []time.Time{t0, t0.Add(cd - time.Second)} {
		if HeartReceivableAt("9880", "9881", at) {
			t.Fatalf("HeartReceivableAt true during cooldown at +%v", at.Sub(t0))
		}
		if sent, _ := HeartSend("9880", "9881", at); sent {
			t.Fatalf("HeartSend counted during cooldown at +%v", at.Sub(t0))
		}
	}
	// Different target and reverse direction are unaffected.
	if !HeartReceivableAt("9880", "9882", t0) || !HeartReceivableAt("9881", "9880", t0) {
		t.Fatal("cooldown leaked to another target or reverse direction")
	}
	if sent, _ := HeartSend("9881", "9880", t0.Add(time.Minute)); !sent {
		t.Fatal("reverse HeartSend refused")
	}
	// Across the 06:00 JST game-day rollover (15 min after t0): still blocked!
	afterRollover := t0.Add(15 * time.Minute)
	if HeartReceivableAt("9880", "9881", afterRollover) {
		t.Fatal("game-day rollover bypassed per-target cooldown")
	}
	if sent, _ := HeartSend("9880", "9881", afterRollover); sent {
		t.Fatal("HeartSend succeeded across game-day rollover during cooldown")
	}
	// Receiving a heart on the new day must not wipe 9880's outgoing cooldown.
	if sent, _ := HeartSend("9882", "9880", afterRollover); !sent {
		t.Fatal("9882 -> 9880 refused")
	}
	if HeartReceivableAt("9880", "9881", afterRollover) {
		t.Fatal("receiving a heart wiped outgoing cooldown")
	}
	// Exact expiry: allowed again, and expired entries get pruned on send.
	atExpiry := t0.Add(cd)
	if !HeartReceivableAt("9880", "9881", atExpiry) {
		t.Fatal("HeartReceivableAt still false at exact cooldown expiry")
	}
	if sent, _ := HeartSend("9880", "9882", atExpiry); !sent {
		t.Fatal("HeartSend to 9882 at expiry refused")
	}
	if _, stale := heartAccount(t, "9880").Hearts.SentAt["9881"]; stale {
		t.Fatal("expired SentAt entry for 9881 was not pruned")
	}
	if sent, _ := HeartSend("9880", "9881", atExpiry); !sent {
		t.Fatal("HeartSend to 9881 at exact expiry refused")
	}
	if got := HeartsReceived("9881"); got != 2 {
		t.Fatalf("9881 received = %d, want 2", got)
	}
}

// TestHeartSaveFailureRollsBack: when accounts.json cannot be saved, the sender's count and the
// target's received total stay as they were.
func TestHeartSaveFailureRollsBack(t *testing.T) {
	heartAccounts(t, "9840", "9841")
	blocker := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocker, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	testutil.FailSaves(t)
	if sent, _ := HeartSend("9840", "9841", jstAt(22, 12, 0)); sent {
		t.Fatal("heart counted despite a failed save")
	}
	if got := heartAccount(t, "9840"); got.Hearts != nil || got.Gems != 0 || got.Cash != 0 {
		t.Fatalf("sender changed: hearts=%+v gems=%d cash=%d", got.Hearts, got.Gems, got.Cash)
	}
	if got := HeartsReceived("9841"); got != 0 {
		t.Fatalf("target received = %d after failed save", got)
	}
}

// TestHeartReceivableAndHaveHeart is the table for the two heart/* answers.
func TestHeartReceivableAndHaveHeart(t *testing.T) {
	heartAccounts(t, "9850", "9851")
	now := jstAt(22, 12, 0)
	for _, c := range []struct {
		name      string
		viewer    string
		target    string
		receive   bool
		haveHeart bool
	}{
		{"real target", "9850", "9851", true, true},
		{"synthetic friend", "9850", store.FriendAID, true, true},
		{"self", "9850", "9850", false, true},
		{"unknown target", "9850", "9999", false, true},
		{"unknown viewer", "9999", "9851", false, false},
	} {
		if got := HeartReceivable(c.viewer, c.target); got != c.receive {
			t.Errorf("%s: receivable = %v, want %v", c.name, got, c.receive)
		}
		if got := HaveHeart(c.viewer, now); got != c.haveHeart {
			t.Errorf("%s: haveHeart = %v, want %v", c.name, got, c.haveHeart)
		}
	}
	if left, ok := HeartsLeft("9999", now); ok || left != 0 {
		t.Fatalf("unknown viewer left = %d, %v", left, ok)
	}
}

// TestHeartResetSeconds: toExpire counts whole seconds to the next 06:00 JST game-day start.
func TestHeartResetSeconds(t *testing.T) {
	for _, c := range []struct {
		name string
		now  time.Time
		want int64
	}{
		{"noon", jstAt(22, 12, 0), 18 * 3600},
		{"before reset", time.Date(2026, time.October, 22, 5, 0, 0, 0, jst), 3600},
		{"exact reset", time.Date(2026, time.October, 22, 6, 0, 0, 0, jst), 24 * 3600},
	} {
		if got := HeartResetSeconds(c.now); got != c.want {
			t.Errorf("%s: %d, want %d", c.name, got, c.want)
		}
	}
}

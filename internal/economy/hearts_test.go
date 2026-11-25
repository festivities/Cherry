package economy

import (
	"os"
	"path/filepath"
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

func sendHearts(t *testing.T, from, to string, n int, now time.Time) {
	t.Helper()
	for i := range n {
		if sent, _ := HeartSend(from, to, now); !sent {
			t.Fatalf("heart %d from %s to %s refused", i+1, from, to)
		}
	}
}

// TestHeartSendCapsAtBase: repeats to one target count, the heart past the daily allowance is refused
// and changes nothing, and the target's received total follows the counted hearts only.
func TestHeartSendCapsAtBase(t *testing.T) {
	heartAccounts(t, "9801", "9802")
	now := jstAt(22, 12, 0)
	sendHearts(t, "9801", "9802", int(HeartBase()), now)
	if sent, _ := HeartSend("9801", "9802", now); sent {
		t.Fatal("heart past the daily allowance counted")
	}
	if left, _ := HeartsLeft("9801", now); left != 0 {
		t.Fatalf("left = %d, want 0", left)
	}
	if got := HeartsReceived("9802"); got != HeartBase() {
		t.Fatalf("received = %d, want %d", got, HeartBase())
	}
	if got := heartAccount(t, "9801").Hearts.Sent; got != HeartBase() {
		t.Fatalf("sent = %d, want %d", got, HeartBase())
	}
}

// TestHeartRewardOncePerDay: the HeartQuestCount()-th heart credits HeartRewardCash() once, and the
// hearts after it up to the daily allowance do not pay again; the next game day can earn it again.
func TestHeartRewardOncePerDay(t *testing.T) {
	heartAccounts(t, "9803", "9804")
	day1 := jstAt(22, 12, 0)
	sendHearts(t, "9803", "9804", int(HeartQuestCount()-1), day1)
	if acc := heartAccount(t, "9803"); acc.Cash != 0 || acc.Gems != 0 {
		t.Fatalf("balances before the quest heart = cash %d gems %d, want 0/0", acc.Cash, acc.Gems)
	}
	sendHearts(t, "9803", "9804", 1, day1)
	if acc := heartAccount(t, "9803"); acc.Cash != HeartRewardCash() || acc.Gems != 0 {
		t.Fatalf("balances after quest heart = cash %d gems %d, want %d/0", acc.Cash, acc.Gems, HeartRewardCash())
	}
	sendHearts(t, "9803", "9804", int(HeartBase()-HeartQuestCount()), day1)
	if acc := heartAccount(t, "9803"); acc.Cash != HeartRewardCash() || acc.Gems != 0 {
		t.Fatalf("balances after the rest of the allowance = cash %d gems %d, want %d/0", acc.Cash, acc.Gems, HeartRewardCash())
	}
	day2 := jstAt(23, 12, 0)
	sendHearts(t, "9803", "9804", int(HeartQuestCount()), day2)
	if acc := heartAccount(t, "9803"); acc.Cash != 2*HeartRewardCash() || acc.Gems != 0 {
		t.Fatalf("balances after day 2 = cash %d gems %d, want %d/0", acc.Cash, acc.Gems, 2*HeartRewardCash())
	}
}

// TestHeartExtraHearts: the quest heart pays once; the heart past the daily allowance is refused without extra
// hearts; with three extra hearts (a staff grant) the next two are sent and one is left; the left-over extra
// heart survives the game day while the daily count resets, and the last one is spent after the allowance.
func TestHeartExtraHearts(t *testing.T) {
	heartAccounts(t, "9860", "9861")
	if HeartQuestCount() >= HeartBase() {
		t.Fatal("this test needs hearts.quest below hearts.base")
	}
	day1, day2 := jstAt(22, 12, 0), jstAt(23, 12, 0)
	sendHearts(t, "9860", "9861", int(HeartBase()), day1)
	if sent, _ := HeartSend("9860", "9861", day1); sent {
		t.Fatal("heart past the allowance counted without extra hearts")
	}
	grantExtra(t, "9860", 3, day1)
	if left, _ := HeartsLeft("9860", day1); left != 3 {
		t.Fatalf("left after the grant = %d, want 3", left)
	}
	sendHearts(t, "9860", "9861", 2, day1)
	if acc := heartAccount(t, "9860"); acc.Hearts.Extra != 1 || acc.Hearts.Sent != HeartBase()+2 || acc.Cash != HeartRewardCash() {
		t.Fatalf("after the extras: hearts %+v cash %d, want extra 1, sent %d, cash %d", *acc.Hearts, acc.Cash, HeartBase()+2, HeartRewardCash())
	}
	if left, _ := HeartsLeft("9860", day2); left != HeartBase()+1 {
		t.Fatalf("day 2 left = %d, want the allowance and the extra heart (%d)", left, HeartBase()+1)
	}
	sendHearts(t, "9860", "9861", int(HeartBase())+1, day2)
	if sent, _ := HeartSend("9860", "9861", day2); sent {
		t.Fatal("heart past the extra hearts counted")
	}
	if acc := heartAccount(t, "9860"); acc.Hearts.Sent != HeartBase()+1 || acc.Hearts.Extra != 0 || acc.Cash != 2*HeartRewardCash() {
		t.Fatalf("day 2 hearts %+v cash %d, want sent %d, extra 0, cash %d", *acc.Hearts, acc.Cash, HeartBase()+1, 2*HeartRewardCash())
	}
}

// TestHeartQuestRules: hearts.quest is from 1 to hearts.base, and an override that leaves it out means hearts.base, so
// an operator file written before the field existed loads and keeps its meaning.
func TestHeartQuestRules(t *testing.T) {
	t.Cleanup(resetOperatorData)
	raw := data.RewardsJSON
	refuseFile(t, rewardsFile.LoadFrom, "rewards.json", mutateDefault(t, raw, `"quest": 20`, `"quest": 31`), "hearts.quest must be from 1 to hearts.base")
	refuseFile(t, rewardsFile.LoadFrom, "rewards.json", mutateDefault(t, raw, `"quest": 20`, `"quest": -1`), "hearts.quest must be from 1 to hearts.base")
	path := filepath.Join(t.TempDir(), "rewards.json")
	if err := os.WriteFile(path, []byte(mutateDefault(t, raw, `"quest": 20, `, ``)), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := rewardsFile.LoadFrom(path); err != nil || HeartQuestCount() != HeartBase() {
		t.Fatalf("override without hearts.quest: err %v, quest %d, base %d", err, HeartQuestCount(), HeartBase())
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
	heartAccounts(t, "9806", "9807")
	day1, day2 := jstAt(22, 12, 0), jstAt(23, 7, 0)
	sendHearts(t, "9806", "9807", int(HeartBase()), day1)
	if senders := HeartSenders("9807", day1); int64(len(senders)) != HeartBase() {
		t.Fatalf("day 1 senders = %d, want %d", len(senders), HeartBase())
	}
	if !HaveHeart("9806", day2) {
		t.Fatal("haveHeart false on the new day")
	}
	if HaveHeart("9806", day1) {
		t.Fatal("haveHeart true at the cap")
	}
	if senders := HeartSenders("9807", day2); len(senders) != 0 {
		t.Fatalf("day 2 senders = %v, want none", senders)
	}
	if got := HeartsReceived("9807"); got != HeartBase() {
		t.Fatalf("received after rollover = %d, want %d", got, HeartBase())
	}
	sendHearts(t, "9806", "9807", 1, day2)
	if got := HeartsReceived("9807"); got != HeartBase()+1 {
		t.Fatalf("received after day 2 heart = %d, want %d", got, HeartBase()+1)
	}
}

// TestHeartSendersBounded: the receiver keeps the newest ActiveRewards().Hearts.FromLimit sender aids of the day.
func TestHeartSendersBounded(t *testing.T) {
	aids := []string{"9810", "9811", "9812", "9813", "9814", "9815", "9816", "9817", "9818", "9819", "9820", "9821"}
	heartAccounts(t, append(aids, "9822")...)
	now := jstAt(22, 12, 0)
	for _, from := range aids[:11] { // 11 senders x 5 hearts = 55 counted, keep the newest 50
		sendHearts(t, from, "9822", 5, now)
	}
	senders := HeartSenders("9822", now)
	if len(senders) != ActiveRewards().Hearts.FromLimit {
		t.Fatalf("senders = %d, want %d", len(senders), ActiveRewards().Hearts.FromLimit)
	}
	if senders[0] != "9811" || senders[len(senders)-1] != "9820" {
		t.Fatalf("senders first %q last %q, want 9811 and 9820", senders[0], senders[len(senders)-1])
	}
	if got := HeartsReceived("9822"); got != 55 {
		t.Fatalf("received = %d, want 55", got)
	}
}

// TestHeartLedgerLine: the reward writes one cash line with reason heart:<yyyymmdd>.
func TestHeartLedgerLine(t *testing.T) {
	heartAccounts(t, "9830", "9831")
	testutil.UseDatabase(t)
	sendHearts(t, "9830", "9831", int(HeartQuestCount()), jstAt(22, 12, 0))
	lines, err := store.LedgerLines()
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != 1 || lines[0].Aid != "9830" || lines[0].Currency != "cash" || lines[0].Delta != HeartRewardCash() || lines[0].Reason != "heart:20261022" {
		t.Fatalf("ledger = %+v, want one cash +%d heart:20261022 line", lines, HeartRewardCash())
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

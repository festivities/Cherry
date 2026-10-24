package economy

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

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
		if !HeartSend(from, to, now) {
			t.Fatalf("heart %d from %s to %s refused", i+1, from, to)
		}
	}
}

// TestHeartSendCapsAtBase: repeats to one target count, the sixth heart of the day is refused
// and changes nothing, and the target's received total follows the counted hearts only.
func TestHeartSendCapsAtBase(t *testing.T) {
	heartAccounts(t, "9801", "9802")
	now := jstAt(22, 12, 0)
	sendHearts(t, "9801", "9802", int(HeartBase()), now)
	if HeartSend("9801", "9802", now) {
		t.Fatal("sixth heart counted")
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

// TestHeartRewardOncePerDay: the HeartBase()-th heart credits HeartRewardGems() once; the next game
// day can earn it again.
func TestHeartRewardOncePerDay(t *testing.T) {
	heartAccounts(t, "9803", "9804")
	day1 := jstAt(22, 12, 0)
	sendHearts(t, "9803", "9804", int(HeartBase()-1), day1)
	if got := heartAccount(t, "9803").Gems; got != 0 {
		t.Fatalf("gems before the base heart = %d, want 0", got)
	}
	sendHearts(t, "9803", "9804", 1, day1)
	if got := heartAccount(t, "9803").Gems; got != HeartRewardGems() {
		t.Fatalf("gems after base heart = %d, want %d", got, HeartRewardGems())
	}
	HeartSend("9803", "9804", day1) // refused: already at base, no second reward
	if got := heartAccount(t, "9803").Gems; got != HeartRewardGems() {
		t.Fatalf("gems after refused heart = %d, want %d", got, HeartRewardGems())
	}
	day2 := jstAt(23, 12, 0)
	sendHearts(t, "9803", "9804", int(HeartBase()), day2)
	if got := heartAccount(t, "9803").Gems; got != 2*HeartRewardGems() {
		t.Fatalf("gems after day 2 = %d, want %d", got, 2*HeartRewardGems())
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
		if HeartSend(c.from, c.to, now) {
			t.Errorf("heart %s -> %s counted", c.from, c.to)
		}
	}
	if !HeartSend("9805", store.FriendAID, now) {
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
		sendHearts(t, from, "9822", int(HeartBase()), now)
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

// TestHeartLedgerLine: the reward writes one gems line with reason heart:<yyyymmdd>.
func TestHeartLedgerLine(t *testing.T) {
	dir := t.TempDir()
	heartAccounts(t, "9830", "9831")
	store.AccountsMu.Lock()
	store.AccountStorePath = filepath.Join(dir, "accounts.json")
	store.AccountsMu.Unlock()
	sendHearts(t, "9830", "9831", int(HeartBase()), jstAt(22, 12, 0))
	f, err := os.Open(filepath.Join(dir, "ledger.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var lines []store.LedgerLine
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var l store.LedgerLine
		if err := json.Unmarshal(sc.Bytes(), &l); err != nil {
			t.Fatal(err)
		}
		lines = append(lines, l)
	}
	if len(lines) != 1 || lines[0].Aid != "9830" || lines[0].Currency != "gems" || lines[0].Delta != HeartRewardGems() || lines[0].Reason != "heart:20261022" {
		t.Fatalf("ledger = %+v, want one gems +%d heart:20261022 line", lines, HeartRewardGems())
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
	store.AccountsMu.Lock()
	store.AccountStorePath = filepath.Join(blocker, "accounts.json")
	store.AccountsMu.Unlock()
	if HeartSend("9840", "9841", jstAt(22, 12, 0)) {
		t.Fatal("heart counted despite a failed save")
	}
	if got := heartAccount(t, "9840"); got.Hearts != nil || got.Gems != 0 {
		t.Fatalf("sender changed: hearts=%+v gems=%d", got.Hearts, got.Gems)
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

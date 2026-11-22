package httpx

import (
	"testing"
	"time"
)

// TestBucketSetRaiseLowerAndOff: Set changes a live bucket. A lowered burst cuts the tokens a key holds, a raised one lets the
// refill go higher, and a zero rate turns the limit off and forgets the keys, so turning it back on starts every key full.
func TestBucketSetRaiseLowerAndOff(t *testing.T) {
	now := time.Unix(1000, 0)
	b := NewBucket(0.5, 5)
	b.Allow("c", now) // 4 tokens left

	b.Set(0.5, 2) // the burst drops to 2: the 4 tokens are cut to 2
	if !b.Allow("c", now) || !b.Allow("c", now) {
		t.Fatal("two hits under the lowered burst refused")
	}
	if b.Allow("c", now) {
		t.Fatal("third hit allowed after the burst was lowered to 2")
	}

	b.Set(0.5, 10) // the burst rises: 20 seconds at 0.5 a second refill 10 tokens
	later := now.Add(20 * time.Second)
	for i := range 10 {
		if !b.Allow("c", later) {
			t.Fatalf("hit %d after the burst was raised to 10 refused", i+1)
		}
	}
	if b.Allow("c", later) {
		t.Fatal("11th hit allowed with a burst of 10")
	}

	b.Set(100, 10) // a faster rate: 10 ms is one token
	if !b.Allow("c", later.Add(10*time.Millisecond)) {
		t.Fatal("one token at 100 a second refused after 10 ms")
	}

	b.Set(0, 0) // off
	if b.On() {
		t.Fatal("On() true after the rate was set to zero")
	}
	for range 100 {
		if !b.Allow("c", later) {
			t.Fatal("a hit refused with the limit off")
		}
	}

	b.Set(1, 3) // on again: every key starts full at the new burst
	if !b.On() {
		t.Fatal("On() false after the rate was set back above zero")
	}
	for i := range 3 {
		if !b.Allow("d", later) {
			t.Fatalf("hit %d of the new burst refused", i+1)
		}
	}
	if b.Allow("d", later) {
		t.Fatal("4th hit allowed with a burst of 3")
	}
}

// TestConnLimitsSetCapsAndCounts: Set changes the caps of a live limiter, Caps reads them back and Counts reports the open
// connections and their addresses; connections already open keep counting after a lower cap.
func TestConnLimitsSetCapsAndCounts(t *testing.T) {
	l := NewConnLimits(10, 10)
	for _, r := range []string{"10.0.0.1:1", "10.0.0.1:2", "10.0.0.2:1"} {
		if !l.Admit(r) {
			t.Fatalf("%s refused", r)
		}
	}
	if open, hosts := l.Counts(); open != 3 || hosts != 2 {
		t.Fatalf("Counts = %d open, %d hosts; want 3, 2", open, hosts)
	}
	l.Set(1, 3) // the open three stay open; the total cap of 3 refuses the next
	if perHost, total := l.Caps(); perHost != 1 || total != 3 {
		t.Fatalf("Caps = %d, %d; want 1, 3", perHost, total)
	}
	if l.Admit("10.0.0.3:1") {
		t.Fatal("a connection admitted at the total cap")
	}
	if open, _ := l.Counts(); open != 3 {
		t.Fatalf("open after a refused admit = %d, want 3", open)
	}
	l.Release("10.0.0.2:1")
	if l.Admit("10.0.0.1:3") {
		t.Fatal("a second connection from one address admitted under a per-address cap of 1")
	}
	if !l.Admit("10.0.0.3:1") {
		t.Fatal("a connection from a new address refused after a release")
	}
}

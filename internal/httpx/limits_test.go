package httpx

import (
	"net"
	"sync/atomic"
	"testing"
	"time"
)

func TestBucketBurstThenRefill(t *testing.T) {
	b := NewBucket(2, 3) // 2 tokens a second, room for 3
	now := time.Unix(1000, 0)
	for i := range 3 {
		if !b.Allow("a", now) {
			t.Fatalf("hit %d of the burst refused", i+1)
		}
	}
	if b.Allow("a", now) {
		t.Fatal("4th hit in the same instant allowed; the burst is 3")
	}
	if !b.Allow("b", now) {
		t.Fatal("another key shares no tokens with a")
	}
	if b.Allow("a", now.Add(400*time.Millisecond)) {
		t.Fatal("0.8 token refilled, still below one")
	}
	if !b.Allow("a", now.Add(500*time.Millisecond)) {
		t.Fatal("one token refilled after 0.5 s at 2/s, refused")
	}
	if b.Allow("a", now.Add(-time.Hour)) {
		t.Fatal("a clock that goes back must not refill the bucket")
	}
}

func TestBucketOffAtZeroRate(t *testing.T) {
	b := NewBucket(0, 0)
	for range 1000 {
		if !b.Allow("a", time.Unix(1, 0)) {
			t.Fatal("a zero rate must not limit")
		}
	}
	var none *Bucket
	if !none.Allow("a", time.Unix(1, 0)) {
		t.Fatal("a nil bucket must not limit")
	}
	if NewBucket(0, 0).On() || none.On() || !NewBucket(1, 1).On() {
		t.Fatal("On() must be false for a zero rate and a nil bucket, true for a rate")
	}
}

func TestConnLimitsPerHostAndTotal(t *testing.T) {
	l := NewConnLimits(2, 3)
	for _, r := range []string{"10.0.0.1:5000", "10.0.0.1:5001"} {
		if !l.Admit(r) {
			t.Fatalf("%s refused under the per-host cap", r)
		}
	}
	if l.Admit("10.0.0.1:5002") {
		t.Fatal("third connection from one host admitted; the cap is 2")
	}
	if !l.Admit("10.0.0.2:5000") {
		t.Fatal("another host refused under the total cap")
	}
	if l.Admit("10.0.0.3:5000") {
		t.Fatal("fourth connection in all admitted; the total cap is 3")
	}
	l.Release("10.0.0.1:5000")
	if !l.Admit("10.0.0.3:5000") {
		t.Fatal("a released slot was not reused")
	}
}

func TestConnLimitsListenerClosesRefused(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	limits := NewConnLimits(1, 0)
	var refused atomic.Int32 // written by the accept goroutine
	wrapped := limits.Listener(ln, func(string) { refused.Add(1) })
	accepted := make(chan net.Conn, 2)
	go func() {
		for range 2 {
			c, err := wrapped.Accept()
			if err != nil {
				return
			}
			accepted <- c
		}
	}()
	var dialed []net.Conn
	for range 2 {
		c, err := net.Dial("tcp", ln.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		dialed = append(dialed, c)
	}
	defer func() {
		for _, c := range dialed {
			c.Close()
		}
	}()
	first := <-accepted // the first connection is admitted and returned
	// The second is refused by the per-host cap of 1: Accept skips it and waits, so only one connection arrives.
	select {
	case <-accepted:
		t.Fatal("second connection from the same host was accepted")
	case <-time.After(200 * time.Millisecond):
	}
	if n := refused.Load(); n != 1 {
		t.Fatalf("refusals = %d, want 1", n)
	}
	first.Close()
	if !limits.Admit("127.0.0.1:1") {
		t.Fatal("closing the admitted connection did not release its slot")
	}
}

func TestTruncateAddr(t *testing.T) {
	for in, want := range map[string]string{
		"203.0.113.77":         "203.0.113.0/24",
		"::ffff:203.0.113.77":  "203.0.113.0/24",
		"2001:db8:1:2:3:4:5:6": "2001:db8:1::/48",
		"not an address":       "",
		"":                     "",
	} {
		if got := TruncateAddr(in); got != want {
			t.Errorf("TruncateAddr(%q) = %q, want %q", in, got, want)
		}
	}
}

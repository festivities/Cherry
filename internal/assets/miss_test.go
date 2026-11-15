package assets

import (
	"strconv"
	"testing"
	"time"
)

// PLAN 8.6b part 5: the memory of R2's 404s is bounded, so a flood of distinct missing paths cannot grow the process; a
// remembered miss is seen until it expires, and expired entries make room first.
func TestMissMemoryIsBounded(t *testing.T) {
	s := &Store{}
	for i := range maxMissing + 100 {
		s.rememberMiss("static/k" + strconv.Itoa(i))
	}
	if n := len(s.missing); n != maxMissing {
		t.Fatalf("%d misses remembered, want the cap %d", n, maxMissing)
	}
	if !s.missed("static/k0") {
		t.Fatal("a remembered miss is not seen")
	}
	s.missMu.Lock()
	for k := range s.missing {
		s.missing[k] = time.Now().Add(-time.Second)
	}
	s.missMu.Unlock()
	s.rememberMiss("static/new")
	if n := len(s.missing); n != 1 || !s.missed("static/new") || s.missed("static/k0") {
		t.Fatalf("after every entry expired: %d entries; the new miss seen = %v", n, s.missed("static/new"))
	}
}

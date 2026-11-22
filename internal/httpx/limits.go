package httpx

import (
	"net"
	"net/netip"
	"strings"
	"sync"
	"time"
)

// maxBucketKeys bounds the keys one Bucket tracks; a new key over the cap is refused until idle keys are swept.
const maxBucketKeys = 65536

// Bucket is a token bucket per key: a key earns rate tokens a second up to burst, and each allowed hit spends one. A new
// key starts full. A rate of zero or less lets everything through (the limit is off).
type Bucket struct {
	mu    sync.Mutex
	rate  float64
	burst float64
	keys  map[string]*bucketState
}

type bucketState struct {
	tokens float64
	last   time.Time
}

// NewBucket returns a bucket that refills rate tokens a second to burst.
func NewBucket(rate float64, burst int) *Bucket {
	return &Bucket{rate: rate, burst: float64(burst), keys: map[string]*bucketState{}}
}

// On reports whether the bucket limits anything (a nil bucket and a zero rate do not).
func (b *Bucket) On() bool {
	if b == nil {
		return false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.rate > 0
}

// Set changes the rate and burst while the bucket serves requests. Tokens a key holds above the new burst are cut to it;
// the others keep theirs. A rate of zero turns the limit off and forgets the keys, so turning it back on starts full.
func (b *Bucket) Set(rate float64, burst int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.rate, b.burst = rate, float64(burst)
	if rate <= 0 {
		b.keys = map[string]*bucketState{}
		return
	}
	for _, st := range b.keys {
		st.tokens = min(st.tokens, b.burst)
	}
}

// Allow spends one token of key's bucket at now and reports whether one was there.
func (b *Bucket) Allow(key string, now time.Time) bool {
	if b == nil || b.rate <= 0 {
		return true
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	st := b.keys[key]
	if st == nil {
		if len(b.keys) >= maxBucketKeys {
			b.sweepLocked(now)
			if len(b.keys) >= maxBucketKeys {
				return false
			}
		}
		st = &bucketState{tokens: b.burst, last: now}
		b.keys[key] = st
	}
	st.tokens = min(b.burst, st.tokens+max(0, now.Sub(st.last).Seconds())*b.rate)
	st.last = now
	if st.tokens < 1 {
		return false
	}
	st.tokens--
	return true
}

// sweepLocked forgets the keys whose bucket is full again: a new bucket is full too, so no answer changes.
func (b *Bucket) sweepLocked(now time.Time) {
	for key, st := range b.keys {
		if st.tokens+max(0, now.Sub(st.last).Seconds())*b.rate >= b.burst {
			delete(b.keys, key)
		}
	}
}

// ConnLimits caps the open connections of the game listeners: at most perHost from one remote host and total in all.
// A connection over a cap is closed at accept, before it is read. A cap of zero or less is off.
type ConnLimits struct {
	mu      sync.Mutex
	perHost int
	total   int
	open    int
	byHost  map[string]int
}

// Conns is the process's connection limit, shared by every game listener (HTTPS, plain HTTP, session and gateway).
// cmd/cherry sets it from configuration before it listens.
var Conns = NewConnLimits(1024, 4096)

// NewConnLimits returns limits with the given caps.
func NewConnLimits(perHost, total int) *ConnLimits {
	return &ConnLimits{perHost: perHost, total: total, byHost: map[string]int{}}
}

// Set replaces the caps. Connections already open keep their count.
func (l *ConnLimits) Set(perHost, total int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.perHost, l.total = perHost, total
}

// Caps returns the caps in effect.
func (l *ConnLimits) Caps() (perHost, total int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.perHost, l.total
}

// Counts returns the connections open now and the addresses holding them.
func (l *ConnLimits) Counts() (open, hosts int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.open, len(l.byHost)
}

// Admit counts one more open connection from remote's host and reports false, counting nothing, at a cap. Every true
// result is paired with Release(remote).
func (l *ConnLimits) Admit(remote string) bool {
	host := RemoteHost(remote)
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.total > 0 && l.open >= l.total || l.perHost > 0 && l.byHost[host] >= l.perHost {
		return false
	}
	l.open++
	l.byHost[host]++
	return true
}

// Release undoes one Admit for remote.
func (l *ConnLimits) Release(remote string) {
	host := RemoteHost(remote)
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.open > 0 {
		l.open--
	}
	if l.byHost[host] <= 1 {
		delete(l.byHost, host)
		return
	}
	l.byHost[host]--
}

// Listener wraps ln so every accepted connection is admitted first. A refused connection is closed and skipped; an
// admitted one releases its count when it is closed. refused is called with the remote address of each refusal (may be
// nil).
func (l *ConnLimits) Listener(ln net.Listener, refused func(remote string)) net.Listener {
	return &limitedListener{Listener: ln, limits: l, refused: refused}
}

type limitedListener struct {
	net.Listener
	limits  *ConnLimits
	refused func(remote string)
}

func (ln *limitedListener) Accept() (net.Conn, error) {
	for {
		c, err := ln.Listener.Accept()
		if err != nil {
			return nil, err
		}
		remote := c.RemoteAddr().String()
		if !ln.limits.Admit(remote) {
			if ln.refused != nil {
				ln.refused(remote)
			}
			_ = c.Close()
			continue
		}
		return &admittedConn{Conn: c, limits: ln.limits, remote: remote}, nil
	}
}

type admittedConn struct {
	net.Conn
	limits *ConnLimits
	remote string
	once   sync.Once
}

func (c *admittedConn) Close() error {
	c.once.Do(func() { c.limits.Release(c.remote) })
	return c.Conn.Close()
}

// RemoteHost is the host part of a remote address ("ip:port"); the whole string when it has no port.
func RemoteHost(remote string) string {
	if host, _, err := net.SplitHostPort(remote); err == nil {
		return host
	}
	return remote
}

// TruncateAddr keeps the network part of an address for the records that outlive a session: the /24 of an IPv4 address
// and the /48 of an IPv6 one ("203.0.113.0/24"). A value that is not an address gives "".
func TruncateAddr(ip string) string {
	addr, err := netip.ParseAddr(strings.TrimSpace(ip))
	if err != nil {
		return ""
	}
	bits := 48
	if addr = addr.Unmap(); addr.Is4() {
		bits = 24
	}
	return netip.PrefixFrom(addr, bits).Masked().String()
}

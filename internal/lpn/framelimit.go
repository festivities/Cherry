package lpn

import (
	"log"
	"time"

	"cherry/internal/httpx"
)

// Frame and relay limits (PLAN 8.6b part 5). A gateway (:10000) or session (:10123) connection earns limits.frame-rate
// frames a second up to limits.frame-burst. A frame over that is dropped before it is dispatched; a connection that keeps
// sending ten times the rate for long enough is closed. The lab's zero rate turns both off.
//
// Sizing (lab request logs, 2026-11-16, one connection per row): the busiest gateway connection peaked at 15 frames in a
// second, 59 in ten seconds and 230 in a minute (a Room Party loop of 2026-10-06, about 4 frames a second for a minute).
// 100 a second with a burst of 400 is 6 times the one-second peak. The session chat bursts of 2026-11-11 (85 to 250 frames
// a second, consecutive transaction numbers, the same 3003 range request) are throttled to the rate: that loop is a bug to
// find, not traffic to carry.
const frameLogEvery = 10 * time.Second

// maxRelayPayload bounds the opaque payload one player's move or action sends to the others (Room Party msgids 8 and 10,
// Square 6 and 9, Circle 5 and 7). The largest such frames in the lab logs (2026-11-16) are 30 bytes for a move and 19 for an
// action, whole frame; 4 KiB is over 130 times the move.
const maxRelayPayload = 4 << 10

// frameMeter is one connection's frame budget. Its reader goroutine is the only caller. The soft bucket drops frames over
// the rate; the hard bucket, at ten times the rate, counts every frame and closes a connection that stays over it.
type frameMeter struct {
	soft, hard *httpx.Bucket
	label      string // "GATEWAY" or "SESSION", for the log
	remote     string
	logger     *log.Logger
	dropped    int
	logged     time.Time
}

// newFrameMeter returns the meter of one connection, or nil when rate is 0 (the limit is off).
func newFrameMeter(label, remote string, logger *log.Logger, rate, burst int) *frameMeter {
	if rate <= 0 {
		return nil
	}
	return &frameMeter{
		soft:  httpx.NewBucket(float64(rate), burst),
		hard:  httpx.NewBucket(float64(10*rate), 10*burst),
		label: label, remote: remote, logger: logger,
	}
}

// admit reports whether the frame just read is dispatched. A dropped frame is logged once per frameLogEvery with the count
// since the last line; closeConn is true when the connection must end. A nil meter admits everything.
func (m *frameMeter) admit(now time.Time) (dispatch, closeConn bool) {
	if m == nil {
		return true, false
	}
	if !m.hard.Allow("", now) {
		m.logger.Printf("%s frame-flood remote=%s: over ten times limits.frame-rate, connection closed", m.label, m.remote)
		return false, true
	}
	if m.soft.Allow("", now) {
		return true, false
	}
	m.dropped++
	if now.Sub(m.logged) >= frameLogEvery {
		m.logger.Printf("%s frame-limit remote=%s dropped=%d (over limits.frame-rate)", m.label, m.remote, m.dropped)
		m.dropped, m.logged = 0, now
	}
	return false, false
}

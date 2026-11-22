package lpn

import (
	"bytes"
	"log"
	"net"
	"strings"
	"testing"
	"time"

	"cherry/internal/config"
	"cherry/internal/metrics"
)

// PLAN 8.6b part 5: the frame budget of a gateway or session connection. The meter is tested on its own (fixed clock), then
// through a real session and gateway connection with a rate of 1 and a burst of 3: the first three pings are echoed, the
// rest dropped, and the connection stays open.

func TestFrameMeterDropsOverRateAndClosesAFlood(t *testing.T) {
	var logs bytes.Buffer
	m := newFrameMeter("TEST", "192.0.2.1:1", log.New(&logs, "", 0), 10, 20) // soft 10 a second, burst 20; hard 100, 200
	now := time.Unix(1_700_000_000, 0)
	droppedBefore := metrics.FramesDropped.Load()
	for i := range 20 {
		if d, c := m.admit(now); !d || c {
			t.Fatalf("frame %d of the burst: dispatch=%v close=%v", i+1, d, c)
		}
	}
	if d, c := m.admit(now); d || c {
		t.Fatalf("frame over the burst: dispatch=%v close=%v, want dropped and open", d, c)
	}
	if d, c := m.admit(now); d || c {
		t.Fatalf("second frame over the burst: dispatch=%v close=%v", d, c)
	}
	if got := metrics.FramesDropped.Load() - droppedBefore; got != 2 {
		t.Fatalf("two dropped frames moved cherry.frames_dropped by %d", got)
	}
	if n := strings.Count(logs.String(), "frame-limit"); n != 1 {
		t.Fatalf("drops in one window logged %d times, want once:\n%s", n, logs.String())
	}
	if d, _ := m.admit(now.Add(time.Second)); !d {
		t.Fatal("the bucket did not refill: a second later the frame was dropped")
	}

	flood := newFrameMeter("TEST", "192.0.2.2:1", log.New(&logs, "", 0), 10, 20)
	closedAt := 0
	for i := 1; i <= 1000 && closedAt == 0; i++ {
		if _, c := flood.admit(now); c {
			closedAt = i
		}
	}
	if closedAt != 201 {
		t.Fatalf("a flood at one instant closed the connection at frame %d, want 201 (ten times the burst of 20, plus one)", closedAt)
	}
	if !strings.Contains(logs.String(), "frame-flood") {
		t.Fatal("the closed flood is not logged")
	}
}

func TestFrameMeterOffInLab(t *testing.T) {
	if m := newFrameMeter("TEST", "r", log.New(&bytes.Buffer{}, "", 0), 0, 400); m != nil {
		t.Fatal("a zero rate made a meter")
	}
	var m *frameMeter
	if d, c := m.admit(time.Now()); !d || c {
		t.Fatalf("a nil meter: dispatch=%v close=%v, want admitted", d, c)
	}
}

func TestSessionFramesOverTheLimitAreDropped(t *testing.T) {
	config.Activate(config.Config{FrameRate: 1, FrameBurst: 3})
	t.Cleanup(func() { config.Activate(config.Config{}) })
	conn, cleanup := startSessionServer(t)
	defer cleanup()
	readFullDeadline(t, conn, len(sessionBanner))
	if _, err := conn.Write(sessionHello); err != nil {
		t.Fatalf("write hello: %v", err)
	}
	// The session socket gets 4x the configured allowance (burst 3 -> 12; transport.go, the 1:1 chat bursts).
	for range 15 {
		writeSessionFrame(t, conn, []byte{'M', 'Q'})
	}
	echo := []byte{0, 0, 0, 2, 'M', 'Q'}
	for i := range 12 {
		if got := readFullDeadline(t, conn, len(echo)); !bytes.Equal(got, echo) {
			t.Fatalf("echo %d = %x", i+1, got)
		}
	}
	assertNoReply(t, conn) // the other three pings were dropped (the bucket is empty for the next quarter second)
}

func TestGatewayFramesOverTheLimitAreDropped(t *testing.T) {
	config.Activate(config.Config{FrameRate: 1, FrameBurst: 3})
	t.Cleanup(func() { config.Activate(config.Config{}) })
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		observeGatewayFrames(&lockedConn{Conn: c}, GatewayAddr, c.RemoteAddr().String(), discardLogger())
	}()
	conn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	for range 6 {
		writeSessionFrame(t, conn, []byte{'M', 'Q'})
	}
	echo := []byte{0, 0, 0, 2, 'M', 'Q'}
	for i := range 3 {
		if got := readFullDeadline(t, conn, len(echo)); !bytes.Equal(got, echo) {
			t.Fatalf("gateway echo %d = %x", i+1, got)
		}
	}
	assertNoReply(t, conn)
}

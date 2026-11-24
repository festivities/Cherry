package lpn

import (
	"encoding/json"
	"net"
	"testing"
	"time"

	"cherry/internal/economy"
)

func addPipeSession(t *testing.T, aid uint64) net.Conn {
	t.Helper()
	server, client := net.Pipe()
	lc := &lockedConn{Conn: server, listener: "session", since: time.Now()}
	bindSession(aid, lc)
	t.Cleanup(func() {
		unbindSession(lc)
		_ = server.Close()
		_ = client.Close()
	})
	return client
}

func TestBroadcastBannerAndMaintPopup(t *testing.T) {
	now := time.UnixMilli(1732450000123)
	old := economy.NowFunc
	economy.NowFunc = func() time.Time { return now }
	t.Cleanup(func() { economy.NowFunc = old })

	if got := SessionCount(); got != 0 {
		t.Fatalf("initial SessionCount() = %d, want 0", got)
	}

	c1 := addPipeSession(t, 9801)
	c2 := addPipeSession(t, 9802)

	if got := SessionCount(); got != 2 {
		t.Fatalf("SessionCount() = %d, want 2", got)
	}

	// Empty / invalid inputs are no-ops and write nothing.
	if sent, failed := BroadcastBanner("   \t\n "); sent != 0 || failed != 0 {
		t.Fatalf("empty BroadcastBanner = (%d, %d), want (0, 0)", sent, failed)
	}
	if sent, failed := BroadcastMaintPopup("", 5*time.Minute); sent != 0 || failed != 0 {
		t.Fatalf("empty BroadcastMaintPopup = (%d, %d), want (0, 0)", sent, failed)
	}
	if sent, failed := BroadcastMaintPopup("msg", 0); sent != 0 || failed != 0 {
		t.Fatalf("zero-duration BroadcastMaintPopup = (%d, %d), want (0, 0)", sent, failed)
	}
	expectNoSessionPush(t, c1)
	expectNoSessionPush(t, c2)

	// Banner broadcast to both pipes concurrently (net.Pipe is unbuffered).
	type counts struct{ sent, failed int }
	done := make(chan counts, 1)
	go func() {
		s, f := BroadcastBanner("Server restarting soon!")
		done <- counts{s, f}
	}()

	for _, c := range []net.Conn{c1, c2} {
		op, txn, payload := readLOPush(t, c)
		if op != loOpPush || txn != 0 {
			t.Fatalf("banner op=%#02x txn=%d, want 0x1b txn 0", op, txn)
		}
		assertPushNotiJSON(t, payload, "notice", "1732450000123", "0", "Server restarting soon!", "PBO", 0, nil)
	}
	if res := <-done; res.sent != 2 || res.failed != 0 {
		t.Fatalf("BroadcastBanner = (%d, %d), want (2, 0)", res.sent, res.failed)
	}

	// Maintenance popup broadcast to both pipes.
	dur := 10 * time.Minute
	done = make(chan counts, 1)
	go func() {
		s, f := BroadcastMaintPopup("Scheduled maintenance", dur)
		done <- counts{s, f}
	}()

	wantStart := "1732449940123"
	wantEnd := "1732450600123"
	for _, c := range []net.Conn{c1, c2} {
		op, txn, payload := readLOPush(t, c)
		if op != loOpPush || txn != 0 {
			t.Fatalf("maint op=%#02x txn=%d, want 0x1b txn 0", op, txn)
		}
		var got map[string]any
		if err := json.Unmarshal(payload, &got); err != nil {
			t.Fatalf("maint payload not JSON: %v (%s)", err, payload)
		}
		if got["catg"] != "maint" {
			t.Fatalf("catg = %#v, want maint", got["catg"])
		}
		if got["msgid"] != "1732450000123" {
			t.Fatalf("msgid = %#v, want 1732450000123", got["msgid"])
		}
		params, ok := got["params"].(map[string]any)
		if !ok {
			t.Fatalf("params = %#v, want object", got["params"])
		}
		if seq, ok := params["seq"].(float64); !ok || int64(seq) != now.Unix() {
			t.Fatalf("params.seq = %#v, want %d", params["seq"], now.Unix())
		}
		if params["target"] != "ALL" {
			t.Fatalf("params.target = %#v, want ALL", params["target"])
		}
		if params["type"] != "MAINT" {
			t.Fatalf("params.type = %#v, want MAINT", params["type"])
		}
		if params["displayType"] != "POPUP" {
			t.Fatalf("params.displayType = %#v, want POPUP", params["displayType"])
		}
		if params["start"] != wantStart {
			t.Fatalf("params.start = %#v, want %q", params["start"], wantStart)
		}
		if params["end"] != wantEnd {
			t.Fatalf("params.end = %#v, want %q", params["end"], wantEnd)
		}
		msgs, ok := params["message"].(map[string]any)
		if !ok {
			t.Fatalf("params.message = %#v, want object", params["message"])
		}
		for _, lang := range []string{"en", "ko", "ja"} {
			if msgs[lang] != "Scheduled maintenance" {
				t.Fatalf("params.message.%s = %#v, want %q", lang, msgs[lang], "Scheduled maintenance")
			}
		}
	}
	if res := <-done; res.sent != 2 || res.failed != 0 {
		t.Fatalf("BroadcastMaintPopup = (%d, %d), want (2, 0)", res.sent, res.failed)
	}

	// Closing one pipe reports sent=1, failed=1.
	_ = c2.Close()
	done = make(chan counts, 1)
	go func() {
		s, f := BroadcastBanner("After close")
		done <- counts{s, f}
	}()
	op, txn, _ := readLOPush(t, c1)
	if op != loOpPush || txn != 0 {
		t.Fatalf("after-close op=%#02x txn=%d", op, txn)
	}
	if res := <-done; res.sent != 1 || res.failed != 1 {
		t.Fatalf("after-close BroadcastBanner = (%d, %d), want (1, 1)", res.sent, res.failed)
	}
}

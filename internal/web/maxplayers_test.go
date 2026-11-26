package web

import (
	"encoding/binary"
	"encoding/json"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"cherry/internal/config"
	"cherry/internal/lpn"
	"cherry/internal/store"
	"cherry/internal/testutil"
)

func bindLPNSession(t *testing.T, addr, sessionKey string) net.Conn {
	t.Helper()
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial session: %v", err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	banner := make([]byte, 15)
	if _, err := io.ReadFull(conn, banner); err != nil {
		_ = conn.Close()
		t.Fatalf("read banner: %v", err)
	}
	hello := []byte{0x00, 0x00, 0x00, 0x04, 0x00, 0x00, 0x01, 0x0A, 0x35, 0xED, 0x6A}
	if _, err := conn.Write(hello); err != nil {
		_ = conn.Close()
		t.Fatalf("write hello: %v", err)
	}
	body, _ := json.Marshal(struct {
		SessionKey string `json:"sessionKey"`
	}{SessionKey: sessionKey})
	loHeader := make([]byte, 16)
	copy(loHeader[0:2], "LO")
	loHeader[2] = 0x10
	loHeader[3] = 0x00
	binary.BigEndian.PutUint32(loHeader[4:8], uint32(len(body)))
	binary.BigEndian.PutUint64(loHeader[8:16], 1)
	payload := append(loHeader, body...)
	frame := binary.BigEndian.AppendUint32(nil, uint32(len(payload)))
	frame = append(frame, payload...)
	if _, err := conn.Write(frame); err != nil {
		_ = conn.Close()
		t.Fatalf("write sendInit: %v", err)
	}
	prefix := make([]byte, 4)
	if _, err := io.ReadFull(conn, prefix); err != nil {
		_ = conn.Close()
		t.Fatalf("read configure prefix: %v", err)
	}
	reply := make([]byte, binary.BigEndian.Uint32(prefix))
	if _, err := io.ReadFull(conn, reply); err != nil {
		_ = conn.Close()
		t.Fatalf("read configure body: %v", err)
	}
	return conn
}

// A full server refuses at app start: setInitConf 503, then the refused address (and only it, once) gets the maintenance
// notice with the server-full text on the plain port; checkSession never refuses (its error deletes the client's login).
func TestServerFullRefusesAtAppStartWithNotice(t *testing.T) {
	testutil.UseDatabase(t)
	installSocialTestAccounts(t, map[string]*store.Account{
		"tok1": {AccessToken: "tok1", SessionKey: "skey1", Aid: "1001", Name: "One"},
		"tok2": {AccessToken: "tok2", SessionKey: "skey2", Aid: "1002", Name: "Two"},
	})
	config.Activate(config.Config{MaxPlayers: 1})
	t.Cleanup(func() { config.Activate(config.Config{}) })

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go lpn.ServeSession(ln, log.New(io.Discard, "", 0))
	t.Cleanup(func() { _ = ln.Close() })
	conn := bindLPNSession(t, ln.Addr().String(), "skey1")
	t.Cleanup(func() {
		_ = conn.Close()
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) && lpn.SessionOnline(1001) {
			time.Sleep(10 * time.Millisecond)
		}
	})

	mux, plain := NewMux(), NewPlainMux()
	get := func(h http.Handler, path, remote string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.RemoteAddr = remote
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	const refusedAddr, otherAddr = "198.51.100.7:4100", "198.51.100.8:4100"

	// An in-game client's notice fetch before any refusal: 404, as before.
	if rec := get(plain, "/img/read/notice/notice.json2", refusedAddr); rec.Code != http.StatusNotFound {
		t.Fatalf("notice before refusal = %d", rec.Code)
	}
	init := get(mux, "/v4/setInitConf?nationCode=JP", refusedAddr)
	if init.Code != http.StatusServiceUnavailable || init.Body.String() != `{"errorCode":"503","errorMessage":"cherry: server full"}` {
		t.Fatalf("setInitConf when full = %d %s", init.Code, init.Body)
	}
	// Another address gets no notice; the refused one gets it once, from another port of the same host.
	if rec := get(plain, "/img/read/notice/notice.json2", otherAddr); rec.Code != http.StatusNotFound {
		t.Fatalf("notice for another address = %d", rec.Code)
	}
	rec := get(plain, "/img/read/notice/notice.json2", "198.51.100.7:4999")
	if rec.Code != http.StatusOK {
		t.Fatalf("notice for the refused address = %d %s", rec.Code, rec.Body)
	}
	var n struct {
		Seq                                   int
		Target, Type, Start, End, DisplayType string
		Message                               map[string]string
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &n); err != nil {
		t.Fatal(err)
	}
	start, _ := strconv.ParseInt(n.Start, 10, 64)
	end, _ := strconv.ParseInt(n.End, 10, 64)
	now := time.Now().UnixMilli()
	if n.Seq != 1 || n.Type != "maint" || !(start < now && now < end) || n.Message["en"] == "" || n.Message["ko"] == "" || n.Message["ja"] == "" {
		t.Fatalf("notice = %s", rec.Body)
	}
	if !strings.Contains(n.Message["en"], "/ 1 max players)") || !strings.Contains(n.Message["ko"], "최대 1명") || !strings.Contains(n.Message["ja"], "最大 1人") || strings.Contains(rec.Body.String(), "%!") {
		t.Fatalf("notice text lacks the counts: %s", rec.Body)
	}
	if rec := get(plain, "/img/read/notice/notice.json2", refusedAddr); rec.Code != http.StatusNotFound {
		t.Fatalf("second notice fetch = %d, want 404 (once)", rec.Code)
	}
	if rec := get(mux, "/v4/maintenance/whitelist/lineplay", refusedAddr); rec.Code != http.StatusOK || rec.Body.String() != `{"result":[]}` {
		t.Fatalf("whitelist = %d %s", rec.Code, rec.Body)
	}

	// checkSession never refuses: an offline player's saved login goes through even when full.
	if rec := checkSession(t, "tok2"); rec.Code != http.StatusOK {
		t.Fatalf("checkSession when full = %d %s, want 200", rec.Code, rec.Body)
	}

	// The live override 0 (unlimited) opens setInitConf again.
	if err := store.SetMaxPlayers(0, "dev-owner"); err != nil {
		t.Fatal(err)
	}
	if rec := get(mux, "/v4/setInitConf?nationCode=JP", otherAddr); rec.Code != http.StatusOK || rec.Body.String() != setInitConfBody {
		t.Fatalf("setInitConf with the unlimited override = %d %s", rec.Code, rec.Body)
	}
}

// Thai and Traditional Chinese travel in the en key (the client shows en for every game language but ko and ja).
func TestFullNoticeLanguage(t *testing.T) {
	for _, c := range []struct{ header, want string }{
		{"th, en-US, th", "th"}, {"th-TH", "th"}, {"zh-Hant, en-US, zh-Hant", "zh-Hant"}, {"zh-Hant-TW", "zh-Hant"},
		{"zh-TW", "zh-Hant"}, {"en, en-US, en", "en"}, {"en-PH, en-PH", "en"}, {"zh-Hans", "en"}, {"", "en"},
	} {
		if got := fullNoticeLang(c.header); got != c.want {
			t.Errorf("fullNoticeLang(%q) = %q, want %q", c.header, got, c.want)
		}
	}
	var n struct{ Message map[string]string }
	if err := json.Unmarshal([]byte(fullNoticeBody(time.Now(), 501, 500, "th, en-US, th")), &n); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(n.Message["en"], "ผู้เล่น 501 คน / สูงสุด 500 คน") || len(n.Message) != 3 || strings.Contains(n.Message["ko"], "%!") {
		t.Fatalf("Thai notice = %v", n.Message)
	}
}

func TestFullNoticeExpires(t *testing.T) {
	now := time.Now()
	rememberFullNotice("203.0.113.9", now)
	if takeFullNotice("203.0.113.9", now.Add(fullNoticeTTL+time.Second)) {
		t.Fatal("a notice older than fullNoticeTTL was served")
	}
}

func TestMaintenanceWhitelist(t *testing.T) {
	testutil.UseDatabase(t)
	acc1 := &store.Account{AccessToken: "wl-tok1", SessionKey: "skey1", Aid: "1001", Name: "One"}
	acc2 := &store.Account{AccessToken: "wl-tok2", SessionKey: "skey2", Aid: "1002", Name: "Two"}
	installSocialTestAccounts(t, map[string]*store.Account{
		"wl-tok1": acc1,
		"wl-tok2": acc2,
	})
	store.LatestAcc = acc1

	if err := store.AddMaintenanceWhitelist("1001", "tester", "dev-owner", time.Now()); err != nil {
		t.Fatal(err)
	}

	mux := NewMux()
	call := func(token string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/v4/maintenance/whitelist/lineplay", nil)
		if token != "" {
			req.Header.Set("Cookie", `AV_AUTH="`+token+`"`)
		}
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec
	}

	// Unauthenticated request must never leak whitelisted entries or fall back to LatestAcc.
	if rec := call(""); rec.Code != http.StatusOK || rec.Body.String() != `{"result":[]}` {
		t.Fatalf("unauthenticated whitelist = %d %s, want 200 {\"result\":[]}", rec.Code, rec.Body)
	}

	// Authenticated non-whitelisted account receives empty result.
	if rec := call("wl-tok2"); rec.Code != http.StatusOK || rec.Body.String() != `{"result":[]}` {
		t.Fatalf("non-whitelisted whitelist = %d %s, want 200 {\"result\":[]}", rec.Code, rec.Body)
	}

	// Authenticated whitelisted account receives only its own aid entry.
	if rec := call("wl-tok1"); rec.Code != http.StatusOK || rec.Body.String() != `{"result":[{"gameId":"lineplay","type":"aid","data":"1001"}]}` {
		t.Fatalf("whitelisted response = %d %s", rec.Code, rec.Body)
	}

	// Removing from whitelist returns empty result again.
	if err := store.RemoveMaintenanceWhitelist("1001", "dev-owner"); err != nil {
		t.Fatal(err)
	}
	if rec := call("wl-tok1"); rec.Code != http.StatusOK || rec.Body.String() != `{"result":[]}` {
		t.Fatalf("after removal whitelist = %d %s, want 200 {\"result\":[]}", rec.Code, rec.Body)
	}
}

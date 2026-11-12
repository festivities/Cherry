package main

import (
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"cherry/internal/admin"
	"cherry/internal/auth"
	"cherry/internal/config"
	"cherry/internal/web"
)

// TestStaffAndGameMuxesStaySeparate walks both muxes: no staff route answers on the game mux (it sets no staff cookie and
// no staff policy), and no game route answers on the staff mux (404).
func TestStaffAndGameMuxesStaySeparate(t *testing.T) {
	base := t.TempDir()
	t.Setenv("LOCALAPPDATA", base)
	t.Setenv("XDG_CACHE_HOME", base)
	cfg := config.Config{
		AuthMode: config.AuthDevFake, Signup: config.SignupOpen, ServerName: "test",
		HTTP: ":80", HTTPS: ":443", Gateway: ":10000", Session: ":10123",
		Staff: ":443", StaffHostname: "staff.test", StaffOwner: "dev-owner",
	}
	logger := log.New(io.Discard, "", 0)
	svc, err := auth.New(cfg, logger)
	if err != nil {
		t.Fatal(err)
	}
	game := web.NewGameMux(svc)
	staff, err := admin.New(cfg, logger)
	if err != nil {
		t.Fatal(err)
	}
	staffHandler := staff.Handler()

	for _, p := range []string{"/", "/docs", "/docs/cherrytips.json", "/docs/cherrytips.json/1", "/audit", "/auth/login", "/auth/callback", "/auth/logout", "/static/staff.css"} {
		req := httptest.NewRequest(http.MethodGet, p, nil)
		req.Host = "staff.test"
		rec := httptest.NewRecorder()
		game.ServeHTTP(rec, req)
		for _, ck := range rec.Header().Values("Set-Cookie") {
			if strings.Contains(ck, "__Host-cherry-") {
				t.Errorf("game mux answers staff path %s with a staff cookie", p)
			}
		}
		if strings.Contains(rec.Header().Get("Content-Security-Policy"), "frame-ancestors 'none'") {
			t.Errorf("game mux answers staff path %s with the staff policy", p)
		}
	}

	for _, p := range []string{"/v4/createSession", "/v4/checkSession", "/v4/account/guest/generate", "/cherry/v1/info", "/auth/v1/discord/start", "/auth/v1/exchange", "/admin/docs", "/admin/grant"} {
		req := httptest.NewRequest(http.MethodGet, p, nil)
		req.Host = "staff.test"
		rec := httptest.NewRecorder()
		staffHandler.ServeHTTP(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Errorf("staff mux answers game path %s with %d, want 404", p, rec.Code)
		}
	}
}

// TestStaffOffStartsNothing: listen.staff empty (the lab) starts no listener and returns nothing.
func TestStaffOffStartsNothing(t *testing.T) {
	s, err := admin.Start(config.Config{AuthMode: config.AuthLabGuest}, nil)
	if err != nil || s != nil {
		t.Fatalf("Start with listen.staff empty: server %v, error %v", s, err)
	}
}

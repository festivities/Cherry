package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"cherry/internal/store"
	"cherry/internal/testutil"
)

// TestChatRoomListRoute: the talk-list route is on the mux. An account with no 1:1 rooms, and a request without a session,
// both get a roomList array (empty, never null). Rooms themselves are covered in internal/lpn.
func TestChatRoomListRoute(t *testing.T) {
	testutil.InstallAccounts(t, map[string]*store.Account{
		"av-web-9971": {AccessToken: "av-web-9971", SessionKey: "sk-av-web-9971", Aid: "9971", Name: "Dee"},
	})
	for _, token := range []string{"av-web-9971", ""} {
		req := httptest.NewRequest(http.MethodGet, "/v4/chat/roomlist/sync/0/200", nil)
		if token != "" {
			req.AddCookie(&http.Cookie{Name: "AV_AUTH", Value: token})
		}
		rec := httptest.NewRecorder()
		NewMux().ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("token %q: status %d", token, rec.Code)
		}
		if body := rec.Body.String(); !strings.Contains(body, `"roomList":[]`) || !strings.Contains(body, `"result":`) {
			t.Fatalf("token %q: body %s, want an empty roomList under result", token, body)
		}
	}
}

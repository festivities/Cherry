package economy

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"cherry/internal/store"
	"cherry/internal/testutil"
)

// TestAdminGuestCode: the staff tool binds a device uniqueCode to an existing account. Non-loopback is 404;
// a bad code, an unknown aid, aid "0" are 400; a code another account holds is 409. The binding is saved,
// so a reload finds the account by its device code.
func TestAdminGuestCode(t *testing.T) {
	const code = "5b1d2c3e-0000-4000-8000-00000000a001"
	a := &store.Account{AccessToken: "gc-a", SessionKey: "sk", Aid: "9601"}
	b := &store.Account{AccessToken: "gc-b", SessionKey: "sk", Aid: "9602"}
	testutil.InstallAccounts(t, map[string]*store.Account{"gc-a": a, "gc-b": b})
	testutil.UseDatabase(t)
	store.AccountsMu.Lock()
	store.NextAvatarID = 9602
	store.AccountsMu.Unlock()
	post := func(remote, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/admin/guestcode", strings.NewReader(body))
		req.RemoteAddr = remote
		rec := httptest.NewRecorder()
		NewAdminMux().ServeHTTP(rec, req)
		return rec
	}
	bind := `{"aid":"9601","uniqueCode":"` + code + `"}`
	if rec := post("192.168.1.9:5000", bind); rec.Code != http.StatusNotFound || a.GuestCode != "" {
		t.Fatalf("non-loopback = %d", rec.Code)
	}
	for _, body := range []string{`{"aid":"9601","uniqueCode":"bad code"}`, `{"aid":"9999","uniqueCode":"` + code + `"}`, `{"aid":"0","uniqueCode":"` + code + `"}`} {
		if rec := post("127.0.0.1:5000", body); rec.Code != http.StatusBadRequest || a.GuestCode != "" {
			t.Fatalf("refused %s = %d", body, rec.Code)
		}
	}
	if rec := post("127.0.0.1:5000", bind); rec.Code != http.StatusOK || a.GuestCode != code || !strings.Contains(rec.Body.String(), `"aid":"9601"`) {
		t.Fatalf("bind = %d %s", rec.Code, rec.Body)
	}
	if rec := post("127.0.0.1:5000", `{"aid":"9602","uniqueCode":"`+code+`"}`); rec.Code != http.StatusConflict || b.GuestCode != "" {
		t.Fatalf("held code = %d", rec.Code)
	}
	if err := store.ReloadAccounts(); err != nil {
		t.Fatal(err)
	}
	store.AccountsMu.Lock()
	holder := store.GuestAccountLocked(code)
	store.AccountsMu.Unlock()
	if holder == nil || holder.Aid != "9601" {
		t.Fatal("binding not saved with the account")
	}
}

package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"cherry/internal/economy"
	"cherry/internal/store"
	"cherry/internal/testutil"
)

// TestHomeIntegratedInfoIsMounted: the Garden home info reaches its economy handler on the real mux while the
// Halloween icon's season is in force. Only the exact path is served; POST and sub-paths are 404.
func TestHomeIntegratedInfoIsMounted(t *testing.T) {
	old := economy.NowFunc
	t.Cleanup(func() { economy.NowFunc = old })
	economy.NowFunc = func() time.Time { return time.Date(2026, time.October, 26, 12, 0, 0, 0, time.FixedZone("JST", 9*3600)) }
	if rec := serve(t, http.MethodGet, "/v4/home/integrated/info"); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"iconId":"halloween_17s"`) {
		t.Fatalf("home info = %d %q", rec.Code, rec.Body.String())
	}
	if rec := serve(t, http.MethodPost, "/v4/home/integrated/info"); rec.Code != http.StatusNotFound {
		t.Fatalf("POST home info = %d, want 404", rec.Code)
	}
	if rec := serve(t, http.MethodGet, "/v4/home/integrated/info/extra"); rec.Code != http.StatusNotFound {
		t.Fatalf("sub-path home info = %d, want 404", rec.Code)
	}
}

// homeListBody is the home menu answer to a request with the AV_AUTH cookie token ("" sends no cookie).
func homeListBody(t *testing.T, token string) string {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/v4/home/list/ext/10.1.0.0/Android", nil)
	if token != "" {
		req.Header.Set("Cookie", `AV_AUTH="`+token+`"`)
	}
	rec := httptest.NewRecorder()
	NewMux().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("home list (token %q) = %d", token, rec.Code)
	}
	return rec.Body.String()
}

// homeRowImage is the picture of the home menu row with that link type in body.
func homeRowImage(t *testing.T, body, linkType string) string {
	t.Helper()
	var doc struct {
		Result struct {
			HomeIconList []struct {
				LinkType string `json:"linkType"`
				Image    string `json:"image"`
			} `json:"homeIconList"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(body), &doc); err != nil {
		t.Fatal(err)
	}
	for _, row := range doc.Result.HomeIconList {
		if row.LinkType == linkType {
			return row.Image
		}
	}
	t.Fatalf("no %s row", linkType)
	return ""
}

// TestHomeProfileEditRowShowsTheRequesterPicture: the Profile Edit row carries the AV_AUTH account's own picture, relative
// (no leading slash). Without a picture, or without a session, the body is the anonymous one with the menu icon; with a
// picture, only that row's image differs.
func TestHomeProfileEditRowShowsTheRequesterPicture(t *testing.T) {
	testutil.InstallAccounts(t, map[string]*store.Account{
		"pic-token":   {Aid: "18", Name: "Pic", ProfileImage: store.ProfileImage{Image: "/lineplay/pr/18_1795286862"}},
		"nopic-token": {Aid: "19", Name: "Plain"},
	})
	anon := homeListBody(t, "")
	if got := homeRowImage(t, anon, homeProfileEditLink); got != "cherry/menu/user-edit.png" {
		t.Fatalf("anonymous Profile Edit image = %q", got)
	}
	for _, token := range []string{"nopic-token", "no-such-token"} {
		if body := homeListBody(t, token); body != anon {
			t.Fatalf("token %q without a picture changed the menu", token)
		}
	}
	pic := homeListBody(t, "pic-token")
	if got := homeRowImage(t, pic, homeProfileEditLink); got != "lineplay/pr/18_1795286862" {
		t.Fatalf("Profile Edit image = %q, want the requester's picture", got)
	}
	if strings.Replace(pic, `"image":"lineplay/pr/18_1795286862"`, `"image":"cherry/menu/user-edit.png"`, 1) != anon {
		t.Fatal("a session picture changed more than the Profile Edit row")
	}
}

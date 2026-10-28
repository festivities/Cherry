package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"cherry/internal/store"
)

// wishFixture: wish-me (9501) owns the list, wish-fr (9502) is an accepted friend and wish-no (9503) a stranger.
// The catalog holds CUTO00001 (top) and RUCH00001 (chair) with art, both sold. The account store stays in memory
// unless a test sets AccountStorePath.
func wishFixture(t *testing.T) {
	t.Helper()
	root, _ := thumbFixture(t)
	writeThumbItem(t, root, "dress", "225000001", "dp.png", []byte("x"))
	writeThumbItem(t, root, "interior", "120400001", "dp.png", []byte("x"))
	installSocialTestAccounts(t, map[string]*store.Account{
		"wish-me": {AccessToken: "wish-me", SessionKey: "sk", Aid: "9501", Name: "Wisher", Gender: "FEMALE"},
		"wish-fr": {AccessToken: "wish-fr", SessionKey: "sk", Aid: "9502", Name: "Friend", Gender: "MALE"},
		"wish-no": {AccessToken: "wish-no", SessionKey: "sk", Aid: "9503", Name: "Stranger", Gender: "MALE"},
	})
	store.ResetSocial()
	t.Cleanup(store.ResetSocial)
	store.SocialMu.Lock()
	store.Friendships = []store.Friendship{{A: "9501", B: "9502", State: "accepted"}}
	store.SocialMu.Unlock()
}

func wishSave(t *testing.T, token, body string) *httptest.ResponseRecorder {
	t.Helper()
	return socialTestRequestAs(t, token, http.MethodPost, "/v4/diary/wish/save", body)
}

// wishList GETs the wish list of aid as token and decodes result as an array of rows.
func wishList(t *testing.T, token, aid string) []map[string]any {
	t.Helper()
	rec := socialTestRequestAs(t, token, http.MethodGet, "/v4/diary/wish/list/"+aid, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("wish list %s as %s = %d %s", aid, token, rec.Code, rec.Body.String())
	}
	var body struct {
		Result []map[string]any `json:"result"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || body.Result == nil {
		t.Fatalf("wish list %s = %s, want a result array (%v)", aid, rec.Body.String(), err)
	}
	return body.Result
}

func wishCodes(rows []map[string]any) []string {
	codes := []string{}
	for _, row := range rows {
		code, _ := row["itemCode"].(string)
		codes = append(codes, code)
	}
	return codes
}

// TestWishListShape: result is an array of rows with exactly the keys the client reads (itemCode, itemType and
// specialEffects strings, selling a bool); an empty list is [], never null.
func TestWishListShape(t *testing.T) {
	wishFixture(t)
	if rec := socialTestRequestAs(t, "wish-me", http.MethodGet, "/v4/diary/wish/list/9501", ""); rec.Body.String() != `{"result":[]}` {
		t.Fatalf("empty wish list = %d %s, want {\"result\":[]}", rec.Code, rec.Body.String())
	}
	if rec := wishSave(t, "wish-me", `["CUTO00001","RUCH00001"]`+"\x00"); rec.Code != http.StatusOK || rec.Body.String() != `{"result":true}` {
		t.Fatalf("save = %d %s, want 200 {\"result\":true}", rec.Code, rec.Body.String())
	}
	rows := wishList(t, "wish-me", "9501")
	if len(rows) != 2 {
		t.Fatalf("rows = %v, want 2", rows)
	}
	wantType := map[string]string{"CUTO00001": "DRESS", "RUCH00001": "ROOM"}
	for _, row := range rows {
		if len(row) != 4 {
			t.Errorf("row %v has %d keys, want itemCode, itemType, selling, specialEffects", row, len(row))
		}
		code, _ := row["itemCode"].(string)
		if itemType, ok := row["itemType"].(string); !ok || itemType != wantType[code] {
			t.Errorf("row %s itemType = %v, want %s", code, row["itemType"], wantType[code])
		}
		if _, ok := row["specialEffects"].(string); !ok {
			t.Errorf("row %s specialEffects = %v, want a string", code, row["specialEffects"])
		}
		if selling, ok := row["selling"].(bool); !ok || !selling {
			t.Errorf("row %s selling = %v, want true (sold now)", code, row["selling"])
		}
	}
}

// TestWishListSaveRoundTrip: a save replaces the list (add, remove, clear) and reaches accounts.json; refused saves
// (five codes, a duplicate, a code the shops do not sell, not an array) answer 400 and keep the stored list.
func TestWishListSaveRoundTrip(t *testing.T) {
	wishFixture(t)
	path := filepath.Join(t.TempDir(), "accounts.json")
	store.AccountsMu.Lock()
	store.AccountStorePath = path
	store.AccountsMu.Unlock()

	if rec := wishSave(t, "wish-me", `["CUTO00001","RUCH00001"]`); rec.Code != http.StatusOK {
		t.Fatalf("add two = %d %s", rec.Code, rec.Body.String())
	}
	saved, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(saved), `"wishes":["CUTO00001","RUCH00001"]`) {
		t.Fatalf("accounts.json does not hold the wish list: %s (%v)", saved, err)
	}
	if got := wishCodes(wishList(t, "wish-me", "9501")); !slices.Equal(got, []string{"CUTO00001", "RUCH00001"}) {
		t.Fatalf("list after add = %v", got)
	}

	if rec := wishSave(t, "wish-me", `["RUCH00001"]`); rec.Code != http.StatusOK {
		t.Fatalf("remove one = %d %s", rec.Code, rec.Body.String())
	}
	if got := wishCodes(wishList(t, "wish-me", "9501")); !slices.Equal(got, []string{"RUCH00001"}) {
		t.Fatalf("list after remove = %v", got)
	}
	if rec := wishSave(t, "wish-me", `[]`); rec.Code != http.StatusOK {
		t.Fatalf("clear = %d %s", rec.Code, rec.Body.String())
	}
	if rec := socialTestRequestAs(t, "wish-me", http.MethodGet, "/v4/diary/wish/list/9501", ""); rec.Body.String() != `{"result":[]}` {
		t.Fatalf("list after clear = %s", rec.Body.String())
	}

	if rec := wishSave(t, "wish-me", `["RUCH00001"]`); rec.Code != http.StatusOK {
		t.Fatalf("set one = %d %s", rec.Code, rec.Body.String())
	}
	refused := []string{
		`["CUTO00001","RUCH00001","CUXX00001","CUXX00002","CUXX00003"]`, // five codes
		`["RUCH00001","RUCH00001"]`,                                     // duplicate
		`["CUXX00001"]`,                                                 // not sold by the shops
		`{"itemCode":"RUCH00001"}`,                                      // not an array
	}
	for _, body := range refused {
		if rec := wishSave(t, "wish-me", body); rec.Code != http.StatusBadRequest {
			t.Errorf("save %s = %d %s, want 400", body, rec.Code, rec.Body.String())
		}
		if got := wishCodes(wishList(t, "wish-me", "9501")); !slices.Equal(got, []string{"RUCH00001"}) {
			t.Errorf("after refused %s the list is %v, want [RUCH00001]", body, got)
		}
	}
}

// TestWishListSaveRollback: an account file that cannot be written answers 500 and keeps the old list.
func TestWishListSaveRollback(t *testing.T) {
	wishFixture(t)
	if rec := wishSave(t, "wish-me", `["RUCH00001"]`); rec.Code != http.StatusOK {
		t.Fatalf("seed = %d %s", rec.Code, rec.Body.String())
	}
	blocker := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	store.AccountsMu.Lock()
	store.AccountStorePath = filepath.Join(blocker, "accounts.json") // its folder is a regular file
	store.AccountsMu.Unlock()
	if rec := wishSave(t, "wish-me", `["CUTO00001"]`); rec.Code != http.StatusInternalServerError {
		t.Fatalf("save into an unwritable store = %d %s, want 500", rec.Code, rec.Body.String())
	}
	if got := wishCodes(wishList(t, "wish-me", "9501")); !slices.Equal(got, []string{"RUCH00001"}) {
		t.Fatalf("list after failed save = %v, want the old [RUCH00001]", got)
	}
}

// TestWishListVisibility: the owner and accepted friends read a list; a stranger, the synthetic friend and an
// unknown aid get an empty list (200, no error popup). A malformed aid or no session is refused. A code the shops
// no longer list keeps its row with selling false.
func TestWishListVisibility(t *testing.T) {
	wishFixture(t)
	if rec := wishSave(t, "wish-me", `["RUCH00001"]`); rec.Code != http.StatusOK {
		t.Fatalf("save = %d %s", rec.Code, rec.Body.String())
	}
	if got := wishCodes(wishList(t, "wish-me", "9501")); !slices.Equal(got, []string{"RUCH00001"}) {
		t.Errorf("owner reads %v", got)
	}
	if got := wishCodes(wishList(t, "wish-fr", "9501")); !slices.Equal(got, []string{"RUCH00001"}) {
		t.Errorf("accepted friend reads %v, want [RUCH00001]", got)
	}
	for _, aid := range []string{"9503", store.FriendAID, "9599"} {
		if got := wishList(t, "wish-no", aid); len(got) != 0 {
			t.Errorf("stranger reads %s = %v, want empty", aid, got)
		}
	}
	if rec := socialTestRequestAs(t, "wish-me", http.MethodGet, "/v4/diary/wish/list/abc", ""); rec.Code != http.StatusNotFound {
		t.Errorf("malformed aid = %d, want 404", rec.Code)
	}
	if rec := serve(t, http.MethodGet, "/v4/diary/wish/list/9501"); rec.Code != http.StatusNotFound {
		t.Errorf("list without session = %d, want 404", rec.Code)
	}

	// A stored code the shops do not list keeps its row, with selling false and no effects.
	store.AccountsMu.Lock()
	store.Accounts["wish-me"].Wishes = []string{"CUXX00009"}
	store.AccountsMu.Unlock()
	rows := wishList(t, "wish-me", "9501")
	if len(rows) != 1 || rows[0]["itemCode"] != "CUXX00009" || rows[0]["selling"] != false || rows[0]["specialEffects"] != "" {
		t.Errorf("unsold stored code row = %v, want CUXX00009 with selling false", rows)
	}
}

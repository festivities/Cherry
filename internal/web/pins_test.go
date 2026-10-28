package web

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"cherry/internal/store"
)

// pinFixture: pin-me (1101) is the requester and friends pin-fr (1102, with a profile picture) on an
// accepted friendship; pin-gone (1103) is deleted later in the test. The account store is file-backed.
func pinFixture(t *testing.T) string {
	t.Helper()
	me := &store.Account{AccessToken: "pin-me", SessionKey: "sk", Aid: "1101", Name: "Pina"}
	fr := &store.Account{AccessToken: "pin-fr", SessionKey: "sk", Aid: "1102", Name: "Fumi",
		ProfileImage: store.ProfileImage{Image: "/lineplay/pr/1102_1700000000", Bg: "profilebg/3.jpg"}}
	gone := &store.Account{AccessToken: "pin-gone", SessionKey: "sk", Aid: "1103", Name: "Gone"}
	installSocialTestAccounts(t, map[string]*store.Account{"pin-me": me, "pin-fr": fr, "pin-gone": gone})
	path := filepath.Join(t.TempDir(), "accounts.json")
	store.AccountsMu.Lock()
	store.AccountStorePath, store.NextAvatarID, store.LatestAcc = path, 1103, me
	store.AccountsMu.Unlock()
	store.ResetSocial()
	t.Cleanup(store.ResetSocial)
	store.SocialMu.Lock()
	store.Friendships = []store.Friendship{{A: "1101", B: "1102", State: "accepted"}}
	store.SocialMu.Unlock()
	return path
}

func pinList(t *testing.T, token string) []pinRow {
	t.Helper()
	rec := socialTestRequestAs(t, token, http.MethodGet, "/v4/pin/list", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("pin list = %d %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Result struct {
			PinList []pinRow `json:"pinList"`
		} `json:"result"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	return body.Result.PinList
}

func TestPinAddListDeleteRoundTrip(t *testing.T) {
	path := pinFixture(t)

	if rec := socialTestRequestAs(t, "pin-me", http.MethodGet, "/v4/pin/list", ""); rec.Body.String() != `{"result":{"pinList":[]}}` {
		t.Fatalf("empty pin list = %s", rec.Body.String())
	}
	for _, aid := range []string{"1102", "1102", "1103", store.FriendAID} { // the second 1102 is a duplicate
		rec := socialTestRequestAs(t, "pin-me", http.MethodGet, "/v4/pin/add/"+aid, "")
		if rec.Code != http.StatusOK || rec.Body.String() != `{"result":true}` {
			t.Fatalf("pin add %s = %d %s", aid, rec.Code, rec.Body.String())
		}
	}
	rows := pinList(t, "pin-me")
	if len(rows) != 3 || rows[0].AvatarNo != "1102" || rows[1].AvatarNo != "1103" || rows[2].AvatarNo != store.FriendAID {
		t.Fatalf("pins after add = %+v, want 1102, 1103, friend in pin order", rows)
	}
	if fr := rows[0]; fr.AvatarName != "Fumi" || fr.ObsProfileImageURL != "/lineplay/pr/1102_1700000000" || fr.ProfileBgURL != "profilebg/3.jpg" || fr.Status != 1 {
		t.Errorf("friend pin row = %+v", fr)
	}
	if gone := rows[1]; gone.AvatarName != "Gone" || gone.Status != 2 {
		t.Errorf("non-friend pin row = %+v, want name Gone and status 2 (none)", gone)
	}

	for _, aid := range []string{"1101", "9999"} { // self and unknown accounts: result false, no error popup
		if rec := socialTestRequestAs(t, "pin-me", http.MethodGet, "/v4/pin/add/"+aid, ""); rec.Code != http.StatusOK || rec.Body.String() != `{"result":false}` {
			t.Errorf("pin add %s = %d %s, want result false", aid, rec.Code, rec.Body.String())
		}
	}
	if rec := socialTestRequestAs(t, "pin-me", http.MethodGet, "/v4/pin/add/abc", ""); rec.Code != http.StatusNotFound {
		t.Errorf("pin add abc = %d, want 404", rec.Code)
	}
	if rec := serve(t, http.MethodGet, "/v4/pin/list"); rec.Code != http.StatusNotFound {
		t.Errorf("pin list without session = %d, want 404", rec.Code)
	}
	saved, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(saved), `"pins":["1102","1103","100000"]`) {
		t.Errorf("accounts.json does not hold the pins: %s", saved)
	}

	// Delete Avatar: the deleted account vanishes from the requester's list at read time.
	store.AccountsMu.Lock()
	gone := store.Accounts["pin-gone"]
	store.AccountsMu.Unlock()
	if err := deleteAccount(gone); err != nil {
		t.Fatal(err)
	}
	if rows := pinList(t, "pin-me"); len(rows) != 2 || rows[0].AvatarNo != "1102" || rows[1].AvatarNo != store.FriendAID {
		t.Fatalf("pins after deleting 1103 = %+v", rows)
	}

	rec := socialTestRequestAs(t, "pin-me", http.MethodPost, "/v4/pin/delete", "[1102,100000]\x00")
	if rec.Code != http.StatusOK || rec.Body.String() != `{"result":true}` {
		t.Fatalf("pin delete = %d %s", rec.Code, rec.Body.String())
	}
	if rows := pinList(t, "pin-me"); len(rows) != 0 {
		t.Fatalf("pins after delete = %+v, want none", rows)
	}
	if rec := socialTestRequestAs(t, "pin-me", http.MethodPost, "/v4/pin/delete", `["x"]`); rec.Code != http.StatusBadRequest {
		t.Errorf("pin delete with a non-numeric id = %d, want 400", rec.Code)
	}
}

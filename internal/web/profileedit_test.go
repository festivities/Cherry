package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"cherry/internal/store"
)

// profileEditFixture: dp-owner (1201, "Yuki") has a saved profile with mixed secret flags and dp-visitor
// (1202) has none. The account store is in memory unless a test sets AccountStorePath.
func profileEditFixture(t *testing.T) {
	t.Helper()
	owner := &store.Account{AccessToken: "dp-owner", SessionKey: "sk", Aid: "1201", Name: "Yuki",
		DiaryProfile: &store.DiaryProfile{Location: "Osaka", LocationSecret: true, Sex: "female",
			BirthDay: "0325", BirthDaySecret: true, BloodType: "A", Job: "student", JobSecret: true}}
	visitor := &store.Account{AccessToken: "dp-visitor", SessionKey: "sk", Aid: "1202", Name: "Ken"}
	installSocialTestAccounts(t, map[string]*store.Account{"dp-owner": owner, "dp-visitor": visitor})
}

func diaryProfileResult(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Result map[string]any `json:"result"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	return body.Result
}

func checkFields(t *testing.T, got map[string]any, want map[string]any) {
	t.Helper()
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %#v, want %#v", k, got[k], v)
		}
	}
}

func TestDiaryProfileGetShapeAndTypes(t *testing.T) {
	profileEditFixture(t)
	got := diaryProfileResult(t, socialTestRequestAs(t, "dp-owner", http.MethodGet, "/v4/diary/profile/1201", ""))
	want := map[string]any{ // JSON types as SetDiaryProfileData reads them: strings, bools, status an int
		"avatarId": "1201", "nickname": "Yuki", "status": float64(1), "created": true,
		"location": "Osaka", "locationSecret": true,
		"sex": "female", "sexSecret": false,
		"birthDay": "0325", "birthDaySecret": true,
		"bloodType": "A", "bloodTypeSecret": false,
		"job": "student", "jobSecret": true,
	}
	if len(got) != len(want) {
		t.Fatalf("keys = %d, want %d: %v", len(got), len(want), got)
	}
	checkFields(t, got, want)

	empty := diaryProfileResult(t, socialTestRequestAs(t, "dp-owner", http.MethodGet, "/v4/diary/profile/1202", ""))
	if len(empty) != len(want) {
		t.Fatalf("unsaved profile keys = %d, want %d", len(empty), len(want))
	}
	checkFields(t, empty, map[string]any{"nickname": "Ken", "location": "", "locationSecret": false, "created": true})

	if rec := serve(t, http.MethodGet, "/v4/diary/profile/"+store.FriendAID); rec.Code != http.StatusOK {
		t.Errorf("friend profile = %d %s", rec.Code, rec.Body.String())
	}
	if rec := serve(t, http.MethodGet, "/v4/diary/profile/9999"); rec.Code != http.StatusNotFound {
		t.Errorf("unknown aid = %d, want 404", rec.Code)
	}
}

func TestDiaryProfileSecretFieldsHiddenFromOthers(t *testing.T) {
	profileEditFixture(t)
	want := map[string]any{ // secret values blanked, flags kept so the client masks them
		"location": "", "locationSecret": true, "sex": "female", "sexSecret": false,
		"birthDay": "", "birthDaySecret": true, "bloodType": "A", "bloodTypeSecret": false,
		"job": "", "jobSecret": true,
	}
	visitor := diaryProfileResult(t, socialTestRequestAs(t, "dp-visitor", http.MethodGet, "/v4/diary/profile/1201", ""))
	checkFields(t, visitor, want)
	anonymous := diaryProfileResult(t, serve(t, http.MethodGet, "/v4/diary/profile/1201"))
	checkFields(t, anonymous, want)
}

func TestDiaryProfileSaveGetRoundTrip(t *testing.T) {
	profileEditFixture(t)
	// The native body ends in a NUL terminator, which UnmarshalNativeJSON strips.
	body := `{"location":"Kyoto","locationSecret":false,"sex":"male","sexSecret":true,"birthDay":"1104","birthDaySecret":false,` +
		`"bloodType":"O","bloodTypeSecret":true,"job":"designer","jobSecret":false}` + "\x00"
	if rec := socialTestRequestAs(t, "dp-owner", http.MethodPost, "/v4/profile/intro/", body); rec.Code != http.StatusOK || rec.Body.String() != `{"result":true}` {
		t.Fatalf("save = %d %s", rec.Code, rec.Body.String())
	}
	got := diaryProfileResult(t, socialTestRequestAs(t, "dp-owner", http.MethodGet, "/v4/diary/profile/1201", ""))
	checkFields(t, got, map[string]any{"location": "Kyoto", "locationSecret": false, "sex": "male", "sexSecret": true,
		"birthDay": "1104", "birthDaySecret": false, "bloodType": "O", "bloodTypeSecret": true, "job": "designer", "jobSecret": false})
	// The visitor sees the same record with the secret values blanked (sex, blood type).
	visitor := diaryProfileResult(t, socialTestRequestAs(t, "dp-visitor", http.MethodGet, "/v4/diary/profile/1201", ""))
	checkFields(t, visitor, map[string]any{"sex": "", "sexSecret": true, "bloodType": "", "bloodTypeSecret": true, "job": "designer", "jobSecret": false})
	if rec := socialTestRequestAs(t, "dp-owner", http.MethodGet, "/v4/profile/1201", ""); rec.Code != http.StatusOK {
		t.Errorf("social profile route = %d, want 200 (the POST routes must not change GET)", rec.Code)
	}
}

func TestDiaryProfileSaveSurvivesReload(t *testing.T) {
	profileEditFixture(t)
	path := filepath.Join(t.TempDir(), "accounts.json")
	store.AccountsMu.Lock()
	store.AccountStorePath, store.NextAvatarID, store.LatestAcc = path, 1300, store.Accounts["dp-owner"]
	store.AccountsMu.Unlock()
	body := `{"location":"Nara","locationSecret":true,"sex":"","sexSecret":false,"birthDay":"","birthDaySecret":false,` +
		`"bloodType":"","bloodTypeSecret":false,"job":"nurse","jobSecret":false}`
	if rec := socialTestRequestAs(t, "dp-owner", http.MethodPost, "/v4/profile/intro/", body); rec.Code != http.StatusOK {
		t.Fatalf("save = %d %s", rec.Code, rec.Body.String())
	}
	if err := store.LoadAccountsFrom(path); err != nil {
		t.Fatal(err)
	}
	store.AccountsMu.Lock()
	acc := store.Accounts["dp-owner"]
	store.AccountsMu.Unlock()
	if acc == nil || acc.DiaryProfile == nil {
		t.Fatalf("reloaded owner has no diary profile: %+v", acc)
	}
	if dp := *acc.DiaryProfile; dp.Location != "Nara" || !dp.LocationSecret || dp.Job != "nurse" || dp.Sex != "" {
		t.Fatalf("reloaded profile = %+v", dp)
	}
}

func TestDiaryProfileImageListShape(t *testing.T) {
	profileEditFixture(t)
	for _, aid := range []string{"1201", store.FriendAID} {
		rec := serve(t, http.MethodGet, "/v4/diary/profile/image/"+aid)
		if rec.Code != http.StatusOK || rec.Body.String() != `{"result":{"imageList":[]}}` {
			t.Errorf("image list %s = %d %s", aid, rec.Code, rec.Body.String())
		}
	}
	if rec := serve(t, http.MethodGet, "/v4/diary/profile/image/9999"); rec.Code != http.StatusNotFound {
		t.Errorf("unknown aid image list = %d, want 404", rec.Code)
	}
}

func TestMileageInvenShape(t *testing.T) {
	rec := serve(t, http.MethodGet, "/v4/mileage/inven/items?timestamp=0&page=-1&size=-1")
	want := `{"result":{"items":[],"totalCount":"0","nextCursor":"","previousCursor":""}}`
	if rec.Code != http.StatusOK || rec.Body.String() != want {
		t.Fatalf("mileage = %d %s, want %s", rec.Code, rec.Body.String(), want)
	}
}

func TestProfileIntroSaveRejectsBadBoundary(t *testing.T) {
	profileEditFixture(t)
	bad := map[string]string{
		"number as text": `{"location":5}`,
		"bool as text":   `{"sex":true}`,
		"text as flag":   `{"birthDaySecret":"true"}`,
		"too long":       `{"job":"` + strings.Repeat("a", 33) + `"}`,
		"control char":   `{"location":"a\u0001b"}`,
		"not json":       `{"location":`,
	}
	for name, body := range bad {
		if rec := socialTestRequestAs(t, "dp-owner", http.MethodPost, "/v4/profile/intro/", body); rec.Code != http.StatusBadRequest {
			t.Errorf("%s = %d %s, want 400", name, rec.Code, rec.Body.String())
		}
	}
	if rec := socialTestRequestAs(t, "", http.MethodPost, "/v4/profile/intro/", `{}`); rec.Code != http.StatusNotFound {
		t.Errorf("no session = %d, want 404", rec.Code)
	}
	if rec := socialTestRequestAs(t, "dp-owner", http.MethodGet, "/v4/profile/intro/", ""); rec.Code != http.StatusNotFound {
		t.Errorf("GET save route = %d, want 404", rec.Code)
	}
	got := diaryProfileResult(t, socialTestRequestAs(t, "dp-owner", http.MethodGet, "/v4/diary/profile/1201", ""))
	checkFields(t, got, map[string]any{"location": "Osaka", "job": "student"})
}

func TestAvatarProfileStatusSaveGetRoundTrip(t *testing.T) {
	profileEditFixture(t)
	// The native body ends in a NUL; name, bg and badgeIconNo are read by the client but not stored.
	body := `{"name":"Yuki","intro":"hello, cherry","bg":"","badgeIconNo":"0"}` + "\x00"
	if rec := socialTestRequestAs(t, "dp-owner", http.MethodPost, "/v4/avatar/1201/profile", body); rec.Code != http.StatusOK || rec.Body.String() != `{"result":true}` {
		t.Fatalf("save = %d %s", rec.Code, rec.Body.String())
	}
	// GET /v4/profile/<aid> returns the Status Message as result.intro (ParseJson reads it as a string).
	for _, token := range []string{"dp-owner", "dp-visitor", ""} {
		got := diaryProfileResult(t, socialTestRequestAs(t, token, http.MethodGet, "/v4/profile/1201", ""))
		checkFields(t, got, map[string]any{"intro": "hello, cherry", "name": "Yuki"})
	}
	// The status save keeps the diary profile fields.
	checkFields(t, diaryProfileResult(t, socialTestRequestAs(t, "dp-owner", http.MethodGet, "/v4/diary/profile/1201", "")),
		map[string]any{"location": "Osaka", "job": "student"})
	// Saving the diary profile keeps the Status Message.
	intro := `{"location":"Nara","locationSecret":false,"sex":"","sexSecret":false,"birthDay":"","birthDaySecret":false,` +
		`"bloodType":"","bloodTypeSecret":false,"job":"nurse","jobSecret":false}`
	if rec := socialTestRequestAs(t, "dp-owner", http.MethodPost, "/v4/profile/intro/", intro); rec.Code != http.StatusOK {
		t.Fatalf("diary save = %d %s", rec.Code, rec.Body.String())
	}
	got := diaryProfileResult(t, socialTestRequestAs(t, "dp-visitor", http.MethodGet, "/v4/profile/1201", ""))
	checkFields(t, got, map[string]any{"intro": "hello, cherry"})
	// An empty status clears it; the accounts store keeps the field under its JSON name.
	if rec := socialTestRequestAs(t, "dp-owner", http.MethodPost, "/v4/avatar/1201/profile", `{"intro":""}`); rec.Code != http.StatusOK {
		t.Fatalf("clear = %d %s", rec.Code, rec.Body.String())
	}
	cleared := diaryProfileResult(t, socialTestRequestAs(t, "dp-owner", http.MethodGet, "/v4/profile/1201", ""))
	checkFields(t, cleared, map[string]any{"intro": ""})
}

func TestProfilePhotoSaveRoundTrip(t *testing.T) {
	profileEditFixture(t)
	body := `[{"imageId":"7","imageUrl":"https://obs.line-scdn.net/x.jpg","mainImage":true}]` + "\x00"
	if rec := socialTestRequestAs(t, "dp-owner", http.MethodPost, "/v4/profile/image", body); rec.Code != http.StatusOK {
		t.Fatalf("photo save = %d %s", rec.Code, rec.Body.String())
	}
	got := diaryProfileResult(t, socialTestRequestAs(t, "dp-visitor", http.MethodGet, "/v4/diary/profile/image/1201", ""))
	list, _ := got["imageList"].([]any)
	if len(list) != 1 || list[0].(map[string]any)["imageId"] != "7" || list[0].(map[string]any)["mainImage"] != true {
		t.Fatalf("imageList = %v", got["imageList"])
	}
	if rec := socialTestRequestAs(t, "dp-owner", http.MethodPost, "/v4/profile/image", `{"imageId":"7"}`); rec.Code != http.StatusBadRequest {
		t.Errorf("object body = %d, want 400", rec.Code)
	}
	if rec := socialTestRequestAs(t, "", http.MethodPost, "/v4/profile/image", `[]`); rec.Code != http.StatusNotFound {
		t.Errorf("no session = %d, want 404", rec.Code)
	}
}

func TestAvatarProfileNicknameRenamesCopies(t *testing.T) {
	profileEditFixture(t)
	store.SocialMu.Lock()
	store.DiaryPosts = append(store.DiaryPosts, store.DiaryPost{DiaryNo: "77", AvatarID: "1201", Nickname: "Old"})
	store.GuestbookEntries = append(store.GuestbookEntries, store.SavedGuestbookEntry{GuestAvtNo: "1201", GuestNickname: "Old"})
	store.SocialMu.Unlock()
	if rec := socialTestRequestAs(t, "dp-owner", http.MethodPost, "/v4/avatar/1201/profile", `{"name":"Hana","intro":""}`); rec.Code != http.StatusOK {
		t.Fatalf("save = %d %s", rec.Code, rec.Body.String())
	}
	store.SocialMu.Lock()
	defer store.SocialMu.Unlock()
	if p := store.DiaryPosts[len(store.DiaryPosts)-1]; p.Nickname != "Hana" {
		t.Errorf("diary post nickname = %q, want Hana", p.Nickname)
	}
	if g := store.GuestbookEntries[len(store.GuestbookEntries)-1]; g.GuestNickname != "Hana" {
		t.Errorf("guestbook nickname = %q, want Hana", g.GuestNickname)
	}
}

func TestAvatarProfileStatusSurvivesReload(t *testing.T) {
	profileEditFixture(t)
	path := filepath.Join(t.TempDir(), "accounts.json")
	store.AccountsMu.Lock()
	store.AccountStorePath, store.NextAvatarID, store.LatestAcc = path, 1300, store.Accounts["dp-owner"]
	store.AccountsMu.Unlock()
	if rec := socialTestRequestAs(t, "dp-owner", http.MethodPost, "/v4/avatar/1201/profile", `{"intro":"kept"}`); rec.Code != http.StatusOK {
		t.Fatalf("save = %d %s", rec.Code, rec.Body.String())
	}
	if err := store.LoadAccountsFrom(path); err != nil {
		t.Fatal(err)
	}
	store.AccountsMu.Lock()
	acc := store.Accounts["dp-owner"]
	store.AccountsMu.Unlock()
	if acc == nil || acc.DiaryProfile == nil || acc.DiaryProfile.StatusMessage != "kept" {
		t.Fatalf("reloaded owner = %+v, want status kept", acc)
	}
}

func TestAvatarProfileStatusRejectsBadInput(t *testing.T) {
	profileEditFixture(t)
	bad := map[string]string{
		"number":       `{"intro":5}`,
		"too long":     `{"intro":"` + strings.Repeat("あ", 51) + `"}`,
		"control char": `{"intro":"a\u0001b"}`,
		"newline":      `{"intro":"a\nb"}`,
		"not json":     `{"intro":`,
	}
	for name, body := range bad {
		if rec := socialTestRequestAs(t, "dp-owner", http.MethodPost, "/v4/avatar/1201/profile", body); rec.Code != http.StatusBadRequest {
			t.Errorf("%s = %d %s, want 400", name, rec.Code, rec.Body.String())
		}
	}
	// Fifty characters (multi-byte) is the limit and is accepted.
	fifty := `{"intro":"` + strings.Repeat("あ", 50) + `"}`
	if rec := socialTestRequestAs(t, "dp-owner", http.MethodPost, "/v4/avatar/1201/profile", fifty); rec.Code != http.StatusOK {
		t.Errorf("fifty characters = %d %s, want 200", rec.Code, rec.Body.String())
	}
	if rec := socialTestRequestAs(t, "dp-owner", http.MethodPost, "/v4/avatar/1202/profile", `{"intro":"x"}`); rec.Code != http.StatusNotFound {
		t.Errorf("another avatar = %d, want 404", rec.Code)
	}
	if rec := socialTestRequestAs(t, "", http.MethodPost, "/v4/avatar/1201/profile", `{"intro":"x"}`); rec.Code != http.StatusNotFound {
		t.Errorf("no session = %d, want 404", rec.Code)
	}
	if rec := socialTestRequestAs(t, "dp-owner", http.MethodGet, "/v4/avatar/1201/profile", ""); rec.Code != http.StatusNotFound {
		t.Errorf("GET = %d, want 404", rec.Code)
	}
	// The rejected saves leave the stored status untouched.
	got := diaryProfileResult(t, socialTestRequestAs(t, "dp-owner", http.MethodGet, "/v4/profile/1201", ""))
	checkFields(t, got, map[string]any{"intro": strings.Repeat("あ", 50)})
}

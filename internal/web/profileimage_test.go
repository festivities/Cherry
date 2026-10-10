package web

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"cherry/internal/store"
)

// TestProfilePictureFlow follows the client: two OBS uploads, then avatar/profile/image,
// then the paths come back in the profile row and the friend sync, and download works.
func TestProfilePictureFlow(t *testing.T) {
	t.Setenv("LocalAppData", t.TempDir()) // UserCacheDir on Windows
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	acc := &store.Account{Aid: "9601", Name: "Pic"}
	friend := &store.Account{Aid: "9602", Name: "Pal"}
	installSocialTestAccounts(t, map[string]*store.Account{"ptok": acc, "ftok": friend})
	store.ResetSocial()
	t.Cleanup(store.ResetSocial)
	store.SocialMu.Lock()
	store.Friendships = append(store.Friendships, store.Friendship{A: "9601", B: "9602", State: "accepted"})
	store.SocialMu.Unlock()

	const user, ctime = "9601", "1760000000"
	oid := user + "_" + ctime
	params, _ := json.Marshal(diaryImageUploadParams{Version: "1.0", Type: "image", Name: oid, UserID: user, OID: oid, CTime: ctime})
	upload := func(token string, shade uint8) (*httptest.ResponseRecorder, []byte) {
		data := diaryTestImage(t, "png", 4, 3, shade)
		req := diaryUploadRequest(t, params, data, "image/png")
		req.URL.Path = "/lineplay/pr/upload.nhn"
		if token != "" {
			req.Header.Set("Cookie", `AV_AUTH="`+token+`"`)
		}
		rec := httptest.NewRecorder()
		NewMux().ServeHTTP(rec, req)
		return rec, data
	}
	// Only the owner may upload: no session or another account is refused.
	for _, tok := range []string{"", "ftok"} {
		if rec, _ := upload(tok, 7); rec.Code != http.StatusBadRequest {
			t.Fatalf("upload by %q = %d", tok, rec.Code)
		}
	}
	rec, data := upload("ptok", 5)
	if rec.Code != 200 {
		t.Fatalf("head upload = %d %s", rec.Code, rec.Body.String())
	}
	// The whole-body image usually shares the head's second (same oid): acknowledged, head kept.
	if rec, _ := upload("ptok", 9); rec.Code != 200 {
		t.Fatalf("same-second upload = %d %s", rec.Code, rec.Body.String())
	}
	for _, p := range []string{"/lineplay/pr/" + oid, "/r/lineplay/pr/" + oid} {
		g := decorReq(t, "", http.MethodGet, p, "")
		if g.Code != 200 || !bytes.Equal(g.Body.Bytes(), data) || g.Header().Get("Content-Type") != "image/png" {
			t.Fatalf("GET %s = %d ct=%s", p, g.Code, g.Header().Get("Content-Type"))
		}
	}
	if g := decorReq(t, "", http.MethodGet, "/lineplay/pr/nope", ""); g.Code != 404 {
		t.Fatalf("bad oid = %d", g.Code)
	}
	if g := decorReq(t, "", http.MethodGet, "/lineplay/pr/9601_1", ""); g.Code != 404 {
		t.Fatalf("unknown oid = %d", g.Code)
	}

	path := "/lineplay/pr/" + oid
	for _, bad := range []string{
		`{"imageUrl":"","wbImageUrl":"","bgUrl":""}`,
		`{"imageUrl":"/lineplay/pr/../../x","wbImageUrl":"","bgUrl":""}`,
		`{"imageUrl":"` + path + `","wbImageUrl":"","bgUrl":"../x.jpg"}`,
	} {
		if r := decorReq(t, "ptok", http.MethodPost, "/v4/avatar/profile/image", bad); r.Code != 400 {
			t.Fatalf("bad save %s = %d", bad, r.Code)
		}
	}
	if r := decorReq(t, "ftok", http.MethodPost, "/v4/avatar/profile/image", `{"imageUrl":"`+path+`"}`); r.Code != 400 {
		t.Fatalf("saving another player's picture = %d", r.Code)
	}
	if r := decorReq(t, "nobody", http.MethodPost, "/v4/avatar/profile/image", `{"imageUrl":"`+path+`"}`); r.Code != 404 {
		t.Fatalf("unknown session = %d", r.Code)
	}
	body := `{"imageUrl":"` + path + `","wbImageUrl":"` + path + `","bgUrl":"profilebg/13.jpg"}`
	if r := decorReq(t, "ptok", http.MethodPost, "/v4/avatar/profile/image", body); r.Code != 200 || r.Body.String() != `{"result":true}` {
		t.Fatalf("save = %d %s", r.Code, r.Body.String())
	}
	if acc.ProfileImage.Image != path || acc.ProfileImage.Bg != "profilebg/13.jpg" {
		t.Fatalf("profile not stored: %+v", acc.ProfileImage)
	}

	var prof struct {
		Result map[string]any `json:"result"`
	}
	if r := decorReq(t, "ftok", http.MethodGet, "/v4/profile/9601", ""); r.Code != 200 || json.Unmarshal(r.Body.Bytes(), &prof) != nil ||
		prof.Result["obsProfileImagePath"] != path || prof.Result["obsWholeBodyProfileImagePath"] != path || prof.Result["bg"] != "profilebg/13.jpg" {
		t.Fatalf("profile = %d %s", r.Code, r.Body.String())
	}
	if r := decorReq(t, "ptok", http.MethodGet, "/v4/profile/9602", ""); strings.Contains(r.Body.String(), "obsProfileImagePath") {
		t.Fatalf("account without a picture must not carry the keys: %s", r.Body.String())
	}
	sync := decorReq(t, "ftok", http.MethodGet, "/v4/sync/friends/0", "")
	if !strings.Contains(sync.Body.String(), `"obsProfileImagePath":"`+path+`"`) {
		t.Fatalf("friend sync lacks the picture: %s", sync.Body.String())
	}
}

func TestProfileImagePersists(t *testing.T) {
	st := store.StoredFrom(&store.Account{Aid: "1", ProfileImage: store.ProfileImage{Image: "/lineplay/pr/a_1"}})
	if st.ProfileImage == nil || st.ProfileImage.Image != "/lineplay/pr/a_1" {
		t.Fatalf("StoredFrom = %+v", st.ProfileImage)
	}
	if store.StoredFrom(&store.Account{Aid: "2"}).ProfileImage != nil {
		t.Fatal("empty profile image must be omitted")
	}
}

package web

import (
	"bytes"
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

// TestProfilePictureFlow follows the client: two OBS uploads, then avatar/profile/image,
// then the paths come back in the profile row and the friend sync, and download works.
func TestProfilePictureFlow(t *testing.T) {
	testutil.UseDatabase(t)
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
	// The friend cell draws only with a non-empty profileImageUrl (NaBaseFriendCell::SetAvatarImage 0x1c4431c); the own
	// "Profile" row reads the top-level myProfileImageUrl + obsProfileImagePath.
	var fs struct {
		Result struct {
			MyProfileImageURL   string `json:"myProfileImageUrl"`
			ObsProfileImagePath string `json:"obsProfileImagePath"`
			MyAvatarName        string `json:"myAvatarName"`
			Mid                 string `json:"mid"`
			BuddyList           []struct {
				AvatarNo, ProfileImageURL, ObsProfileImagePath string
			} `json:"buddyList"`
		} `json:"result"`
	}
	if err := json.Unmarshal(sync.Body.Bytes(), &fs); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, b := range fs.Result.BuddyList {
		if b.AvatarNo == "9601" {
			found = b.ProfileImageURL == path && b.ObsProfileImagePath == path
		}
	}
	if !found || fs.Result.MyProfileImageURL != "" || fs.Result.ObsProfileImagePath != "" || fs.Result.Mid != "9602" {
		t.Fatalf("friend's sync rows/own keys wrong: %s", sync.Body.String())
	}
	own := decorReq(t, "ptok", http.MethodGet, "/v4/sync/friends/0", "")
	fs.Result.MyProfileImageURL, fs.Result.ObsProfileImagePath = "", ""
	if err := json.Unmarshal(own.Body.Bytes(), &fs); err != nil || fs.Result.MyProfileImageURL != path ||
		fs.Result.ObsProfileImagePath != path || fs.Result.Mid != "9601" || fs.Result.MyAvatarName == "" {
		t.Fatalf("own sync lacks the own picture: %s", own.Body.String())
	}
}

// TestProfileCellPicture: the home MENU "Profile" cell draws the photo only with result.profile and a non-empty result.bg
// (profilecell-spec.md, 2026-11-22). A picture without a background gets the Cherry fallback profilebg/12.jpg, a stored
// background is sent as it is, and an account without a picture has no profile key.
func TestProfileCellPicture(t *testing.T) {
	testutil.UseDatabase(t)
	installSocialTestAccounts(t, map[string]*store.Account{
		"pc-nobg":  {Aid: "9701", Name: "NoBg", ProfileImage: store.ProfileImage{Image: "/lineplay/pr/9701_1"}},
		"pc-bg":    {Aid: "9702", Name: "Bg", ProfileImage: store.ProfileImage{Image: "/lineplay/pr/9702_1", Bg: "profilebg/3.jpg"}},
		"pc-plain": {Aid: "9703", Name: "Plain"},
	})
	profile := func(aid string) map[string]any {
		t.Helper()
		rec := decorReq(t, "", http.MethodGet, "/v4/profile/"+aid+"?deviceType=Android", "")
		var doc struct {
			Result map[string]any `json:"result"`
		}
		if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &doc) != nil {
			t.Fatalf("profile %s = %d %s", aid, rec.Code, rec.Body.String())
		}
		return doc.Result
	}
	if got := profile("9701"); got["profile"] != "/lineplay/pr/9701_1" || got["bg"] != "profilebg/12.jpg" {
		t.Fatalf("picture without a background: profile=%v bg=%v", got["profile"], got["bg"])
	}
	if got := profile("9702"); got["profile"] != "/lineplay/pr/9702_1" || got["bg"] != "profilebg/3.jpg" {
		t.Fatalf("picture with a background: profile=%v bg=%v", got["profile"], got["bg"])
	}
	if got := profile("9703"); got["profile"] != nil {
		t.Fatalf("account without a picture has profile=%v", got["profile"])
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

func TestProfilePicturesAcrossSocialRoutes(t *testing.T) {
	installSocialTestAccounts(t, map[string]*store.Account{
		"viewer-tok": {
			AccessToken: "viewer-tok", Aid: "9801", Name: "Viewer", Country: "JP",
			ProfileImage: store.ProfileImage{Image: "/lineplay/pr/9801_1"},
			Pins:         []string{"9802"},
		},
		"friend-tok": {
			AccessToken: "friend-tok", Aid: "9802", Name: "FriendPic", Country: "JP",
			ProfileImage: store.ProfileImage{Image: "/lineplay/pr/9802_1", WholeBody: "/lineplay/pr/9802_2", Bg: "profilebg/14.jpg"},
		},
		"plain-tok": {
			AccessToken: "plain-tok", Aid: "9803", Name: "PlainFriend", Country: "JP",
		},
	})
	store.ResetSocial()
	t.Cleanup(store.ResetSocial)
	store.SocialMu.Lock()
	store.Friendships = []store.Friendship{{A: "9801", B: "9802", State: "accepted"}}
	store.SocialMu.Unlock()

	// 1. Diary intro (GET /v4/diary2/intro/<aid>): profile, bg, obsProfileImagePath.
	rec := decorReq(t, "viewer-tok", http.MethodGet, "/v4/diary2/intro/9802", "")
	var introDoc struct {
		Result map[string]any `json:"result"`
	}
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &introDoc) != nil {
		t.Fatalf("diary intro 9802 = %d %s", rec.Code, rec.Body.String())
	}
	if introDoc.Result["profile"] != "/lineplay/pr/9802_1" || introDoc.Result["bg"] != "profilebg/14.jpg" || introDoc.Result["obsProfileImagePath"] != "/lineplay/pr/9802_1" {
		t.Fatalf("diary intro 9802 = %+v", introDoc.Result)
	}
	rec = decorReq(t, "viewer-tok", http.MethodGet, "/v4/diary2/intro/9801", "")
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &introDoc) != nil {
		t.Fatalf("diary intro 9801 = %d %s", rec.Code, rec.Body.String())
	}
	if introDoc.Result["profile"] != "/lineplay/pr/9801_1" || introDoc.Result["bg"] != store.DefaultProfileBg || introDoc.Result["obsProfileImagePath"] != "/lineplay/pr/9801_1" {
		t.Fatalf("diary intro 9801 fallback = %+v", introDoc.Result)
	}

	// 2. Friend sync (GET /v4/sync/friends/0): own + buddy bg and picture gate fields.
	rec = decorReq(t, "viewer-tok", http.MethodGet, "/v4/sync/friends/0", "")
	var syncDoc struct {
		Result struct {
			MyProfileImageURL   string     `json:"myProfileImageUrl"`
			ObsProfileImagePath string     `json:"obsProfileImagePath"`
			Bg                  string     `json:"bg"`
			BuddyList           []buddyRow `json:"buddyList"`
		} `json:"result"`
	}
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &syncDoc) != nil {
		t.Fatalf("sync friends = %d %s", rec.Code, rec.Body.String())
	}
	if syncDoc.Result.MyProfileImageURL != "/lineplay/pr/9801_1" || syncDoc.Result.ObsProfileImagePath != "/lineplay/pr/9801_1" || syncDoc.Result.Bg != store.DefaultProfileBg {
		t.Fatalf("sync own = %+v", syncDoc.Result)
	}
	found9802 := false
	for _, b := range syncDoc.Result.BuddyList {
		if b.AvatarNo == "9802" {
			found9802 = true
			if b.ProfileImageURL != "/lineplay/pr/9802_1" || b.ObsProfileImagePath != "/lineplay/pr/9802_1" || b.Bg != "profilebg/14.jpg" {
				t.Fatalf("sync buddy 9802 = %+v", b)
			}
		}
	}
	if !found9802 {
		t.Fatal("buddy 9802 missing from friend sync")
	}

	// 3. Friend search (POST /v4/square/friends/search).
	rec = decorReq(t, "viewer-tok", http.MethodPost, "/v4/square/friends/search?page=0&size=10", `{"country":"JP","caricNickName":"FriendPic","searchType":"NICK"}`)
	var searchDoc struct {
		Result struct {
			Items []friendSearchItem `json:"items"`
		} `json:"result"`
	}
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &searchDoc) != nil || len(searchDoc.Result.Items) != 1 {
		t.Fatalf("friend search = %d %s", rec.Code, rec.Body.String())
	}
	it := searchDoc.Result.Items[0]
	if it.ProfileURL != "/lineplay/pr/9802_1" || it.ObsProfileImagePath != "/lineplay/pr/9802_1" || it.WholeBodyProfileURL != "/lineplay/pr/9802_2" || it.BG != "profilebg/14.jpg" {
		t.Fatalf("friend search item = %+v", it)
	}

	// 4. My Pins (GET /v4/pin/list).
	rec = decorReq(t, "viewer-tok", http.MethodGet, "/v4/pin/list", "")
	var pinDoc struct {
		Result struct {
			PinList []pinRow `json:"pinList"`
		} `json:"result"`
	}
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &pinDoc) != nil || len(pinDoc.Result.PinList) != 1 {
		t.Fatalf("pin list = %d %s", rec.Code, rec.Body.String())
	}
	pr := pinDoc.Result.PinList[0]
	if pr.ProfileImageURL != "/lineplay/pr/9802_1" || pr.ObsProfileImageURL != "/lineplay/pr/9802_1" || pr.ProfileBgURL != "profilebg/14.jpg" {
		t.Fatalf("pin row = %+v", pr)
	}
}

// TestAvatarInfoProfilePicture verifies GET /v4/avatar/<aid> serves profile, obsProfileImagePath, and
// obsWholeBodyProfileImagePath (falling back to Image), while intentionally omitting bg (spec §2.2),
// and omitting all three keys when the account has no saved profile picture.
func TestAvatarInfoProfilePicture(t *testing.T) {
	installSocialTestAccounts(t, map[string]*store.Account{
		"av-full": {
			AccessToken: "av-full", Aid: "9811", Name: "Full", Country: "JP",
			ProfileImage: store.ProfileImage{Image: "/lineplay/pr/9811_1", WholeBody: "/lineplay/pr/9811_2", Bg: "profilebg/14.jpg"},
		},
		"av-fallback": {
			AccessToken: "av-fallback", Aid: "9812", Name: "Fallback", Country: "JP",
			ProfileImage: store.ProfileImage{Image: "/lineplay/pr/9812_1"},
		},
		"av-plain": {
			AccessToken: "av-plain", Aid: "9813", Name: "Plain", Country: "JP",
		},
	})

	getAvatar := func(aid string) map[string]any {
		t.Helper()
		rec := decorReq(t, "", http.MethodGet, "/v4/avatar/"+aid, "")
		var doc struct {
			Result map[string]any `json:"result"`
		}
		if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &doc) != nil {
			t.Fatalf("GET /v4/avatar/%s = %d %s", aid, rec.Code, rec.Body.String())
		}
		return doc.Result
	}

	full := getAvatar("9811")
	if full["profile"] != "/lineplay/pr/9811_1" ||
		full["obsProfileImagePath"] != "/lineplay/pr/9811_1" ||
		full["obsWholeBodyProfileImagePath"] != "/lineplay/pr/9811_2" {
		t.Fatalf("full avatar profile fields = %+v", full)
	}
	if _, hasBg := full["bg"]; hasBg {
		t.Fatalf("GET /v4/avatar/9811 must intentionally omit bg, got %+v", full)
	}

	fb := getAvatar("9812")
	if fb["profile"] != "/lineplay/pr/9812_1" ||
		fb["obsProfileImagePath"] != "/lineplay/pr/9812_1" ||
		fb["obsWholeBodyProfileImagePath"] != "/lineplay/pr/9812_1" {
		t.Fatalf("fallback avatar profile fields = %+v", fb)
	}
	if _, hasBg := fb["bg"]; hasBg {
		t.Fatalf("GET /v4/avatar/9812 must intentionally omit bg, got %+v", fb)
	}

	plain := getAvatar("9813")
	for _, k := range []string{"profile", "obsProfileImagePath", "obsWholeBodyProfileImagePath", "bg"} {
		if _, ok := plain[k]; ok {
			t.Fatalf("plain avatar must omit %s, got %+v", k, plain)
		}
	}
}

// TestProfileMiniPopupStats verifies friendsCount, heartCount, myGardenAvatarInfo on GET /v4/profile/<aid>,
// and todayVisitor/totalVisitor on GET /v4/diary2/intro/<aid>.
func TestProfileMiniPopupStats(t *testing.T) {
	now := time.Date(2026, time.November, 24, 1, 0, 0, 0, time.UTC)
	prevNow := economy.NowFunc
	economy.NowFunc = func() time.Time { return now }
	t.Cleanup(func() { economy.NowFunc = prevNow })

	today := economy.AttendDayKey(now)
	installSocialTestAccounts(t, map[string]*store.Account{
		"p-viewer": {AccessToken: "p-viewer", Aid: "9821", Name: "Viewer"},
		"p-target": {
			AccessToken: "p-target", Aid: "9822", Name: "Target",
			ProfileImage: store.ProfileImage{Image: "/lineplay/pr/9822_1"},
			Hearts:       &store.Hearts{Day: today, Received: 7},
			GardenStats: store.GardenStats{
				Exp:         250, // level 3
				Footprints:  12,
				TodayDay:    today,
				TodayVisits: 4,
				TodaySeen:   []string{"9821"},
				UserPlanet:  2,
			},
		},
		"p-stale": {
			AccessToken: "p-stale", Aid: "9823", Name: "Stale",
			GardenStats: store.GardenStats{
				Footprints:  5,
				TodayDay:    today - 1,
				TodayVisits: 3,
			},
		},
	})
	store.ResetSocial()
	t.Cleanup(store.ResetSocial)
	store.SocialMu.Lock()
	store.Friendships = []store.Friendship{
		{A: "9821", B: "9822", State: "accepted"},
		{A: "9822", B: "9823", State: "accepted"},
		{A: "9822", B: "9824", State: "pending"},
		{A: "9825", B: "9822", State: "removed"},
	}
	store.SocialMu.Unlock()

	getMap := func(path string) map[string]any {
		t.Helper()
		rec := decorReq(t, "p-viewer", http.MethodGet, path, "")
		var doc struct {
			Result map[string]any `json:"result"`
		}
		if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &doc) != nil {
			t.Fatalf("GET %s = %d %s", path, rec.Code, rec.Body.String())
		}
		return doc.Result
	}

	prof := getMap("/v4/profile/9822?deviceType=Android")
	// 2 accepted friendships + 1 for synthetic Friend (100000) = 3.
	if prof["friendsCount"] != float64(3) {
		t.Fatalf("friendsCount = %v, want 3", prof["friendsCount"])
	}
	if prof["heartCount"] != float64(7) {
		t.Fatalf("heartCount = %v, want 7", prof["heartCount"])
	}
	if prof["obsWholeBodyProfileImagePath"] != "/lineplay/pr/9822_1" || prof["bg"] != store.DefaultProfileBg {
		t.Fatalf("profile image fields = %+v", prof)
	}
	garden, ok := prof["myGardenAvatarInfo"].(map[string]any)
	if !ok || garden["level"] != float64(3) || garden["footprintCount"] != float64(12) || garden["userPlanet"] != float64(2) {
		t.Fatalf("myGardenAvatarInfo = %#v", prof["myGardenAvatarInfo"])
	}

	// Synthetic Friend 100000 gets default myGardenAvatarInfo (level 1) and does not self-count.
	friendProf := getMap("/v4/profile/100000?deviceType=Android")
	if friendProf["friendsCount"] != float64(0) {
		t.Fatalf("100000 friendsCount = %v, want 0", friendProf["friendsCount"])
	}
	fg, ok := friendProf["myGardenAvatarInfo"].(map[string]any)
	if !ok || fg["level"] != float64(1) || fg["footprintCount"] != float64(0) || fg["userPlanet"] != float64(0) {
		t.Fatalf("100000 myGardenAvatarInfo = %#v", friendProf["myGardenAvatarInfo"])
	}

	// Removing Friend drops friendsCount from 3 to 2 for real accounts.
	store.SocialMu.Lock()
	store.FriendRemoved = true
	store.SocialMu.Unlock()
	if got := getMap("/v4/profile/9822")["friendsCount"]; got != float64(2) {
		t.Fatalf("friendsCount after FriendRemoved = %v, want 2", got)
	}

	// diary2/intro/<aid> wires todayVisitor and totalVisitor.
	intro := getMap("/v4/diary2/intro/9822")
	if intro["todayVisitor"] != float64(4) || intro["totalVisitor"] != float64(12) {
		t.Fatalf("9822 diary intro visitors = (%v, %v), want (4, 12)", intro["todayVisitor"], intro["totalVisitor"])
	}
	staleIntro := getMap("/v4/diary2/intro/9823")
	if staleIntro["todayVisitor"] != float64(0) || staleIntro["totalVisitor"] != float64(5) {
		t.Fatalf("9823 stale diary intro visitors = (%v, %v), want (0, 5)", staleIntro["todayVisitor"], staleIntro["totalVisitor"])
	}
}

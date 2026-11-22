package web

import (
	"crypto/tls"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"cherry/internal/auth"
	"cherry/internal/config"
	"cherry/internal/httpx"
	"cherry/internal/store"
	"cherry/internal/testutil"
)

// PLAN 8.6b part 4, the edge of the game: the plain port's paths, the request limits, the AV_AUTH flags, the catalogue
// check of create/avatar and the guestbook count.

func TestPlainListenerAnswersOnlyTheClientPaths(t *testing.T) {
	for path, want := range map[string]bool{
		"/img/read/arts_item_dress_225000263/dp.png":          true,
		"/img/read/arts_item_garden_428400003/lpitem.aniproj": true,
		"/img/read/notice/notice.json2":                       true,
		"/v1/lineplay/android":                                true,
		"/v1/lineplay/ios":                                    true,
		"/img/read/notice/ios/checkresource2.json":            true,
		// The iOS static base is http://…/img/read/ (Raspberry CONTRACT §4, 2026-11-20).
		"/img/read/arts_animation_ini_00531/update_en.ini":                  true,
		"/img/read/arts_ui_jp_00540/UIImage_hd/x.png":                       true,
		"/img/read/arts_animation_en_00531/../garden/1908231803.mworldpack": true,
		"/img/read/garden/1908231803.mworldpack":                            true,
		"/img/read/circle/2012221744.mworldpack":                            true,
		"/img/read/garden01/items/item110000.png":                           true,
		"/img/read/photozone/frame/a/b.png":                                 true,
		"/img/read/arts_session/sckey.enc":                                  true,
		"/v4/setInitConf":                                                   false,
		"/v4/create/avatar":                                                 false,
		"/arts_item_dress_225000263/dp.png":                                 false, // HTTPS only: the plain client path has /img/read
		"/img/read/v4/setInitConf":                                          false,
		"/img/read/lineplay/d/upload.nhn":                                   false,
		"/img/read/cherry/fish/4901.png":                                    false,
		"/img/read/photozone/x.png":                                         false,
		"/img/read/gardenx/a.mworldpack":                                    false,
		"/lineplay/d/upload.nhn":                                            false,
		"/cherry/fish/4901.png":                                             false,

		// The profile pictures of the home menu and Lounge Chat rows (relative lineplay/pr/<oid>); the upload is not.
		"/img/read/lineplay/pr/18_1795286862": true,
		"/img/read/lineplay/pr/upload.nhn":    false,
		"/lineplay/pr/18_1795286862":          false,
	} {
		if got := plainPathAllowed(path); got != want {
			t.Errorf("plainPathAllowed(%q) = %v, want %v", path, got, want)
		}
	}

	// The game mux answers the setInitConf body; the plain listener does not route it at all.
	rec := httptest.NewRecorder()
	NewMux().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v4/setInitConf?nationCode=JP&appVer=10.1.0.0", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("game mux setInitConf = %d, want 200", rec.Code)
	}
	rec = httptest.NewRecorder()
	NewPlainMux().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v4/setInitConf?nationCode=JP&appVer=10.1.0.0", nil))
	if rec.Code != http.StatusNotFound || rec.Body.String() != httpx.NotFoundBody {
		t.Fatalf("plain setInitConf = %d %q, want the 404 body", rec.Code, rec.Body.String())
	}
}

// The plain listener serves an iOS /img/read/ path with the body the HTTPS route gives without the prefix.
func TestPlainListenerServesTheIOSStaticBase(t *testing.T) {
	webOperatorDir(t)
	plain := NewPlainMux()
	for ios, https := range map[string]string{
		"/img/read/notice/ios/checkresource2.json":         "/notice/adr/checkresource2.json",
		"/img/read/arts_animation_ini_00531/update_en.ini": "/arts_animation_ini_00531/update_en.ini",
	} {
		rec := httptest.NewRecorder()
		plain.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, ios, nil))
		want := serve(t, http.MethodGet, https)
		if rec.Code != http.StatusOK || want.Code != http.StatusOK || rec.Body.String() != want.Body.String() {
			t.Fatalf("plain %s = %d %q, want HTTPS %s = %d %q", ios, rec.Code, rec.Body, https, want.Code, want.Body)
		}
	}
	var root struct {
		IOS map[string]string `json:"ios"`
	}
	if err := json.Unmarshal([]byte(serve(t, http.MethodGet, "/notice/ios/checkresource2.json").Body.String()), &root); err != nil || root.IOS["10.1.0"] != "1" {
		t.Fatalf("ios key: %v %v", root.IOS, err)
	}
	// Android's plain notice path still answers 404.
	rec := httptest.NewRecorder()
	plain.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/img/read/notice/notice.json2", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("notice.json2 = %d, want 404", rec.Code)
	}
}

// The plain listener downloads a profile picture at its iOS path (/img/read/lineplay/pr/<oid>, the relative path the home
// menu and Lounge Chat rows carry) and refuses the upload there: the owner's POST to upload.nhn stores nothing.
func TestPlainListenerServesProfilePictures(t *testing.T) {
	testutil.UseDatabase(t)
	t.Setenv("LocalAppData", t.TempDir()) // UserCacheDir on Windows
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	installSocialTestAccounts(t, map[string]*store.Account{"ptok": {Aid: "9603", Name: "Plain"}})
	const user = "9603"
	upload := func(ctime string, target string) (*httptest.ResponseRecorder, []byte) {
		oid := user + "_" + ctime
		params, _ := json.Marshal(diaryImageUploadParams{Version: "1.0", Type: "image", Name: oid, UserID: user, OID: oid, CTime: ctime})
		data := diaryTestImage(t, "png", 4, 3, 6)
		req := diaryUploadRequest(t, params, data, "image/png")
		req.URL.Path = target
		req.Header.Set("Cookie", `AV_AUTH="ptok"`)
		rec := httptest.NewRecorder()
		if target == "/img/read/lineplay/pr/upload.nhn" {
			NewPlainMux().ServeHTTP(rec, req)
		} else {
			NewMux().ServeHTTP(rec, req)
		}
		return rec, data
	}
	rec, data := upload("1760000000", "/lineplay/pr/upload.nhn")
	if rec.Code != http.StatusOK {
		t.Fatalf("owner upload on the game port = %d %s", rec.Code, rec.Body.String())
	}
	oid := user + "_1760000000"
	got := httptest.NewRecorder()
	NewPlainMux().ServeHTTP(got, httptest.NewRequest(http.MethodGet, "/img/read/lineplay/pr/"+oid, nil))
	if got.Code != http.StatusOK || got.Body.String() != string(data) || got.Header().Get("Content-Type") != "image/png" {
		t.Fatalf("plain GET of the picture = %d ct=%s, want the stored image", got.Code, got.Header().Get("Content-Type"))
	}
	// The upload path is refused on the plain port (the picture is not stored either).
	if rec, _ := upload("1760000001", "/img/read/lineplay/pr/upload.nhn"); rec.Code != http.StatusNotFound {
		t.Fatalf("plain POST upload.nhn = %d, want 404", rec.Code)
	}
	if g := decorReq(t, "", http.MethodGet, "/lineplay/pr/"+user+"_1760000001", ""); g.Code != http.StatusNotFound {
		t.Fatalf("a picture uploaded through the plain port exists: %d", g.Code)
	}
}

func TestAVAuthCookieFlags(t *testing.T) {
	plain := httptest.NewRequest(http.MethodGet, "/v4/checkSession", nil)
	got := avAuthSetCookie("tok", plain)
	for _, want := range []string{`AV_AUTH="tok"`, "Path=/", "HttpOnly", "SameSite=Lax"} {
		if !strings.Contains(got, want) {
			t.Errorf("cookie %q lacks %q", got, want)
		}
	}
	if strings.Contains(got, "Secure") {
		t.Errorf("a plain request set Secure: %q", got)
	}
	tlsReq := httptest.NewRequest(http.MethodGet, "https://fapi.example/v4/checkSession", nil)
	tlsReq.TLS = &tls.ConnectionState{}
	if got := avAuthSetCookie("tok", tlsReq); !strings.HasSuffix(got, "; Secure") {
		t.Errorf("a TLS request cookie = %q, want Secure", got)
	}
}

// catalogueCodes collects every item code the two creation catalogue handlers serve (cd and elementCodes fields).
func catalogueCodes(t *testing.T, body string) []string {
	t.Helper()
	var out []string
	var walk func(v any)
	walk = func(v any) {
		switch x := v.(type) {
		case map[string]any:
			for k, val := range x {
				if s, ok := val.(string); ok && k == "cd" {
					out = append(out, s)
				}
				if list, ok := val.([]any); ok && k == "elementCodes" {
					for _, e := range list {
						if s, ok := e.(string); ok {
							out = append(out, s)
						}
					}
				}
				walk(val)
			}
		case []any:
			for _, e := range x {
				walk(e)
			}
		}
	}
	var doc any
	if err := json.Unmarshal([]byte(body), &doc); err != nil {
		t.Fatalf("catalogue body is not JSON: %v", err)
	}
	walk(doc)
	return out
}

// TestCreateAvatarCodesAreTheServedCatalogue: every code the creation catalogue serves for a sex (read from the two
// handlers' own answers, not from the data file) and every stock base item is granted for that sex; a code the
// catalogue does not list is refused.
func TestCreateAvatarCodesAreTheServedCatalogue(t *testing.T) {
	var all struct {
		Result map[string]json.RawMessage `json:"result"`
	}
	rec := httptest.NewRecorder()
	handleCreateAllItems(rec, httptest.NewRequest(http.MethodGet, "/v4/create/all/items", nil))
	if err := json.Unmarshal(rec.Body.Bytes(), &all); err != nil {
		t.Fatal(err)
	}
	var roll struct {
		Result []struct {
			AvatarType string          `json:"avatarType"`
			SetItem    json.RawMessage `json:"setItem"`
		} `json:"result"`
	}
	rec = httptest.NewRecorder()
	handleCreateFaceSetItemsRoll(rec, httptest.NewRequest(http.MethodGet, "/v4/create/face/setitems/roll", nil))
	if err := json.Unmarshal(rec.Body.Bytes(), &roll); err != nil {
		t.Fatal(err)
	}
	for sex, key := range map[string]string{"MALE": "M", "FEMALE": "F", "ANIMAL": "A"} {
		served := catalogueCodes(t, `{"x":`+string(all.Result[key])+`}`)
		for _, entry := range roll.Result {
			if entry.AvatarType == sex {
				served = append(served, catalogueCodes(t, `{"x":`+string(entry.SetItem)+`}`)...)
			}
		}
		if len(served) < 10 {
			t.Fatalf("%s: the catalogue serves %d codes; expected the real list", sex, len(served))
		}
		if n := unlistedCreationCodes(served); n != 0 {
			t.Errorf("%s: %d codes the catalogue serves are refused", sex, n)
		}
		if n := unlistedCreationCodes(nativeBaseItems); n != 0 {
			t.Errorf("%s: %d stock base items refused", sex, n)
		}
	}
	// A real stock creation (production aid 11, 2026-11-16) sent these face parts: they must pass.
	if n := unlistedCreationCodes([]string{"CUEB00001", "CUEY00002", "CUHE0000L", "CUMO00002", "CUNO00001"}); n != 0 {
		t.Errorf("stock creation face parts refused: %d", n)
	}
	if n := unlistedCreationCodes([]string{"CUTO0011X", "123456789", "CUHA0036Z"}); n != 2 {
		t.Errorf("unlisted = %d, want 2 (a number and a closet original are not in the creation catalogue)", n)
	}
}

// TestRequestLimitsPerAddressAndAccount: the game bucket refuses past its burst with 429, static GETs are exempt, the
// guest bucket is separate, and an account's bucket follows its AV_AUTH token.
func TestRequestLimitsPerAddressAndAccount(t *testing.T) {
	// SwapAccountsForTest keys the accounts by their token.
	testutil.InstallAccounts(t, map[string]*store.Account{"lim-token": {AccessToken: "lim-token", Aid: "41", Name: "Lim"}})
	cfg := config.Config{APIRate: 1, APIBurst: 2, AccountRate: 1, AccountBurst: 1, GuestPerMinute: 1}
	h := newLimited(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }), cfg)
	get := func(path, remote string, cookie string) int {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.RemoteAddr = remote
		if cookie != "" {
			req.AddCookie(&http.Cookie{Name: "AV_AUTH", Value: cookie})
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code
	}
	for i := range 2 {
		if c := get("/v4/profile/41", "192.0.2.10:1000", ""); c != http.StatusOK {
			t.Fatalf("request %d of the burst = %d", i+1, c)
		}
	}
	if c := get("/v4/profile/41", "192.0.2.10:1001", ""); c != http.StatusTooManyRequests {
		t.Fatalf("request past the address burst = %d, want 429", c)
	}
	if c := get("/v4/profile/41", "192.0.2.11:1000", ""); c != http.StatusOK {
		t.Fatalf("another address shares the bucket: %d", c)
	}
	for range 20 {
		if c := get("/arts_item_dress_1/dp.png", "192.0.2.10:1002", ""); c != http.StatusOK {
			t.Fatalf("a static GET was limited: %d", c)
		}
	}
	// The account bucket: one token, then 429 for the same account from any address.
	if c := get("/v4/checkSession", "192.0.2.20:1", "lim-token"); c != http.StatusOK {
		t.Fatalf("first account request = %d", c)
	}
	if c := get("/v4/checkSession", "192.0.2.21:1", "lim-token"); c != http.StatusTooManyRequests {
		t.Fatalf("second account request from another address = %d, want 429", c)
	}
	// guest/generate has its own bucket per address.
	if c := get("/v4/account/guest/generate", "192.0.2.30:1", ""); c != http.StatusOK {
		t.Fatalf("first guest call = %d", c)
	}
	if c := get("/v4/account/guest/generate", "192.0.2.30:2", ""); c != http.StatusTooManyRequests {
		t.Fatalf("second guest call = %d, want 429", c)
	}
}

// TestRequestLimitsOffInLab: with the lab's zero rates nothing is refused (the lab keeps its behaviour).
func TestRequestLimitsOffInLab(t *testing.T) {
	h := newLimited(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }), config.Config{})
	for range 500 {
		req := httptest.NewRequest(http.MethodGet, "/v4/profile/41", nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("lab request refused: %d", rec.Code)
		}
	}
}

func TestGuestbookCountClearNeedsASession(t *testing.T) {
	testutil.InstallAccounts(t, map[string]*store.Account{"host": {AccessToken: "host-token", Aid: "31", Name: "Hosty"}})
	req := httptest.NewRequest(http.MethodGet, "/v4/guestbookcount/clear/31", nil)
	rec := httptest.NewRecorder()
	handleGuestbookCountClear(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("anonymous clear = %d, want 404 (no session, no marker change)", rec.Code)
	}
}

// TestCreateAvatarRefusesAnUnlistedCodeInIdentityMode: in discord and dev-fake a creation that names an item the creation
// catalogue does not serve gets the 400 every malformed creation gets, and makes no avatar; a catalogue code still works.
func TestCreateAvatarRefusesAnUnlistedCodeInIdentityMode(t *testing.T) {
	cfg := devFakeConfig()
	useConfig(t, cfg)
	guestAccountTest(t)
	svc, err := auth.New(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	tok := guestGenerateCode(t, devSignUp(t, NewGameMux(svc), "idc"))
	av := avAuthValue(t, createSession(t, tok))
	rec := createAvatar(t, av, []byte(`{"name":"Eve","avatarType":"FEMALE","nationCode":"JP","skinColor":"1","itemCodes":["CAFE0000Z"]}`), "")
	if rec.Code != http.StatusBadRequest || rec.Body.String() != httpx.BadRequestBody {
		t.Fatalf("unlisted code = %d %s, want the 400 body", rec.Code, rec.Body.String())
	}
	withAccount(t, tok, func(acc *store.Account) {
		if acc.Aid != "0" {
			t.Fatalf("a refused creation gave the account aid %s", acc.Aid)
		}
	})
	createAvatarID(t, av, `{"name":"Eve","avatarType":"FEMALE","nationCode":"JP","skinColor":"1","itemCodes":["CUON001CH"]}`)
}

// TestStaticGETsHaveTheirOwnBucket (PLAN 8.6b part 5): static GETs spend their own bucket per address, the game bucket of
// that address is not spent by them, and another address has its own static bucket.
func TestStaticGETsHaveTheirOwnBucket(t *testing.T) {
	cfg := config.Config{StaticRate: 1, StaticBurst: 2, APIRate: 1, APIBurst: 1}
	h := newLimited(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }), cfg)
	get := func(path, remote string) int {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.RemoteAddr = remote
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code
	}
	for i := range 2 {
		if c := get("/arts_item_dress_1/dp.png", "192.0.2.40:1"); c != http.StatusOK {
			t.Fatalf("static GET %d of the burst = %d", i+1, c)
		}
	}
	if c := get("/arts_item_dress_1/dp.png", "192.0.2.40:2"); c != http.StatusTooManyRequests {
		t.Fatalf("static GET past the burst = %d, want 429", c)
	}
	if c := get("/v4/profile/41", "192.0.2.40:3"); c != http.StatusOK {
		t.Fatalf("the game bucket was spent by static GETs: %d", c)
	}
	if c := get("/v4/profile/41", "192.0.2.40:4"); c != http.StatusTooManyRequests {
		t.Fatalf("second game request = %d, want 429 from the game bucket", c)
	}
	if c := get("/arts_item_dress_1/dp.png", "192.0.2.41:1"); c != http.StatusOK {
		t.Fatalf("another address shares the static bucket: %d", c)
	}
}

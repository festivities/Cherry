package main

import (
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type setInitConfResult struct {
	SessionServerInfo string   `json:"sessionServerInfo"`
	StaticDomain      string   `json:"staticDomain"`
	NationCode        string   `json:"nationCode"`
	IsGdprNation      bool     `json:"isGdprNation"`
	SnsLoginUIList    []string `json:"snsLoginUIList"`
	SnsSignUpUIList   []string `json:"snsSignUpUIList"`
}

type setInitConfResponse struct {
	Result *setInitConfResult `json:"result"`
}

func serve(t *testing.T, method, target string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	newMux().ServeHTTP(rec, httptest.NewRequest(method, target, nil))
	return rec
}

func TestSetInitConf(t *testing.T) {
	rec := serve(t, http.MethodGet, "/v4/setInitConf")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json; charset=utf-8" {
		t.Fatalf("Content-Type = %q, want %q", ct, "application/json; charset=utf-8")
	}

	var raw struct {
		Result map[string]json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatalf("body is not valid JSON: %v", err)
	}
	for _, field := range []string{"sessionServerInfo", "staticDomain", "nationCode", "isGdprNation", "snsLoginUIList", "snsSignUpUIList"} {
		if _, ok := raw.Result[field]; !ok {
			t.Errorf("result field %q missing", field)
		}
	}
	if len(raw.Result) != 6 {
		t.Errorf("result has %d fields, want 6", len(raw.Result))
	}

	var body setInitConfResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body decode failed: %v", err)
	}
	if body.Result == nil {
		t.Fatal("result missing")
	}
	want := setInitConfResult{
		SessionServerInfo: "XPN:/p=XTCP;ip=session.play.naver.jp;port=10123",
		StaticDomain:      "https://play-static.line-scdn.net/",
		NationCode:        "JP",
		IsGdprNation:      false,
		SnsLoginUIList:    []string{"LD_GUEST"},
		SnsSignUpUIList:   []string{"LD_GUEST"},
	}
	if body.Result.SessionServerInfo != want.SessionServerInfo {
		t.Errorf("sessionServerInfo = %q, want %q", body.Result.SessionServerInfo, want.SessionServerInfo)
	}
	if body.Result.StaticDomain != want.StaticDomain {
		t.Errorf("staticDomain = %q, want %q", body.Result.StaticDomain, want.StaticDomain)
	}
	if body.Result.NationCode != want.NationCode {
		t.Errorf("nationCode = %q, want %q", body.Result.NationCode, want.NationCode)
	}
	if body.Result.IsGdprNation != want.IsGdprNation {
		t.Errorf("isGdprNation = %v, want %v", body.Result.IsGdprNation, want.IsGdprNation)
	}
	if len(body.Result.SnsLoginUIList) != 1 || body.Result.SnsLoginUIList[0] != "LD_GUEST" {
		t.Errorf("snsLoginUIList = %v, want [LD_GUEST]", body.Result.SnsLoginUIList)
	}
	if len(body.Result.SnsSignUpUIList) != 1 || body.Result.SnsSignUpUIList[0] != "LD_GUEST" {
		t.Errorf("snsSignUpUIList = %v, want [LD_GUEST]", body.Result.SnsSignUpUIList)
	}
}

func TestSetInitConfFixtureByteIdentical(t *testing.T) {
	rec := serve(t, http.MethodGet, "/v4/setInitConf")
	if got := rec.Body.String(); got != setInitConfBody {
		t.Fatalf("body = %q, want %q", got, setInitConfBody)
	}
}

func TestUnknownRoute(t *testing.T) {
	for _, target := range []string{"/", "/v4/checkSession/x", "/v4/setInitConf/nested"} {
		rec := serve(t, http.MethodGet, target)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s: status = %d, want %d", target, rec.Code, http.StatusNotFound)
		}
		if got := rec.Body.String(); got != notFoundBody {
			t.Errorf("%s: body = %q, want %q", target, got, notFoundBody)
		}
		if ct := rec.Header().Get("Content-Type"); ct != "application/json; charset=utf-8" {
			t.Errorf("%s: Content-Type = %q, want %q", target, ct, "application/json; charset=utf-8")
		}
	}
}

func TestWrongMethod(t *testing.T) {
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodHead} {
		rec := serve(t, method, "/v4/setInitConf")
		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s: status = %d, want %d", method, rec.Code, http.StatusNotFound)
		}
		if got := rec.Body.String(); got != notFoundBody {
			t.Errorf("%s: body = %q, want %q", method, got, notFoundBody)
		}
	}
}

func TestCheckResource2(t *testing.T) {
	rec := serve(t, http.MethodGet, "/notice/adr/checkresource2.json")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if rec.Body.String() != checkResource2Body {
		t.Fatalf("body = %q", rec.Body.String())
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatal(err)
	}
	if _, ok := raw["result"]; ok {
		t.Fatal("unexpected result wrapper")
	}
	var adr map[string]string
	if err := json.Unmarshal(raw["adr"], &adr); err != nil {
		t.Fatal(err)
	}
	if adr["10.1.0.0"] != "1" {
		t.Fatalf("adr[10.1.0.0] = %q", adr["10.1.0.0"])
	}
	var packs map[string]map[string]string
	if err := json.Unmarshal(raw["resource"], &packs); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"animation", "he", "ch", "sound", "subui", "tx", "squareui", "ui"} {
		if _, ok := packs["1"][k]; !ok {
			t.Errorf("missing key %q", k)
		}
	}
	if got := packs["1"]["tx"]; got != "543" {
		t.Errorf("resource[1].tx = %q, want %q", got, "543")
	}
}

func TestSplashSkip(t *testing.T) {
	for _, path := range []string{"/v4/resource/splash/1789929708/0/JP", "/v4/resource/splash/1/0/US"} {
		rec := serve(t, http.MethodGet, path)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: status = %d", path, rec.Code)
		}
		if rec.Body.String() != splashSkipBody {
			t.Fatalf("%s: body = %q", path, rec.Body.String())
		}
	}
	for _, path := range []string{"/v4/resource/splash/", "/v4/resource/splash/1/0", "/v4/resource/splash/x/0/JP", "/v4/resource/splash/1/0/JP/extra"} {
		rec := serve(t, http.MethodGet, path)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s: status = %d, want 404", path, rec.Code)
		}
	}
}

func TestUpdateIni(t *testing.T) {
	paths := []string{
		"/arts_animation_ini_00531/update_jp.ini",
		"/arts_sound_ini_00530/update_en.ini",
		"/arts_tx_ini_00540/update_ja.ini",
		"/arts_tx_ini_00541/update_jp.ini",
		"/arts_tx_ini_00542/update_jp.ini",
		"/arts_tx_ini_00543/update_ja.ini",
		"/arts_ui_ini_00540/update_jp.ini",
		"/arts_subui_ini_00000/update_jp.ini",
	}
	for _, path := range paths {
		rec := serve(t, http.MethodGet, path)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: status = %d", path, rec.Code)
		}
		if ct := rec.Header().Get("Content-Type"); ct != "text/plain; charset=utf-8" {
			t.Fatalf("%s: Content-Type = %q", path, ct)
		}
		if rec.Body.String() != emptyUpdateIniBody || rec.Body.Len() != 2 {
			t.Fatalf("%s: body = %q len=%d", path, rec.Body.String(), rec.Body.Len())
		}
	}
	rec := serve(t, http.MethodGet, "/arts_ui_ini_00540/other.ini")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown ini: %d", rec.Code)
	}
}

func TestTx543UpdateIni(t *testing.T) {
	const want = "[start=1,end=1,size=454656,/arts_strings.ast:version=1492F278EC281078AC7F35479A85F197]"
	for _, name := range []string{"update_en.ini", "update_jp.ini"} {
		path := "/arts_tx_ini_00543/" + name
		rec := serve(t, http.MethodGet, path)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: status = %d, want 200", path, rec.Code)
		}
		if ct := rec.Header().Get("Content-Type"); ct != "text/plain; charset=utf-8" {
			t.Fatalf("%s: Content-Type = %q", path, ct)
		}
		if rec.Body.String() != want {
			t.Fatalf("%s: body = %q, want %q", path, rec.Body.String(), want)
		}
		if rec.Body.Len() != len(want) {
			t.Fatalf("%s: len = %d, want %d", path, rec.Body.Len(), len(want))
		}
	}
	if tx543UpdateIniBody != want {
		t.Fatalf("tx543UpdateIniBody = %q, want %q", tx543UpdateIniBody, want)
	}
}

func TestSkinFileIni(t *testing.T) {
	paths := []string{
		"/arts_diaryskin_jp_00007/common/UIImage_hd/skin_file.ini",
		"/arts_diaryskin_jp/common/UIImage_hd/skin_file.ini",
		"/arts_diaryskin_en_00007/00001/UIImage_hd/skin_file.ini",
	}
	for _, path := range paths {
		rec := serve(t, http.MethodGet, path)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: status = %d, want 200", path, rec.Code)
		}
		if ct := rec.Header().Get("Content-Type"); ct != "text/plain; charset=utf-8" {
			t.Fatalf("%s: Content-Type = %q", path, ct)
		}
		if rec.Body.String() != skinFileIniBody || rec.Body.Len() == 0 {
			t.Fatalf("%s: body = %q len=%d", path, rec.Body.String(), rec.Body.Len())
		}
	}

	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodHead} {
		rec := serve(t, method, "/arts_diaryskin_jp_00007/common/UIImage_hd/skin_file.ini")
		if rec.Code != http.StatusNotFound || rec.Body.String() != notFoundBody {
			t.Fatalf("%s: status = %d body = %q", method, rec.Code, rec.Body.String())
		}
	}

	for _, path := range []string{"/arts_diaryskin_jp_00007/common/UIImage_hd/other.ini", "/other_diaryskin_jp/skin_file.ini", "/arts_diaryskin_.ini"} {
		rec := serve(t, http.MethodGet, path)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s: status = %d, want 404", path, rec.Code)
		}
	}
}

func TestArtsStringsAST(t *testing.T) {
	if len(artsStringsAST) != 454656 {
		t.Fatalf("ast size = %d, want 454656", len(artsStringsAST))
	}
	sum := md5.Sum(artsStringsAST)
	if got := strings.ToUpper(hex.EncodeToString(sum[:])); got != artsStringsMD5 {
		t.Fatalf("embedded ast md5 = %s, want %s", got, artsStringsMD5)
	}
	for _, path := range []string{"/arts_strings.ast", "/img/read/arts_strings.ast", "/arts_tx_jp_00540/arts_strings.ast", "/arts_tx_jp_00540/arts_strings.ast]"} {
		rec := serve(t, http.MethodGet, path)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: status = %d", path, rec.Code)
		}
		if ct := rec.Header().Get("Content-Type"); ct != "application/octet-stream" {
			t.Fatalf("%s: Content-Type = %q", path, ct)
		}
		if rec.Body.Len() != 454656 {
			t.Fatalf("%s: empty body len=%d", path, rec.Body.Len())
		}
		if rec.Header().Get("Content-Length") != "454656" {
			t.Fatalf("%s: Content-Length = %q", path, rec.Header().Get("Content-Length"))
		}
	}
}

func TestSnsTermsAndTermAll(t *testing.T) {
	cases := []struct{ path, body string }{
		{"/v4/social/terms/sns", snsTermsBody},
		{"/v4/social/terms/sns?deviceType=Android", snsTermsBody},
		{"/v4/setting/term/all", termAllBody},
		{"/v4/setting/term/all?deviceType=Android", termAllBody},
	}
	for _, c := range cases {
		rec := serve(t, http.MethodGet, c.path)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: status = %d, want 200", c.path, rec.Code)
		}
		if ct := rec.Header().Get("Content-Type"); ct != "application/json; charset=utf-8" {
			t.Fatalf("%s: Content-Type = %q", c.path, ct)
		}
		if got := rec.Body.String(); got != c.body {
			t.Fatalf("%s: body = %q, want %q", c.path, got, c.body)
		}
	}
	for _, path := range []string{"/v4/social/terms/sns", "/v4/setting/term/all"} {
		rec := serve(t, http.MethodPost, path)
		if rec.Code != http.StatusNotFound || rec.Body.String() != notFoundBody {
			t.Fatalf("POST %s: status = %d body = %q", path, rec.Code, rec.Body.String())
		}
	}
}

func TestProfile(t *testing.T) {
	for _, path := range []string{"/v4/profile/0?deviceType=Android", "/v4/profile/12345"} {
		rec := serve(t, http.MethodGet, path)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: status = %d, want 200", path, rec.Code)
		}
		var raw struct {
			Result map[string]json.RawMessage `json:"result"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
			t.Fatalf("%s: body is not valid JSON: %v", path, err)
		}
		if _, ok := raw.Result["name"]; !ok {
			t.Fatalf("%s: result.name missing: %q", path, rec.Body.String())
		}
	}
}

func TestFapiStubs(t *testing.T) {
	cases := []struct{ path, body string }{
		{"/v4/popup/isExists", popupIsExistsBody},
		{"/v4/popup/isExists?officialAvatarId=12345", popupIsExistsBody},
		{"/v4/eventFlag/flagList?deviceType=Android", eventFlagListBody},
	}
	for _, c := range cases {
		rec := serve(t, http.MethodGet, c.path)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: status = %d", c.path, rec.Code)
		}
		if got := rec.Body.String(); got != c.body {
			t.Fatalf("%s: body = %q, want %q", c.path, got, c.body)
		}
	}
	for _, path := range []string{"/v4/popup/isExists", "/v4/eventFlag/flagList"} {
		rec := serve(t, http.MethodPost, path)
		if rec.Code != http.StatusNotFound || rec.Body.String() != notFoundBody {
			t.Fatalf("POST %s: status = %d body = %q", path, rec.Code, rec.Body.String())
		}
	}
}

type guestResult struct {
	Provider    string `json:"provider"`
	AccessToken string `json:"accessToken"`
}

type guestResponse struct {
	Result *guestResult `json:"result"`
}

type sessionResult struct {
	SessionKey   string `json:"sessionKey"`
	Mid          string `json:"mid"`
	AvatarUserID string `json:"avatarUserId"`
	Aid          string `json:"aid"`
	LineID       string `json:"lineId"`
	LineName     string `json:"lineName"`
	TermAge      bool   `json:"termAge"`
}

type sessionResponse struct {
	Timestamp string         `json:"Timestamp"`
	Result    *sessionResult `json:"result"`
}

func serveRequest(t *testing.T, req *http.Request) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	newMux().ServeHTTP(rec, req)
	return rec
}

func guestGenerate(t *testing.T) string {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v4/account/guest/generate", strings.NewReader(`{"uniqueCode":"00000000-0000-0000-0000-000000000000"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-LINEPLAY-ACNT", "test")
	rec := serveRequest(t, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("generate: status = %d, want 200, body = %q", rec.Code, rec.Body.String())
	}
	var body guestResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("generate: body is not valid JSON: %v", err)
	}
	if body.Result == nil {
		t.Fatal("generate: result missing")
	}
	if body.Result.Provider != "lineplay" {
		t.Fatalf("generate: provider = %q, want %q", body.Result.Provider, "lineplay")
	}
	if body.Result.AccessToken == "" {
		t.Fatal("generate: accessToken is empty")
	}
	return body.Result.AccessToken
}

func createSession(t *testing.T, accessToken string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/v4/createSession?nationCode=JP", nil)
	if accessToken != "" {
		req.AddCookie(&http.Cookie{Name: "accessToken", Value: accessToken})
	}
	return serveRequest(t, req)
}

func checkSession(t *testing.T, avAuth string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/v4/checkSession", nil)
	if avAuth != "" {
		req.AddCookie(&http.Cookie{Name: "AV_AUTH", Value: avAuth})
	}
	return serveRequest(t, req)
}

func decodeSession(t *testing.T, rec *httptest.ResponseRecorder) *sessionResult {
	t.Helper()
	var body sessionResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body is not valid JSON: %v, body = %q", err, rec.Body.String())
	}
	if body.Result == nil {
		t.Fatalf("result missing: body = %q", rec.Body.String())
	}
	return body.Result
}

func avAuthValue(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	setCookie := rec.Header().Get("Set-Cookie")
	const prefix = `AV_AUTH="`
	const suffix = `"; Path=/`
	if !strings.HasPrefix(setCookie, prefix) || !strings.HasSuffix(setCookie, suffix) {
		t.Fatalf("Set-Cookie = %q, want %s<token>%s", setCookie, prefix, suffix)
	}
	value := strings.TrimSuffix(strings.TrimPrefix(setCookie, prefix), suffix)
	if len(value) <= 100 {
		t.Fatalf("AV_AUTH value length = %d, want > 100", len(value))
	}
	return value
}

func assertSessionKeys(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	var raw struct {
		Result map[string]json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatalf("body is not valid JSON: %v", err)
	}
	want := []string{"sessionKey", "mid", "avatarUserId", "aid", "lineId", "lineName", "termAge"}
	if len(raw.Result) != len(want) {
		t.Fatalf("result has %d fields, want %d: %q", len(raw.Result), len(want), rec.Body.String())
	}
	for _, key := range want {
		if _, ok := raw.Result[key]; !ok {
			t.Errorf("result field %q missing: %q", key, rec.Body.String())
		}
	}
}

func TestGenerateGuest(t *testing.T) {
	guestGenerate(t)
}

func TestCreateSession(t *testing.T) {
	accessToken := guestGenerate(t)
	rec := createSession(t, accessToken)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body = %q", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json; charset=utf-8" {
		t.Fatalf("Content-Type = %q", ct)
	}
	avAuthValue(t, rec)
	assertSessionKeys(t, rec)
	res := decodeSession(t, rec)
	if res.Aid != "0" {
		t.Fatalf("aid = %q, want %q", res.Aid, "0")
	}
	if res.SessionKey == "" || res.Mid == "" || res.AvatarUserID == "" {
		t.Fatalf("session fields empty: %+v", res)
	}
	if res.Aid == res.AvatarUserID {
		t.Fatalf("aid and avatarUserId both %q", res.Aid)
	}
	if res.TermAge {
		t.Fatal("termAge = true, want false")
	}

	fallback := createSession(t, "")
	if fallback.Code != http.StatusOK {
		t.Fatalf("no-cookie fallback: status = %d, want 200", fallback.Code)
	}
	avAuthValue(t, fallback)
	if got := decodeSession(t, fallback); got.Aid != res.Aid || got.SessionKey != res.SessionKey {
		t.Fatalf("fallback account mismatch: got %+v, want %+v", got, res)
	}
}

func TestCheckSession(t *testing.T) {
	accessToken := guestGenerate(t)
	token := avAuthValue(t, createSession(t, accessToken))

	rec := checkSession(t, token)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body = %q", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json; charset=utf-8" {
		t.Fatalf("Content-Type = %q", ct)
	}
	assertSessionKeys(t, rec)
	var body sessionResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body is not valid JSON: %v", err)
	}
	if body.Timestamp == "" || !isAllDigits(body.Timestamp) {
		t.Fatalf("Timestamp = %q, want unix seconds string", body.Timestamp)
	}
	if body.Result == nil {
		t.Fatal("result missing")
	}
	rotated := avAuthValue(t, rec)
	if rotated == token {
		t.Fatal("Set-Cookie token was not rotated")
	}
}

func TestCheckSessionUnknown(t *testing.T) {
	rec := checkSession(t, "not-a-real-token")
	if rec.Code != http.StatusNotFound || rec.Body.String() != unknownSessionBody {
		t.Fatalf("unknown token: status = %d, body = %q", rec.Code, rec.Body.String())
	}
	rec = checkSession(t, "")
	if rec.Code != http.StatusNotFound || rec.Body.String() != unknownSessionBody {
		t.Fatalf("missing cookie: status = %d, body = %q", rec.Code, rec.Body.String())
	}
	req := httptest.NewRequest(http.MethodGet, "/v4/checkSession", nil)
	req.Header.Set("Cookie", `AV_AUTH="not-a-real-token"`)
	rec = serveRequest(t, req)
	if rec.Code != http.StatusNotFound || rec.Body.String() != unknownSessionBody {
		t.Fatalf("quoted unknown token: status = %d, body = %q", rec.Code, rec.Body.String())
	}
}

func TestAuthRoundTrip(t *testing.T) {
	accessToken := guestGenerate(t)

	create := createSession(t, accessToken)
	if create.Code != http.StatusOK {
		t.Fatalf("createSession: status = %d, body = %q", create.Code, create.Body.String())
	}
	first := decodeSession(t, create)
	token := avAuthValue(t, create)

	check := checkSession(t, token)
	if check.Code != http.StatusOK {
		t.Fatalf("checkSession: status = %d, body = %q", check.Code, check.Body.String())
	}
	second := decodeSession(t, check)
	if *second != *first {
		t.Fatalf("session changed: first = %+v, second = %+v", first, second)
	}

	rotated := avAuthValue(t, check)
	again := checkSession(t, rotated)
	if again.Code != http.StatusOK {
		t.Fatalf("rotated checkSession: status = %d, body = %q", again.Code, again.Body.String())
	}
	if third := decodeSession(t, again); third.SessionKey != first.SessionKey {
		t.Fatalf("sessionKey changed: %q -> %q", first.SessionKey, third.SessionKey)
	}
}

func TestQuotedAvAuthCookie(t *testing.T) {
	accessToken := guestGenerate(t)
	token := avAuthValue(t, createSession(t, accessToken))

	req := httptest.NewRequest(http.MethodGet, "/v4/checkSession", nil)
	req.Header.Set("Cookie", `AV_AUTH="`+token+`"; other=x`)
	rec := serveRequest(t, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("quoted cookie: status = %d, want 200, body = %q", rec.Code, rec.Body.String())
	}
	if decodeSession(t, rec).SessionKey == "" {
		t.Fatal("sessionKey empty")
	}
}

func TestAuthWrongMethods(t *testing.T) {
	cases := []struct{ method, path string }{
		{http.MethodGet, "/v4/account/guest/generate"},
		{http.MethodPut, "/v4/account/guest/generate"},
		{http.MethodPost, "/v4/createSession"},
		{http.MethodDelete, "/v4/createSession"},
		{http.MethodPost, "/v4/checkSession"},
		{http.MethodHead, "/v4/checkSession"},
	}
	for _, c := range cases {
		rec := serveRequest(t, httptest.NewRequest(c.method, c.path, nil))
		if rec.Code != http.StatusNotFound || rec.Body.String() != notFoundBody {
			t.Fatalf("%s %s: status = %d, body = %q", c.method, c.path, rec.Code, rec.Body.String())
		}
	}
}

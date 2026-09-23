package main

import (
	"compress/gzip"
	"crypto/rand"
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

const setInitConfBody = `{"result":{"sessionServerInfo":"XPN:/p=XTCP;ip=session.play.naver.jp;port=10123","staticDomain":"https://play-static.line-scdn.net/","nationCode":"JP","isGdprNation":false,"snsLoginUIList":["LD_GUEST"],"snsSignUpUIList":["LD_GUEST"]}}`

const checkResource2Body = `{"adr":{"10.1.0.0":"1"},"resource":{"1":{"animation":"531","he":"0","ch":"0","sound":"530","subui":"0","tx":"543","squareui":"0","ui":"540"}}}`

const splashSkipBody = `{"result":{"update":false,"resourceID":-1,"resourceImageCount":0,"resourceImages":[]}}`

const emptyUpdateIniBody = `[]`

const tx543UpdateIniBody = `[start=1,end=1,size=454656,/arts_strings.ast:version=1492F278EC281078AC7F35479A85F197]`

const skinFileIniBody = `[empty]`

const popupIsExistsBody = `{"result":false}`

const eventFlagListBody = `{"result":[]}`

const snsTermsBody = `{"result":true}`

const termAllBody = `{"result":{"1":true}}`

const profileBody = `{"result":{"name":"guest","newbie":true}}`

const notFoundBody = `{"errorCode":"404","errorMessage":"cherry: unknown route"}`

const unknownSessionBody = `{"errorCode":"404","errorMessage":"cherry: unknown session"}`

const badRequestBody = `{"errorCode":"400","errorMessage":"cherry: bad request"}`

const createCompleteBody = `{"result":{"status":true,"rewardCoin":300}}`

const settingAllBody = `{"result":{"notiFlag":false,"changeCountry":false,"countryName":"","soundConfig":false,"notiConfig":{},"privacyConfig":{},"roomSize":{"max":0,"cur":0}}}`

const friendSyncBody = `{"result":{"existProfile":false,"nextCursor":0,"timestamp":"0","friendsCount":0,"buddyList":[],"newbieRecommendList":[],"nearbyRecommendList":[],"bookmarks":{}}}`

const friendLineBuddyBody = `{"result":{"nextCursor":0,"buddyList":[]}}`

const friendBrandBuddyBody = `{"result":[]}`

const artsStringsMD5 = "1492F278EC281078AC7F35479A85F197"

//go:embed testdata/arts_strings.ast
var artsStringsAST []byte

func newMux() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/v4/account/guest/generate", handleGuestGenerate)
	mux.HandleFunc("/v4/createSession", handleCreateSession)
	mux.HandleFunc("/v4/checkSession", handleCheckSession)
	mux.HandleFunc("/v4/create/avatar", handleCreateAvatar)
	mux.HandleFunc("/v4/create/all/items", handleCreateAllItems)
	mux.HandleFunc("/v4/create/face/setitems/roll", handleCreateFaceSetItemsRoll)
	mux.HandleFunc("/v2/create/face/analyze", handleCreateFaceAnalyze)
	mux.HandleFunc("/v4/create/complete", handleCreateComplete)
	mux.HandleFunc("/arts_session/sckey.enc", handleSckeyEnc)
	mux.HandleFunc("/v4/setInitConf", handleSetInitConf)
	mux.HandleFunc("/v4/resource/splash/", handleSplash)
	mux.HandleFunc("/notice/adr/checkresource2.json", handleCheckResource2)
	mux.HandleFunc("/v4/popup/isExists", handleJSONBody(popupIsExistsBody))
	mux.HandleFunc("/v4/eventFlag/flagList", handleJSONBody(eventFlagListBody))
	mux.HandleFunc("/v4/social/terms/sns", handleSnsTerms)
	mux.HandleFunc("/v4/setting/term/all", handleJSONBody(termAllBody))
	mux.HandleFunc("/v4/setting/all", handleJSONBody(settingAllBody))
	mux.HandleFunc("/v4/sync/friends/", handleJSONBody(friendSyncBody))
	mux.HandleFunc("/v4/buddy/list/type/0", handleJSONBody(friendSyncBody))
	mux.HandleFunc("/v4/line/buddy/v4/list", handleJSONBody(friendLineBuddyBody))
	mux.HandleFunc("/v4/brand/list", handleJSONBody(friendBrandBuddyBody))
	mux.HandleFunc("/v4/profile/", handleJSONBody(profileBody))
	mux.HandleFunc("/v4/avatar/", handleAvatarInfo)
	mux.HandleFunc("/", handleRoot)
	return mux
}

func handleJSONBody(body string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			serveNotFound(w)
			return
		}
		writeJSON(w, http.StatusOK, body)
	}
}

func handleSnsTerms(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodPut {
		serveNotFound(w)
		return
	}
	writeJSON(w, http.StatusOK, snsTermsBody)
}

func handleArtsStrings(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		serveNotFound(w)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", strconv.Itoa(len(artsStringsAST)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(artsStringsAST)
}

func handleSetInitConf(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		serveNotFound(w)
		return
	}
	writeJSON(w, http.StatusOK, setInitConfBody)
}

func handleCheckResource2(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		serveNotFound(w)
		return
	}
	writeJSON(w, http.StatusOK, checkResource2Body)
}

func handleSplash(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet || !validSplashPath(r.URL.Path) {
		serveNotFound(w)
		return
	}
	writeJSON(w, http.StatusOK, splashSkipBody)
}

func handleGuestGenerate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		serveNotFound(w)
		return
	}
	acc := newAccount()
	writeJSON(w, http.StatusOK, `{"result":{"provider":"lineplay","accessToken":"`+acc.accessToken+`"}}`)
}

func handleCreateSession(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		serveNotFound(w)
		return
	}
	acc := currentAccount(r)
	if acc == nil {
		serveNotFound(w)
		return
	}
	token := randomToken(256)
	accountsMu.Lock()
	accounts[token] = acc
	accountsMu.Unlock()
	w.Header().Add("Set-Cookie", avAuthSetCookie(token))
	writeJSON(w, http.StatusOK, `{"result":`+sessionResultBody(acc)+`}`)
}

func handleCheckSession(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		serveNotFound(w)
		return
	}
	accountsMu.Lock()
	acc := accounts[cookieValue(r, "AV_AUTH")]
	accountsMu.Unlock()
	if acc == nil {
		writeJSON(w, http.StatusNotFound, unknownSessionBody)
		return
	}
	token := randomToken(256)
	accountsMu.Lock()
	accounts[token] = acc
	accountsMu.Unlock()
	w.Header().Add("Set-Cookie", avAuthSetCookie(token))
	writeJSON(w, http.StatusOK, fmt.Sprintf(`{"Timestamp":"%d","result":%s}`, time.Now().Unix(), sessionResultBody(acc)))
}

const maxCreateAvatarBody = 1 << 20

type createAvatarRequest struct {
	Name       string            `json:"name"`
	AvatarType string            `json:"avatarType"`
	NationCode string            `json:"nationCode"`
	SkinColor  json.RawMessage   `json:"skinColor"`
	ItemCodes  []json.RawMessage `json:"itemCodes"`
}

// itemCodesFromJSON accepts the client's item code strings and the legacy
// numeric base/skin codes; anything else (objects/arrays/bools/null) is
// rejected before the account is touched.
func itemCodesFromJSON(raws []json.RawMessage) ([]string, bool) {
	codes := make([]string, 0, len(raws))
	for _, raw := range raws {
		if string(raw) == "null" {
			return nil, false
		}
		var s string
		if err := json.Unmarshal(raw, &s); err == nil {
			codes = append(codes, s)
			continue
		}
		var n json.Number
		if err := json.Unmarshal(raw, &n); err == nil {
			codes = append(codes, n.String())
			continue
		}
		return nil, false
	}
	return codes, true
}

func handleCreateAvatar(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		serveNotFound(w)
		return
	}
	body := io.Reader(r.Body)
	if strings.EqualFold(strings.TrimSpace(r.Header.Get("Content-Encoding")), "gzip") {
		zr, err := gzip.NewReader(r.Body)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, badRequestBody)
			return
		}
		defer zr.Close()
		body = zr
	}
	raw, err := io.ReadAll(io.LimitReader(body, maxCreateAvatarBody+1))
	if err != nil || len(raw) > maxCreateAvatarBody {
		writeJSON(w, http.StatusBadRequest, badRequestBody)
		return
	}
	var req createAvatarRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, badRequestBody)
		return
	}
	gender := normalizeAvatarType(req.AvatarType)
	if gender == "" {
		writeJSON(w, http.StatusBadRequest, badRequestBody)
		return
	}
	itemCodes, ok := itemCodesFromJSON(req.ItemCodes)
	if !ok {
		writeJSON(w, http.StatusBadRequest, badRequestBody)
		return
	}
	skin, ok := skinColorString(req.SkinColor)
	if !ok {
		writeJSON(w, http.StatusBadRequest, badRequestBody)
		return
	}
	if skin == "" {
		skin = "1"
	}

	accountsMu.Lock()
	acc := accounts[cookieValue(r, "AV_AUTH")]
	accountsMu.Unlock()
	if acc == nil {
		acc = newAccount()
	}
	token := randomToken(256)
	accountsMu.Lock()
	if acc.aid == "0" {
		nextAvatarID++
		acc.aid = strconv.FormatUint(nextAvatarID, 10)
	}
	acc.name = req.Name
	acc.gender = gender
	acc.skin = skin
	acc.country = req.NationCode
	acc.itemCodes = append([]string(nil), itemCodes...)
	aid, sessionKey := acc.aid, acc.sessionKey
	accounts[token] = acc
	accountsMu.Unlock()

	w.Header().Add("Set-Cookie", avAuthSetCookie(token))
	payload, _ := json.Marshal(struct {
		Result avatarResult `json:"result"`
	}{avatarResult{
		AvatarID:    aid,
		Name:        req.Name,
		Gender:      req.AvatarType,
		SessionKey:  sessionKey,
		AvatarCode:  "ac",
		Items:       avatarItemsFromCodes(itemCodes),
		PetProfiles: []string{},
	}})
	writeJSON(w, http.StatusOK, string(payload))
}

// skinColorString accepts only a scalar string or number (or absent/null);
// objects, arrays and bools are rejected before the account is touched.
func skinColorString(raw json.RawMessage) (string, bool) {
	if len(raw) == 0 || string(raw) == "null" {
		return "", true
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s, true
	}
	var n json.Number
	if err := json.Unmarshal(raw, &n); err == nil {
		return n.String(), true
	}
	return "", false
}

func handleCreateComplete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		serveNotFound(w)
		return
	}
	writeJSON(w, http.StatusOK, createCompleteBody)
}

// tutorialAvatarTypes maps the four avatar IDs requested by
// NaCreateLayer::InitCloneAvatars to a synthesized appearance. The sex
// assignment is a deterministic stand-in; the authentic per-ID appearances are
// not in the archives.
var tutorialAvatarTypes = map[string]string{
	"1151262555912366100": "MALE",
	"1151262590612422506": "FEMALE",
	"1151262602112441105": "ANIMAL",
	"1151262571612391402": "MALE",
}

func handleAvatarInfo(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		serveNotFound(w)
		return
	}
	id, ok := strings.CutPrefix(r.URL.Path, "/v4/avatar/")
	if !ok || id == "" || strings.Contains(id, "/") || !isAllDigits(id) {
		serveNotFound(w)
		return
	}
	info := avatarInfoResult{
		AvatarID:    id,
		Gender:      "FEMALE",
		SType:       "NORMAL",
		Skin:        "1",
		Country:     "JP",
		Items:       []avatarItem{},
		PetProfiles: []string{},
	}
	if sex, ok := tutorialAvatarTypes[id]; ok {
		info.Name = "cherry"
		info.Gender = sex
		info.Items = avatarItemsFromCodes(workableLook(sex))
	} else if acc, ok := accountByAvatarID(id); ok {
		info.Name = acc.name
		if acc.gender != "" {
			info.Gender = acc.gender
		}
		if acc.skin != "" {
			info.Skin = acc.skin
		}
		info.Country = acc.country
		if acc.itemCodes != nil {
			info.Items = avatarItemsFromCodes(acc.itemCodes)
		}
	}
	payload, _ := json.Marshal(struct {
		Result avatarInfoResult `json:"result"`
	}{Result: info})
	writeJSON(w, http.StatusOK, string(payload))
}

// accountSnapshot is a copy of the fields /v4/avatar/<id> serves, taken under
// accountsMu so the handler never reads fields that handleCreateAvatar may be
// writing concurrently.
type accountSnapshot struct {
	name      string
	gender    string
	skin      string
	country   string
	itemCodes []string
}

func accountByAvatarID(id string) (accountSnapshot, bool) {
	accountsMu.Lock()
	defer accountsMu.Unlock()
	for _, acc := range accounts {
		if acc.aid == id {
			return accountSnapshot{
				name:      acc.name,
				gender:    acc.gender,
				skin:      acc.skin,
				country:   acc.country,
				itemCodes: append([]string(nil), acc.itemCodes...),
			}, true
		}
	}
	return accountSnapshot{}, false
}

func handleRoot(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet && isUpdateIniPath(r.URL.Path) {
		body := emptyUpdateIniBody
		if isTx543UpdateIniPath(r.URL.Path) {
			body = tx543UpdateIniBody
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(body))
		return
	}
	if r.Method == http.MethodGet && isSkinFileIniPath(r.URL.Path) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(skinFileIniBody))
		return
	}
	if r.Method == http.MethodGet && strings.Contains(r.URL.Path, "arts_strings.ast") {
		handleArtsStrings(w, r)
		return
	}
	serveNotFound(w)
}

func validSplashPath(path string) bool {
	rest, ok := strings.CutPrefix(path, "/v4/resource/splash/")
	if !ok || rest == "" {
		return false
	}
	parts := strings.Split(rest, "/")
	if len(parts) != 3 {
		return false
	}
	for _, p := range parts {
		if p == "" {
			return false
		}
	}
	return isAllDigits(parts[0]) && isAllDigits(parts[1])
}

func isSkinFileIniPath(path string) bool {
	return strings.HasPrefix(path, "/arts_diaryskin_") && strings.HasSuffix(path, "/skin_file.ini")
}

func isUpdateIniPath(path string) bool {
	name := path
	if i := strings.LastIndex(path, "/"); i >= 0 {
		name = path[i+1:]
	}
	switch name {
	case "update_en.ini", "update_jp.ini", "update_ja.ini":
	default:
		return false
	}
	dir := path[:len(path)-len(name)]
	prefixes := []string{
		"/arts_animation_ini_00531/",
		"/arts_sound_ini_00530/",
		"/arts_tx_ini_00540/",
		"/arts_tx_ini_00541/",
		"/arts_tx_ini_00542/",
		"/arts_tx_ini_00543/",
		"/arts_ui_ini_00540/",
		"/arts_subui_ini_00000/",
	}
	for _, p := range prefixes {
		if dir == p {
			return true
		}
	}
	return false
}

func isTx543UpdateIniPath(path string) bool {
	switch path {
	case "/arts_tx_ini_00543/update_en.ini", "/arts_tx_ini_00543/update_jp.ini":
		return true
	}
	return false
}

func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

type account struct {
	accessToken  string
	sessionKey   string
	mid          string
	avatarUserID string
	aid          string
	name         string
	gender       string
	skin         string
	country      string
	itemCodes    []string
}

// avatarItem is the object form parsed by sDataAvatar::SetData @0x1c0a39c.
type avatarItem struct {
	CD                     string `json:"cd"`
	InvenSeq               string `json:"invenSeq"`
	DyeType                int    `json:"dyeType"`
	ColorAndTransparencies []any  `json:"colorAndTransparencies"`
}

func avatarItemsFromCodes(codes []string) []avatarItem {
	items := make([]avatarItem, 0, len(codes))
	for _, cd := range codes {
		items = append(items, avatarItem{CD: cd, InvenSeq: "0", ColorAndTransparencies: []any{}})
	}
	return items
}

type avatarResult struct {
	AvatarID    string       `json:"avatarId"`
	Name        string       `json:"name"`
	Gender      string       `json:"gender"`
	SessionKey  string       `json:"sessionKey"`
	AvatarCode  string       `json:"avatarCode"`
	Items       []avatarItem `json:"items"`
	PetProfiles []string     `json:"petProfiles"`
}

type avatarInfoResult struct {
	AvatarID    string       `json:"avatarId"`
	Name        string       `json:"name"`
	Gender      string       `json:"gender"`
	SType       string       `json:"sType"`
	Skin        string       `json:"skin"`
	Country     string       `json:"country"`
	Items       []avatarItem `json:"items"`
	PetProfiles []string     `json:"petProfiles"`
}

var (
	accountsMu   sync.Mutex
	accounts     = make(map[string]*account)
	latestAcc    *account
	nextAvatarID uint64
)

func newAccount() *account {
	acc := &account{
		accessToken:  randomToken(32),
		sessionKey:   randomToken(32),
		mid:          "1",
		avatarUserID: "3001",
		aid:          "0",
	}
	accountsMu.Lock()
	accounts[acc.accessToken] = acc
	latestAcc = acc
	accountsMu.Unlock()
	return acc
}

func currentAccount(r *http.Request) *account {
	accountsMu.Lock()
	defer accountsMu.Unlock()
	if acc := accounts[cookieValue(r, "accessToken")]; acc != nil {
		return acc
	}
	return latestAcc
}

func sessionResultBody(acc *account) string {
	accountsMu.Lock()
	sessionKey, mid, avatarUserID, aid := acc.sessionKey, acc.mid, acc.avatarUserID, acc.aid
	accountsMu.Unlock()
	return fmt.Sprintf(`{"sessionKey":"%s","mid":"%s","avatarUserId":"%s","aid":"%s","lineId":"","lineName":"","termAge":false}`,
		sessionKey, mid, avatarUserID, aid)
}

func avAuthSetCookie(token string) string {
	return `AV_AUTH="` + token + `"; Path=/`
}

func cookieValue(r *http.Request, name string) string {
	for _, part := range strings.Split(r.Header.Get("Cookie"), ";") {
		key, value, ok := strings.Cut(strings.TrimSpace(part), "=")
		if ok && key == name {
			return strings.Trim(value, `"`)
		}
	}
	return ""
}

func randomToken(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return base64.StdEncoding.EncodeToString(b)
}

func serveNotFound(w http.ResponseWriter) {
	writeJSON(w, http.StatusNotFound, notFoundBody)
}

func writeJSON(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(body))
}

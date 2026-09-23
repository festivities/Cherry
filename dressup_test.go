package main

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// dpReadyCodes lists advertised catalog/set codes that have BOTH device render
// assets (1409_iteminfo.artsitem + render PNGs in the santi restore) AND an
// authentic dp.png (in the santi restore or the iOS 2014 pack). The iOS pack
// originals are 140x140; NaCreateItemCell scales them to 130x100.
var dpReadyCodes = map[string]bool{
	// Human M.
	"CUHE0000M": true, "CUNO00006": true, "CUNO00016": true,
	"CUEY0000Q": true, "CUEY0000S": true, "CUEB0000A": true, "CUEB0000G": true,
	"CUMO0000C": true, "CUMO0000F": true,
	"CUHA0004X": true, "CUHA0004Z": true, "CUHA00053": true,
	"CUFE0004H": true, "CUFE0008L": true,
	"CUON00164": true, "CUON001BD": true, "CUTO000JN": true, "CUTO000T3": true,
	"CUPA000DG": true, "CUSH000DU": true, "CUSH000KV": true,
	// Human F.
	"CUEY000CB": true, "CUEY000CG": true, "CUHA0003L": true, "CUHA0003E": true,
	"CUHA0003C": true, "CUFE00013": true, "CUFE00016": true,
	"CUON001CH": true, "CUON001GS": true, "CUTO000AQ": true,
	"CUPA00091": true, "CUPA000DA": true, "CUPA000EP": true, "CUPA000EQ": true,
	"CUSH000Q5": true, "CUSH0005G": true, "CUSH000VR": true,
	// Animal.
	"CAFA0000S": true, "CAFA0000X": true, "CAEY0000D": true, "CAEY0000G": true,
	"CAMO00002": true, "CAMO00009": true, "CANO00001": true, "CANO00005": true,
	"CAEA0000E": true, "CAEA0000M": true, "CATL00006": true, "CATL0000K": true,
	"CATO000GR": true, "CATO000IU": true,
}

// dpLessSetElements are fully-renderable animal bottoms/shoes kept only inside
// set elementCodes; they have no dp.png and are not advertised as list cells.
var dpLessSetElements = map[string]bool{
	"CAPA000BP": true, "CAPA0006Q": true, "CASH00001": true, "CASH0003L": true,
}

// fixtureItemCodes is the union of every code any handler may serve.
var fixtureItemCodes = func() map[string]bool {
	m := map[string]bool{}
	for cd := range dpReadyCodes {
		m[cd] = true
	}
	for cd := range dpLessSetElements {
		m[cd] = true
	}
	return m
}()

var (
	// 2014 items.db use_man=1 / use_girl=0, or the male Cherry alternatives.
	maleOnlyCodes = map[string]bool{
		"CUHA0004X": true, "CUHA0004Z": true, "CUHA00053": true,
		"CUFE0004H": true, "CUFE0008L": true,
		"CUON00164": true, "CUON001BD": true, "CUTO000JN": true, "CUTO000T3": true,
		"CUPA000DG": true, "CUSH000DU": true, "CUSH000KV": true,
	}
	// 2014 items.db use_girl=1 / use_man=0.
	femaleOnlyCodes = map[string]bool{
		"CUEY000CB": true, "CUEY000CG": true,
		"CUHA0003L": true, "CUHA0003E": true, "CUHA0003C": true,
		"CUON001GS": true,
		"CUPA00091": true, "CUPA000DA": true, "CUPA000EP": true,
		"CUSH000Q5": true, "CUSH000VR": true,
	}
	// The four Cherry closet originals and their starter look must not be
	// advertised by the player-creation endpoints.
	retiredCherryCodes = map[string]bool{
		"CUHA0036Z": true, "CUON004TV": true, "CUSH00267": true, "CUAH004JH": true,
		"CUHA002GJ": true, "CUEY000K3": true, "CUEB0009Y": true, "CUNO00042": true,
		"CUMO000E9": true, "CUFE000AV": true, "CUON0059S": true, "CUSH002BH": true,
		"CUAH0067V": true,
	}
)

func allSetCodes(sets []dressSet) map[string]bool {
	codes := map[string]bool{}
	for _, set := range sets {
		codes[set.CD] = true
		for _, cd := range set.ElementCodes {
			codes[cd] = true
		}
	}
	return codes
}

type rollEntry struct {
	AvatarType string     `json:"avatarType"`
	SetItem    []dressSet `json:"setItem"`
}

func assertFixtureCodes(t *testing.T, label string, items []dressItem) {
	t.Helper()
	for _, item := range items {
		if item.CD == "" {
			t.Errorf("%s: empty cd", label)
			continue
		}
		if !fixtureItemCodes[item.CD] {
			t.Errorf("%s: unverified fixture code %q", label, item.CD)
		}
	}
}

// assertCatalogCodes enforces the advertising contract: a displayed list-cell
// code must have a verified dp.png and must not be a retired Cherry original.
func assertCatalogCodes(t *testing.T, label string, items []dressItem) {
	t.Helper()
	assertFixtureCodes(t, label, items)
	for _, item := range items {
		if !dpReadyCodes[item.CD] {
			t.Errorf("%s: advertises dp-less code %q", label, item.CD)
		}
		if retiredCherryCodes[item.CD] {
			t.Errorf("%s: advertises retired Cherry original %q", label, item.CD)
		}
	}
}

func TestCreateFaceSetItemsRoll(t *testing.T) {
	rec := serve(t, http.MethodGet, "/v4/create/face/setitems/roll?locale=JP&deviceType=Android")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body = %q", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json; charset=utf-8" {
		t.Fatalf("Content-Type = %q", ct)
	}
	var body struct {
		Result []rollEntry `json:"result"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body decode failed: %v", err)
	}
	if len(body.Result) != 3 {
		t.Fatalf("result has %d entries, want 3: %q", len(body.Result), rec.Body.String())
	}
	byType := map[string][]dressSet{}
	for _, entry := range body.Result {
		if _, dup := byType[entry.AvatarType]; dup {
			t.Fatalf("duplicate avatarType %q", entry.AvatarType)
		}
		byType[entry.AvatarType] = entry.SetItem
	}
	for _, sex := range []string{"MALE", "FEMALE", "ANIMAL"} {
		sets, ok := byType[sex]
		if !ok {
			t.Fatalf("avatarType %q missing", sex)
		}
		if len(sets) < 2 {
			t.Fatalf("%s has %d sets, want at least 2", sex, len(sets))
		}
		seen := map[string]bool{}
		for _, set := range sets {
			if set.CD == "" || set.Name == "" {
				t.Errorf("%s: set has empty cd/name: %+v", sex, set)
			}
			if seen[set.CD] {
				t.Errorf("%s: duplicate set cd %q", sex, set.CD)
			}
			seen[set.CD] = true
			if len(set.ElementCodes) == 0 {
				t.Errorf("%s: set %q has no element codes", sex, set.CD)
			}
			if !dpReadyCodes[set.CD] {
				t.Errorf("%s: set cd %q has no verified dp.png", sex, set.CD)
			}
			for _, cd := range set.ElementCodes {
				if !dpReadyCodes[cd] && !dpLessSetElements[cd] {
					t.Errorf("%s: set %q element %q has no verified dp.png", sex, set.CD, cd)
				}
				if !fixtureItemCodes[cd] {
					t.Errorf("%s: set %q element %q is not a verified fixture code", sex, set.CD, cd)
				}
				if retiredCherryCodes[cd] {
					t.Errorf("%s: set %q advertises retired Cherry original %q", sex, set.CD, cd)
				}
			}
		}
	}
	if got := len(byType["ANIMAL"]); got != 2 {
		t.Errorf("ANIMAL set count = %d, want the two complete sets", got)
	}

	male := allSetCodes(byType["MALE"])
	for cd := range male {
		if femaleOnlyCodes[cd] {
			t.Errorf("MALE set contains female-only code %q", cd)
		}
	}
	if !male["CUHA0004X"] || !male["CUON00164"] || !male["CUSH000DU"] {
		t.Errorf("MALE sets miss archive-verified M items: %v", male)
	}
	female := allSetCodes(byType["FEMALE"])
	for cd := range female {
		if maleOnlyCodes[cd] {
			t.Errorf("FEMALE set contains male-only code %q", cd)
		}
		if retiredCherryCodes[cd] {
			t.Errorf("FEMALE set advertises retired Cherry original %q", cd)
		}
	}
	if !female["CUHA0003L"] || !female["CUON001CH"] || !female["CUSH000Q5"] {
		t.Errorf("FEMALE sets miss archive-verified F items: %v", female)
	}
	if len(female) == 0 || len(male) == 0 {
		t.Fatal("empty sex set")
	}
	animal := allSetCodes(byType["ANIMAL"])
	for _, cd := range []string{"CATO000IU", "CATO000GR"} {
		if !animal[cd] {
			t.Errorf("ANIMAL sets miss dp-ready top %q", cd)
		}
	}

	post := serve(t, http.MethodPost, "/v4/create/face/setitems/roll")
	if post.Code != http.StatusNotFound || post.Body.String() != notFoundBody {
		t.Fatalf("POST: status = %d, body = %q", post.Code, post.Body.String())
	}
}

func TestCreateAllItems(t *testing.T) {
	rec := serve(t, http.MethodGet, "/v4/create/all/items?isFull=false&locale=JP")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body = %q", rec.Code, rec.Body.String())
	}
	var body struct {
		Result map[string]map[string]json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body decode failed: %v", err)
	}
	if len(body.Result) != 3 {
		t.Fatalf("result has %d sexes, want M/F/A: %q", len(body.Result), rec.Body.String())
	}
	wantKeys := map[string][]string{
		"M": {"MEYE", "MFACE", "MMOUTH", "MFHAIR", "MNOSE", "MBROW", "METC", "DRESS", "SET"},
		"F": {"FEYE", "FFACE", "FMOUTH", "FFHAIR", "FNOSE", "FBROW", "FETC", "DRESS", "SET"},
		"A": {"AEYE", "AFACE", "AMOUTH", "AEAR", "ANOSE", "ATAIL", "AETC", "DRESS", "SET"},
	}
	for sexKey, keys := range wantKeys {
		sexObj, ok := body.Result[sexKey]
		if !ok {
			t.Fatalf("sex %q missing", sexKey)
		}
		if len(sexObj) != len(keys) {
			t.Fatalf("sex %q has %d keys, want %d: %q", sexKey, len(sexObj), len(keys), rec.Body.String())
		}
		for _, key := range keys {
			if _, ok := sexObj[key]; !ok {
				t.Errorf("sex %q key %q missing", sexKey, key)
			}
		}
	}

	for _, faceKey := range []string{"MFACE", "FFACE"} {
		var cat dressCategory
		if err := json.Unmarshal(body.Result[faceKey[:1]][faceKey], &cat); err != nil {
			t.Fatalf("%s decode failed: %v", faceKey, err)
		}
		if len(cat.Items) != 0 {
			t.Errorf("%s = %q, want empty per project decision", faceKey, body.Result[faceKey[:1]][faceKey])
		}
	}

	for sexKey, bad := range map[string]map[string]bool{"M": femaleOnlyCodes, "F": maleOnlyCodes} {
		sexObj := body.Result[sexKey]
		for key, raw := range sexObj {
			if key == "SET" {
				continue
			}
			var items []dressItem
			if key == "DRESS" {
				if err := json.Unmarshal(raw, &items); err != nil {
					t.Fatalf("%s DRESS decode failed: %v", sexKey, err)
				}
			} else {
				var cat dressCategory
				if err := json.Unmarshal(raw, &cat); err != nil {
					t.Fatalf("%s %s decode failed: %v", sexKey, key, err)
				}
				items = cat.Items
			}
			for _, item := range items {
				if bad[item.CD] {
					t.Errorf("%s %s exposes sex-mismatched code %q", sexKey, key, item.CD)
				}
			}
		}
	}

	var aetc dressCategory
	if err := json.Unmarshal(body.Result["A"]["AETC"], &aetc); err != nil {
		t.Fatalf("AETC decode failed: %v", err)
	}
	if len(aetc.Items) != 0 {
		t.Errorf("AETC = %+v, want empty (no CAFE item has render+dp; CAAH is cat 54)", aetc.Items)
	}

	for _, sexKey := range []string{"M", "F", "A"} {
		sexObj := body.Result[sexKey]
		for key, raw := range sexObj {
			switch key {
			case "SET":
				var group dressSetGroup
				if err := json.Unmarshal(raw, &group); err != nil {
					t.Fatalf("%s SET decode failed: %v", sexKey, err)
				}
				if len(group.Items) < 2 {
					t.Fatalf("%s SET has %d sets, want at least 2", sexKey, len(group.Items))
				}
				for _, set := range group.Items {
					if set.CD == "" || len(set.ElementCodes) == 0 {
						t.Errorf("%s SET: bad set %+v", sexKey, set)
					}
					if !dpReadyCodes[set.CD] {
						t.Errorf("%s SET cd %q has no verified dp.png", sexKey, set.CD)
					}
					for _, cd := range set.ElementCodes {
						if !dpReadyCodes[cd] && !dpLessSetElements[cd] {
							t.Errorf("%s SET %q element %q has no verified dp.png", sexKey, set.CD, cd)
						}
						if !fixtureItemCodes[cd] {
							t.Errorf("%s SET %q element %q unverified", sexKey, set.CD, cd)
						}
					}
				}
			case "DRESS":
				var items []dressItem
				if err := json.Unmarshal(raw, &items); err != nil {
					t.Fatalf("%s DRESS is not a direct array: %v (got %s)", sexKey, err, raw)
				}
				if len(items) < 2 {
					t.Errorf("%s DRESS has %d items, want workable choices", sexKey, len(items))
				}
				assertCatalogCodes(t, sexKey+" DRESS", items)
			default:
				var cat dressCategory
				if err := json.Unmarshal(raw, &cat); err != nil {
					t.Fatalf("%s %s decode failed: %v", sexKey, key, err)
				}
				assertCatalogCodes(t, sexKey+" "+key, cat.Items)
			}
		}
	}

	// Animal bottoms/shoes must stay invisible in the catalog even though they
	// remain valid set elements.
	var animalDress []dressItem
	if err := json.Unmarshal(body.Result["A"]["DRESS"], &animalDress); err != nil {
		t.Fatalf("A DRESS decode failed: %v", err)
	}
	for _, item := range animalDress {
		if dpLessSetElements[item.CD] {
			t.Errorf("A DRESS advertises dp-less set-only item %q", item.CD)
		}
	}

	post := serve(t, http.MethodPost, "/v4/create/all/items")
	if post.Code != http.StatusNotFound || post.Body.String() != notFoundBody {
		t.Fatalf("POST: status = %d, body = %q", post.Code, post.Body.String())
	}
}

func analyzePost(t *testing.T, target string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, target, strings.NewReader(
		"------AvatarDataInvoker_123\r\nContent-Disposition: form-data; name=\"image\"; filename=\"face.jpg\"\r\n"+
			"Content-Type: image/jpeg\r\n\r\nJPEGDATA\r\n------AvatarDataInvoker_123--\r\n"))
	req.Header.Set("Content-Type", "multipart/form-data; boundary=----AvatarDataInvoker_123")
	return serveRequest(t, req)
}

func TestCreateFaceAnalyze(t *testing.T) {
	for _, c := range []struct{ query, sex string }{
		{"?avatarType=MALE", "MALE"},
		{"?avatarType=FEMALE", "FEMALE"},
		{"?avatarType=ANIMAL", "ANIMAL"},
		{"?avatarType=male", "MALE"},
	} {
		rec := analyzePost(t, "/v2/create/face/analyze"+c.query)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: status = %d, body = %q", c.query, rec.Code, rec.Body.String())
		}
		var body struct {
			Result []dressItem `json:"result"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("%s: body decode failed: %v", c.query, err)
		}
		want := workableLook(c.sex)
		if len(body.Result) != len(want) {
			t.Fatalf("%s: result has %d codes, want %d", c.query, len(body.Result), len(want))
		}
		for i, item := range body.Result {
			if item.CD != want[i] {
				t.Errorf("%s: result[%d] = %q, want %q", c.query, i, item.CD, want[i])
			}
		}
		assertFixtureCodes(t, c.query, body.Result)
	}
	for _, query := range []string{"", "?avatarType=", "?avatarType=ROBOT"} {
		rec := analyzePost(t, "/v2/create/face/analyze"+query)
		if rec.Code != http.StatusBadRequest || rec.Body.String() != badRequestBody {
			t.Fatalf("%q: status = %d, body = %q, want 400 %q", query, rec.Code, rec.Body.String(), badRequestBody)
		}
	}
	// The native call is POST (RequestBuilder::PostMultiParts @0x1bba868).
	get := serve(t, http.MethodGet, "/v2/create/face/analyze?avatarType=MALE")
	if get.Code != http.StatusNotFound || get.Body.String() != notFoundBody {
		t.Fatalf("GET: status = %d, body = %q", get.Code, get.Body.String())
	}
}

func TestAvatarInfoTutorialFixtures(t *testing.T) {
	for id, sex := range tutorialAvatarTypes {
		rec := serve(t, http.MethodGet, "/v4/avatar/"+id)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: status = %d, body = %q", id, rec.Code, rec.Body.String())
		}
		var body struct {
			Result *avatarInfoResult `json:"result"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("%s: body decode failed: %v", id, err)
		}
		if body.Result == nil {
			t.Fatalf("%s: result missing", id)
		}
		if body.Result.AvatarID != id {
			t.Errorf("%s: avatarId = %q, want exact echo", id, body.Result.AvatarID)
		}
		if body.Result.Gender != sex {
			t.Errorf("%s: gender = %q, want %q", id, body.Result.Gender, sex)
		}
		if body.Result.Country != "JP" {
			t.Errorf("%s: country = %q, want JP", id, body.Result.Country)
		}
		if body.Result.Skin == "" || body.Result.Name == "" || body.Result.SType == "" {
			t.Errorf("%s: skin/name/sType empty: %+v", id, body.Result)
		}
		want := workableLook(sex)
		if len(body.Result.Items) != len(want) {
			t.Fatalf("%s: items = %+v, want sex-appropriate look %v", id, body.Result.Items, want)
		}
		for i, item := range body.Result.Items {
			if item.CD != want[i] {
				t.Errorf("%s: item[%d] = %q, want %q (sex-appropriate look)", id, i, item.CD, want[i])
			}
			if !fixtureItemCodes[item.CD] {
				t.Errorf("%s: item %q unverified", id, item.CD)
			}
			if item.InvenSeq != "0" || item.DyeType != 0 || len(item.ColorAndTransparencies) != 0 {
				t.Errorf("%s: item = %+v, want invenSeq 0, dyeType 0, [] transparencies", id, item)
			}
		}
		if sex == "MALE" {
			for _, item := range body.Result.Items {
				if retiredCherryCodes[item.CD] || femaleOnlyCodes[item.CD] {
					t.Errorf("%s: male tutorial got non-male code %q", id, item.CD)
				}
			}
		}
		if body.Result.PetProfiles == nil {
			t.Errorf("%s: petProfiles missing", id)
		}
	}
}

func TestAvatarInfoNamedRoutesNotFound(t *testing.T) {
	for _, path := range []string{
		"/v4/avatar/gmAvatarList",
		"/v4/avatar/additionalInfo",
		"/v4/avatar/",
		"/v4/avatar/1/x",
		"/v4/avatar/12ab",
	} {
		rec := serve(t, http.MethodGet, path)
		if rec.Code != http.StatusNotFound || rec.Body.String() != notFoundBody {
			t.Fatalf("%s: status = %d, body = %q, want 404", path, rec.Code, rec.Body.String())
		}
	}
}

func TestCreateAvatarAccountRoundTrip(t *testing.T) {
	accessToken := guestGenerate(t)
	token := avAuthValue(t, createSession(t, accessToken))

	payload := []byte(`{"name":"Hana","avatarType":"FEMALE","nationCode":"JP","skinColor":2,"itemCodes":["CUHA0036Z","CUON0059S","CUAH004JH"],"useMid":true}`)
	rec := createAvatar(t, token, payload, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %q", rec.Code, rec.Body.String())
	}
	var create avatarResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &create); err != nil {
		t.Fatalf("body decode failed: %v", err)
	}
	if create.Result == nil {
		t.Fatal("result missing")
	}
	if create.Result.Name != "Hana" || create.Result.Gender != "FEMALE" {
		t.Errorf("result = %+v, want name Hana / gender FEMALE", create.Result)
	}
	codes := []string{"CUHA0036Z", "CUON0059S", "CUAH004JH"}
	if len(create.Result.Items) != len(codes) {
		t.Fatalf("items = %+v, want %d", create.Result.Items, len(codes))
	}
	for i, item := range create.Result.Items {
		if item.CD != codes[i] || item.InvenSeq != "0" || item.DyeType != 0 || len(item.ColorAndTransparencies) != 0 {
			t.Errorf("items[%d] = %+v", i, item)
		}
	}

	rec = serve(t, http.MethodGet, "/v4/avatar/"+create.Result.AvatarID)
	if rec.Code != http.StatusOK {
		t.Fatalf("avatar info: status = %d, body = %q", rec.Code, rec.Body.String())
	}
	var info struct {
		Result *avatarInfoResult `json:"result"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &info); err != nil {
		t.Fatalf("info body decode failed: %v", err)
	}
	if info.Result == nil {
		t.Fatal("info result missing")
	}
	if info.Result.AvatarID != create.Result.AvatarID || info.Result.Name != "Hana" {
		t.Errorf("info avatarId/name = %q/%q", info.Result.AvatarID, info.Result.Name)
	}
	if info.Result.Gender != "FEMALE" || info.Result.Skin != "2" || info.Result.Country != "JP" {
		t.Errorf("info gender/skin/country = %q/%q/%q, want FEMALE/2/JP", info.Result.Gender, info.Result.Skin, info.Result.Country)
	}
	if len(info.Result.Items) != len(codes) {
		t.Fatalf("info items = %+v, want %d", info.Result.Items, len(codes))
	}
	for i, item := range info.Result.Items {
		if item.CD != codes[i] {
			t.Errorf("info items[%d] = %+v", i, item)
		}
	}
	if info.Result.PetProfiles == nil {
		t.Error("info petProfiles missing")
	}
}

func TestCreateAvatarLegacyShortTypeAndNumericCode(t *testing.T) {
	accessToken := guestGenerate(t)
	token := avAuthValue(t, createSession(t, accessToken))

	payload := []byte(`{"name":"","avatarType":"F","nationCode":"JP","skinColor":"1","itemCodes":["1001"],"useMid":true}`)
	rec := createAvatar(t, token, payload, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %q", rec.Code, rec.Body.String())
	}
	var create avatarResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &create); err != nil {
		t.Fatalf("body decode failed: %v", err)
	}
	if create.Result == nil {
		t.Fatal("result missing")
	}
	if create.Result.Gender != "F" {
		t.Errorf("create gender = %q, want legacy wire echo %q", create.Result.Gender, "F")
	}
	if len(create.Result.Items) != 1 || create.Result.Items[0].CD != "1001" {
		t.Errorf("create items = %+v, want base/skin code 1001 preserved", create.Result.Items)
	}

	rec = serve(t, http.MethodGet, "/v4/avatar/"+create.Result.AvatarID)
	var info struct {
		Result *avatarInfoResult `json:"result"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &info); err != nil {
		t.Fatalf("info body decode failed: %v", err)
	}
	if info.Result == nil || info.Result.Gender != "FEMALE" {
		t.Fatalf("info = %+v, want normalized FEMALE", info.Result)
	}
	if len(info.Result.Items) != 1 || info.Result.Items[0].CD != "1001" {
		t.Errorf("info items = %+v, want code 1001 preserved", info.Result.Items)
	}
}

func TestCreateAvatarMalformedInput(t *testing.T) {
	accessToken := guestGenerate(t)
	token := avAuthValue(t, createSession(t, accessToken))

	rec := createAvatar(t, token, []byte("not gzip"), "gzip")
	if rec.Code != http.StatusBadRequest || rec.Body.String() != badRequestBody {
		t.Fatalf("bad gzip: status = %d, body = %q", rec.Code, rec.Body.String())
	}
	rec = createAvatar(t, token, []byte("{not json"), "")
	if rec.Code != http.StatusBadRequest || rec.Body.String() != badRequestBody {
		t.Fatalf("bad json: status = %d, body = %q", rec.Code, rec.Body.String())
	}

	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write([]byte(`{"name":"x","avatarType":"MALE","itemCodes":["CUON0059S"]}`)); err != nil {
		t.Fatalf("gzip write: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("gzip close: %v", err)
	}
	truncated := buf.Bytes()[:buf.Len()-8]
	rec = createAvatar(t, token, truncated, "gzip")
	if rec.Code != http.StatusBadRequest || rec.Body.String() != badRequestBody {
		t.Fatalf("truncated gzip: status = %d, body = %q", rec.Code, rec.Body.String())
	}

	check := checkSession(t, token)
	if check.Code != http.StatusOK {
		t.Fatalf("checkSession: status = %d, body = %q", check.Code, check.Body.String())
	}
	if aid := decodeSession(t, check).Aid; aid != "0" {
		t.Fatalf("aid = %q after malformed requests, want unchanged %q", aid, "0")
	}
}

func TestCreateAvatarAccountConcurrent(t *testing.T) {
	accessToken := guestGenerate(t)
	token := avAuthValue(t, createSession(t, accessToken))

	payload := []byte(`{"name":"Race","avatarType":"MALE","nationCode":"JP","skinColor":"1","itemCodes":["CUON00164"],"useMid":true}`)
	rec := createAvatar(t, token, payload, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %q", rec.Code, rec.Body.String())
	}
	var body avatarResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body decode failed: %v", err)
	}
	if body.Result == nil {
		t.Fatal("result missing")
	}
	id := body.Result.AvatarID

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			createAvatar(t, token, payload, "")
		}()
		go func() {
			defer wg.Done()
			rec := serve(t, http.MethodGet, "/v4/avatar/"+id)
			if rec.Code != http.StatusOK {
				t.Errorf("avatar info: status = %d, body = %q", rec.Code, rec.Body.String())
			}
		}()
	}
	wg.Wait()
}

func TestSessionResponsesConcurrentWithAvatarCreation(t *testing.T) {
	accessToken := guestGenerate(t)
	initialSession := createSession(t, accessToken)
	if initialSession.Code != http.StatusOK {
		t.Fatalf("createSession: status = %d, body = %q", initialSession.Code, initialSession.Body.String())
	}
	avAuth := avAuthValue(t, initialSession)
	payload := []byte(`{"name":"Concurrent","avatarType":"MALE","nationCode":"JP","skinColor":"1","itemCodes":["CUON00164"],"useMid":true}`)

	start := make(chan struct{})
	createdIDs := make(chan string, 1)
	errs := make(chan string, 3)
	var wg sync.WaitGroup
	serve := func(req *http.Request) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		newMux().ServeHTTP(rec, req)
		return rec
	}

	wg.Add(3)
	go func() {
		defer wg.Done()
		<-start
		req := httptest.NewRequest(http.MethodPost, "/v4/create/avatar", bytes.NewReader(payload))
		req.AddCookie(&http.Cookie{Name: "AV_AUTH", Value: avAuth})
		rec := serve(req)
		if rec.Code != http.StatusOK {
			errs <- "create/avatar: " + rec.Body.String()
			return
		}
		var body avatarResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || body.Result == nil {
			errs <- "create/avatar response could not be decoded"
			return
		}
		createdIDs <- body.Result.AvatarID
	}()
	go func() {
		defer wg.Done()
		<-start
		req := httptest.NewRequest(http.MethodGet, "/v4/checkSession", nil)
		req.AddCookie(&http.Cookie{Name: "AV_AUTH", Value: avAuth})
		rec := serve(req)
		var body sessionResponse
		if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &body) != nil || body.Result == nil {
			errs <- "checkSession response failed: " + rec.Body.String()
		}
	}()
	go func() {
		defer wg.Done()
		<-start
		req := httptest.NewRequest(http.MethodGet, "/v4/createSession?nationCode=JP", nil)
		req.AddCookie(&http.Cookie{Name: "accessToken", Value: accessToken})
		rec := serve(req)
		var body sessionResponse
		if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &body) != nil || body.Result == nil {
			errs <- "createSession response failed: " + rec.Body.String()
		}
	}()
	close(start)
	wg.Wait()
	close(errs)
	failed := false
	for err := range errs {
		t.Error(err)
		failed = true
	}
	if failed {
		return
	}
	var createdID string
	select {
	case createdID = <-createdIDs:
	default:
		t.Fatal("concurrent create/avatar did not return an avatarId")
	}
	if !isAllDigits(createdID) || createdID == "0" {
		t.Fatalf("created avatarId = %q, want nonzero numeric id", createdID)
	}

	check := checkSession(t, avAuth)
	if check.Code != http.StatusOK {
		t.Fatalf("subsequent checkSession: status = %d, body = %q", check.Code, check.Body.String())
	}
	if got := decodeSession(t, check).Aid; got != createdID {
		t.Fatalf("subsequent checkSession aid = %q, want %q", got, createdID)
	}
	created := createSession(t, accessToken)
	if created.Code != http.StatusOK {
		t.Fatalf("subsequent createSession: status = %d, body = %q", created.Code, created.Body.String())
	}
	if got := decodeSession(t, created).Aid; got != createdID {
		t.Fatalf("subsequent createSession aid = %q, want %q", got, createdID)
	}
}

func TestCreateAvatarRejectsNonScalarSkinAndBadType(t *testing.T) {
	accessToken := guestGenerate(t)
	token := avAuthValue(t, createSession(t, accessToken))

	for _, payload := range []string{
		`{"name":"x","avatarType":"MALE","skinColor":{"a":1},"itemCodes":["CUON00164"]}`,
		`{"name":"x","avatarType":"MALE","skinColor":[1],"itemCodes":["CUON00164"]}`,
		`{"name":"x","avatarType":"MALE","skinColor":true,"itemCodes":["CUON00164"]}`,
		`{"name":"x","avatarType":"ROBOT","skinColor":"1","itemCodes":["CUON00164"]}`,
		`{"name":"x","avatarType":"","skinColor":"1","itemCodes":["CUON00164"]}`,
		`{"name":"x","avatarType":"MALE","skinColor":"1","itemCodes":[{"cd":"CUON00164"}]}`,
		`{"name":"x","avatarType":"MALE","skinColor":"1","itemCodes":[null]}`,
	} {
		rec := createAvatar(t, token, []byte(payload), "")
		if rec.Code != http.StatusBadRequest || rec.Body.String() != badRequestBody {
			t.Fatalf("%s: status = %d, body = %q, want 400", payload, rec.Code, rec.Body.String())
		}
	}

	check := checkSession(t, token)
	if check.Code != http.StatusOK {
		t.Fatalf("checkSession: status = %d, body = %q", check.Code, check.Body.String())
	}
	if aid := decodeSession(t, check).Aid; aid != "0" {
		t.Fatalf("aid = %q after rejected requests, want unchanged %q", aid, "0")
	}
}

func TestCreateAvatarRejectsOversizeBody(t *testing.T) {
	accessToken := guestGenerate(t)
	token := avAuthValue(t, createSession(t, accessToken))

	payload := `{"name":"` + strings.Repeat("a", maxCreateAvatarBody) + `","avatarType":"MALE"}`
	rec := createAvatar(t, token, []byte(payload), "")
	if rec.Code != http.StatusBadRequest || rec.Body.String() != badRequestBody {
		t.Fatalf("status = %d, body len = %d, want 400", rec.Code, rec.Body.Len())
	}

	check := checkSession(t, token)
	if aid := decodeSession(t, check).Aid; aid != "0" {
		t.Fatalf("aid = %q after oversize request, want unchanged %q", aid, "0")
	}
}

func TestCreateAvatarNumericItemCodes(t *testing.T) {
	accessToken := guestGenerate(t)
	token := avAuthValue(t, createSession(t, accessToken))

	rec := createAvatar(t, token, []byte(`{"name":"","avatarType":"FEMALE","skinColor":1,"itemCodes":[1001,"CUON001CH"],"useMid":true}`), "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %q", rec.Code, rec.Body.String())
	}
	var body avatarResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body decode failed: %v", err)
	}
	if body.Result == nil || len(body.Result.Items) != 2 {
		t.Fatalf("result = %+v, want two items", body.Result)
	}
	if body.Result.Items[0].CD != "1001" || body.Result.Items[1].CD != "CUON001CH" {
		t.Errorf("items = %+v, want numeric 1001 then CUON001CH", body.Result.Items)
	}
}

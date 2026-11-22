package room

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"cherry/internal/store"
	"cherry/internal/testutil"
)

// petBookKinds is the ResPetBookList element as IDA reads it (rows-d-spec 4.2, 0x1c34340): every key with its
// JSON kind, n number, s string, b bool.
const petBookKinds = "raceId:n catgCd:s itemNo:n itemCode:s itemSaleType:s localeCd:s raceNm:s petId:s petMemo:s " +
	"avatarId:s firstVisited:s firstLevelUp:s lastVisited:s initActivityRemainTimestamp:s callPayCode:s " +
	"exposureYn:b arrangedYn:b defaultCallYn:b defaultCallRemainTimestamp:s giftItemId:n giftItemCode:s " +
	"petSleepingYn:b finalLevelYn:b coppaStatus:n petName:s exp:n maxExp:n level:n receiveHeart:n skinSerial:s " +
	"skinId:n skinCode:s linkPosX:n linkPosY:n linkScale:n firstVisitYn:b representYn:b interiorOwnYn:b " +
	"paidCallCount:n maxPaidCallCount:n callPrice:n"

// petBookReply decodes a pet book reply into its raw text and its list rows.
func petBookReply(t *testing.T, rec *httptest.ResponseRecorder) (string, []map[string]any) {
	t.Helper()
	var env struct {
		Result struct {
			List []map[string]any `json:"list"`
		} `json:"result"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("reply %s: %v", rec.Body, err)
	}
	return rec.Body.String(), env.Result.List
}

// checkPetBookRow fails unless row has exactly the petBookKinds keys, each with its JSON kind.
func checkPetBookRow(t *testing.T, row map[string]any) {
	t.Helper()
	kinds := strings.Fields(petBookKinds)
	if len(row) != len(kinds) {
		t.Fatalf("row has %d keys, want %d: %v", len(row), len(kinds), row)
	}
	for _, spec := range kinds {
		key, want, _ := strings.Cut(spec, ":")
		v, ok := row[key]
		if !ok {
			t.Fatalf("missing key %s", key)
		}
		var got string
		switch v.(type) {
		case float64:
			got = "n"
		case string:
			got = "s"
		case bool:
			got = "b"
		default:
			got = "?"
		}
		if got != want {
			t.Fatalf("%s is %s, want %s", key, got, want)
		}
	}
}

// TestPetBookRowTypes: the list is an array of elements with exactly the IDA keys and types, for owned pets
// (MYPET) and for catalog pets of a race tab; a tab with no catalog pet and an unknown session are handled.
func TestPetBookRowTypes(t *testing.T) {
	const tok = "pet-book-token"
	testutil.InstallAccounts(t, map[string]*store.Account{tok: {Aid: "1001", Pets: []store.PetItem{
		{ID: 100101, Cd: PetItemCode(16), Name: "Minipet 1", Rep: true},
		{ID: 100102, Cd: PetItemCode(17), Name: "Minipet 2", Room: "LEVEL_1"},
	}}})
	species := func() []PetSpecies {
		return []PetSpecies{
			{Code: PetItemCode(16), ID: int64(PetSkinID(PetItemCode(16))), Catg: "UDOG", Name: "Mixed Breed"},
			{Code: PetItemCode(18), ID: int64(PetSkinID(PetItemCode(18))), Catg: "UDOG", Name: "Mame Shiba"},
		}
	}
	get := func(category string, withSession bool) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/v4/pet/book/race/list/"+category, nil)
		if withSession {
			req.AddCookie(&http.Cookie{Name: "AV_AUTH", Value: tok})
		}
		HandlePetBook(species)(rec, req)
		return rec
	}

	_, mine := petBookReply(t, get("MYPET", true))
	if len(mine) != 2 {
		t.Fatalf("MYPET rows = %d, want 2", len(mine))
	}
	for _, row := range mine {
		checkPetBookRow(t, row)
		if row["defaultCallYn"] != true {
			t.Fatalf("owned row %v: defaultCallYn is not true", row)
		}
	}
	if mine[0]["petId"] != "100101" || mine[0]["petName"] != "Minipet 1" || mine[0]["representYn"] != true || mine[0]["raceNm"] != "Mixed Breed" {
		t.Fatalf("owned row 0 = %v", mine[0])
	}
	if mine[1]["arrangedYn"] != true || mine[1]["catgCd"] != "" || mine[1]["raceNm"] != "Minipet" {
		t.Fatalf("owned row 1 = %v (arranged; no catalog entry for code 17)", mine[1])
	}

	_, udog := petBookReply(t, get("UDOG", false))
	if len(udog) != 2 {
		t.Fatalf("UDOG rows = %d, want 2 (the catalog Dogs)", len(udog))
	}
	checkPetBookRow(t, udog[1])
	if udog[1]["petId"] != "" || udog[1]["catgCd"] != "UDOG" || udog[1]["itemCode"] != PetItemCode(18) || udog[1]["raceNm"] != "Mame Shiba" {
		t.Fatalf("catalog row = %v", udog[1])
	}

	raw, none := petBookReply(t, get("UTOY", false))
	if len(none) != 0 || !strings.Contains(raw, `"list":[]`) {
		t.Fatalf("empty tab reply %s, want an empty list array", raw)
	}
	if rec := get("MYPET", false); rec.Code != http.StatusNotFound {
		t.Fatalf("MYPET without a session: status %d, want 404", rec.Code)
	}
}

// petReq calls HandlePet for path (under /v4/pet/) with the session token tok; no cookie when tok is empty.
func petReq(t *testing.T, tok, path string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v4/pet/"+path, nil)
	if tok != "" {
		req.AddCookie(&http.Cookie{Name: "AV_AUTH", Value: tok})
	}
	HandlePet(rec, req)
	return rec
}

// petResult decodes the result object of a pet reply.
func petResult(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var env struct {
		Result map[string]any `json:"result"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("reply %s: %v", rec.Body, err)
	}
	return env.Result
}

// petRepIDs returns the petIds of the represent list (inven/represent) of avatar aid.
func petRepIDs(t *testing.T, tok, aid string) []string {
	t.Helper()
	var env struct {
		Result []map[string]any `json:"result"`
	}
	rec := petReq(t, tok, "inven/represent?avatarId="+aid)
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("represent reply %s: %v", rec.Body, err)
	}
	ids := []string{}
	for _, row := range env.Result {
		id, _ := row["petId"].(string)
		ids = append(ids, id)
	}
	return ids
}

// TestPetCallArrangesOwnedPet: room/call arranges an unarranged owned pet on LEVEL_1 and keeps an arranged pet's
// level; the reply has int resultCode and balance, string groundLevel; another account's or an unknown pet is 400.
func TestPetCallArrangesOwnedPet(t *testing.T) {
	const tok, other = "pet-call-token", "pet-call-other"
	testutil.InstallAccounts(t, map[string]*store.Account{
		tok: {Aid: "1001", Gems: 500, Pets: []store.PetItem{
			{ID: 100101, Cd: PetItemCode(16), Name: "Minipet 1"},
			{ID: 100102, Cd: PetItemCode(17), Name: "Minipet 2", Room: "LEVEL_2"},
		}},
		other: {Aid: "1002", Pets: []store.PetItem{{ID: 100201, Cd: PetItemCode(18), Name: "Minipet 3"}}},
	})

	rec := petReq(t, tok, "room/call/100101")
	if rec.Code != http.StatusOK {
		t.Fatalf("call status %d: %s", rec.Code, rec.Body)
	}
	res := petResult(t, rec)
	if res["resultCode"] != float64(0) || res["balance"] != float64(500) || res["balanceUnit"] != "GEM" || res["groundLevel"] != "LEVEL_1" {
		t.Fatalf("call reply %v", res)
	}
	var arranged struct {
		Result struct {
			List []map[string]any `json:"list"`
		} `json:"result"`
	}
	if err := json.Unmarshal(petReq(t, tok, "room/arrange/list/1001/LEVEL_1").Body.Bytes(), &arranged); err != nil {
		t.Fatalf("arrange list: %v", err)
	}
	if len(arranged.Result.List) != 1 || arranged.Result.List[0]["petId"] != "100101" {
		t.Fatalf("LEVEL_1 arrange list %v, want pet 100101", arranged.Result.List)
	}

	if res := petResult(t, petReq(t, tok, "room/call/100102")); res["groundLevel"] != "LEVEL_2" {
		t.Fatalf("arranged pet level changed: %v", res)
	}
	for _, path := range []string{"room/call/100201", "room/call/999"} {
		if rec := petReq(t, tok, path); rec.Code != http.StatusBadRequest || rec.Body.String() != `{"errorCode":"400"}` {
			t.Fatalf("%s: status %d body %s, want 400", path, rec.Code, rec.Body)
		}
	}
	if rec := petReq(t, "", "room/call/100101"); rec.Code != http.StatusNotFound {
		t.Fatalf("call without a session: status %d, want 404", rec.Code)
	}
}

// TestPetCallArrangeCap: a ninth pet is refused on LEVEL_1 (PetMaxArrange), an arranged one still answers.
func TestPetCallArrangeCap(t *testing.T) {
	const tok = "pet-cap-token"
	pets := []store.PetItem{}
	for i := range PetMaxArrange + 1 {
		p := store.PetItem{ID: int64(100301 + i), Cd: PetItemCode(16), Name: "Minipet"}
		if i < PetMaxArrange {
			p.Room = DefaultLevel
		}
		pets = append(pets, p)
	}
	testutil.InstallAccounts(t, map[string]*store.Account{tok: {Aid: "1003", Pets: pets}})

	if rec := petReq(t, tok, "room/call/100309"); rec.Code != http.StatusBadRequest {
		t.Fatalf("ninth pet: status %d, want 400", rec.Code)
	}
	if res := petResult(t, petReq(t, tok, "room/call/100301")); res["groundLevel"] != "LEVEL_1" {
		t.Fatalf("arranged pet at the cap: %v", res)
	}
}

// TestPetFollowToggles: inven/status/represent makes the pet the single Rep, clears it when it already is one.
func TestPetFollowToggles(t *testing.T) {
	const tok = "pet-follow-token"
	testutil.InstallAccounts(t, map[string]*store.Account{tok: {Aid: "1004", Pets: []store.PetItem{
		{ID: 100401, Cd: PetItemCode(16), Name: "Minipet 1", Rep: true},
		{ID: 100402, Cd: PetItemCode(17), Name: "Minipet 2"},
	}}})
	steps := []struct {
		id   string
		want []string
	}{
		{"100402", []string{"100402"}}, // switches the single Rep
		{"100402", []string{}},         // already Rep: cleared
		{"100401", []string{"100401"}}, // back on
	}
	for _, s := range steps {
		rec := petReq(t, tok, "inven/status/represent/"+s.id)
		if rec.Code != http.StatusOK || rec.Body.String() != `{"result":{}}` {
			t.Fatalf("follow %s: status %d body %s", s.id, rec.Code, rec.Body)
		}
		if got := petRepIDs(t, tok, "1004"); !slices.Equal(got, s.want) {
			t.Fatalf("after follow %s: rep list %v, want %v", s.id, got, s.want)
		}
	}
	if rec := petReq(t, tok, "inven/status/represent/999"); rec.Code != http.StatusBadRequest {
		t.Fatalf("follow of an unknown pet: status %d, want 400", rec.Code)
	}
}

// TestPetDefaultSkinEmpty: the DP skin info is the silent empty object for a session, 404 without one.
func TestPetDefaultSkinEmpty(t *testing.T) {
	const tok = "pet-skin-token"
	testutil.InstallAccounts(t, map[string]*store.Account{tok: {Aid: "1005"}})
	const path = "book/race/info/default/skin?interiorItemCode=RUPI0000001"
	if rec := petReq(t, tok, path); rec.Code != http.StatusOK || rec.Body.String() != `{"result":{}}` {
		t.Fatalf("default skin: status %d body %s", rec.Code, rec.Body)
	}
	if rec := petReq(t, "", path); rec.Code != http.StatusNotFound {
		t.Fatalf("default skin without a session: status %d, want 404", rec.Code)
	}
}

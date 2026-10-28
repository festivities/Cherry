package room

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
		return []PetSpecies{{Code: PetItemCode(18), ID: int64(PetSkinID(PetItemCode(18))), Catg: "UDOG"}}
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
	}
	if mine[0]["petId"] != "100101" || mine[0]["petName"] != "Minipet 1" || mine[0]["representYn"] != true {
		t.Fatalf("owned row 0 = %v", mine[0])
	}
	if mine[1]["arrangedYn"] != true || mine[1]["catgCd"] != "" {
		t.Fatalf("owned row 1 = %v (arranged; no catalog category for code 17)", mine[1])
	}

	_, udog := petBookReply(t, get("UDOG", false))
	if len(udog) != 1 {
		t.Fatalf("UDOG rows = %d, want 1 (the catalog Dog)", len(udog))
	}
	checkPetBookRow(t, udog[0])
	if udog[0]["petId"] != "" || udog[0]["catgCd"] != "UDOG" || udog[0]["itemCode"] != PetItemCode(18) {
		t.Fatalf("catalog row = %v", udog[0])
	}

	raw, none := petBookReply(t, get("UTOY", false))
	if len(none) != 0 || !strings.Contains(raw, `"list":[]`) {
		t.Fatalf("empty tab reply %s, want an empty list array", raw)
	}
	if rec := get("MYPET", false); rec.Code != http.StatusNotFound {
		t.Fatalf("MYPET without a session: status %d, want 404", rec.Code)
	}
}

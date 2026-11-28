package economy

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"cherry/internal/room"
	"cherry/internal/store"
	"cherry/internal/testutil"
)

// miniPetClock pins NowFunc for one test.
func miniPetClock(t *testing.T, at time.Time) *time.Time {
	t.Helper()
	clock := at
	prev := NowFunc
	NowFunc = func() time.Time { return clock }
	t.Cleanup(func() { NowFunc = prev })
	return &clock
}

// miniPetOwner is an account whose LEVEL_1 holds the room item of PUPE0000G (pet 16, cuddles 100, 800 Gems).
func miniPetOwner(aid string, gems int64, placed bool) *store.Account {
	acc := &store.Account{Aid: aid, Gems: gems, RoomItems: []store.RoomItem{{Seq: 100000, Cd: "RUPI00007"}}}
	acc.Rooms = map[string]store.RoomLayout{}
	if placed {
		acc.Rooms["LEVEL_1"] = store.RoomLayout{Placed: []store.RoomPlaced{{Seq: 100000, Cd: "RUPI00007"}}}
	}
	return acc
}

// miniPetReq sends one request to the Minipet routes with the token cookie (none when tok is "").
func miniPetReq(t *testing.T, tok, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(method, "/v4/pet/"+path, strings.NewReader(body))
	if tok != "" {
		req.AddCookie(&http.Cookie{Name: "AV_AUTH", Value: tok})
	}
	HandlePet(rec, req)
	return rec
}

// miniPetErr decodes a refusal: its status and errorCode.
func miniPetErr(t *testing.T, rec *httptest.ResponseRecorder) (int, string) {
	t.Helper()
	var body struct {
		ErrorCode string `json:"errorCode"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("refusal %s: %v", rec.Body, err)
	}
	return rec.Code, body.ErrorCode
}

// miniPetResult decodes the result object of a 200 reply.
func miniPetResult(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	var env struct {
		Result map[string]any `json:"result"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("reply %s: %v", rec.Body, err)
	}
	return env.Result
}

// TestMiniPetCheckPairs: the startup check refuses a bad room item, a duplicate item and bad tuning.
func TestMiniPetCheckPairs(t *testing.T) {
	const ok = `"origin":"mixed","note":"x",`
	const rules = `"minipet":{"gained_exp":10,"visit_minutes":60,"presents":["CUON004A9"]},`
	cases := []struct{ name, raw, want string }{
		{"not a room item", `{` + ok + rules + `"pets":{"PUPE0000G":{"name":"A","item":"RUCH0011X"}}}`, "must be a RUPI room item"},
		{"undecodable", `{` + ok + rules + `"pets":{"PUPE0000G":{"name":"A","item":"RUPIZZZZZ"}}}`, "must be a RUPI room item"},
		{"two pets one item", `{` + ok + rules + `"pets":{"PUPE0000G":{"name":"A","item":"RUPI00007"},"PUPE0000H":{"name":"B","item":"RUPI00007"}}}`, "already places pet"},
		{"zero gained exp", `{` + ok + `"minipet":{"gained_exp":0,"visit_minutes":60,"presents":["CUON004A9"]},"pets":{}}`, "gained_exp"},
		{"bad present", `{` + ok + `"minipet":{"gained_exp":10,"visit_minutes":60,"presents":["RUPI00007"]},"pets":{}}`, "present"},
		{"no presents", `{` + ok + `"minipet":{"gained_exp":10,"visit_minutes":60,"presents":[]},"pets":{}}`, "presents must not be empty"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var c PetCatalog
			if err := json.Unmarshal([]byte(tc.raw), &c); err != nil {
				t.Fatal(err)
			}
			err := checkPets(&c)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("checkPets = %v, want %q", err, tc.want)
			}
		})
	}
}

// TestMiniPetCallNeedsPlacedItem: a call without the placed room item is refused with 600202 (popup 2201).
func TestMiniPetCallNeedsPlacedItem(t *testing.T) {
	testutil.InstallAccounts(t, map[string]*store.Account{"mp-unplaced": miniPetOwner("7002", 0, false)})
	testutil.SetLab(t, "7002", false)
	miniPetClock(t, time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC))
	id := strconv.FormatInt(room.MiniPetVisitID("7002", "PUPE0000G"), 10)
	rec := miniPetReq(t, "mp-unplaced", http.MethodGet, "room/call/"+id, "")
	if status, code := miniPetErr(t, rec); status != http.StatusBadRequest || code != errPetNotPlaced {
		t.Fatalf("unplaced call: %d %s, want 400 %s", status, rec.Body, errPetNotPlaced)
	}
}

// TestMiniPetCallVisitArrangeAndCooldown: a placed item's call starts a visit (the arrange row shows the pet with the
// placed item code, exposed and touchable), the free call waits for its cooldown, a paid call costs Gems and ignores it.
func TestMiniPetCallVisitArrangeAndCooldown(t *testing.T) {
	testutil.InstallAccounts(t, map[string]*store.Account{"mp-owner": miniPetOwner("7001", 100, true)})
	testutil.SetLab(t, "7001", false)
	clock := miniPetClock(t, time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC))
	id := strconv.FormatInt(room.MiniPetVisitID("7001", "PUPE0000G"), 10)

	wantRemainMs := strconv.FormatInt(int64(ActivePets().Minipet.FreeCallMinutes)*60*1000, 10)
	res := miniPetResult(t, miniPetReq(t, "mp-owner", http.MethodGet, "room/call/"+id, ""))
	if res["resultCode"] != float64(0) || res["groundLevel"] != "LEVEL_1" || res["purchasedCallFlag"] != false ||
		res["callPayCode"] != "GEM" || res["defaultCallRemainTimestamp"] != wantRemainMs {
		t.Fatalf("free call reply %v (want remain %s)", res, wantRemainMs)
	}
	if status, code := miniPetErr(t, miniPetReq(t, "mp-owner", http.MethodGet, "room/call/"+id, "")); status != http.StatusBadRequest || code != errPetCooldown {
		t.Fatalf("second free call: %d %s, want %s", status, code, errPetCooldown)
	}

	var arranged struct {
		Result struct {
			List []map[string]any `json:"list"`
		} `json:"result"`
	}
	rec := miniPetReq(t, "mp-owner", http.MethodGet, "room/arrange/list/7001/LEVEL_1", "")
	if err := json.Unmarshal(rec.Body.Bytes(), &arranged); err != nil || len(arranged.Result.List) != 1 {
		t.Fatalf("arrange list %s: %v", rec.Body, err)
	}
	row := arranged.Result.List[0]
	want := map[string]any{"itemCode": "RUPI00007", "skinCode": "PUPE0000G", "petId": id, "avatarId": "7001",
		"exposureYn": true, "petTouchableYn": true, "defaultCallYn": false, "callPayCode": "GEM",
		"defaultCallRemainTimestamp": wantRemainMs, "exp": float64(0), "maxExp": float64(100),
		"groundLevel": "LEVEL_1", "finalLevelYn": false}
	for k, v := range want {
		if row[k] != v {
			t.Fatalf("arrange row %s = %v, want %v (row %v)", k, row[k], v, row)
		}
	}

	// A paid call (Gems only, accepting "GEM" or "") starts the next visit at once, 30 Gems.
	body := `{"language":"en","deviceType":"x","cpId":"avtplay","coinUnit":"GEM","price":30,"petId":"` + id + `"}`
	res = miniPetResult(t, miniPetReq(t, "mp-owner", http.MethodPost, "book/prch/call", body))
	if res["balance"] != float64(70) || res["purchasedCallFlag"] != true ||
		res["callPayCode"] != "GEM" || res["defaultCallRemainTimestamp"] != wantRemainMs {
		t.Fatalf("paid call reply %v", res)
	}
	emptyUnit := `{"coinUnit":"","petId":"` + id + `"}`
	res = miniPetResult(t, miniPetReq(t, "mp-owner", http.MethodPost, "book/prch/call", emptyUnit))
	if res["balance"] != float64(40) || res["purchasedCallFlag"] != true {
		t.Fatalf("empty coinUnit paid call reply %v", res)
	}
	cash := `{"coinUnit":"CASH","petId":"` + id + `"}`
	if status, _ := miniPetErr(t, miniPetReq(t, "mp-owner", http.MethodPost, "book/prch/call", cash)); status != http.StatusBadRequest {
		t.Fatalf("cash paid call status %d, want 400", status)
	}

	// The visit ends after VisitMinutes: the pet is no longer exposed, and a free call is open after its cooldown.
	*clock = clock.Add(2 * time.Hour)
	rec = miniPetReq(t, "mp-owner", http.MethodGet, "room/arrange/represent/list/7001/LEVEL_1", "")
	if !strings.Contains(rec.Body.String(), `"exposureYn":false`) {
		t.Fatalf("visit still exposed after 2 h: %s", rec.Body)
	}
	*clock = clock.Add(3 * time.Hour)
	miniPetResult(t, miniPetReq(t, "mp-owner", http.MethodGet, "room/call/"+id, ""))
}

// TestMiniPetPettingAndAdoption: one petting per visit adds gainedExp; the petting that reaches maxExp adopts the pet
// (it joins the owned pets with the same petId) and gives a present to the closet.
func TestMiniPetPettingAndAdoption(t *testing.T) {
	testutil.InstallAccounts(t, map[string]*store.Account{"mp-pet": miniPetOwner("7003", 0, true)})
	testutil.SetLab(t, "7003", false)
	clock := miniPetClock(t, time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC))
	prevPick := presentPick
	presentPick = func(int) int { return 0 }
	t.Cleanup(func() { presentPick = prevPick })

	id := room.MiniPetVisitID("7003", "PUPE0000G")
	if _, ok := MiniPetStroke("7003", id); ok {
		t.Fatal("a pet that is not visiting was petted")
	}
	call := func() {
		t.Helper()
		miniPetResult(t, miniPetReq(t, "mp-pet", http.MethodGet, "room/call/"+strconv.FormatInt(id, 10), ""))
	}
	call()
	reply, ok := MiniPetStroke("7003", id)
	if !ok || reply.Exp != 10 || reply.MaxExp != 100 || reply.Gained != 10 || reply.Final || reply.LevelUp {
		t.Fatalf("first petting = %+v %v", reply, ok)
	}
	if _, ok := MiniPetStroke("7003", id); ok {
		t.Fatal("second petting in one visit was counted")
	}

	for range 9 {
		*clock = clock.Add(4 * time.Hour)
		call()
		if _, ok := MiniPetStroke("7003", id); !ok {
			t.Fatal("petting after a new visit was refused")
		}
	}
	// The tenth petting reached maxExp: the last reply was that one, so check the account instead.
	store.AccountsMu.Lock()
	acc := store.AccountByTokenLocked("mp-pet")
	owned, isOwned := ownedPet(acc, "PUPE0000G")
	inventory := store.AccountInventoryCodes(acc)
	store.AccountsMu.Unlock()
	if !isOwned || owned.ID != id || owned.Name != PetDisplayName("PUPE0000G") {
		t.Fatalf("adopted pet = %+v, owned %v", owned, isOwned)
	}
	if present := ActivePets().Minipet.Presents[0]; !slices.Contains(inventory, present) {
		t.Fatalf("present %s not in the closet: %v", present, inventory)
	}
}

// TestMiniPetSkinAndCap: default/skin names the pet of a paired item and is empty for any other code; the arrange cap
// is five.
func TestMiniPetSkinAndCap(t *testing.T) {
	testutil.InstallAccounts(t, map[string]*store.Account{"mp-skin": miniPetOwner("7004", 0, true)})
	testutil.SetLab(t, "7004", false)
	res := miniPetResult(t, miniPetReq(t, "mp-skin", http.MethodGet, "book/race/info/default/skin?interiorItemCode=RUPI00007", ""))
	if res["itemCode"] != "RUPI00007" || res["skinCode"] != "PUPE0000G" || res["linkScale"] != float64(100) || res["linkPosX"] != float64(0) {
		t.Fatalf("default skin %v", res)
	}
	if res := miniPetResult(t, miniPetReq(t, "mp-skin", http.MethodGet, "book/race/info/default/skin?interiorItemCode=RUCH0011X", "")); len(res) != 0 {
		t.Fatalf("unpaired item skin %v, want empty", res)
	}
	if res := miniPetResult(t, miniPetReq(t, "mp-skin", http.MethodGet, "room/arrange/maxcount", "")); res["maxCount"] != float64(5) {
		t.Fatalf("maxcount %v", res)
	}
}

// TestMiniPetBookStateAndDetail verifies Pet Book state across MYPET, UDOG (owned/placed, visiting, cooldown,
// and unowned), and GET /v4/pet/book/race/detail?petId=<id>.
func TestMiniPetBookStateAndDetail(t *testing.T) {
	root := shopFixture(t, map[string][]string{
		"interior/122900007": {"dp.png"}, // RUPI00007 -> PUPE0000G
		"interior/122900012": {"dp.png"}, // RUPI0000C -> PUPE0000I
		"pet/326400016":      {"skeleton.xml", "dp.png", "behavior.xml"},
		"pet/326400018":      {"skeleton.xml", "dp.png", "behavior.xml"},
	})
	for _, id := range []string{"326400016", "326400018"} {
		if err := os.WriteFile(filepath.Join(root, "pet", id, "behavior.xml"), []byte(`{"sndindex":215}`), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	bookList := func(tok, cat string) []map[string]any {
		t.Helper()
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/v4/pet/book/race/list/"+cat, nil)
		if tok != "" {
			req.AddCookie(&http.Cookie{Name: "AV_AUTH", Value: tok})
		}
		HandlePetBook(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("HandlePetBook(%s) status %d: %s", cat, rec.Code, rec.Body)
		}
		var env struct {
			Result struct {
				List []map[string]any `json:"list"`
			} `json:"result"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
			t.Fatalf("decode %s: %v", rec.Body, err)
		}
		return env.Result.List
	}
	findRow := func(rows []map[string]any, skin string) map[string]any {
		t.Helper()
		for _, r := range rows {
			if r["skinCode"] == skin {
				return r
			}
		}
		t.Fatalf("missing skinCode %s in %v", skin, rows)
		return nil
	}

	// 1. UDOG: RUPI00007 owned and placed, not visiting yet; PUPE0000I unowned.
	testutil.InstallAccounts(t, map[string]*store.Account{"mp-book": miniPetOwner("7007", 100, true)})
	testutil.SetLab(t, "7007", false)
	clock := miniPetClock(t, time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC))
	visitID := strconv.FormatInt(room.MiniPetVisitID("7007", "PUPE0000G"), 10)

	udog := bookList("mp-book", "UDOG")
	g := findRow(udog, "PUPE0000G")
	if g["interiorOwnYn"] != true || g["arrangedYn"] != true || g["exposureYn"] != false ||
		g["defaultCallYn"] != true || g["level"] != float64(0) || g["callPayCode"] != "GEM" {
		t.Fatalf("owned+placed UDOG before visit = %v", g)
	}
	unowned := findRow(udog, "PUPE0000I")
	if unowned["interiorOwnYn"] != false || unowned["skinSerial"] != "0" || unowned["petId"] != "0" || unowned["exposureYn"] != true || unowned["callPayCode"] != "GEM" {
		t.Fatalf("unowned UDOG = %v", unowned)
	}

	// 2. Start visit -> exposureYn: true, defaultCallYn: false; detail returns single PetBookRow object.
	wantFullRemain := strconv.FormatInt(int64(ActivePets().Minipet.FreeCallMinutes)*60*1000, 10)
	miniPetResult(t, miniPetReq(t, "mp-book", http.MethodGet, "room/call/"+visitID, ""))
	g = findRow(bookList("mp-book", "UDOG"), "PUPE0000G")
	if g["exposureYn"] != true || g["defaultCallYn"] != false || g["interiorOwnYn"] != true ||
		g["arrangedYn"] != true || g["level"] != float64(0) || g["callPayCode"] != "GEM" ||
		g["defaultCallRemainTimestamp"] != wantFullRemain {
		t.Fatalf("UDOG during visit = %v", g)
	}
	detailVisit := miniPetResult(t, miniPetReq(t, "mp-book", http.MethodGet, "book/race/detail?petId="+visitID, ""))
	if detailVisit["petId"] != visitID || detailVisit["exposureYn"] != true || detailVisit["defaultCallYn"] != false ||
		detailVisit["level"] != float64(0) || detailVisit["callPayCode"] != "GEM" ||
		detailVisit["defaultCallRemainTimestamp"] != wantFullRemain {
		t.Fatalf("detail visited pet = %v", detailVisit)
	}

	// 3. Visit ends while still on free-call cooldown -> exposureYn: false, defaultCallYn: false, remain in ms.
	elapsedMin := ActivePets().Minipet.VisitMinutes + 1
	*clock = clock.Add(time.Duration(elapsedMin) * time.Minute)
	wantRemain := int64(ActivePets().Minipet.FreeCallMinutes-elapsedMin) * 60 * 1000
	g = findRow(bookList("mp-book", "UDOG"), "PUPE0000G")
	remain, err := strconv.ParseInt(g["defaultCallRemainTimestamp"].(string), 10, 64)
	if err != nil || remain != wantRemain || g["defaultCallYn"] != false || g["exposureYn"] != false || g["callPayCode"] != "GEM" {
		t.Fatalf("UDOG on cooldown = %v (remain %d, want %d, err %v)", g, remain, wantRemain, err)
	}

	// 4. MYPET and detail for owned pets (paired and unpaired), including other player's pet lookup and zeroed cooldown.
	const ownedPairedID, ownedUnpairedID = "700801", "700802"
	accOwned := miniPetOwner("7008", 100, true)
	accOwned.Pets = []store.PetItem{
		{ID: 700801, Cd: "PUPE0000G", Name: "Mixed Breed", Rep: true},
		{ID: 700802, Cd: room.PetItemCode(1), Name: "Legacy Pet", Room: "LEVEL_1"},
	}
	accOwned.MiniPets = []store.MiniPetState{{Cd: "PUPE0000G", NextCall: clock.Add(2 * time.Hour).Unix(), Exp: 100}}
	accViewer := miniPetOwner("7009", 0, false)
	testutil.InstallAccounts(t, map[string]*store.Account{"mp-owned": accOwned, "mp-viewer": accViewer})
	testutil.SetLab(t, "7008", false)
	testutil.SetLab(t, "7009", false)

	mine := bookList("mp-owned", "MYPET")
	if len(mine) != 2 {
		t.Fatalf("MYPET len = %d, want 2", len(mine))
	}
	for _, r := range mine {
		if r["interiorOwnYn"] != true || r["level"] != float64(1) || r["callPayCode"] != "GEM" || r["defaultCallRemainTimestamp"] != "0" {
			t.Fatalf("MYPET row bad fields: %v", r)
		}
	}
	pRow := findRow(mine, "PUPE0000G")
	if pRow["itemCode"] != "RUPI00007" || pRow["arrangedYn"] != true || pRow["exposureYn"] != true || pRow["representYn"] != true {
		t.Fatalf("MYPET paired row = %v", pRow)
	}
	ownedUdog := findRow(bookList("mp-owned", "UDOG"), "PUPE0000G")
	if ownedUdog["defaultCallRemainTimestamp"] != "0" || ownedUdog["defaultCallYn"] != false || ownedUdog["callPayCode"] != "GEM" {
		t.Fatalf("owned UDOG with pre-adoption NextCall = %v", ownedUdog)
	}
	arrRes := miniPetResult(t, miniPetReq(t, "mp-owned", http.MethodGet, "room/arrange/list/7008/LEVEL_1", ""))
	arrList := arrRes["list"].([]any)
	arrPaired := arrList[0].(map[string]any)
	if arrPaired["defaultCallRemainTimestamp"] != "0" || arrPaired["defaultCallYn"] != false || arrPaired["callPayCode"] != "GEM" {
		t.Fatalf("owned arrange row with pre-adoption NextCall = %v", arrPaired)
	}
	detailOwned := miniPetResult(t, miniPetReq(t, "mp-owned", http.MethodGet, "book/race/detail?petId="+ownedPairedID, ""))
	if detailOwned["petId"] != ownedPairedID || detailOwned["interiorOwnYn"] != true || detailOwned["level"] != float64(1) || detailOwned["callPayCode"] != "GEM" {
		t.Fatalf("detail owned paired = %v", detailOwned)
	}
	detailLegacy := miniPetResult(t, miniPetReq(t, "mp-owned", http.MethodGet, "book/race/detail?petId="+ownedUnpairedID, ""))
	if detailLegacy["petId"] != ownedUnpairedID || detailLegacy["interiorOwnYn"] != true || detailLegacy["itemCode"] != room.PetItemCode(1) || detailLegacy["callPayCode"] != "GEM" {
		t.Fatalf("detail owned unpaired = %v", detailLegacy)
	}
	detailOther := miniPetResult(t, miniPetReq(t, "mp-viewer", http.MethodGet, "book/race/detail?petId="+ownedPairedID, ""))
	if detailOther["petId"] != ownedPairedID || detailOther["avatarId"] != "7008" || detailOther["skinCode"] != "PUPE0000G" ||
		detailOther["interiorOwnYn"] != true || detailOther["callPayCode"] != "GEM" {
		t.Fatalf("detail other player's pet = %v", detailOther)
	}

	// 5. Detail error cases: 400 for unknown/missing petId, 404 without session.
	if rec := miniPetReq(t, "mp-owned", http.MethodGet, "book/race/detail?petId=999999", ""); rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown detail status %d, want 400", rec.Code)
	}
	if rec := miniPetReq(t, "mp-owned", http.MethodGet, "book/race/detail", ""); rec.Code != http.StatusBadRequest {
		t.Fatalf("missing petId status %d, want 400", rec.Code)
	}
	if rec := miniPetReq(t, "", http.MethodGet, "book/race/detail?petId="+ownedPairedID, ""); rec.Code != http.StatusNotFound {
		t.Fatalf("no-session detail status %d, want 404", rec.Code)
	}
}

// TestMiniPetMemoAndCrossPlayerVisitDetail verifies POST /v4/pet/book/change/memo, petMemo in Pet Book
// responses and adoption preservation, and cross-player visiting pet detail lookups.
func TestMiniPetMemoAndCrossPlayerVisitDetail(t *testing.T) {
	root := shopFixture(t, map[string][]string{
		"interior/122900007": {"dp.png"}, // RUPI00007 -> PUPE0000G
		"interior/122900012": {"dp.png"}, // RUPI0000C -> PUPE0000I
		"pet/326400016":      {"skeleton.xml", "dp.png", "behavior.xml"},
		"pet/326400018":      {"skeleton.xml", "dp.png", "behavior.xml"},
	})
	for _, id := range []string{"326400016", "326400018"} {
		if err := os.WriteFile(filepath.Join(root, "pet", id, "behavior.xml"), []byte(`{"sndindex":215}`), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	clock := miniPetClock(t, time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC))
	prevPick := presentPick
	presentPick = func(int) int { return 0 }
	t.Cleanup(func() { presentPick = prevPick })

	accOwner := miniPetOwner("7010", 100, true)
	accOther := miniPetOwner("7011", 0, true)
	accOther.Pets = []store.PetItem{
		{ID: 701101, Cd: room.PetItemCode(1), Name: "Other Legacy", Memo: "legacy"},
	}
	testutil.InstallAccounts(t, map[string]*store.Account{
		"mp-memo-owner": accOwner,
		"mp-memo-other": accOther,
	})
	testutil.SetLab(t, "7010", false)
	testutil.SetLab(t, "7011", false)

	postMemo := func(tok, petID, memo string) *httptest.ResponseRecorder {
		t.Helper()
		payload, err := json.Marshal(map[string]string{"petId": petID, "petMemo": memo})
		if err != nil {
			t.Fatal(err)
		}
		return miniPetReq(t, tok, http.MethodPost, "book/change/memo", string(payload)+"\x00")
	}
	assertMemoOK := func(rec *httptest.ResponseRecorder) {
		t.Helper()
		if rec.Code != http.StatusOK {
			t.Fatalf("change/memo status %d: %s", rec.Code, rec.Body)
		}
		var env struct {
			Result bool `json:"result"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil || !env.Result {
			t.Fatalf("change/memo reply = %s (err %v), want {\"result\":true}", rec.Body, err)
		}
	}
	bookList := func(tok, cat string) []map[string]any {
		t.Helper()
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/v4/pet/book/race/list/"+cat, nil)
		req.AddCookie(&http.Cookie{Name: "AV_AUTH", Value: tok})
		HandlePetBook(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("HandlePetBook(%s) status %d: %s", cat, rec.Code, rec.Body)
		}
		var env struct {
			Result struct {
				List []map[string]any `json:"list"`
			} `json:"result"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
			t.Fatalf("decode %s: %v", rec.Body, err)
		}
		return env.Result.List
	}
	findRow := func(rows []map[string]any, skin string) map[string]any {
		t.Helper()
		for _, r := range rows {
			if r["skinCode"] == skin {
				return r
			}
		}
		t.Fatalf("missing skinCode %s in %v", skin, rows)
		return nil
	}

	visitID := strconv.FormatInt(room.MiniPetVisitID("7010", "PUPE0000G"), 10)
	unownedVisitID := strconv.FormatInt(room.MiniPetVisitID("7010", "PUPE0000I"), 10)
	otherVisitID := strconv.FormatInt(room.MiniPetVisitID("7011", "PUPE0000G"), 10)
	otherUnownedVisitID := strconv.FormatInt(room.MiniPetVisitID("7011", "PUPE0000I"), 10)
	missingAidVisitID := strconv.FormatInt(room.MiniPetVisitID("9999", "PUPE0000G"), 10)

	// 1. Validation & auth failures on book/change/memo.
	if rec := miniPetReq(t, "mp-memo-owner", http.MethodPost, "book/change/memo", "{bad"); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad json status %d, want 400", rec.Code)
	}
	if status, code := miniPetErr(t, postMemo("mp-memo-owner", "0", "hi")); status != http.StatusBadRequest || code != "400" {
		t.Fatalf("zero petId: %d %s, want 400", status, code)
	}
	if status, code := miniPetErr(t, postMemo("mp-memo-owner", visitID, strings.Repeat("a", 51))); status != http.StatusBadRequest || code != "400" {
		t.Fatalf("51-rune memo: %d %s, want 400", status, code)
	}
	if rec := postMemo("", visitID, "hi"); rec.Code != http.StatusNotFound {
		t.Fatalf("no session status %d, want 404", rec.Code)
	}

	// 2. Unowned/other-player pets are rejected with 600101.
	for _, badID := range []string{unownedVisitID, otherVisitID, "701101", "999999"} {
		if status, code := miniPetErr(t, postMemo("mp-memo-owner", badID, "nope")); status != http.StatusBadRequest || code != "600101" {
			t.Fatalf("unowned/other petId %s: %d %s, want 400 600101", badID, status, code)
		}
	}

	// 3. Edit memo on un-adopted paired pet (newline stripping + 50 multi-byte runes allowed).
	assertMemoOK(postMemo("mp-memo-owner", visitID, "loves\r\nbones"))
	if got := findRow(bookList("mp-memo-owner", "UDOG"), "PUPE0000G")["petMemo"]; got != "lovesbones" {
		t.Fatalf("UDOG petMemo = %v, want lovesbones", got)
	}
	if got := miniPetResult(t, miniPetReq(t, "mp-memo-owner", http.MethodGet, "book/race/detail?petId="+visitID, ""))["petMemo"]; got != "lovesbones" {
		t.Fatalf("detail petMemo = %v, want lovesbones", got)
	}

	fiftyRunes := strings.Repeat("犬", 50)
	assertMemoOK(postMemo("mp-memo-owner", visitID, fiftyRunes+"\n"))
	if got := miniPetResult(t, miniPetReq(t, "mp-memo-owner", http.MethodGet, "book/race/detail?petId="+visitID, ""))["petMemo"]; got != fiftyRunes {
		t.Fatalf("50-rune petMemo = %v, want %q", got, fiftyRunes)
	}

	// 4. Cross-player GET /v4/pet/book/race/detail for visiting MiniPetVisitID.
	miniPetResult(t, miniPetReq(t, "mp-memo-owner", http.MethodGet, "room/call/"+visitID, ""))
	cross := miniPetResult(t, miniPetReq(t, "mp-memo-other", http.MethodGet, "book/race/detail?petId="+visitID, ""))
	if cross["petId"] != visitID || cross["avatarId"] != "7010" || cross["skinCode"] != "PUPE0000G" ||
		cross["petMemo"] != fiftyRunes || cross["exposureYn"] != true || cross["interiorOwnYn"] != true {
		t.Fatalf("cross-player visiting detail = %v", cross)
	}
	for _, unknownVisit := range []string{otherUnownedVisitID, missingAidVisitID} {
		if rec := miniPetReq(t, "mp-memo-owner", http.MethodGet, "book/race/detail?petId="+unknownVisit, ""); rec.Code != http.StatusBadRequest {
			t.Fatalf("cross-player unowned/missing visiting id %s status %d, want 400", unknownVisit, rec.Code)
		}
	}

	// 5. Memo is preserved on adoption (MiniPetStroke).
	assertMemoOK(postMemo("mp-memo-owner", visitID, "keep-me"))
	visitNum := room.MiniPetVisitID("7010", "PUPE0000G")
	for range 10 {
		if _, ok := MiniPetStroke("7010", visitNum); !ok {
			t.Fatal("MiniPetStroke failed before adoption")
		}
		*clock = clock.Add(4 * time.Hour)
		miniPetResult(t, miniPetReq(t, "mp-memo-owner", http.MethodGet, "room/call/"+visitID, ""))
	}
	if got := findRow(bookList("mp-memo-owner", "MYPET"), "PUPE0000G")["petMemo"]; got != "keep-me" {
		t.Fatalf("MYPET petMemo after adoption = %v, want keep-me", got)
	}
	if got := findRow(bookList("mp-memo-owner", "UDOG"), "PUPE0000G")["petMemo"]; got != "keep-me" {
		t.Fatalf("UDOG petMemo after adoption = %v, want keep-me", got)
	}

	// 6. Edit and clear memo on adopted pet (updates MYPET, UDOG, and detail).
	assertMemoOK(postMemo("mp-memo-owner", visitID, "adopted memo"))
	if got := findRow(bookList("mp-memo-owner", "MYPET"), "PUPE0000G")["petMemo"]; got != "adopted memo" {
		t.Fatalf("MYPET petMemo after edit = %v", got)
	}
	if got := miniPetResult(t, miniPetReq(t, "mp-memo-owner", http.MethodGet, "book/race/detail?petId="+visitID, ""))["petMemo"]; got != "adopted memo" {
		t.Fatalf("detail petMemo after edit = %v", got)
	}
	assertMemoOK(postMemo("mp-memo-owner", visitID, ""))
	if got := findRow(bookList("mp-memo-owner", "MYPET"), "PUPE0000G")["petMemo"]; got != "" {
		t.Fatalf("MYPET petMemo after clear = %q, want empty", got)
	}
	if got := findRow(bookList("mp-memo-owner", "UDOG"), "PUPE0000G")["petMemo"]; got != "" {
		t.Fatalf("UDOG petMemo after clear = %q, want empty", got)
	}
	if got := miniPetResult(t, miniPetReq(t, "mp-memo-owner", http.MethodGet, "book/race/detail?petId="+visitID, ""))["petMemo"]; got != "" {
		t.Fatalf("detail petMemo after clear = %q, want empty", got)
	}

	// 7. Edit and clear memo on an unpaired/legacy adopted pet.
	assertMemoOK(postMemo("mp-memo-other", "701101", "old pal"))
	if got := miniPetResult(t, miniPetReq(t, "mp-memo-other", http.MethodGet, "book/race/detail?petId=701101", ""))["petMemo"]; got != "old pal" {
		t.Fatalf("legacy adopted petMemo = %v, want old pal", got)
	}
}

// TestMiniPetBookOrderingLegacyUnpairedAndCollection verifies race-tab ownership sorting, unowned
// silhouette markers (skinSerial=="0"), legacy owned pets without MiniPets state (exp/finalLevelYn/gift),
// unpaired owned pets in MYPET/race tabs/collection, and GET /v4/pet/gift/list/<cat>.
func TestMiniPetBookOrderingLegacyUnpairedAndCollection(t *testing.T) {
	// Real pairings from data/pets.json:
	// PUPE0000G (326400016) -> RUPI00007 (122900007)
	// PUPE0000H (326400017) -> RUPI00008 (122900008)
	// PUPE0000I (326400018) -> RUPI0000C (122900012)
	// PUPE0000J (326400019) -> RUPI00041 (122900145)
	// PUPE0000L (326400021) -> RUPI00009 (122900009)
	// PUPE0000M (326400022) -> unpaired
	root := shopFixture(t, map[string][]string{
		"interior/122900007": {"dp.png"},
		"interior/122900008": {"dp.png"},
		"interior/122900012": {"dp.png"},
		"interior/122900145": {"dp.png"},
		"interior/122900009": {"dp.png"},
		"pet/326400016":      {"skeleton.xml", "dp.png", "behavior.xml"},
		"pet/326400017":      {"skeleton.xml", "dp.png", "behavior.xml"},
		"pet/326400018":      {"skeleton.xml", "dp.png", "behavior.xml"},
		"pet/326400019":      {"skeleton.xml", "dp.png", "behavior.xml"},
		"pet/326400021":      {"skeleton.xml", "dp.png", "behavior.xml"},
		"pet/326400022":      {"skeleton.xml", "dp.png", "behavior.xml"},
	})
	for _, id := range []string{"326400016", "326400017", "326400018", "326400019", "326400021", "326400022"} {
		if err := os.WriteFile(filepath.Join(root, "pet", id, "behavior.xml"), []byte(`{"sndindex":215}`), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	clock := miniPetClock(t, time.Date(2026, 11, 28, 12, 0, 0, 0, time.UTC))
	prevPick := presentPick
	presentPick = func(int) int { return 0 }
	t.Cleanup(func() { presentPick = prevPick })

	// Account owns:
	// - PUPE0000L: adopted + Rep=true, NO MiniPets state (legacy pre-loop pet)
	// - PUPE0000H: adopted, Rep=false, NO MiniPets state
	// - PUPE0000M: unpaired adopted pet, Rep=false
	// - RUPI00041 (PUPE0000J): placed on LEVEL_1, not yet adopted
	// - RUPI0000C (PUPE0000I): owned in room inventory, not placed
	// - PUPE0000G: completely unowned (alphabetically FIRST in PetSpecies!)
	acc := &store.Account{
		Aid: "7020",
		Pets: []store.PetItem{
			{ID: 702001, Cd: "PUPE0000H", Name: "Legacy Dog"},
			{ID: 702002, Cd: "PUPE0000L", Name: "Star Dog", Rep: true},
			{ID: 702003, Cd: "PUPE0000M", Name: "Classic Pup"},
		},
		RoomItems: []store.RoomItem{
			{Seq: 100000, Cd: "RUPI00041"},
			{Seq: 100001, Cd: "RUPI0000C"},
			{Seq: 100002, Cd: "RUPI00008"},
			{Seq: 100003, Cd: "RUPI00009"},
		},
		Rooms: map[string]store.RoomLayout{
			"LEVEL_1": {Placed: []store.RoomPlaced{
				{Seq: 100000, Cd: "RUPI00041"},
				{Seq: 100002, Cd: "RUPI00008"},
			}},
		},
	}
	testutil.InstallAccounts(t, map[string]*store.Account{"mp-order": acc})
	testutil.SetLab(t, "7020", false)

	// 1. Race tab UDOG sorts Represent -> Adopted -> Placed -> Owned RUPI -> Unowned, and includes unpaired PUPE0000M.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v4/pet/book/race/list/UDOG", nil)
	req.AddCookie(&http.Cookie{Name: "AV_AUTH", Value: "mp-order"})
	HandlePetBook(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("UDOG status %d: %s", rec.Code, rec.Body)
	}
	var bookEnv struct {
		Result struct {
			List []map[string]any `json:"list"`
		} `json:"result"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &bookEnv); err != nil {
		t.Fatalf("decode book: %v", err)
	}
	gotOrder := make([]string, 0, len(bookEnv.Result.List))
	byCode := map[string]map[string]any{}
	for _, r := range bookEnv.Result.List {
		cd := r["skinCode"].(string)
		gotOrder = append(gotOrder, cd)
		byCode[cd] = r
	}
	wantOrder := []string{"PUPE0000L", "PUPE0000H", "PUPE0000M", "PUPE0000J", "PUPE0000I", "PUPE0000G"}
	if !slices.Equal(gotOrder, wantOrder) {
		t.Fatalf("UDOG order = %v, want %v", gotOrder, wantOrder)
	}
	// Legacy owned paired pet (no MiniPets state) has max exp, finalLevelYn=true, and deterministic gift.
	hRow := byCode["PUPE0000H"]
	if hRow["exp"] != hRow["maxExp"] || hRow["finalLevelYn"] != true || hRow["giftItemCode"] == "" || hRow["giftItemId"] == float64(0) || hRow["petName"] != "Legacy Dog" {
		t.Fatalf("legacy owned UDOG row = %v", hRow)
	}
	// Unowned PUPE0000G has silhouette sentinel skinSerial="0", petId="0".
	gRow := byCode["PUPE0000G"]
	if gRow["skinSerial"] != "0" || gRow["petId"] != "0" || gRow["giftItemCode"] != "" || gRow["giftItemId"] != float64(0) {
		t.Fatalf("unowned UDOG row = %v", gRow)
	}
	// Legacy owned pet in arrange list also has exp==maxExp and finalLevelYn==true.
	arrRes := miniPetResult(t, miniPetReq(t, "mp-order", http.MethodGet, "room/arrange/list/7020/LEVEL_1", ""))
	for _, raw := range arrRes["list"].([]any) {
		row := raw.(map[string]any)
		if row["skinCode"] == "PUPE0000H" && (row["exp"] != row["maxExp"] || row["finalLevelYn"] != true) {
			t.Fatalf("legacy owned arrange row = %v", row)
		}
	}

	// 2. MYPET gives unpaired PUPE0000M its real catgCd ("UDOG"), finalLevelYn=true, and gift.
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/v4/pet/book/race/list/MYPET", nil)
	req.AddCookie(&http.Cookie{Name: "AV_AUTH", Value: "mp-order"})
	HandlePetBook(rec, req)
	if err := json.Unmarshal(rec.Body.Bytes(), &bookEnv); err != nil {
		t.Fatalf("decode MYPET: %v", err)
	}
	for _, r := range bookEnv.Result.List {
		if r["skinCode"] == "PUPE0000M" {
			if r["catgCd"] != "UDOG" || r["finalLevelYn"] != true || r["exp"] != r["maxExp"] || r["giftItemCode"] == "" {
				t.Fatalf("unpaired MYPET row = %v", r)
			}
		}
	}

	// 3. GET /v4/pet/gift/list/UDOG: 15-field schema, gathered rows first, deterministic present for legacy pets.
	decodeCollection := func(rec *httptest.ResponseRecorder) []map[string]any {
		t.Helper()
		if rec.Code != http.StatusOK {
			t.Fatalf("gift/list status %d: %s", rec.Code, rec.Body)
		}
		var env struct {
			Result []map[string]any `json:"result"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
			t.Fatalf("decode gift/list %s: %v", rec.Body, err)
		}
		return env.Result
	}
	col := decodeCollection(miniPetReq(t, "mp-order", http.MethodGet, "gift/list/UDOG", ""))
	if len(col) != 6 {
		t.Fatalf("gift/list/UDOG len = %d, want 6", len(col))
	}
	// First 3 rows must be gathered (PUPE0000K, PUPE0000L, PUPE00001), remaining 3 ungathered.
	wantFields := []string{
		"gatherYn", "raceCategoryCode", "raceId", "level", "giftItemId", "giftItemCode",
		"itemGradeCode", "itemName", "itemType", "description", "registerDate",
		"avatarId", "petId", "petName", "petSkinItemCode",
	}
	for i, row := range col {
		if len(row) != len(wantFields) {
			t.Fatalf("col[%d] has %d keys, want %d: %v", i, len(row), len(wantFields), row)
		}
		for _, k := range wantFields {
			if _, ok := row[k]; !ok {
				t.Fatalf("col[%d] missing key %s", i, k)
			}
		}
		if i < 3 {
			if row["gatherYn"] != true || row["level"] != float64(1) || row["giftItemCode"] == "" ||
				row["giftItemId"] == float64(0) || row["itemType"] != "DRESS" || row["avatarId"] != "7020" ||
				!strings.HasPrefix(row["description"].(string), "Present from your pet ") {
				t.Fatalf("gathered col[%d] = %v", i, row)
			}
		} else if row["gatherYn"] != false || row["level"] != float64(0) || row["giftItemCode"] != "" {
			t.Fatalf("ungathered col[%d] = %v", i, row)
		}
	}
	// Deterministic present across calls for legacy adopted pet.
	col2 := decodeCollection(miniPetReq(t, "mp-order", http.MethodGet, "gift/list/UDOG", ""))
	if col[0]["giftItemCode"] != col2[0]["giftItemCode"] {
		t.Fatalf("legacy gift not deterministic: %v vs %v", col[0]["giftItemCode"], col2[0]["giftItemCode"])
	}

	// 4. Adopting PUPE0000J records FirstVisit, AdoptedAt, and Gift -> reflected in detail and gift/list.
	visitJ := room.MiniPetVisitID("7020", "PUPE0000J")
	visitJStr := strconv.FormatInt(visitJ, 10)
	firstCallMs := strconv.FormatInt(clock.Unix()*1000, 10)
	for range 10 {
		miniPetResult(t, miniPetReq(t, "mp-order", http.MethodGet, "room/call/"+visitJStr, ""))
		if _, ok := MiniPetStroke("7020", visitJ); !ok {
			t.Fatal("MiniPetStroke failed for PUPE0000J")
		}
		*clock = clock.Add(4 * time.Hour)
	}
	detailJ := miniPetResult(t, miniPetReq(t, "mp-order", http.MethodGet, "book/race/detail?petId="+visitJStr, ""))
	wantGift := ActivePets().Minipet.Presents[0]
	if detailJ["firstVisited"] != firstCallMs || detailJ["giftItemCode"] != wantGift || detailJ["finalLevelYn"] != true {
		t.Fatalf("adopted PUPE0000J detail = %v (want firstVisited=%s gift=%s)", detailJ, firstCallMs, wantGift)
	}
	colAfter := decodeCollection(miniPetReq(t, "mp-order", http.MethodGet, "gift/list/UDOG", ""))
	foundJ := false
	for _, row := range colAfter {
		if row["petSkinItemCode"] == "PUPE0000J" {
			foundJ = true
			if row["gatherYn"] != true || row["giftItemCode"] != wantGift || row["registerDate"] == "" {
				t.Fatalf("adopted PUPE0000J collection row = %v", row)
			}
		}
	}
	if !foundJ {
		t.Fatal("PUPE0000J missing from gift/list/UDOG")
	}

	// 5. Unknown category returns 200 {"result":[]}; unauthenticated returns 404.
	if empty := decodeCollection(miniPetReq(t, "mp-order", http.MethodGet, "gift/list/BOGUS", "")); len(empty) != 0 {
		t.Fatalf("unknown category gift/list = %v, want []", empty)
	}
	if rec := miniPetReq(t, "", http.MethodGet, "gift/list/UDOG", ""); rec.Code != http.StatusNotFound {
		t.Fatalf("no-session gift/list status %d, want 404", rec.Code)
	}
}

// TestMigrateOwnedPetItems verifies startup backfill of paired RUPI room items for legacy owned pets
// (including empty RoomItems starter seeding, GiftPaid non-recyclability, idempotency, and rollback).
func TestMigrateOwnedPetItems(t *testing.T) {
	// accEmpty: owns paired PUPE0000G (RUPI00007) + unpaired PUPE00001, RoomItems initially empty.
	accEmpty := &store.Account{
		Aid: "7030",
		Pets: []store.PetItem{
			{ID: 703001, Cd: "PUPE0000G", Name: "Croc"},
			{ID: 703002, Cd: "PUPE00001", Name: "Unpaired"},
			{ID: 703003, Cd: "PUPE0000G", Name: "Dup"},
		},
	}
	// accHas: already owns RUPI00007 (must not be duplicated or altered).
	accHas := &store.Account{
		Aid:       "7031",
		Pets:      []store.PetItem{{ID: 703101, Cd: "PUPE0000G", Name: "Done"}},
		RoomItems: []store.RoomItem{{Seq: 100005, Cd: "RUPI00007"}},
	}
	// accUnpaired: only owns unpaired pet (must not be touched).
	accUnpaired := &store.Account{
		Aid:  "7032",
		Pets: []store.PetItem{{ID: 703201, Cd: "PUPE00001", Name: "Classic"}},
	}
	testutil.InstallAccounts(t, map[string]*store.Account{
		"mig-empty":    accEmpty,
		"mig-empty-2":  accEmpty, // alias sharing the same account
		"mig-has":      accHas,
		"mig-unpaired": accUnpaired,
	})
	testutil.SetLab(t, "7030", false)
	testutil.SetLab(t, "7031", false)
	testutil.SetLab(t, "7032", false)

	if err := MigrateOwnedPetItems(); err != nil {
		t.Fatalf("MigrateOwnedPetItems: %v", err)
	}
	// accEmpty must have starter kit (RoomItems seeded) plus exactly one RUPI00007 marked GiftPaid.
	rupiCount := 0
	for _, it := range accEmpty.RoomItems {
		if it.Cd == "RUPI00007" {
			rupiCount++
			if it.Paid != store.GiftPaid {
				t.Fatalf("migrated RUPI00007 Paid = %d, want GiftPaid (%d)", it.Paid, store.GiftPaid)
			}
			if got := soldPrice(it.Cd, it.Paid); got != 0 {
				t.Fatalf("migrated RUPI00007 soldPrice = %d, want 0", got)
			}
		}
	}
	if rupiCount != 1 {
		t.Fatalf("accEmpty RUPI00007 count = %d, want 1 (items=%+v)", rupiCount, accEmpty.RoomItems)
	}
	if len(accEmpty.RoomItems) <= 1 {
		t.Fatalf("accEmpty starter kit was not seeded before migration: %+v", accEmpty.RoomItems)
	}
	if len(accHas.RoomItems) != 1 || accHas.RoomItems[0].Paid != 0 {
		t.Fatalf("accHas modified unexpectedly: %+v", accHas.RoomItems)
	}
	if len(accUnpaired.RoomItems) != 0 {
		t.Fatalf("accUnpaired modified unexpectedly: %+v", accUnpaired.RoomItems)
	}

	// Idempotent on second call.
	prevSeq := accEmpty.NextRoomSeq
	prevLen := len(accEmpty.RoomItems)
	if err := MigrateOwnedPetItems(); err != nil {
		t.Fatalf("second MigrateOwnedPetItems: %v", err)
	}
	if accEmpty.NextRoomSeq != prevSeq || len(accEmpty.RoomItems) != prevLen {
		t.Fatalf("second call modified accEmpty: seq %d->%d len %d->%d", prevSeq, accEmpty.NextRoomSeq, prevLen, len(accEmpty.RoomItems))
	}

	// Rollback on save failure.
	accFail := &store.Account{
		Aid:       "7033",
		Pets:      []store.PetItem{{ID: 703301, Cd: "PUPE0000I", Name: "Fail"}},
		RoomItems: []store.RoomItem{{Seq: 100000, Cd: "RUCH0011X"}},
	}
	testutil.InstallAccounts(t, map[string]*store.Account{"mig-fail": accFail})
	testutil.SetLab(t, "7033", false)
	testutil.FailSaves(t)
	if err := MigrateOwnedPetItems(); err == nil {
		t.Fatal("MigrateOwnedPetItems succeeded when saves fail")
	}
	if len(accFail.RoomItems) != 1 || accFail.RoomItems[0].Cd != "RUCH0011X" {
		t.Fatalf("accFail not rolled back: %+v", accFail.RoomItems)
	}
}

// TestGrantNewPairedPetGrantsRoomItem verifies that granting a paired PUPE pet (e.g. via mailbox/season)
// also grants its paired RUPI room item marked GiftPaid without duplicating if already owned.
func TestGrantNewPairedPetGrantsRoomItem(t *testing.T) {
	shopFixture(t, map[string][]string{
		"interior/122900007": {"dp.png"},
		"pet/326400016":      {"skeleton.xml", "dp.png"},
	})
	acc := &store.Account{Aid: "7040"}
	testutil.InstallAccounts(t, map[string]*store.Account{"grant-pet": acc})
	testutil.SetLab(t, "7040", false)

	store.AccountsMu.Lock()
	granted, err := grantNewItemLocked(acc, "PUPE0000G", false)
	store.AccountsMu.Unlock()
	if err != nil || !granted {
		t.Fatalf("grantNewItemLocked(PUPE0000G) = (%v, %v), want (true, nil)", granted, err)
	}
	if len(acc.Pets) != 1 || acc.Pets[0].Cd != "PUPE0000G" {
		t.Fatalf("acc.Pets = %+v", acc.Pets)
	}
	found := false
	for _, it := range acc.RoomItems {
		if it.Cd == "RUPI00007" {
			found = true
			if it.Paid != store.GiftPaid {
				t.Fatalf("paired RUPI00007 Paid = %d, want GiftPaid", it.Paid)
			}
		}
	}
	if !found {
		t.Fatalf("paired RUPI00007 not granted alongside PUPE0000G: %+v", acc.RoomItems)
	}
}

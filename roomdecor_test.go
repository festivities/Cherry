package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func decorReq(t *testing.T, token, method, target, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	if token != "" {
		req.Header.Set("Cookie", `AV_AUTH="`+token+`"`)
	}
	rec := httptest.NewRecorder()
	newMux().ServeHTTP(rec, req)
	return rec
}

func decorSetup(t *testing.T) *account {
	t.Helper()
	acc := &account{aid: "9101", name: "Decor"}
	installSocialTestAccounts(t, map[string]*account{"dtok": acc})
	return acc
}

// seqOf returns the seq of the owned copy of cd (fatal if absent).
func seqOf(t *testing.T, acc *account, cd string) int64 {
	t.Helper()
	accountsMu.Lock()
	defer accountsMu.Unlock()
	for _, it := range acc.roomItems {
		if it.Cd == cd {
			return it.Seq
		}
	}
	t.Fatalf("no owned %s", cd)
	return 0
}

func decorRoomInfo(t *testing.T, path, token string) map[string]any {
	t.Helper()
	rec := decorReq(t, token, http.MethodGet, path, "")
	var b struct {
		Result struct {
			RoomInfo map[string]any `json:"roomInfo"`
		} `json:"result"`
	}
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &b) != nil {
		t.Fatalf("GET %s = %d %s", path, rec.Code, rec.Body.String())
	}
	return b.Result.RoomInfo
}

func rowJSON(seq int64, cd string, x, y, z int, dir string) string {
	return fmt.Sprintf(`{"x":%d,"y":%d,"z":%d,"direction":%q,"seq":%d,"cd":%q}`, x, y, z, dir, seq, cd)
}

func saveRoom(t *testing.T, body string) string {
	t.Helper()
	rec := decorReq(t, "dtok", http.MethodPost, "/v4/room/save/new", body+"\x00")
	if rec.Code != 200 {
		t.Fatalf("save = %d %s", rec.Code, rec.Body.String())
	}
	return rec.Body.String()
}

func TestRoomShowcaseSeedAndInventory(t *testing.T) {
	acc := decorSetup(t)
	rec := decorReq(t, "dtok", http.MethodGet, "/v4/inven/interior/items/all?isFull=false&usePurchasePrice=true", "")
	var b struct {
		Result []map[string]any `json:"result"`
	}
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &b) != nil || len(b.Result) != len(roomShowcaseCodes) {
		t.Fatalf("inventory = %d %s", rec.Code, rec.Body.String())
	}
	first := b.Result[0]
	if first["seq"] != "100000" || first["cd"] != "RUWA000HR" || first["grade"] != "N" || first["categoryCode"] != "UWLPP" ||
		first["price"] != float64(0) || first["newArrival"] != false || first["specialEffects"] != "" {
		t.Fatalf("row shape = %v", first)
	}
	cats := map[string]string{}
	for _, r := range b.Result {
		cats[r["cd"].(string)] = r["categoryCode"].(string)
	}
	if cats["RUTI000EW"] != "UFLOR" || cats["RUCH0011X"] != "UCHAIR" || cats["RUTD003K2"] != "UFLDC" {
		t.Fatalf("categories = %v", cats)
	}
	accountsMu.Lock()
	lay := acc.rooms[defaultLevel]
	next := acc.nextRoomSeq
	invCodes := len(acc.inventoryCodes)
	accountsMu.Unlock()
	if lay.Floor.Cd != "RUTI000EW" || lay.Floor.Seq != seqOf(t, acc, "RUTI000EW") || lay.Wall.Cd != "RUWA000HR" || lay.Wall.Seq != 100000 || next != 100056 || len(lay.Placed) != 2 || invCodes != 0 {
		t.Fatalf("layout = %+v next=%d inv=%d", lay, next, invCodes)
	}
	if rec := decorReq(t, "", http.MethodGet, "/v4/inven/interior/items/all", ""); rec.Code != http.StatusNotFound {
		t.Fatalf("anonymous inventory = %d", rec.Code)
	}
	// second request must not reseed
	decorReq(t, "dtok", http.MethodGet, "/v4/inven/interior/items/all", "")
	accountsMu.Lock()
	n := len(acc.roomItems)
	accountsMu.Unlock()
	if n != len(roomShowcaseCodes) {
		t.Fatalf("reseeded: %d", n)
	}
}

func TestRoomDivideFloor(t *testing.T) {
	acc := decorSetup(t)
	rec := decorReq(t, "dtok", http.MethodGet, "/v4/inven/use/list/interior/dividefloor", "")
	var b struct {
		Result [][]string `json:"result"`
	}
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &b) != nil || len(b.Result) != roomPartyFloors || len(b.Result[0]) != 4 || len(b.Result[1]) != 0 { // floor, wall, default door + diary
		t.Fatalf("dividefloor = %d %s", rec.Code, rec.Body.String())
	}
	chair := seqOf(t, acc, "RUCH0011X")
	saveRoom(t, `{"additionItems":[`+rowJSON(chair, "RUCH0011X", 3, 4, 0, "FR")+`],"groundLevel":"LEVEL_1"}`)
	rec = decorReq(t, "dtok", http.MethodGet, "/v4/inven/use/list/interior/dividefloor", "")
	if !strings.Contains(rec.Body.String(), fmt.Sprintf(`"%d"`, chair)) || !strings.Contains(rec.Body.String(), `"100000"`) {
		t.Fatalf("dividefloor after place = %s", rec.Body.String())
	}
}

func TestRoomSaveFlow(t *testing.T) {
	acc := decorSetup(t)
	decorReq(t, "dtok", http.MethodGet, "/v4/inven/interior/items/all", "")
	chair, td, wd := seqOf(t, acc, "RUCH0011X"), seqOf(t, acc, "RUTD003K2"), seqOf(t, acc, "RUWD0000S")
	add := `{"additionItems":[` + rowJSON(chair, "RUCH0011X", 3, 4, 0, "FR") + `,` + rowJSON(td, "RUTD003K2", 5, 5, 0, "BL") + `,` + rowJSON(wd, "RUWD0000S", 0, 2, 7, "BR") + `],"groundLevel":"LEVEL_1"}`
	if got := saveRoom(t, add); got != `{"result":true}` {
		t.Fatalf("add = %s", got)
	}
	info := decorRoomInfo(t, "/v4/room/myroom/LEVEL_1", "dtok")
	item, floorItem, wallItem := info["item"].([]any), info["floorItem"].([]any), info["wallItem"].([]any)
	if len(item) != 2 || len(floorItem) != 1 || len(wallItem) != 2 { // + default diary (item) and door (wallItem)
		t.Fatalf("split = %v", info)
	}
	row := item[1].(map[string]any)
	if row["seq"] != fmt.Sprint(chair) || row["cd"] != "RUCH0011X" || row["direction"] != "FR" || row["x"] != float64(3) {
		t.Fatalf("item row = %v", row)
	}
	if wr := wallItem[1].(map[string]any); wr["z"] != float64(7) || wr["y"] != float64(2) || floorItem[0].(map[string]any)["cd"] != "RUTD003K2" {
		t.Fatalf("wall/floor rows = %v %v", wallItem, floorItem)
	}
	// guest view via enter/<aid>
	if g := decorRoomInfo(t, "/v4/room/enter/9101/LEVEL_1", ""); len(g["item"].([]any)) != 2 {
		t.Fatalf("enter view = %v", g)
	}
	// move chair, delete td
	mod := `{"modifiedItems":[` + rowJSON(chair, "RUCH0011X", 6, 6, 0, "FL") + `],"deleteItems":[` + fmt.Sprint(td) + `],"groundLevel":"LEVEL_1"}`
	if got := saveRoom(t, mod); got != `{"result":true}` {
		t.Fatalf("mod = %s", got)
	}
	info = decorRoomInfo(t, "/v4/room/myroom/LEVEL_1", "dtok")
	row = info["item"].([]any)[1].(map[string]any)
	if row["x"] != float64(6) || row["direction"] != "FL" || len(info["floorItem"].([]any)) != 0 {
		t.Fatalf("after move = %v", info)
	}
	// wall + floor swap; old serials in deleteItems are not furniture deletes
	oldFloor, oldWall := seqOf(t, acc, "RUTI000EW"), seqOf(t, acc, "RUWA000HR")
	newFloor, newWall := seqOf(t, acc, "RUTI0007H"), seqOf(t, acc, "RUWA000IC")
	swap := fmt.Sprintf(`{"deleteItems":[%d,%d],"additionItems":[%s,%s],"groundLevel":"LEVEL_1"}`, oldFloor, oldWall,
		fmt.Sprintf(`{"seq":%d,"cd":"RUTI0007H"}`, newFloor), fmt.Sprintf(`{"seq":%d,"cd":"RUWA000IC"}`, newWall)) // native bare tile rows
	if got := saveRoom(t, swap); got != `{"result":true}` {
		t.Fatalf("swap = %s", got)
	}
	info = decorRoomInfo(t, "/v4/room/myroom/LEVEL_1", "dtok")
	if info["floor"].(map[string]any)["cd"] != "RUTI0007H" || info["wall"].(map[string]any)["seq"] != fmt.Sprint(newWall) || len(info["item"].([]any)) != 2 {
		t.Fatalf("after swap = %v", info)
	}
}

func TestRoomSaveRejectsWithoutMutation(t *testing.T) {
	acc := decorSetup(t)
	decorReq(t, "dtok", http.MethodGet, "/v4/inven/interior/items/all", "")
	chair, chair2 := seqOf(t, acc, "RUCH0011X"), seqOf(t, acc, "RUCH0011Y")
	saveRoom(t, `{"additionItems":[`+rowJSON(chair, "RUCH0011X", 1, 1, 0, "FR")+`],"groundLevel":"LEVEL_1"}`)
	snap := func() string {
		accountsMu.Lock()
		defer accountsMu.Unlock()
		b, _ := json.Marshal(acc.rooms)
		return string(b)
	}
	before := snap()
	good := rowJSON(chair2, "RUCH0011Y", 2, 2, 0, "FR")
	for name, body := range map[string]string{
		"unowned seq":     `{"additionItems":[` + rowJSON(5, "RUCH0011Y", 2, 2, 0, "FR") + `],"groundLevel":"LEVEL_1"}`,
		"cd mismatch":     `{"additionItems":[` + rowJSON(chair2, "RUCH0011X", 2, 2, 0, "FR") + `],"groundLevel":"LEVEL_1"}`,
		"double place":    `{"additionItems":[` + rowJSON(chair, "RUCH0011X", 2, 2, 0, "FR") + `],"groundLevel":"LEVEL_1"}`,
		"dup in request":  `{"additionItems":[` + good + `,` + good + `],"groundLevel":"LEVEL_1"}`,
		"bad direction":   `{"additionItems":[` + rowJSON(chair2, "RUCH0011Y", 2, 2, 0, "XX") + `],"groundLevel":"LEVEL_1"}`,
		"out of bounds":   `{"additionItems":[` + rowJSON(chair2, "RUCH0011Y", 5000, 2, 0, "FR") + `],"groundLevel":"LEVEL_1"}`,
		"negative":        `{"additionItems":[` + rowJSON(chair2, "RUCH0011Y", -1, 2, 0, "FR") + `],"groundLevel":"LEVEL_1"}`,
		"delete unplaced": `{"deleteItems":[` + fmt.Sprint(chair2) + `],"groundLevel":"LEVEL_1"}`,
		"modify unplaced": `{"modifiedItems":[` + good + `],"groundLevel":"LEVEL_1"}`,
		"unknown level":   `{"additionItems":[` + good + `],"groundLevel":"LEVEL_9"}`,
		"valid then bad":  `{"additionItems":[` + good + `,` + rowJSON(7, "RUCH0011Y", 2, 2, 0, "FR") + `],"groundLevel":"LEVEL_1"}`,
		"bad superpose":   `{"additionItems":[{"x":1,"y":1,"z":0,"direction":"FR","seq":` + fmt.Sprint(chair2) + `,"cd":"RUCH0011Y","superpose":{"seq":[9]}}],"groundLevel":"LEVEL_1"}`,
		"malformed json":  `{"additionItems":`,
	} {
		if got := saveRoom(t, body); got != `{"result":false}` {
			t.Errorf("%s = %s", name, got)
		}
		if snap() != before {
			t.Fatalf("%s mutated layout", name)
		}
	}
	if rec := decorReq(t, "nope", http.MethodPost, "/v4/room/save/new", `{}`); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown credentials = %d", rec.Code)
	}
}

func TestRoomHandleRoomDefaultsAndFriend(t *testing.T) {
	decorSetup(t)
	info := decorRoomInfo(t, "/v4/room/myroom/LEVEL_2", "dtok") // no layout for this level
	if info["floor"].(map[string]any)["cd"] != roomFloorCode || len(info["item"].([]any)) != 0 {
		t.Fatalf("level 2 = %v", info)
	}
	info = decorRoomInfo(t, "/v4/room/enter/"+friendAID+"/LEVEL_1", "dtok")
	if info["floor"].(map[string]any)["seq"] != "1" {
		t.Fatalf("friend room = %v", info)
	}
}

func TestRoomPersistenceRoundTrip(t *testing.T) {
	accountsMu.Lock()
	pa, pl, pn, pp := accounts, latestAcc, nextAvatarID, accountStorePath
	accounts, latestAcc, nextAvatarID, accountStorePath = make(map[string]*account), nil, 0, ""
	accountsMu.Unlock()
	t.Cleanup(func() {
		accountsMu.Lock()
		accounts, latestAcc, nextAvatarID, accountStorePath = pa, pl, pn, pp
		accountsMu.Unlock()
	})
	path := filepath.Join(t.TempDir(), "accounts.json")
	if err := loadAccountsFrom(path); err != nil {
		t.Fatal(err)
	}
	acc, _ := newAccount()
	setLab(t, acc.aid, true)
	accountsMu.Lock()
	accounts[acc.accessToken] = acc
	latestAcc = acc
	accountStorePath = path
	accountsMu.Unlock()
	tok := acc.accessToken
	decorReq(t, tok, http.MethodGet, "/v4/inven/interior/items/all", "")
	chair := seqOf(t, acc, "RUCH0011X")
	body := `{"additionItems":[` + rowJSON(chair, "RUCH0011X", 3, 4, 0, "FR") + `],"groundLevel":"LEVEL_1"}`
	if rec := decorReq(t, tok, http.MethodPost, "/v4/room/save/new", body+"\n"); rec.Body.String() != `{"result":true}` {
		t.Fatalf("save = %s", rec.Body.String())
	}
	accountsMu.Lock()
	accounts, latestAcc = make(map[string]*account), nil
	accountsMu.Unlock()
	if err := loadAccountsFrom(path); err != nil {
		t.Fatal(err)
	}
	accountsMu.Lock()
	got := accounts[tok]
	accountsMu.Unlock()
	if got == nil || len(got.roomItems) != 56 || got.nextRoomSeq != 100056 || len(got.rooms[defaultLevel].Placed) != 3 || got.rooms[defaultLevel].Placed[2].Seq != chair {
		t.Fatalf("round trip lost layout: %+v", got)
	}
	// failed persistence rolls the layout back
	accountsMu.Lock()
	accountStorePath = filepath.Join(path, "sub", "accounts.json") // parent is a file
	accountsMu.Unlock()
	chair2 := seqOf(t, got, "RUCH0011Y")
	rec := decorReq(t, tok, http.MethodPost, "/v4/room/save/new", `{"additionItems":[`+rowJSON(chair2, "RUCH0011Y", 1, 1, 0, "FR")+`],"groundLevel":"LEVEL_1"}`)
	accountsMu.Lock()
	n := len(got.rooms[defaultLevel].Placed)
	accountsMu.Unlock()
	if rec.Code != http.StatusInternalServerError || n != 3 {
		t.Fatalf("rollback: %d placed=%d", rec.Code, n)
	}
}

func TestRoomOldStoreLoadsUnchanged(t *testing.T) {
	accountsMu.Lock()
	pa, pl, pn, pp := accounts, latestAcc, nextAvatarID, accountStorePath
	accountsMu.Unlock()
	t.Cleanup(func() {
		accountsMu.Lock()
		accounts, latestAcc, nextAvatarID, accountStorePath = pa, pl, pn, pp
		accountsMu.Unlock()
	})
	path := filepath.Join(t.TempDir(), "accounts.json")
	old := `{"version":1,"accounts":{"t1":{"accessToken":"t1","sessionKey":"k","mid":"1","avatarUserId":"u","aid":"10","name":"Old","gender":"MALE","skin":"1","country":"JP","itemCodes":["CUON00164"],"inventoryCodes":["CUON00164"]}},"aliases":{"t1":"t1"},"latest":"t1","nextAvatarId":10}`
	if err := os.WriteFile(path, []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := loadAccountsFrom(path); err != nil {
		t.Fatal(err)
	}
	accountsMu.Lock()
	acc := accounts["t1"]
	accountsMu.Unlock()
	if acc == nil || acc.name != "Old" || len(acc.roomItems) != 0 || acc.rooms != nil || acc.nextRoomSeq != firstRoomSeq {
		t.Fatalf("old store = %+v", acc)
	}
	accountsMu.Lock()
	err := saveAccountsLocked()
	accountsMu.Unlock()
	data, _ := os.ReadFile(path)
	if err != nil || strings.Contains(string(data), "roomItems") || strings.Contains(string(data), `"rooms"`) {
		t.Fatalf("unseeded account must stay additive-free: %v %s", err, data)
	}
}

func TestRoomThumbnailRouting(t *testing.T) {
	for path, want := range map[string][2]string{
		"/img/read/arts_item_interior_120300837/dp.png": {"interior", "120300837"},
		"/arts_item_interior_121400639/dp.png":          {"interior", "121400639"},
		"/img/read/arts_item_tile_121500536/dp.png":     {"tile", "121500536"},
		"/arts_item_tile_121500193/dp.png":              {"tile", "121500193"},
	} {
		if k, id, ok := parseDpPath(path); !ok || k != want[0] || id != want[1] {
			t.Errorf("%s -> %s %s %v", path, k, id, ok)
		}
	}
	for _, bad := range []string{"/arts_item_interior_x/dp.png", "/arts_item_tile_1/other.png", "/arts_item_room_1/dp.png"} {
		if _, _, ok := parseDpPath(bad); ok {
			t.Errorf("%s accepted", bad)
		}
	}
	root, cache := setDpFixture(t)
	big := solidPNG(t, 400, 200, color.RGBA{200, 10, 10, 255})
	writeItemFile(t, root, "interior", "122000001", "fl_l0_a00.png", big)
	writeItemFile(t, root, "interior", "121400639", "wall_l_1.png", big)
	writeItemFile(t, root, "tile", "121500536", "tile_1.png", big)
	writeItemFile(t, root, "interior", "122000002", "dp.png", solidPNG(t, 8, 8, color.RGBA{1, 2, 3, 255}))
	for _, c := range []struct {
		path string
		w, h int
	}{
		{"/arts_item_interior_122000001/dp.png", 128, 64},
		{"/img/read/arts_item_interior_121400639/dp.png", 128, 64},
		{"/img/read/arts_item_tile_121500536/dp.png", 128, 64},
		{"/arts_item_interior_122000002/dp.png", 8, 8},
	} {
		rec := serve(t, http.MethodGet, c.path)
		if rec.Code != 200 || rec.Header().Get("Content-Type") != "image/png" {
			t.Fatalf("%s = %d", c.path, rec.Code)
		}
		cfg := pngSize(t, rec.Body.Bytes())
		if cfg[0] != c.w || cfg[1] != c.h {
			t.Errorf("%s size = %v", c.path, cfg)
		}
	}
	if _, err := os.Stat(filepath.Join(cache, "interior_122000001.png")); err != nil {
		t.Errorf("generated thumbnail not cached: %v", err)
	}
	if rec := serve(t, http.MethodGet, "/arts_item_interior_999/dp.png"); rec.Code != http.StatusNotFound {
		t.Errorf("missing art = %d", rec.Code)
	}
}

func pngSize(t *testing.T, b []byte) [2]int {
	t.Helper()
	cfg, err := png.DecodeConfig(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	return [2]int{cfg.Width, cfg.Height}
}

func TestRoomSecondFloor(t *testing.T) {
	acc := decorSetup(t)
	ri := decorRoomInfo(t, "/v4/room/myroom/LEVEL_2", "dtok")
	if ri["groundLevel"] != "LEVEL_2" || ri["floor"].(map[string]any)["seq"] != "0" {
		t.Fatalf("LEVEL_2 default = %v", ri)
	}
	chair := seqOf(t, acc, "RUCH0011X")
	tile := seqOf(t, acc, "RUTI0007H")
	body := `{"deleteItems":[0],"additionItems":[` + rowJSON(chair, "RUCH0011X", 3, 3, 0, "FR") +
		`,{"seq":` + fmt.Sprint(tile) + `,"cd":"RUTI0007H"}],"groundLevel":"LEVEL_2"}`
	if got := saveRoom(t, body); got != `{"result":true}` {
		t.Fatalf("LEVEL_2 save = %s", got)
	}
	ri = decorRoomInfo(t, "/v4/room/enter/9101/LEVEL_2", "")
	if len(ri["item"].([]any)) != 1 || ri["floor"].(map[string]any)["cd"] != "RUTI0007H" {
		t.Fatalf("LEVEL_2 after save = %v", ri)
	}
	// The chair is placed on LEVEL_2, so LEVEL_1 cannot reuse it.
	if got := saveRoom(t, `{"additionItems":[`+rowJSON(chair, "RUCH0011X", 2, 2, 0, "FR")+`],"groundLevel":"LEVEL_1"}`); got != roomSaveFalse {
		t.Fatalf("cross-level reuse = %s", got)
	}
	if got := saveRoom(t, `{"groundLevel":"LEVEL_3"}`); got != roomSaveFalse {
		t.Fatalf("LEVEL_3 = %s", got)
	}
	rec := decorReq(t, "dtok", http.MethodGet, "/v4/inven/use/list/interior/dividefloor", "")
	var d struct{ Result [][]string }
	if json.Unmarshal(rec.Body.Bytes(), &d) != nil || len(d.Result) != 2 || len(d.Result[1]) != 2 {
		t.Fatalf("dividefloor = %s", rec.Body.String())
	}
}

func TestRoomDiaryTopUp(t *testing.T) {
	acc := decorSetup(t)
	accountsMu.Lock()
	for i, cd := range roomShowcaseCodes[:55] { // pre-diary grant, already seeded with nothing placed
		acc.roomItems = append(acc.roomItems, roomItem{Seq: int64(firstRoomSeq + i), Cd: cd})
	}
	acc.nextRoomSeq = firstRoomSeq + 55
	acc.rooms = map[string]roomLayout{defaultLevel: {Floor: roomItem{Seq: 100013, Cd: "RUTI000EW"}, Placed: []roomPlaced{}}}
	accountsMu.Unlock()
	decorReq(t, "dtok", http.MethodGet, "/v4/inven/interior/items/all", "")
	accountsMu.Lock()
	lay, n, next := acc.rooms[defaultLevel], len(acc.roomItems), acc.nextRoomSeq
	accountsMu.Unlock()
	if n != 56 || next != 100056 || len(lay.Placed) != 2 || lay.Floor.Seq != 100013 || lay.Placed[1].Seq != 100055 {
		t.Fatalf("top-up = %+v n=%d next=%d", lay, n, next)
	}
	// idempotent: a second read changes nothing
	decorReq(t, "dtok", http.MethodGet, "/v4/inven/interior/items/all", "")
	accountsMu.Lock()
	again := len(acc.rooms[defaultLevel].Placed)
	accountsMu.Unlock()
	if again != 2 {
		t.Fatalf("second ensure placed %d", again)
	}
}

func TestRoomPresetList(t *testing.T) {
	decorSetup(t)
	rec := decorReq(t, "dtok", http.MethodGet, "/v4/room/preset/list", "")
	if rec.Code != http.StatusOK || rec.Body.String() != `{"result":{"availableSlotCount":9,"slots":[]}}` {
		t.Fatalf("preset list = %d %s", rec.Code, rec.Body.String())
	}
	if rec := decorReq(t, "nobody", http.MethodGet, "/v4/room/preset/list", ""); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown session = %d", rec.Code)
	}
}

func TestRoomPresetSaveListRemove(t *testing.T) {
	acc := decorSetup(t)
	decorReq(t, "dtok", http.MethodGet, "/v4/inven/interior/items/all", "") // seeds the showcase
	chair := seqOf(t, acc, "RUCH0011X")
	wall := seqOf(t, acc, "RUWA000IC")
	img := "https://obs.line-apps.com/lineplay/r/download.nhn?oid=o1&ctime=5&userid=u1"
	liveItems := len(decorRoomInfo(t, "/v4/room/myroom/LEVEL_1", "dtok")["item"].([]any))
	body := fmt.Sprintf(`{"tileSize":12,"representImagePath":%q,"groundLevel":"LEVEL_1","additionItems":[{"seq":%d,"cd":"RUWA000IC"},%s]}`,
		img, wall, rowJSON(chair, "RUCH0011X", 3, 4, 0, "FR"))
	if rec := decorReq(t, "dtok", http.MethodPost, "/v4/room/preset/save/1", body); rec.Body.String() != `{"result":true}` {
		t.Fatalf("preset save = %d %s", rec.Code, rec.Body.String())
	}
	accountsMu.Lock()
	p := acc.presets["1"]
	accountsMu.Unlock()
	if p.Layout.Wall.Seq != wall || len(p.Layout.Placed) != 1 || p.Layout.Placed[0].X != 3 || p.TileSize != 12 {
		t.Fatalf("snapshot = %+v", p)
	}
	if got := len(decorRoomInfo(t, "/v4/room/myroom/LEVEL_1", "dtok")["item"].([]any)); got != liveItems {
		t.Fatalf("preset save changed the live room: items %d -> %d", liveItems, got)
	}
	rec := decorReq(t, "dtok", http.MethodGet, "/v4/room/preset/list", "")
	want := fmt.Sprintf(`{"result":{"availableSlotCount":9,"slots":[{"presetSeq":1,"representImagePath":%q,"tileSize":12,"openable":true}]}}`, img)
	if strings.ReplaceAll(rec.Body.String(), "\\u0026", "&") != want {
		t.Fatalf("list = %s", rec.Body.String())
	}
	// Load: roomInfo-shaped body plus tileSize; missing slot and bad index.
	rec = decorReq(t, "dtok", http.MethodGet, "/v4/room/preset/find/1", "")
	var found struct {
		Result struct {
			TileSize  int              `json:"tileSize"`
			Wall      map[string]any   `json:"wall"`
			Item      []map[string]any `json:"item"`
			FloorItem []any            `json:"floorItem"`
		} `json:"result"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &found); err != nil || found.Result.TileSize != 12 ||
		found.Result.Wall["cd"] != "RUWA000IC" || len(found.Result.Item) != 1 || found.Result.Item[0]["cd"] != "RUCH0011X" || found.Result.FloorItem == nil {
		t.Fatalf("find = %s", rec.Body.String())
	}
	if rec := decorReq(t, "dtok", http.MethodGet, "/v4/room/preset/find/2", ""); rec.Body.String() != `{"errorCode":"404"}` {
		t.Fatalf("find empty slot = %s", rec.Body.String())
	}
	if rec := decorReq(t, "dtok", http.MethodGet, "/v4/room/preset/find/10", ""); rec.Code != http.StatusNotFound {
		t.Fatalf("find slot 10 = %d", rec.Code)
	}
	// Overwrite after remove, and rejects: slot out of range, unowned seq, unknown session.
	if rec := decorReq(t, "dtok", http.MethodPost, "/v4/room/preset/remove/1", ""); rec.Body.String() != `{"result":true}` {
		t.Fatalf("remove = %s", rec.Body.String())
	}
	if rec := decorReq(t, "dtok", http.MethodGet, "/v4/room/preset/list", ""); !strings.Contains(rec.Body.String(), `"slots":[]`) {
		t.Fatalf("list after remove = %s", rec.Body.String())
	}
	if rec := decorReq(t, "dtok", http.MethodPost, "/v4/room/preset/save/10", body); rec.Code != http.StatusNotFound {
		t.Fatalf("slot 10 = %d", rec.Code)
	}
	if rec := decorReq(t, "dtok", http.MethodPost, "/v4/room/preset/save/setitem/1", body); rec.Code != http.StatusNotFound {
		t.Fatalf("setitem = %d", rec.Code)
	}
	bad := `{"groundLevel":"LEVEL_1","additionItems":[` + rowJSON(999, "RUCH0011X", 1, 1, 0, "FR") + `]}`
	if rec := decorReq(t, "dtok", http.MethodPost, "/v4/room/preset/save/1", bad); rec.Body.String() != roomSaveFalse {
		t.Fatalf("unowned = %s", rec.Body.String())
	}
	if rec := decorReq(t, "nobody", http.MethodPost, "/v4/room/preset/save/1", body); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown session = %d", rec.Code)
	}
	accountsMu.Lock()
	n := len(acc.presets)
	accountsMu.Unlock()
	if n != 0 {
		t.Fatalf("failed saves left %d presets", n)
	}
}

func TestRoomImageUploadDownload(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "room-media")
	const userID, ctime, oid = "u1", "1760000000", "abc_def" // oid need not be userid_ctime
	params, _ := json.Marshal(diaryImageUploadParams{Version: "1.0", Type: "image", Name: oid, UserID: userID, OID: oid, CTime: ctime})
	data := diaryTestImage(t, "png", 3, 2, 7)
	rec := httptest.NewRecorder()
	handleMediaUpload(rec, diaryUploadRequest(t, params, data, "image/png"), dir, maxDiaryMediaStorage, true)
	if rec.Code != http.StatusOK || rec.Body.String() != "x-obs-oid: "+oid+"\r\n" || rec.Header()["x-obs-oid"][0] != oid {
		t.Fatalf("upload = %d %s", rec.Code, rec.Body.String())
	}
	get := func(path string) *httptest.ResponseRecorder {
		g := httptest.NewRecorder()
		handleMediaDownload(g, httptest.NewRequest(http.MethodGet, path+"?oid="+oid+"&ctime="+ctime+"&userid="+userID, nil), dir, true)
		return g
	}
	if g := get(roomDownloadPath); g.Code != 200 || !bytes.Equal(g.Body.Bytes(), data) {
		t.Fatalf("download = %d", g.Code)
	}
	if g := get(diaryDownloadPath); g.Code != http.StatusNotFound {
		t.Fatalf("wrong path = %d", g.Code)
	}
	// Routed through the mux the diary handler still rejects this non-diary oid.
	bad := httptest.NewRecorder()
	handleDiaryImageUploadAt(bad, diaryUploadRequest(t, params, data, "image/png"), t.TempDir())
	if bad.Code != http.StatusBadRequest {
		t.Fatalf("diary mode accepted room oid: %d", bad.Code)
	}
}

func TestRoomPresetSecondFloorDefaults(t *testing.T) {
	acc := decorSetup(t)
	decorReq(t, "dtok", http.MethodGet, "/v4/inven/interior/items/all", "")
	chair := seqOf(t, acc, "RUCH0011X")
	// Floor 2 keeps the client's default wall/floor, sent as seq 0 rows.
	body := `{"tileSize":12,"representImagePath":"lineplay/r/download.nhn?oid=x&ctime=1&userid=9101","groundLevel":"LEVEL_2","additionItems":[` +
		`{"seq":0,"cd":"RUWA0006D"},{"seq":0,"cd":"RUTI0005D"},` + rowJSON(chair, "RUCH0011X", 3, 3, 0, "FR") + `]}`
	rec := decorReq(t, "dtok", http.MethodPost, "/v4/room/preset/save/1", body)
	if rec.Body.String() != `{"result":true}` {
		t.Fatalf("LEVEL_2 preset save = %s", rec.Body.String())
	}
	accountsMu.Lock()
	p := acc.presets["1"]
	accountsMu.Unlock()
	if p.Layout.Wall.Seq != 0 || p.Layout.Floor.Seq != 0 || len(p.Layout.Placed) != 1 {
		t.Fatalf("preset = %+v", p)
	}
	if rec := decorReq(t, "", http.MethodPost, "/lineplay/r/delete.nhn?oid=x", ""); rec.Code != 200 {
		t.Fatalf("delete = %d", rec.Code)
	}
}

// Package room serves My Room decor (furniture, floor layouts, presets) and pets.
package room

import (
	"compress/gzip"
	"encoding/json"
	"io"
	"maps"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"cherry/internal/httpx"
	"cherry/internal/store"
)

// My Room furniture persistence: owned copies, per-level layouts, and the
// inventory / dividefloor / save routes (libgame.so 10.1.0.0).

const (
	maxRoomPlaced = 500
	maxRoomCoord  = 100 // ponytail: tileSize is 12 in handleRoom; generous cap until per-level sizes are known
	DefaultLevel  = "LEVEL_1"
)

// ponytail: lab showcase grant for every account: one copy of each renderable
// wall/tile and of 33 furniture codes so the editor has something to place.
var RoomShowcaseCodes = strings.Fields(
	"RUWA000HR RUWA000IC RUWA000UD RUWA000W6 RUWA000Y8 RUWA000YY RUWA000ZA RUWA000ZM RUWA000ZY RUWA0010A RUWA00136 " +
		"RUTI0007H RUTI0008E RUTI000EW RUTI000FH RUTI000HF RUTI000SA RUTI000SJ RUTI000T6 RUTI000TF RUTI000UV RUTI000VV " +
		"RUTA0004S RUTA00068 RUTA00069 RUCH0011X RUCH0011Y RUCH0011Z RUCH00120 RUBE000KH RUBE000O0 RUBE000O8 RUKI00022 RUKI00023 " +
		"RUCL000EE RUCL000EF RUTB000GB RUTB000GG RUTB000NO RUPL0004M RUPL00055 RUPL0005J RUTD003K2 RUTD003QS RUTD003RC " +
		"RUTD00401 RUTD00405 RUTD0040C RUWD0000S RUWD00080 RUWD00089 RUWI00033 RUWI000IE RUDO0002Z RUDO000B6 " + roomDiaryCode)

// New accounts start with the default floor, wall, door and diary only (no pets);
// the showcase above and the pets are granted to lab accounts alone.
var roomStarterCodes = []string{RoomFloorCode, RoomWallCode, roomDoorCode, roomDiaryCode}

// RoomCategoryCode maps the cd category letters to the client's categoryCode.
// Confidence: WA->UWLPP and TI->UFLOR medium (names; the qword_3BDDB20 table is
// NOT positional against catIdx, only coincidentally at 14/15), WD/DO/TD/CH/TA/
// BE/KI/BA/PL/FD/GC/AB/VO fairly high by name, the rest are guesses falling to
// UPROP. The parser only uses it for UI tab grouping.
func RoomCategoryCode(cd string) string {
	if len(cd) < 4 {
		return "UPROP"
	}
	switch cd[2:4] {
	case "WA":
		return "UWLPP"
	case "TI":
		return "UFLOR"
	case "TA", "TB":
		return "UTABL"
	case "CH":
		return "UCHAIR"
	case "CL", "DR":
		return "UDRES"
	case "BE":
		return "UBAD"
	case "KI":
		return "UKICH"
	case "BA":
		return "UBATH"
	case "EL":
		return "UMEDIA"
	case "GO":
		return "UPROD"
	case "PL":
		return "UPLANT"
	case "FD":
		return "UFOOD"
	case "AB":
		return "UALBM"
	case "GC":
		return "UGACH"
	case "WD":
		return "UWNDW"
	case "DO":
		return "UDOOR"
	case "WI", "BW":
		return "UWLDC"
	case "TD":
		return "UFLDC"
	case "VO":
		return "UVOICE"
	case "PT", "MP":
		return "UENVI"
	}
	return "UPROP"
}

// Floor 1 cannot be saved without a door (type 18) and a diary (type 24) and the
// client seeds neither (NaMyRoomEditLayer::CheckValidSaveRoom @0x20e5490); floors
// 2+ must have none. RUAB00002 is the only diary in the full-client dataset.
const (
	roomDiaryCode = "RUAB00002"
	roomDoorCode  = "RUDO0002Z"
)

// ponytail: guessed default spots (diary near the back corner, door on the left
// wall); the user can move them in the editor.
var roomDefaultPlaced = []store.RoomPlaced{
	{Cd: roomDoorCode, X: 5, Y: 0, Z: 3, Dir: "FL"}, // z = height on the wall; z 3 stands on the floor (user-saved placement 2026-10-07)
	{Cd: roomDiaryCode, X: 2, Y: 2, Dir: "FR"},
}

// EnsureRoomLocked grants missing showcase codes and gives LEVEL_1 a default
// floor/wall (first seed) and door/diary (whenever none is placed). Caller holds accountsMu.
func EnsureRoomLocked(acc *store.Account) error {
	owned := map[string]int64{}
	for _, it := range acc.RoomItems {
		if _, ok := owned[it.Cd]; !ok {
			owned[it.Cd] = it.Seq
		}
	}
	fresh := len(acc.RoomItems) == 0
	items := slices.Clone(acc.RoomItems)
	seq := max(acc.NextRoomSeq, store.FirstRoomSeq)
	codes := roomStarterCodes
	if store.LabAids[acc.Aid] {
		codes = RoomShowcaseCodes
	}
	for _, cd := range codes {
		// Seed only an empty room: re-granting missing codes would refill sold items (sell-back loop).
		if _, ok := owned[cd]; !ok && fresh {
			items = append(items, store.RoomItem{Seq: seq, Cd: cd})
			owned[cd] = seq
			seq++
		}
	}
	lay, ok := acc.Rooms[DefaultLevel]
	if !ok {
		lay = store.RoomLayout{Placed: []store.RoomPlaced{}}
	}
	if fresh {
		lay.Floor = store.RoomItem{Seq: owned[RoomFloorCode], Cd: RoomFloorCode}
		lay.Wall = store.RoomItem{Seq: owned[RoomWallCode], Cd: RoomWallCode}
	}
	used := map[int64]bool{}
	for _, l := range acc.Rooms {
		for _, p := range l.Placed {
			used[p.Seq] = true
		}
	}
	placed := slices.Clone(lay.Placed)
	for _, d := range roomDefaultPlaced {
		cat := d.Cd[2:4]
		seq, have := owned[d.Cd]
		if !have || slices.ContainsFunc(placed, func(p store.RoomPlaced) bool { return p.Cd[2:4] == cat }) || used[seq] {
			continue
		}
		d.Seq = seq
		placed = append(placed, d)
	}
	if len(items) == len(acc.RoomItems) && len(placed) == len(lay.Placed) && ok {
		return nil
	}
	lay.Placed = placed
	prevItems, prevNext, prevRooms := acc.RoomItems, acc.NextRoomSeq, acc.Rooms
	acc.RoomItems, acc.NextRoomSeq = items, seq
	acc.Rooms = maps.Clone(prevRooms)
	if acc.Rooms == nil {
		acc.Rooms = map[string]store.RoomLayout{}
	}
	acc.Rooms[DefaultLevel] = lay
	if err := store.SaveAccountsLocked(); err != nil {
		acc.RoomItems, acc.NextRoomSeq, acc.Rooms = prevItems, prevNext, prevRooms
		return err
	}
	return nil
}

// roomAccount resolves the requester and seeds it; on failure it has already
// written the response and returns nil. Caller must unlock accountsMu iff non-nil.
func roomAccount(w http.ResponseWriter, r *http.Request) *store.Account {
	store.AccountsMu.Lock()
	acc := store.Accounts[httpx.CookieValue(r, "AV_AUTH")]
	if acc == nil {
		store.AccountsMu.Unlock()
		httpx.WriteJSON(w, http.StatusNotFound, httpx.UnknownSessionBody)
		return nil
	}
	if err := EnsureRoomLocked(acc); err != nil {
		store.AccountsMu.Unlock()
		httpx.WriteJSON(w, http.StatusInternalServerError, `{"errorCode":"500"}`)
		return nil
	}
	return acc
}

// InvenInteriorHandler serves GET inven/interior/items/all. priceOf gives the
// sell-back price and grade code per item (economy owns the table and sits above room).
func InvenInteriorHandler(priceOf func(code string) (int64, string)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			httpx.ServeNotFound(w)
			return
		}
		acc := roomAccount(w, r)
		if acc == nil {
			return
		}
		items := slices.Clone(acc.RoomItems)
		store.AccountsMu.Unlock()
		invenInteriorRows(w, items, priceOf)
	}
}

func invenInteriorRows(w http.ResponseWriter, items []store.RoomItem, priceOf func(string) (int64, string)) {
	type row struct {
		Seq            string `json:"seq"`
		Cd             string `json:"cd"`
		Price          int    `json:"price"`
		NewArrival     bool   `json:"newArrival"`
		SpecialEffects string `json:"specialEffects"`
		Grade          string `json:"grade"`
		CategoryCode   string `json:"categoryCode"`
	}
	rows := make([]row, 0, len(items))
	for _, it := range items {
		price, grade := priceOf(it.Cd)
		rows = append(rows, row{Seq: strconv.FormatInt(it.Seq, 10), Cd: it.Cd, Price: int(price), Grade: grade, CategoryCode: RoomCategoryCode(it.Cd)})
	}
	body, _ := json.Marshal(map[string]any{"result": rows})
	httpx.WriteJSON(w, http.StatusOK, string(body))
}

func roomLevelNum(level string) int {
	n, _ := strconv.Atoi(strings.TrimPrefix(level, "LEVEL_"))
	return n
}

func HandleInvenDivideFloor(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpx.ServeNotFound(w)
		return
	}
	acc := roomAccount(w, r)
	if acc == nil {
		return
	}
	var lays []store.RoomLayout
	for n := 1; n <= RoomPartyFloors; n++ {
		lay, _ := roomLevelLayout(acc, "LEVEL_"+strconv.Itoa(n))
		lays = append(lays, lay)
	}
	store.AccountsMu.Unlock()
	out := make([][]string, 0, len(lays))
	for _, lay := range lays {
		used := []string{}
		for _, s := range []int64{lay.Floor.Seq, lay.Wall.Seq} {
			if s != 0 {
				used = append(used, strconv.FormatInt(s, 10))
			}
		}
		for _, p := range lay.Placed {
			used = append(used, strconv.FormatInt(p.Seq, 10))
		}
		out = append(out, used)
	}
	body, _ := json.Marshal(map[string]any{"result": out})
	httpx.WriteJSON(w, http.StatusOK, string(body))
}

type roomSaveRow struct {
	X         int    `json:"x"`
	Y         int    `json:"y"`
	Z         int    `json:"z"`
	Direction string `json:"direction"`
	Seq       int64  `json:"seq"`
	Cd        string `json:"cd"`
	Superpose *struct {
		Seq []int64 `json:"seq"`
	} `json:"superpose"`
}

type roomSaveReq struct {
	DeleteItems   []int64       `json:"deleteItems"`
	ModifiedItems []roomSaveRow `json:"modifiedItems"`
	AdditionItems []roomSaveRow `json:"additionItems"`
	GroundLevel   string        `json:"groundLevel"`
}

const RoomSaveFalse = `{"result":false}`

func roomDirValid(d string) bool { return d == "FR" || d == "BR" || d == "BL" || d == "FL" }

func inRoomBounds(v int) bool { return v >= 0 && v <= maxRoomCoord }

// applyRoomSave validates req against the owner's items and returns the new
// layout for req.GroundLevel without mutating acc, or false if anything is invalid.
func applyRoomSave(acc *store.Account, req roomSaveReq) (store.RoomLayout, bool) {
	old, ok := roomLevelLayout(acc, req.GroundLevel)
	if !ok {
		return store.RoomLayout{}, false
	}
	owned := make(map[int64]string, len(acc.RoomItems))
	for _, it := range acc.RoomItems {
		owned[it.Seq] = it.Cd
	}
	// Seqs used by other levels cannot be reused here.
	elsewhere := map[int64]bool{}
	for lv, lay := range acc.Rooms {
		if lv == req.GroundLevel {
			continue
		}
		elsewhere[lay.Floor.Seq], elsewhere[lay.Wall.Seq] = true, true
		for _, p := range lay.Placed {
			elsewhere[p.Seq] = true
		}
	}
	lay := store.RoomLayout{Floor: old.Floor, Wall: old.Wall, Placed: slices.Clone(old.Placed)}
	if lay.Placed == nil {
		lay.Placed = []store.RoomPlaced{}
	}
	idx := func(seq int64) int {
		return slices.IndexFunc(lay.Placed, func(p store.RoomPlaced) bool { return p.Seq == seq })
	}
	rowOK := func(row roomSaveRow) (store.RoomPlaced, bool) {
		cd, have := owned[row.Seq]
		if !have || cd != row.Cd || len(cd) < 4 || !roomDirValid(row.Direction) ||
			!inRoomBounds(row.X) || !inRoomBounds(row.Y) || !inRoomBounds(row.Z) {
			return store.RoomPlaced{}, false
		}
		p := store.RoomPlaced{Seq: row.Seq, Cd: cd, X: row.X, Y: row.Y, Z: row.Z, Dir: row.Direction}
		if row.Superpose != nil {
			for _, s := range row.Superpose.Seq {
				if _, have := owned[s]; !have {
					return store.RoomPlaced{}, false
				}
			}
			p.Superpose = slices.Clone(row.Superpose.Seq)
		}
		return p, true
	}

	for _, seq := range req.DeleteItems {
		if seq == old.Floor.Seq || seq == old.Wall.Seq {
			continue // superseded floor/wall, not a furniture delete
		}
		i := idx(seq)
		if i < 0 {
			return store.RoomLayout{}, false
		}
		lay.Placed = slices.Delete(lay.Placed, i, i+1)
	}
	for _, row := range req.ModifiedItems {
		p, ok := rowOK(row)
		i := idx(row.Seq)
		if !ok || i < 0 {
			return store.RoomLayout{}, false
		}
		lay.Placed[i] = p
	}
	for _, row := range req.AdditionItems {
		if elsewhere[row.Seq] {
			return store.RoomLayout{}, false
		}
		// Wall/floor changes arrive as bare {seq, cd} rows (no direction or position).
		if cd := owned[row.Seq]; cd == row.Cd && len(cd) >= 4 && (cd[2:4] == "WA" || cd[2:4] == "TI") {
			if cd[2:4] == "WA" {
				lay.Wall = store.RoomItem{Seq: row.Seq, Cd: cd}
			} else {
				lay.Floor = store.RoomItem{Seq: row.Seq, Cd: cd}
			}
			continue
		}
		p, ok := rowOK(row)
		if !ok || idx(row.Seq) >= 0 || len(lay.Placed) >= maxRoomPlaced {
			return store.RoomLayout{}, false
		}
		lay.Placed = append(lay.Placed, p)
	}
	return lay, true
}

// readRoomBody reads a (possibly gzip) JSON request body into v.
func readRoomBody(r *http.Request, v any) ([]byte, error) {
	body := io.Reader(r.Body)
	if strings.EqualFold(strings.TrimSpace(r.Header.Get("Content-Encoding")), "gzip") {
		zr, err := gzip.NewReader(r.Body)
		if err != nil {
			return nil, err
		}
		defer zr.Close()
		body = zr
	}
	raw, err := io.ReadAll(io.LimitReader(body, httpx.MaxJSONBody+1))
	if err == nil && len(raw) <= httpx.MaxJSONBody {
		_, err = httpx.UnmarshalNativeJSON(raw, v)
	}
	return raw, err
}

func HandleRoomSaveNew(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpx.ServeNotFound(w)
		return
	}
	var req roomSaveReq
	raw, err := readRoomBody(r, &req)
	acc := roomAccount(w, r)
	if acc == nil {
		return
	}
	defer store.AccountsMu.Unlock()
	if err != nil || len(raw) > httpx.MaxJSONBody {
		httpx.WriteJSON(w, http.StatusOK, RoomSaveFalse)
		return
	}
	lay, ok := applyRoomSave(acc, req)
	if !ok {
		httpx.WriteJSON(w, http.StatusOK, RoomSaveFalse)
		return
	}
	prev := acc.Rooms
	acc.Rooms = maps.Clone(prev)
	acc.Rooms[req.GroundLevel] = lay
	if err := store.SaveAccountsLocked(); err != nil {
		acc.Rooms = prev
		httpx.WriteJSON(w, http.StatusInternalServerError, `{"errorCode":"500"}`)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, `{"result":true}`)
}

// RoomDecorInfo returns the saved-layout roomInfo members for level, or nil to
// use the default empty room. aid=="" selects the requester (by cookie).
func RoomDecorInfo(r *http.Request, aid, level string) map[string]any {
	store.AccountsMu.Lock()
	defer store.AccountsMu.Unlock()
	var acc *store.Account
	if aid == "" {
		acc = store.Accounts[httpx.CookieValue(r, "AV_AUTH")]
	} else {
		for _, a := range store.Accounts {
			if a.Aid == aid {
				acc = a
				break
			}
		}
	}
	if acc == nil || EnsureRoomLocked(acc) != nil {
		return nil
	}
	lay, ok := roomLevelLayout(acc, level)
	if !ok {
		return nil
	}
	return roomLayoutInfo(lay)
}

// roomLayoutInfo renders a layout as the roomInfo members the client parses
// (also the body of a loaded preset slot).
func roomLayoutInfo(lay store.RoomLayout) map[string]any {
	tile := func(it store.RoomItem, def, typ string) map[string]any {
		if it.Seq == 0 {
			return map[string]any{"seq": "0", "cd": def, "type": typ}
		}
		return map[string]any{"seq": strconv.FormatInt(it.Seq, 10), "cd": it.Cd, "type": typ}
	}
	floorItem, wallItem, item := []any{}, []any{}, []any{}
	for _, p := range lay.Placed {
		row := map[string]any{"seq": strconv.FormatInt(p.Seq, 10), "cd": p.Cd, "direction": p.Dir, "x": p.X, "y": p.Y}
		switch p.Cd[2:4] {
		case "WD", "DO", "WI", "BW":
			row["z"] = p.Z
			wallItem = append(wallItem, row)
		case "TD":
			floorItem = append(floorItem, row)
		default:
			sup := make([]string, 0, len(p.Superpose))
			for _, s := range p.Superpose {
				sup = append(sup, strconv.FormatInt(s, 10))
			}
			row["superpose"] = map[string]any{"seq": sup}
			item = append(item, row)
		}
	}
	return map[string]any{
		"floor": tile(lay.Floor, RoomFloorCode, "floorTile"), "wall": tile(lay.Wall, RoomWallCode, "wallTile"),
		"floorItem": floorItem, "wallItem": wallItem, "item": item,
	}
}

// roomLevelLayout returns the saved layout for level, or an empty one (floor/wall
// seq 0 = client default art) for an unsaved owned floor 1..roomPartyFloors.
func roomLevelLayout(acc *store.Account, level string) (store.RoomLayout, bool) {
	if lay, ok := acc.Rooms[level]; ok {
		return lay, true
	}
	n := roomLevelNum(level)
	if !RoomLevelValid(level) || n < 1 || n > RoomPartyFloors {
		return store.RoomLayout{}, false
	}
	return store.RoomLayout{Placed: []store.RoomPlaced{}}, true
}

// ponytail: nine free slots instead of purchasable ones. The panel builds one
// cell per availableSlotCount (3 per scrolling row) and never shows a buy cell
// (ResMyRoomPresetSlotList always leaves its canBuy flag 0), so slot/price and
// slot/buy are unreachable from the stock UI and not served.
const roomPresetSlots = 9

type roomPresetReq struct {
	roomSaveReq
	TileSize           int    `json:"tileSize"`
	RepresentImagePath string `json:"representImagePath"`
}

func presetSlotNum(path, prefix string) (int, bool) {
	n, err := strconv.Atoi(strings.TrimPrefix(path, prefix))
	return n, err == nil && n >= 1 && n <= roomPresetSlots
}

// HandleRoomPresetSave serves POST /v4/room/preset/save/<n>. The body is the
// editor's whole current state as additionItems (wall, floor, every placed row),
// so the snapshot is built by applying it to an empty level.
func HandleRoomPresetSave(w http.ResponseWriter, r *http.Request) {
	n, ok := presetSlotNum(r.URL.Path, "/v4/room/preset/save/")
	if r.Method != http.MethodPost || !ok {
		httpx.ServeNotFound(w)
		return
	}
	var req roomPresetReq
	raw, err := readRoomBody(r, &req)
	acc := roomAccount(w, r)
	if acc == nil {
		return
	}
	defer store.AccountsMu.Unlock()
	if err != nil || len(raw) > httpx.MaxJSONBody || len(req.RepresentImagePath) > 1024 || req.TileSize < 0 || req.TileSize > maxRoomCoord {
		httpx.WriteJSON(w, http.StatusOK, RoomSaveFalse)
		return
	}
	// Shadow account: the level starts empty so every row is a plain addition.
	shadow := *acc
	shadow.Rooms = maps.Clone(acc.Rooms)
	if shadow.Rooms == nil {
		shadow.Rooms = map[string]store.RoomLayout{}
	}
	shadow.Rooms[req.GroundLevel] = store.RoomLayout{Placed: []store.RoomPlaced{}}
	req.DeleteItems, req.ModifiedItems = nil, nil
	// An unchanged default wall/floor (floors 2+) comes as seq 0: keep it as the default.
	req.AdditionItems = slices.DeleteFunc(req.AdditionItems, func(row roomSaveRow) bool { return row.Seq == 0 })
	lay, ok := applyRoomSave(&shadow, req.roomSaveReq)
	if !ok {
		httpx.WriteJSON(w, http.StatusOK, RoomSaveFalse)
		return
	}
	prev := acc.Presets
	acc.Presets = maps.Clone(prev)
	if acc.Presets == nil {
		acc.Presets = map[string]store.RoomPreset{}
	}
	acc.Presets[strconv.Itoa(n)] = store.RoomPreset{Layout: lay, Level: req.GroundLevel, Image: req.RepresentImagePath, TileSize: req.TileSize}
	if err := store.SaveAccountsLocked(); err != nil {
		acc.Presets = prev
		httpx.WriteJSON(w, http.StatusInternalServerError, `{"errorCode":"500"}`)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, `{"result":true}`)
}

// HandleRoomPresetRemove serves POST /v4/room/preset/remove/<n> (the client
// clears a filled slot before overwriting it).
func HandleRoomPresetRemove(w http.ResponseWriter, r *http.Request) {
	n, ok := presetSlotNum(r.URL.Path, "/v4/room/preset/remove/")
	if r.Method != http.MethodPost || !ok {
		httpx.ServeNotFound(w)
		return
	}
	acc := roomAccount(w, r)
	if acc == nil {
		return
	}
	defer store.AccountsMu.Unlock()
	prev := acc.Presets
	if _, have := prev[strconv.Itoa(n)]; have {
		acc.Presets = maps.Clone(prev)
		delete(acc.Presets, strconv.Itoa(n))
		if err := store.SaveAccountsLocked(); err != nil {
			acc.Presets = prev
			httpx.WriteJSON(w, http.StatusInternalServerError, `{"errorCode":"500"}`)
			return
		}
	}
	httpx.WriteJSON(w, http.StatusOK, `{"result":true}`)
}

// HandleRoomPresetList serves GET /v4/room/preset/list (editor SAVE panel). The
// client re-requests until availableSlotCount > 0 (CheckPresetSlotData).
func HandleRoomPresetList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpx.ServeNotFound(w)
		return
	}
	acc := roomAccount(w, r)
	if acc == nil {
		return
	}
	type slot struct {
		PresetSeq          int    `json:"presetSeq"`
		RepresentImagePath string `json:"representImagePath"`
		TileSize           int    `json:"tileSize"`
		Openable           bool   `json:"openable"`
	}
	slots := []slot{}
	for n := 1; n <= roomPresetSlots; n++ {
		if p, ok := acc.Presets[strconv.Itoa(n)]; ok {
			slots = append(slots, slot{n, p.Image, p.TileSize, true})
		}
	}
	store.AccountsMu.Unlock()
	body, _ := json.Marshal(map[string]any{"result": map[string]any{"availableSlotCount": roomPresetSlots, "slots": slots}})
	httpx.WriteJSON(w, http.StatusOK, string(body))
}

// HandleRoomPresetFind serves GET /v4/room/preset/find/<n> (panel LOAD mode,
// tapping a filled slot). The client applies result as a roomInfo-shaped layout
// in the editor (LoadPresetMapFromJson); a tileSize larger than the current room
// is refused client-side. The user then saves through the normal editor SAVE.
func HandleRoomPresetFind(w http.ResponseWriter, r *http.Request) {
	n, ok := presetSlotNum(r.URL.Path, "/v4/room/preset/find/")
	if r.Method != http.MethodGet || !ok {
		httpx.ServeNotFound(w)
		return
	}
	acc := roomAccount(w, r)
	if acc == nil {
		return
	}
	p, have := acc.Presets[strconv.Itoa(n)]
	store.AccountsMu.Unlock()
	if !have {
		httpx.WriteJSON(w, http.StatusOK, `{"errorCode":"404"}`)
		return
	}
	info := roomLayoutInfo(p.Layout)
	info["tileSize"] = p.TileSize
	body, _ := json.Marshal(map[string]any{"result": info})
	httpx.WriteJSON(w, http.StatusOK, string(body))
}

const (
	RoomFloorCode = "RUTI000EW"
	RoomWallCode  = "RUWA000HR" // 121400639: has wall_l_1.png and wall_r_1.png
)

// RoomPartyFloors is sent as rc_room_enter_res ExtendFloors (floor count; the
// client hides the floor selector when it is 1).
// ponytail: every room has 2 floors until a remodeling shop / ownership exists.
const RoomPartyFloors = 2

// RoomLevelValid accepts LEVEL_<n>.
func RoomLevelValid(s string) bool {
	n, ok := strings.CutPrefix(s, "LEVEL_")
	if !ok || n == "" {
		return false
	}
	for _, c := range n {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

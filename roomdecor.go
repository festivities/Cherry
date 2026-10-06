package main

import (
	"compress/gzip"
	"encoding/json"
	"image"
	"image/png"
	"io"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

// My Room furniture persistence: owned copies, per-level layouts, and the
// inventory / dividefloor / save routes (libgame.so 10.1.0.0).

const (
	firstRoomSeq  = 100000
	maxRoomPlaced = 500
	maxRoomCoord  = 100 // ponytail: tileSize is 12 in handleRoom; generous cap until per-level sizes are known
	defaultLevel  = "LEVEL_1"
)

type roomItem struct {
	Seq int64  `json:"seq"`
	Cd  string `json:"cd"`
}

type roomPlaced struct {
	Seq       int64   `json:"seq"`
	Cd        string  `json:"cd"`
	X         int     `json:"x"`
	Y         int     `json:"y"`
	Z         int     `json:"z"`
	Dir       string  `json:"dir"`
	Superpose []int64 `json:"superpose,omitempty"`
}

type roomLayout struct {
	Floor  roomItem     `json:"floor"`
	Wall   roomItem     `json:"wall"`
	Placed []roomPlaced `json:"placed"`
}

// ponytail: lab showcase grant for every account: one copy of each renderable
// wall/tile and of 33 furniture codes so the editor has something to place.
var roomShowcaseCodes = strings.Fields(
	"RUWA000HR RUWA000IC RUWA000UD RUWA000W6 RUWA000Y8 RUWA000YY RUWA000ZA RUWA000ZM RUWA000ZY RUWA0010A RUWA00136 " +
		"RUTI0007H RUTI0008E RUTI000EW RUTI000FH RUTI000HF RUTI000SA RUTI000SJ RUTI000T6 RUTI000TF RUTI000UV RUTI000VV " +
		"RUTA0004S RUTA00068 RUTA00069 RUCH0011X RUCH0011Y RUCH0011Z RUCH00120 RUBE000KH RUBE000O0 RUBE000O8 RUKI00022 RUKI00023 " +
		"RUCL000EE RUCL000EF RUTB000GB RUTB000GG RUTB000NO RUPL0004M RUPL00055 RUPL0005J RUTD003K2 RUTD003QS RUTD003RC " +
		"RUTD00401 RUTD00405 RUTD0040C RUWD0000S RUWD00080 RUWD00089 RUWI00033 RUWI000IE RUDO0002Z RUDO000B6 " + roomDiaryCode)

// New accounts start with the default floor, wall, door and diary only (no pets);
// the showcase above and the pets are granted to lab accounts alone.
var roomStarterCodes = []string{roomFloorCode, roomWallCode, roomDoorCode, roomDiaryCode}

// roomCategoryCode maps the cd category letters to the client's categoryCode.
// Confidence: WA->UWLPP and TI->UFLOR medium (names; the qword_3BDDB20 table is
// NOT positional against catIdx, only coincidentally at 14/15), WD/DO/TD/CH/TA/
// BE/KI/BA/PL/FD/GC/AB/VO fairly high by name, the rest are guesses falling to
// UPROP. The parser only uses it for UI tab grouping.
func roomCategoryCode(cd string) string {
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
var roomDefaultPlaced = []roomPlaced{
	{Cd: roomDoorCode, X: 6, Y: 0, Dir: "FL"},
	{Cd: roomDiaryCode, X: 2, Y: 2, Dir: "FR"},
}

// ensureRoomLocked grants missing showcase codes and gives LEVEL_1 a default
// floor/wall (first seed) and door/diary (whenever none is placed). Caller holds accountsMu.
func ensureRoomLocked(acc *account) error {
	owned := map[string]int64{}
	for _, it := range acc.roomItems {
		if _, ok := owned[it.Cd]; !ok {
			owned[it.Cd] = it.Seq
		}
	}
	fresh := len(acc.roomItems) == 0
	items := slices.Clone(acc.roomItems)
	seq := max(acc.nextRoomSeq, firstRoomSeq)
	codes := roomStarterCodes
	if labAids[acc.aid] {
		codes = roomShowcaseCodes
	}
	for _, cd := range codes {
		if _, ok := owned[cd]; !ok {
			items = append(items, roomItem{Seq: seq, Cd: cd})
			owned[cd] = seq
			seq++
		}
	}
	lay, ok := acc.rooms[defaultLevel]
	if !ok {
		lay = roomLayout{Placed: []roomPlaced{}}
	}
	if fresh {
		lay.Floor = roomItem{Seq: owned[roomFloorCode], Cd: roomFloorCode}
		lay.Wall = roomItem{Seq: owned[roomWallCode], Cd: roomWallCode}
	}
	used := map[int64]bool{}
	for _, l := range acc.rooms {
		for _, p := range l.Placed {
			used[p.Seq] = true
		}
	}
	placed := slices.Clone(lay.Placed)
	for _, d := range roomDefaultPlaced {
		cat := d.Cd[2:4]
		if slices.ContainsFunc(placed, func(p roomPlaced) bool { return p.Cd[2:4] == cat }) || used[owned[d.Cd]] {
			continue
		}
		d.Seq = owned[d.Cd]
		placed = append(placed, d)
	}
	if len(items) == len(acc.roomItems) && len(placed) == len(lay.Placed) && ok {
		return nil
	}
	lay.Placed = placed
	prevItems, prevNext, prevRooms := acc.roomItems, acc.nextRoomSeq, acc.rooms
	acc.roomItems, acc.nextRoomSeq = items, seq
	acc.rooms = maps.Clone(prevRooms)
	if acc.rooms == nil {
		acc.rooms = map[string]roomLayout{}
	}
	acc.rooms[defaultLevel] = lay
	if err := saveAccountsLocked(); err != nil {
		acc.roomItems, acc.nextRoomSeq, acc.rooms = prevItems, prevNext, prevRooms
		return err
	}
	return nil
}

// roomAccount resolves the requester and seeds it; on failure it has already
// written the response and returns nil. Caller must unlock accountsMu iff non-nil.
func roomAccount(w http.ResponseWriter, r *http.Request) *account {
	accountsMu.Lock()
	acc := accounts[cookieValue(r, "AV_AUTH")]
	if acc == nil {
		accountsMu.Unlock()
		writeJSON(w, http.StatusNotFound, unknownSessionBody)
		return nil
	}
	if err := ensureRoomLocked(acc); err != nil {
		accountsMu.Unlock()
		writeJSON(w, http.StatusInternalServerError, `{"errorCode":"500"}`)
		return nil
	}
	return acc
}

func handleInvenInterior(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		serveNotFound(w)
		return
	}
	acc := roomAccount(w, r)
	if acc == nil {
		return
	}
	items := slices.Clone(acc.roomItems)
	accountsMu.Unlock()
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
		rows = append(rows, row{Seq: strconv.FormatInt(it.Seq, 10), Cd: it.Cd, Grade: "N", CategoryCode: roomCategoryCode(it.Cd)})
	}
	body, _ := json.Marshal(map[string]any{"result": rows})
	writeJSON(w, http.StatusOK, string(body))
}

func roomLevelNum(level string) int {
	n, _ := strconv.Atoi(strings.TrimPrefix(level, "LEVEL_"))
	return n
}

func handleInvenDivideFloor(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		serveNotFound(w)
		return
	}
	acc := roomAccount(w, r)
	if acc == nil {
		return
	}
	var lays []roomLayout
	for n := 1; n <= roomPartyFloors; n++ {
		lay, _ := roomLevelLayout(acc, "LEVEL_"+strconv.Itoa(n))
		lays = append(lays, lay)
	}
	accountsMu.Unlock()
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
	writeJSON(w, http.StatusOK, string(body))
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

const roomSaveFalse = `{"result":false}`

func roomDirValid(d string) bool { return d == "FR" || d == "BR" || d == "BL" || d == "FL" }

func inRoomBounds(v int) bool { return v >= 0 && v <= maxRoomCoord }

// applyRoomSave validates req against the owner's items and returns the new
// layout for req.GroundLevel without mutating acc, or false if anything is invalid.
func applyRoomSave(acc *account, req roomSaveReq) (roomLayout, bool) {
	old, ok := roomLevelLayout(acc, req.GroundLevel)
	if !ok {
		return roomLayout{}, false
	}
	owned := make(map[int64]string, len(acc.roomItems))
	for _, it := range acc.roomItems {
		owned[it.Seq] = it.Cd
	}
	// Seqs used by other levels cannot be reused here.
	elsewhere := map[int64]bool{}
	for lv, lay := range acc.rooms {
		if lv == req.GroundLevel {
			continue
		}
		elsewhere[lay.Floor.Seq], elsewhere[lay.Wall.Seq] = true, true
		for _, p := range lay.Placed {
			elsewhere[p.Seq] = true
		}
	}
	lay := roomLayout{Floor: old.Floor, Wall: old.Wall, Placed: slices.Clone(old.Placed)}
	if lay.Placed == nil {
		lay.Placed = []roomPlaced{}
	}
	idx := func(seq int64) int {
		return slices.IndexFunc(lay.Placed, func(p roomPlaced) bool { return p.Seq == seq })
	}
	rowOK := func(row roomSaveRow) (roomPlaced, bool) {
		cd, have := owned[row.Seq]
		if !have || cd != row.Cd || len(cd) < 4 || !roomDirValid(row.Direction) ||
			!inRoomBounds(row.X) || !inRoomBounds(row.Y) || !inRoomBounds(row.Z) {
			return roomPlaced{}, false
		}
		p := roomPlaced{Seq: row.Seq, Cd: cd, X: row.X, Y: row.Y, Z: row.Z, Dir: row.Direction}
		if row.Superpose != nil {
			for _, s := range row.Superpose.Seq {
				if _, have := owned[s]; !have {
					return roomPlaced{}, false
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
			return roomLayout{}, false
		}
		lay.Placed = slices.Delete(lay.Placed, i, i+1)
	}
	for _, row := range req.ModifiedItems {
		p, ok := rowOK(row)
		i := idx(row.Seq)
		if !ok || i < 0 {
			return roomLayout{}, false
		}
		lay.Placed[i] = p
	}
	for _, row := range req.AdditionItems {
		if elsewhere[row.Seq] {
			return roomLayout{}, false
		}
		// Wall/floor changes arrive as bare {seq, cd} rows (no direction or position).
		if cd := owned[row.Seq]; cd == row.Cd && len(cd) >= 4 && (cd[2:4] == "WA" || cd[2:4] == "TI") {
			if cd[2:4] == "WA" {
				lay.Wall = roomItem{Seq: row.Seq, Cd: cd}
			} else {
				lay.Floor = roomItem{Seq: row.Seq, Cd: cd}
			}
			continue
		}
		p, ok := rowOK(row)
		if !ok || idx(row.Seq) >= 0 || len(lay.Placed) >= maxRoomPlaced {
			return roomLayout{}, false
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
	raw, err := io.ReadAll(io.LimitReader(body, maxJSONBody+1))
	if err == nil && len(raw) <= maxJSONBody {
		_, err = unmarshalNativeJSON(raw, v)
	}
	return raw, err
}

func handleRoomSaveNew(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		serveNotFound(w)
		return
	}
	var req roomSaveReq
	raw, err := readRoomBody(r, &req)
	acc := roomAccount(w, r)
	if acc == nil {
		return
	}
	defer accountsMu.Unlock()
	if err != nil || len(raw) > maxJSONBody {
		writeJSON(w, http.StatusOK, roomSaveFalse)
		return
	}
	lay, ok := applyRoomSave(acc, req)
	if !ok {
		writeJSON(w, http.StatusOK, roomSaveFalse)
		return
	}
	prev := acc.rooms
	acc.rooms = maps.Clone(prev)
	acc.rooms[req.GroundLevel] = lay
	if err := saveAccountsLocked(); err != nil {
		acc.rooms = prev
		writeJSON(w, http.StatusInternalServerError, `{"errorCode":"500"}`)
		return
	}
	writeJSON(w, http.StatusOK, `{"result":true}`)
}

// roomDecorInfo returns the saved-layout roomInfo members for level, or nil to
// use the default empty room. aid=="" selects the requester (by cookie).
func roomDecorInfo(r *http.Request, aid, level string) map[string]any {
	accountsMu.Lock()
	defer accountsMu.Unlock()
	var acc *account
	if aid == "" {
		acc = accounts[cookieValue(r, "AV_AUTH")]
	} else {
		for _, a := range accounts {
			if a.aid == aid {
				acc = a
				break
			}
		}
	}
	if acc == nil || ensureRoomLocked(acc) != nil {
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
func roomLayoutInfo(lay roomLayout) map[string]any {
	tile := func(it roomItem, def, typ string) map[string]any {
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
		"floor": tile(lay.Floor, roomFloorCode, "floorTile"), "wall": tile(lay.Wall, roomWallCode, "wallTile"),
		"floorItem": floorItem, "wallItem": wallItem, "item": item,
	}
}

// composeRoomDP builds a thumbnail for interior/tile item folders without an
// authentic dp.png: the item's main sprite scaled down to fit 128x128.
func composeRoomDP(dir, kind string) (*image.RGBA, bool) {
	names := []string{"fl_l0_a00.png", "wall_l_1.png"}
	if kind == "tile" {
		names = []string{"tile_1.png"}
	}
	for _, name := range names {
		f, err := os.Open(filepath.Join(dir, name))
		if err != nil {
			continue
		}
		src, err := png.Decode(f)
		f.Close()
		if err != nil {
			continue
		}
		b := src.Bounds()
		if b.Dx() == 0 || b.Dy() == 0 {
			continue
		}
		const maxDim = 128
		w, h := b.Dx(), b.Dy()
		if w > maxDim || h > maxDim {
			if w >= h {
				w, h = maxDim, max(1, h*maxDim/w)
			} else {
				w, h = max(1, w*maxDim/h), maxDim
			}
		}
		dst := image.NewRGBA(image.Rect(0, 0, w, h))
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ { // box average over the source cell (premultiplied)
				x0, x1 := b.Min.X+x*b.Dx()/w, b.Min.X+(x+1)*b.Dx()/w
				y0, y1 := b.Min.Y+y*b.Dy()/h, b.Min.Y+(y+1)*b.Dy()/h
				x1, y1 = max(x1, x0+1), max(y1, y0+1)
				var sr, sg, sb, sa, n uint64
				for sy := y0; sy < y1; sy++ {
					for sx := x0; sx < x1; sx++ {
						r, g, bl, a := src.At(sx, sy).RGBA()
						sr, sg, sb, sa, n = sr+uint64(r), sg+uint64(g), sb+uint64(bl), sa+uint64(a), n+1
					}
				}
				i := dst.PixOffset(x, y)
				dst.Pix[i], dst.Pix[i+1], dst.Pix[i+2], dst.Pix[i+3] = uint8(sr/n>>8), uint8(sg/n>>8), uint8(sb/n>>8), uint8(sa/n>>8)
			}
		}
		return dst, true
	}
	return nil, false
}

// roomLevelLayout returns the saved layout for level, or an empty one (floor/wall
// seq 0 = client default art) for an unsaved owned floor 1..roomPartyFloors.
func roomLevelLayout(acc *account, level string) (roomLayout, bool) {
	if lay, ok := acc.rooms[level]; ok {
		return lay, true
	}
	n := roomLevelNum(level)
	if !roomLevelValid(level) || n < 1 || n > roomPartyFloors {
		return roomLayout{}, false
	}
	return roomLayout{Placed: []roomPlaced{}}, true
}

// roomNextSeq keeps the allocator above every owned seq after a load.
func roomNextSeq(items []roomItem, next int64) int64 {
	next = max(next, firstRoomSeq)
	for _, it := range items {
		next = max(next, it.Seq+1)
	}
	return next
}

// roomPreset is one saved quick-pick slot: a snapshot of a level layout plus the
// uploaded thumbnail URL the client sent as representImagePath.
type roomPreset struct {
	Layout   roomLayout `json:"layout"`
	Level    string     `json:"level"`
	Image    string     `json:"image"`
	TileSize int        `json:"tileSize"`
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

// handleRoomPresetSave serves POST /v4/room/preset/save/<n>. The body is the
// editor's whole current state as additionItems (wall, floor, every placed row),
// so the snapshot is built by applying it to an empty level.
func handleRoomPresetSave(w http.ResponseWriter, r *http.Request) {
	n, ok := presetSlotNum(r.URL.Path, "/v4/room/preset/save/")
	if r.Method != http.MethodPost || !ok {
		serveNotFound(w)
		return
	}
	var req roomPresetReq
	raw, err := readRoomBody(r, &req)
	acc := roomAccount(w, r)
	if acc == nil {
		return
	}
	defer accountsMu.Unlock()
	if err != nil || len(raw) > maxJSONBody || len(req.RepresentImagePath) > 1024 || req.TileSize < 0 || req.TileSize > maxRoomCoord {
		writeJSON(w, http.StatusOK, roomSaveFalse)
		return
	}
	// Shadow account: the level starts empty so every row is a plain addition.
	shadow := *acc
	shadow.rooms = maps.Clone(acc.rooms)
	if shadow.rooms == nil {
		shadow.rooms = map[string]roomLayout{}
	}
	shadow.rooms[req.GroundLevel] = roomLayout{Placed: []roomPlaced{}}
	req.DeleteItems, req.ModifiedItems = nil, nil
	// An unchanged default wall/floor (floors 2+) comes as seq 0: keep it as the default.
	req.AdditionItems = slices.DeleteFunc(req.AdditionItems, func(row roomSaveRow) bool { return row.Seq == 0 })
	lay, ok := applyRoomSave(&shadow, req.roomSaveReq)
	if !ok {
		writeJSON(w, http.StatusOK, roomSaveFalse)
		return
	}
	prev := acc.presets
	acc.presets = maps.Clone(prev)
	if acc.presets == nil {
		acc.presets = map[string]roomPreset{}
	}
	acc.presets[strconv.Itoa(n)] = roomPreset{Layout: lay, Level: req.GroundLevel, Image: req.RepresentImagePath, TileSize: req.TileSize}
	if err := saveAccountsLocked(); err != nil {
		acc.presets = prev
		writeJSON(w, http.StatusInternalServerError, `{"errorCode":"500"}`)
		return
	}
	writeJSON(w, http.StatusOK, `{"result":true}`)
}

// handleRoomPresetRemove serves POST /v4/room/preset/remove/<n> (the client
// clears a filled slot before overwriting it).
func handleRoomPresetRemove(w http.ResponseWriter, r *http.Request) {
	n, ok := presetSlotNum(r.URL.Path, "/v4/room/preset/remove/")
	if r.Method != http.MethodPost || !ok {
		serveNotFound(w)
		return
	}
	acc := roomAccount(w, r)
	if acc == nil {
		return
	}
	defer accountsMu.Unlock()
	prev := acc.presets
	if _, have := prev[strconv.Itoa(n)]; have {
		acc.presets = maps.Clone(prev)
		delete(acc.presets, strconv.Itoa(n))
		if err := saveAccountsLocked(); err != nil {
			acc.presets = prev
			writeJSON(w, http.StatusInternalServerError, `{"errorCode":"500"}`)
			return
		}
	}
	writeJSON(w, http.StatusOK, `{"result":true}`)
}

// handleRoomPresetList serves GET /v4/room/preset/list (editor SAVE panel). The
// client re-requests until availableSlotCount > 0 (CheckPresetSlotData).
func handleRoomPresetList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		serveNotFound(w)
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
		if p, ok := acc.presets[strconv.Itoa(n)]; ok {
			slots = append(slots, slot{n, p.Image, p.TileSize, true})
		}
	}
	accountsMu.Unlock()
	body, _ := json.Marshal(map[string]any{"result": map[string]any{"availableSlotCount": roomPresetSlots, "slots": slots}})
	writeJSON(w, http.StatusOK, string(body))
}

// handleRoomPresetFind serves GET /v4/room/preset/find/<n> (panel LOAD mode,
// tapping a filled slot). The client applies result as a roomInfo-shaped layout
// in the editor (LoadPresetMapFromJson); a tileSize larger than the current room
// is refused client-side. The user then saves through the normal editor SAVE.
func handleRoomPresetFind(w http.ResponseWriter, r *http.Request) {
	n, ok := presetSlotNum(r.URL.Path, "/v4/room/preset/find/")
	if r.Method != http.MethodGet || !ok {
		serveNotFound(w)
		return
	}
	acc := roomAccount(w, r)
	if acc == nil {
		return
	}
	p, have := acc.presets[strconv.Itoa(n)]
	accountsMu.Unlock()
	if !have {
		writeJSON(w, http.StatusOK, `{"errorCode":"404"}`)
		return
	}
	info := roomLayoutInfo(p.Layout)
	info["tileSize"] = p.TileSize
	body, _ := json.Marshal(map[string]any{"result": info})
	writeJSON(w, http.StatusOK, string(body))
}

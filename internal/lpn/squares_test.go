package lpn

import (
	"strings"
	"testing"

	"cherry/internal/assets"
	"cherry/internal/opdata"
)

func TestSquareSpawnPx(t *testing.T) {
	for _, tc := range []struct {
		name       string
		size, x, y uint64
		px, py     uint64
	}{
		{"LINE City", 160, 93, 80, 6920, 2920},
		{"LINE Café", 160, 87, 85, 6480, 2940},
		{"Runway Square", 180, 85, 81, 7360, 3860},
		{"Pet Theme Park", 160, 89, 97, 6080, 2660}, // racing-spec T2: AVATAR_START (89,97) = (6080,2660)
		// The AVATAR_START of each new map (its .mworld, 2026-11-17).
		{"LINE Park", 180, 91, 72, 7960, 3920},
		{"Treasure Island", 160, 73, 98, 5400, 2960},
		{"LINE City Shibuya", 160, 110, 80, 7600, 2580},
		{"Disney Square", 220, 153, 153, 8800, 2660},
		{"Disney Disco", 140, 71, 95, 4640, 2260},
		{"Hatsune Miku Square", 140, 81, 77, 5760, 2420},
		{"SANRIO CHARACTERS Square", 160, 117, 111, 6640, 1820},
	} {
		sq := Square{Name: tc.name, MapSize: tc.size, Spawn: Tile{X: tc.x, Y: tc.y}}
		if x, y := sq.spawnPx(); x != tc.px || y != tc.py {
			t.Fatalf("%s spawn = (%d,%d), want (%d,%d)", tc.name, x, y, tc.px, tc.py)
		}
	}
}

// Every entry of the embedded default is a distinct room: unique themes and list names, a map folder the store serves as a
// world pack, a spawn inside its map, and no boss, race or pet-park theme (28-31) listed.
func TestSquaresListedEntries(t *testing.T) {
	themes, names := map[uint64]bool{}, map[string]bool{}
	for _, sq := range squaresFile.Get().Squares {
		if themes[sq.Theme] || names[sq.ListName] {
			t.Fatalf("theme %d or list name %q repeated", sq.Theme, sq.ListName)
		}
		themes[sq.Theme], names[sq.ListName] = true, true
		if !assets.ValidPack("world", sq.Map+".mworldpack") {
			t.Fatalf("theme %d: map %q is not a world pack name", sq.Theme, sq.Map)
		}
		if sq.Spawn.X >= sq.MapSize || sq.Spawn.Y >= sq.MapSize {
			t.Fatalf("theme %d: spawn %d,%d outside the %d-tile map", sq.Theme, sq.Spawn.X, sq.Spawn.Y, sq.MapSize)
		}
		if sq.Theme >= 28 && sq.Theme <= 31 {
			t.Fatalf("theme %d is listed: boss, race and pet-park worlds are entered from the Square", sq.Theme)
		}
	}
}

// The embedded default lists the eleven Squares in order, each on its own map. Treasure Island (theme 6) and LINE City
// (theme 36) keep their list positions (5 and 0).
func TestSquaresDefaultList(t *testing.T) {
	list := squaresFile.Get().listed()
	if len(list) != 11 {
		t.Fatalf("listed = %d, want 11", len(list))
	}
	for i, theme := range []uint64{36, 32, 33, 34, 35, 6, 37, 10, 27, 38, 39} {
		if list[i].Theme != theme || list[i].Map != squareTestMaps[theme] {
			t.Fatalf("entry %d = theme %d map %s", i, list[i].Theme, list[i].Map)
		}
	}
	// IconPath (field 7) carries the list picture; Runway Square has none and keeps the client default.
	if info := squareThemeInfo(list[0], 0); !strings.Contains(string(info), "animation/world_icon/square_thum_city.png") {
		t.Fatalf("LINE City theme_info lacks its icon: %q", info)
	}
	if list[2].Icon != "" {
		t.Fatalf("Runway Square icon = %q, want none", list[2].Icon)
	}
}

func TestSquaresCheckRejectsBadData(t *testing.T) {
	city := `{"theme":36,"name":"LINE City","list_name":"LINE City","map":"2401039000","map_size":160,"spawn":{"x":93,"y":80},"index":0,"visible":true}`
	island := `{"theme":6,"name":"Treasure Island","list_name":"Treasure Island","map":"2108176000","map_size":160,"spawn":{"x":73,"y":98},"index":5,"visible":true}`
	cafe := `{"theme":32,"name":"LINE Café","list_name":"LINE Café","map":"2202250100","map_size":160,"spawn":{"x":87,"y":85},"index":1,"visible":true}`
	doc := func(entries ...string) string {
		return `{"origin":"mixed","note":"test","squares":[` + strings.Join(entries, ",") + `]}`
	}
	for name, bad := range map[string]string{
		"no Treasure Island":      doc(city, cafe),
		"no LINE City":            doc(island, cafe),
		"City listed twice":       doc(city, city, island),
		"City hidden":             doc(strings.Replace(city, `"visible":true`, `"visible":false`, 1), island, cafe),
		"City on other map":       doc(strings.Replace(city, "2401039000", "2108176000", 1), island, cafe),
		"Island on other map":     doc(city, strings.Replace(island, "2108176000", "2401039000", 1), cafe),
		"Island hidden":           doc(city, strings.Replace(island, `"visible":true`, `"visible":false`, 1), cafe),
		"empty name":              doc(strings.Replace(city, `"name":"LINE City"`, `"name":""`, 1), island, cafe),
		"empty list name":         doc(strings.Replace(city, `"list_name":"LINE City"`, `"list_name":""`, 1), island, cafe),
		"empty map":               doc(city, island, strings.Replace(cafe, "2202250100", "", 1)),
		"map is a path":           doc(city, island, strings.Replace(cafe, "2202250100", "../x", 1)),
		"spawn off the map":       doc(city, island, strings.Replace(cafe, `"x":87`, `"x":160`, 1)),
		"zero map size":           doc(city, island, strings.Replace(cafe, `"map_size":160`, `"map_size":0`, 1)),
		"unknown key":             doc(city, island, strings.Replace(cafe, `"visible":true`, `"visible":true,"extra":1`, 1)),
		"not an object list":      `{"name":"LINE City"}`,
		"icon outside world_icon": doc(city, island, strings.Replace(cafe, `"visible":true`, `"visible":true,"icon":"UIResource/x.png"`, 1)),
		"icon climbs out":         doc(city, island, strings.Replace(cafe, `"visible":true`, `"visible":true,"icon":"animation/world_icon/../x.png"`, 1)),
		"boss world listed":       doc(city, island, strings.Replace(cafe, `"theme":32`, `"theme":28`, 1)),
		"race world listed":       doc(city, island, strings.Replace(cafe, `"theme":32`, `"theme":31`, 1)),
	} {
		if _, err := opdata.Decode([]byte(bad), checkSquares); err == nil {
			t.Fatalf("%s accepted: %s", name, bad)
		}
	}
	if _, err := opdata.Decode([]byte(doc(city, island, cafe)), checkSquares); err != nil {
		t.Fatalf("valid document refused: %v", err)
	}
}

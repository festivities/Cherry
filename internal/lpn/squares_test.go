package lpn

import (
	"strings"
	"testing"

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
	} {
		sq := Square{Name: tc.name, MapSize: tc.size, Spawn: Tile{X: tc.x, Y: tc.y}}
		if x, y := sq.spawnPx(); x != tc.px || y != tc.py {
			t.Fatalf("%s spawn = (%d,%d), want (%d,%d)", tc.name, x, y, tc.px, tc.py)
		}
	}
}

// The embedded default lists the four Squares in order, each on its own map.
func TestSquaresDefaultList(t *testing.T) {
	list := squaresFile.Get().listed()
	if len(list) != 4 {
		t.Fatalf("listed = %d, want 4", len(list))
	}
	for i, theme := range []uint64{6, 32, 33, 34} {
		if list[i].Theme != theme || list[i].Map != squareTestMaps[theme] {
			t.Fatalf("entry %d = theme %d map %s", i, list[i].Theme, list[i].Map)
		}
	}
}

func TestSquaresCheckRejectsBadData(t *testing.T) {
	city := `{"theme":6,"name":"LINE City","list_name":"LINE City","map":"2401039000","map_size":160,"spawn":{"x":93,"y":80},"index":0,"visible":true}`
	cafe := `{"theme":32,"name":"LINE Café","list_name":"LINE Café","map":"2202250100","map_size":160,"spawn":{"x":87,"y":85},"index":1,"visible":true}`
	doc := func(entries ...string) string {
		return `{"origin":"mixed","note":"test","squares":[` + strings.Join(entries, ",") + `]}`
	}
	for name, bad := range map[string]string{
		"no LINE City":       doc(cafe),
		"City listed twice":  doc(city, city),
		"City hidden":        doc(strings.Replace(city, `"visible":true`, `"visible":false`, 1)),
		"City on other map":  doc(strings.Replace(city, "2401039000", "2202250100", 1)),
		"empty name":         doc(strings.Replace(city, `"name":"LINE City"`, `"name":""`, 1), cafe),
		"empty list name":    doc(strings.Replace(city, `"list_name":"LINE City"`, `"list_name":""`, 1)),
		"empty map":          doc(city, strings.Replace(cafe, "2202250100", "", 1)),
		"map is a path":      doc(city, strings.Replace(cafe, "2202250100", "../x", 1)),
		"spawn off the map":  doc(city, strings.Replace(cafe, `"x":87`, `"x":160`, 1)),
		"zero map size":      doc(city, strings.Replace(cafe, `"map_size":160`, `"map_size":0`, 1)),
		"unknown key":        doc(city, strings.Replace(cafe, `"visible":true`, `"visible":true,"extra":1`, 1)),
		"not an object list": `{"name":"LINE City"}`,
	} {
		if _, err := opdata.Decode([]byte(bad), checkSquares); err == nil {
			t.Fatalf("%s accepted: %s", name, bad)
		}
	}
	if _, err := opdata.Decode([]byte(doc(city, cafe)), checkSquares); err != nil {
		t.Fatalf("valid document refused: %v", err)
	}
}

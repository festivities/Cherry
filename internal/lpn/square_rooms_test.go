package lpn

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// rosterAids returns the aids of a relaystart roster (PlayerInfo entries, field 2), self first.
func rosterAids(body []byte) []uint64 {
	var aids []uint64
	pbScan(body, func(f, _ uint64, d []byte) {
		if f == 2 {
			aid, _ := protobufUintField(d, 1)
			aids = append(aids, aid)
		}
	})
	return aids
}

// Each listed Square enters its own map with its own spawn (the iso conversion of data/squares.json).
func TestSquareEnterEachThemeSpawnsOnItsMap(t *testing.T) {
	spawn := map[uint64][2]uint64{6: {6920, 2920}, 32: {6480, 2940}, 33: {7360, 3860}, 34: {6080, 2660}}
	for theme, px := range spawn {
		c := newSqClientIn(t, &squareRooms{}, 1)
		roster := c.joinTheme(11, theme)
		if !bytes.Contains(roster, squarePositionInfo(px[0], px[1], 0)) {
			t.Fatalf("theme %d roster lacks its spawn %v: %x", theme, px, roster)
		}
	}
}

func TestSquareRoomsAreSeparate(t *testing.T) {
	rooms := &squareRooms{}
	a := newSqClientIn(t, rooms, 1) // LINE City
	b := newSqClientIn(t, rooms, 2) // LINE Café
	c := newSqClientIn(t, rooms, 3) // LINE City
	a.join(11)
	b.joinTheme(22, 32)
	c.join(33)
	a.read() // adduser 33: same Square
	b.none() // the Café never hears of City players

	b.send(8, pbLen(nil, 1, []byte("hi")))
	b.expect(11, pbVar(pbLen(pbVar(pbVar(nil, 1, 22), 2, 1), 3, []byte("hi")), 4, 0))
	a.none()
	c.none()

	a.send(6, pbLen(nil, 1, squarePositionInfo(7000, 2800, 2)))
	c.expect(9, pbLen(pbVar(nil, 1, 11), 2, squarePositionInfo(7000, 2800, 2)))
	b.none()

	a.send(1, pbVar(nil, 1, 0))
	_, body := a.read()
	infos, _ := themeList(t, body)
	counts := map[uint64]uint64{}
	for _, ti := range infos {
		counts[ti.id] = ti.count
	}
	if counts[6] != 2 || counts[32] != 1 || counts[33] != 0 || counts[34] != 0 {
		t.Fatalf("user counts = %v, want City 2, Café 1, others 0", counts)
	}
}

// Going to another Square leaves the current one for its players, and the new roster holds only the
// newcomer; a move after that reaches nobody in the old Square.
func TestSquareSwitchLeavesOldRoom(t *testing.T) {
	rooms := &squareRooms{}
	a := newSqClientIn(t, rooms, 1)
	b := newSqClientIn(t, rooms, 2)
	a.join(11)
	b.join(22)
	a.read() // adduser 22

	a.send(2, pbVar(nil, 1, 32))
	id, enter := a.read()
	if id != 6 {
		t.Fatalf("switch enter msgid = %d", id)
	}
	a.checkEnter(enter, 32, squareTestMaps[32])
	b.expect(3, []byte{0x08, 11, 0x10, 0}) // deluser from City

	a.send(5, nil)
	id, roster := a.read()
	if id != 8 {
		t.Fatalf("switch relaystart msgid = %d", id)
	}
	if aids := rosterAids(roster[len(relayResultBytes()):]); len(aids) != 1 || aids[0] != 11 {
		t.Fatalf("Café roster = %v, want [11] alone", aids)
	}
	a.send(6, pbLen(nil, 1, squarePositionInfo(6600, 2900, 1)))
	b.none()
}

// Themes that are not listed, including the boss world (28) and the race (31), return to LINE City.
func TestSquareUnlistedThemeReturnsToCity(t *testing.T) {
	for _, theme := range []uint64{0, 2, 28, 31, 99} {
		c := newSqClientIn(t, &squareRooms{}, 1)
		c.send(0, loginBody(11))
		c.read()
		c.send(2, pbVar(nil, 1, theme))
		id, enter := c.read()
		if id != 6 || !c.checkEnter(enter, squareThemeID, squareTestMaps[squareThemeID]) {
			t.Fatalf("theme %d: enter msgid=%d (want City with NpcList)", theme, id)
		}
	}
}

// An operator file can hide a Square: it is not listed and a request for it goes to LINE City.
func TestSquareOperatorFileHidesSquare(t *testing.T) {
	path := filepath.Join(t.TempDir(), "squares.json")
	doc := `{"origin":"mixed","note":"test","squares":[
	  {"theme":6,"name":"LINE City","list_name":"LINE City","map":"2401039000","map_size":160,"spawn":{"x":93,"y":80},"index":0,"visible":true},
	  {"theme":33,"name":"Runway Square","list_name":"Runway Square","map":"2310272600","map_size":180,"spawn":{"x":85,"y":81},"index":1,"visible":false}
	]}`
	if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(squaresFile.Reset)
	if err := squaresFile.LoadFrom(path); err != nil {
		t.Fatal(err)
	}
	c := newSqClientIn(t, &squareRooms{}, 1)
	c.send(0, loginBody(11))
	c.read()
	c.send(1, pbVar(nil, 1, 0))
	_, body := c.read()
	if infos, _ := themeList(t, body); len(infos) != 1 || infos[0].id != squareThemeID {
		t.Fatalf("list = %+v, want City alone", infos)
	}
	c.send(2, pbVar(nil, 1, 33))
	id, enter := c.read()
	if id != 6 || !c.checkEnter(enter, squareThemeID, squareTestMaps[squareThemeID]) {
		t.Fatalf("hidden theme enter msgid=%d", id)
	}
}

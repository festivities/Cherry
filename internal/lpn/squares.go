package lpn

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"cherry/data"
	"cherry/internal/opdata"
)

// Squares is data/squares.json: the Squares in the Square list (rc_themelist_res) and the map and spawn
// each one loads (rc_room_enter_res, relaystart). Theme is the world type the client keys on (ThemeId).
type Squares struct {
	opdata.Provenance
	Squares []Square `json:"squares"`
}

// Square is one entry of data/squares.json. Names are Cherry's choice from the client's string table
// (LINE City 9050, LINE Café 9018); Map is a folder under files/world, MapSize its tiles per side and
// Spawn its AVATAR_START tile. Only visible entries are listed or enterable.
type Square struct {
	Theme    uint64 `json:"theme"`
	Name     string `json:"name"`
	ListName string `json:"list_name"`
	Map      string `json:"map"`
	MapSize  uint64 `json:"map_size"`
	Spawn    Tile   `json:"spawn"`
	Index    uint64 `json:"index"`
	Visible  bool   `json:"visible"`
	// Icon is the list picture, a path under the device files/ tree (theme_info IconPath): the client draws it only
	// when that file is on the device, so it names an animation-list file (animation/world_icon/...); "" keeps the
	// client's default picture.
	Icon string `json:"icon,omitempty"`
}

// Tile is a map tile (wx, wy).
type Tile struct {
	X uint64 `json:"x"`
	Y uint64 `json:"y"`
}

var squaresFile = opdata.New("squares.json", data.SquaresJSON, checkSquares, nil)

// LoadOperatorData loads the game-connection operator data (squares.json) at startup.
func LoadOperatorData() error { return squaresFile.Load() }

// checkSquares: unique themes, names and map folder set, spawn inside the map, and the two Squares that carry
// NPCs listed on their own maps: Treasure Island (theme 6: fishing, Soff, Treasure World) and LINE City (theme 36: the
// race and the Ride Point shop). Themes 28-31 (boss, race, pet park) are never listed: the client enters them from
// the Square and entry() sends them back.
func checkSquares(s *Squares) error {
	if err := s.CheckOrigin("mixed"); err != nil {
		return err
	}
	seen := map[uint64]bool{}
	island, city := false, false
	for _, sq := range s.Squares {
		switch {
		case seen[sq.Theme]:
			return fmt.Errorf("theme %d listed twice", sq.Theme)
		case sq.Name == "" || sq.ListName == "":
			return fmt.Errorf("theme %d: name and list_name must not be empty", sq.Theme)
		case sq.Map == "" || strings.ContainsAny(sq.Map, `/\.`):
			return fmt.Errorf("theme %d: map must be a map folder name", sq.Theme)
		case sq.MapSize == 0 || sq.Spawn.X >= sq.MapSize || sq.Spawn.Y >= sq.MapSize:
			return fmt.Errorf("theme %d: spawn %d,%d is outside the %d-tile map", sq.Theme, sq.Spawn.X, sq.Spawn.Y, sq.MapSize)
		case sq.Theme >= 28 && sq.Theme <= 31:
			return fmt.Errorf("theme %d is the boss or race/pet world: it is entered from the Square, not listed", sq.Theme)
		case sq.Icon != "" && (!strings.HasPrefix(sq.Icon, "animation/world_icon/") || strings.Contains(sq.Icon, "..")):
			return fmt.Errorf("theme %d: icon %q must be an animation/world_icon/ file", sq.Theme, sq.Icon)
		}
		seen[sq.Theme] = true
		switch sq.Theme {
		case squareThemeID:
			if sq.Map != squareMapFileName || !sq.Visible {
				return fmt.Errorf("theme %d (Treasure Island) must be visible on map %s", sq.Theme, squareMapFileName)
			}
			island = true
		case squareCityThemeID:
			if sq.Map != squareCityMapFileName || !sq.Visible {
				return fmt.Errorf("theme %d (LINE City) must be visible on map %s", sq.Theme, squareCityMapFileName)
			}
			city = true
		}
	}
	if !island {
		return errors.New("theme 6 (Treasure Island) must be listed")
	}
	if !city {
		return errors.New("theme 36 (LINE City) must be listed")
	}
	return nil
}

// listed returns the visible Squares in list order.
// SquareName is the name the operator file gives the Square with that theme, hidden ones included ("" for none). The staff
// Online page names a room with it.
func SquareName(theme uint64) string {
	for _, sq := range squaresFile.Get().Squares {
		if sq.Theme == theme {
			return sq.Name
		}
	}
	return ""
}

func (s *Squares) listed() []Square {
	out := []Square{}
	for _, sq := range s.Squares {
		if sq.Visible {
			out = append(out, sq)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Index < out[j].Index })
	return out
}

// entry returns the visible Square with that theme. The boss world (28) returns to Treasure Island and the race
// (31) to LINE City; any other theme (unknown or hidden) falls back to theme 6, Treasure Island, which the fallback
// has always used.
func (s *Squares) entry(theme uint64) Square {
	switch theme {
	case squareBossThemeID:
		theme = squareThemeID
	case squareRacingTheme:
		theme = squareCityThemeID
	}
	var island Square
	for _, sq := range s.Squares {
		if sq.Visible && sq.Theme == theme {
			return sq
		}
		if sq.Theme == squareThemeID {
			island = sq
		}
	}
	return island
}

// spawnPx is the game-px position of the spawn tile: X=40*(wx-wy+N), Y=20*(2N-1-wx-wy) with N the map
// size. Checked: City (93,80)->(6920,2920), petrace (89,97)->(6080,2660) (racing-spec T2).
// Runway's N=180 (its map size) is [I].
func (sq Square) spawnPx() (uint64, uint64) { return tilePx(sq.MapSize, sq.Spawn) }

// tilePx is tile t of a map n tiles per side in game px (the same conversion for Squares and the circle room).
func tilePx(n uint64, t Tile) (uint64, uint64) {
	return 40 * (t.X + n - t.Y), 20 * (2*n - 1 - t.X - t.Y)
}

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
}

// Tile is a map tile (wx, wy).
type Tile struct {
	X uint64 `json:"x"`
	Y uint64 `json:"y"`
}

var squaresFile = opdata.New("squares.json", data.SquaresJSON, checkSquares, nil)

// LoadOperatorData loads the game-connection operator data (squares.json) at startup.
func LoadOperatorData() error { return squaresFile.Load() }

// checkSquares: unique themes, names and map folder set, spawn inside the map, and LINE City (theme 6)
// listed on its own map: the fishing, boss, exchange, race and Ride Point NPCs are placed there.
func checkSquares(s *Squares) error {
	seen := map[uint64]bool{}
	city := false
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
		}
		seen[sq.Theme] = true
		if sq.Theme == squareThemeID {
			if sq.Map != squareMapFileName || !sq.Visible {
				return fmt.Errorf("theme %d (LINE City) must be visible on map %s", sq.Theme, squareMapFileName)
			}
			city = true
		}
	}
	if !city {
		return errors.New("theme 6 (LINE City) must be listed")
	}
	return nil
}

// listed returns the visible Squares in list order.
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

// entry returns the visible Square with that theme. Any other theme (unknown, hidden, or the boss world
// 28 and race 31 that return to the Square) goes to LINE City, as before the Square list existed.
func (s *Squares) entry(theme uint64) Square {
	var city Square
	for _, sq := range s.Squares {
		if sq.Visible && sq.Theme == theme {
			return sq
		}
		if sq.Theme == squareThemeID {
			city = sq
		}
	}
	return city
}

// spawnPx is the game-px position of the spawn tile: X=40*(wx-wy+N), Y=20*(2N-1-wx-wy) with N the map
// size. Checked: City (93,80)->(6920,2920), petrace (89,97)->(6080,2660) (racing-spec T2).
// Runway's N=180 (its map size) is [I].
func (sq Square) spawnPx() (uint64, uint64) {
	n := sq.MapSize
	return 40 * (sq.Spawn.X + n - sq.Spawn.Y), 20 * (2*n - 1 - sq.Spawn.X - sq.Spawn.Y)
}

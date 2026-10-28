package economy

import (
	"fmt"
	"image/color"
	"net/http"
	"slices"
	"strconv"

	"cherry/data"
	"cherry/internal/assets"
	"cherry/internal/httpx"
	"cherry/internal/opdata"
	"cherry/internal/store"
	"cherry/internal/thumbs"
)

// Garden maps (data/gardens.json). A Garden shows the map of its owner: the room-enter reply names it
// (lpn observer.go), so visitors see the owner's map. Every stem is a Garden pack the client downloads.

// Gardens is data/gardens.json: the Garden map list. The stems come from the client's Garden packs;
// the labels are Cherry-made.
//
//	default      the map stem a Garden shows when its owner has no override
//	gardens[]    {map, label, name, landscape, generated, fish}: the maps an owner may choose. map is the stem:
//	             digits only, unique in the file. label is a plain reading of it for the staff tool (2401290846
//	             is 2024-01-29 08:46).
//	             landscape is optional: the item code that switches a Garden to this map (landscape.go), unique
//	             in the file. A landscape needs a name, its shop and editor name.
//	             generated marks a landscape with no archive folder: Cherry serves its item files.
//	             fish is the map's fishing spot, optional: a map without it has no fish spot.
//	             sky is the backdrop of the map's generated landscape thumbnail: two colours ("#rrggbb") and whether
//	             the walls carry stars. Required for generated; optional otherwise. Cherry-made: the archive holds no
//	             sky, so these are hand-set colours (landscape-gen-20261026, v6 palettes).
//
// The default must be one of gardens[]. An owner's override is set with /admin/garden; an override that
// is no longer listed falls back to the default.
type Gardens struct {
	opdata.Provenance
	Default string        `json:"default"`
	Gardens []GardenEntry `json:"gardens"`
}

// GardenEntry is one map in the list.
type GardenEntry struct {
	Map       string      `json:"map"`
	Label     string      `json:"label"`
	Name      string      `json:"name,omitempty"`
	Landscape string      `json:"landscape,omitempty"`
	Generated bool        `json:"generated,omitempty"`
	Fish      *GardenFish `json:"fish,omitempty"`
	Sky       *GardenSky  `json:"sky,omitempty"`
}

// GardenSky is a map's backdrop (thumbs.Sky): the wall colour at the top and at the bottom edge, and whether the
// walls carry the map's star sprites (only the night, dusk and moonblue skies do in the v6 pictures).
type GardenSky struct {
	Top    string `json:"top"`
	Bottom string `json:"bottom"`
	Stars  bool   `json:"stars,omitempty"`
}

// thumbSky parses the two colours of s; the error names the value that is not "#rrggbb".
func (s GardenSky) thumbSky() (thumbs.Sky, error) {
	top, err := parseSkyColour(s.Top)
	if err != nil {
		return thumbs.Sky{}, err
	}
	bottom, err := parseSkyColour(s.Bottom)
	if err != nil {
		return thumbs.Sky{}, err
	}
	return thumbs.Sky{Top: top, Bottom: bottom, Stars: s.Stars}, nil
}

// parseSkyColour reads "#rrggbb" (six hex digits, either case) as an opaque colour.
func parseSkyColour(s string) (color.NRGBA, error) {
	if len(s) != 7 || s[0] != '#' {
		return color.NRGBA{}, fmt.Errorf("colour %q must be #rrggbb", s)
	}
	v, err := strconv.ParseUint(s[1:], 16, 32)
	if err != nil {
		return color.NRGBA{}, fmt.Errorf("colour %q must be #rrggbb", s)
	}
	return color.NRGBA{R: uint8(v >> 16), G: uint8(v >> 8), B: uint8(v), A: 0xff}, nil
}

// GardenFish is a map's fish spot: the map object the fish spot links to (host, its item code), by
// that object's dword@0 (idx, the high half of its serial) and its tile (x, y). The object must already
// be on the map: the client links the spot to it and does not place a copy (lpn garden_gather.go).
type GardenFish struct {
	Host string `json:"host"`
	Idx  uint64 `json:"idx"`
	X    int    `json:"x"`
	Y    int    `json:"y"`
}

var gardensFile = opdata.New("gardens.json", data.GardensJSON, checkGardens, nil)

// checkGardens: every map is digits only and listed once, a landscape is a landscape item code listed
// once and named, generated needs a landscape, a fish spot names a host, a dword@0 above 0 and a tile on
// the map, and the default is one of them.
func checkGardens(g *Gardens) error {
	if err := g.CheckOrigin("mixed"); err != nil {
		return err
	}
	seen := map[string]bool{}
	landscapes := map[string]bool{}
	for i, e := range g.Gardens {
		if _, err := strconv.ParseUint(e.Map, 10, 64); err != nil {
			return fmt.Errorf("gardens[%d]: map %q must be digits", i+1, e.Map)
		}
		if seen[e.Map] {
			return fmt.Errorf("gardens[%d]: map %s is listed twice", i+1, e.Map)
		}
		seen[e.Map] = true
		if e.Landscape != "" {
			if _, ok := landscapeItemID(e.Landscape); !ok {
				return fmt.Errorf("gardens[%d]: landscape %q must be a GULS item code (GULS and five base-36 digits)", i+1, e.Landscape)
			}
			if landscapes[e.Landscape] {
				return fmt.Errorf("gardens[%d]: landscape %s is listed twice", i+1, e.Landscape)
			}
			landscapes[e.Landscape] = true
			if e.Name == "" {
				return fmt.Errorf("gardens[%d]: landscape %s needs a name", i+1, e.Landscape)
			}
		}
		if e.Generated && e.Landscape == "" {
			return fmt.Errorf("gardens[%d]: generated needs a landscape", i+1)
		}
		if e.Generated && e.Sky == nil {
			return fmt.Errorf("gardens[%d]: map %s: generated needs a sky", i+1, e.Map)
		}
		if e.Sky != nil {
			if _, err := e.Sky.thumbSky(); err != nil {
				return fmt.Errorf("gardens[%d]: map %s: sky %v", i+1, e.Map, err)
			}
		}
		// A Cherry-made landscape takes an id from Cherry's own block and an original never does, so the two
		// can never be confused (assets.IsCherryItem; the asset uploader keeps that block out of static/).
		if id, _ := landscapeItemID(e.Landscape); e.Landscape != "" && e.Generated != assets.IsCherryItem(id) {
			return fmt.Errorf("gardens[%d]: landscape %s: generated items use ids %d..%d, originals never do", i+1, e.Landscape, assets.CherryItemFirst, assets.CherryItemLast)
		}
		if f := e.Fish; f != nil && (f.Host == "" || f.Idx == 0 || f.X < 0 || f.X > 59 || f.Y < 0 || f.Y > 59) {
			return fmt.Errorf("gardens[%d]: fish needs a host, an idx above 0 and a tile in 0..59", i+1)
		}
	}
	if !seen[g.Default] {
		return fmt.Errorf("default %q is not in gardens", g.Default)
	}
	return nil
}

// gardenSky is the backdrop of map stem for its generated landscape thumbnail; an error when the map has none.
func gardenSky(stem string) (thumbs.Sky, error) {
	g := gardensFile.Get()
	i := slices.IndexFunc(g.Gardens, func(e GardenEntry) bool { return e.Map == stem })
	if i < 0 || g.Gardens[i].Sky == nil {
		return thumbs.Sky{}, fmt.Errorf("garden map %s has no sky", stem)
	}
	return g.Gardens[i].Sky.thumbSky()
}

// GardenFishSpot is the fish spot of map stem; ok is false when the map has none (or is not listed).
func GardenFishSpot(stem string) (fish GardenFish, ok bool) {
	g := gardensFile.Get()
	i := slices.IndexFunc(g.Gardens, func(e GardenEntry) bool { return e.Map == stem })
	if i < 0 || g.Gardens[i].Fish == nil {
		return GardenFish{}, false
	}
	return *g.Gardens[i].Fish, true
}

// DefaultGardenMap is the map stem a Garden shows without an override.
func DefaultGardenMap() string { return gardensFile.Get().Default }

// GardenMap is the map stem the Garden of ownerAid shows: the owner's override while it is still listed,
// else the default.
func GardenMap(ownerAid string) string {
	store.AccountsMu.Lock()
	defer store.AccountsMu.Unlock()
	return gardenMapLocked(store.AccountByAidLocked(ownerAid))
}

// gardenMapLocked is the map acc's Garden shows; acc may be nil. The caller holds AccountsMu.
func gardenMapLocked(acc *store.Account) string {
	g := gardensFile.Get()
	if acc != nil && gardenListed(g, acc.GardenMap) {
		return acc.GardenMap
	}
	return g.Default
}

// gardenListed reports whether stem is one of the maps in g (an empty stem never is).
func gardenListed(g *Gardens, stem string) bool {
	return slices.ContainsFunc(g.Gardens, func(e GardenEntry) bool { return e.Map == stem })
}

// POST /admin/garden sets the Garden map an owner shows. map "" clears the override, so the default
// applies again. A map that is not in gardens.json, and an unknown aid, are refused (400):
//
//	curl -X POST http://127.0.0.1:8099/admin/garden -d "{\"aid\":\"1001\",\"map\":\"1908231803\"}"
//	curl -X POST http://127.0.0.1:8099/admin/garden -d "{\"aid\":\"1001\",\"map\":\"\"}"
//
// The reply is the map the Garden shows now: {"aid":..., "map":...}.
func handleAdminGarden(w http.ResponseWriter, r *http.Request) {
	if !adminLoopback(r) {
		httpx.ServeNotFound(w)
		return
	}
	var req struct {
		Aid string `json:"aid"`
		Map string `json:"map"`
	}
	if !readAdminJSON(r, &req) || (req.Map != "" && !gardenListed(gardensFile.Get(), req.Map)) {
		httpx.WriteJSON(w, http.StatusBadRequest, httpx.BadRequestBody)
		return
	}
	store.AccountsMu.Lock()
	defer store.AccountsMu.Unlock()
	acc := store.AccountByAidLocked(req.Aid)
	if acc == nil {
		httpx.WriteJSON(w, http.StatusBadRequest, httpx.BadRequestBody)
		return
	}
	prev := acc.GardenMap
	acc.GardenMap = req.Map
	if err := store.SaveAccountsLocked(); err != nil {
		acc.GardenMap = prev
		httpx.WriteJSON(w, http.StatusInternalServerError, `{"errorCode":"500"}`)
		return
	}
	httpx.WriteObj(w, map[string]any{"aid": acc.Aid, "map": gardenMapLocked(acc)})
}

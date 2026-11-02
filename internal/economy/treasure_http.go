package economy

import (
	"io/fs"
	"net/http"
	"path"
	"strings"

	"cherry/internal/assets"
	"cherry/internal/httpx"
)

// Treasure World money answers (transport-money.md section 3.4 and 7.3). Nothing is sold: the treasure pass is not
// offered, and the special pack list is empty. The spot routes (bill/prod/spot/gem, cash, heart) are shared with the
// generic lack-of-money popup and are answered by RegisterEconomyRoutes with an empty list; they are not repeated here.

// treasureIconBody: ResBlueMarblePakageAvailable reads result.available; false leaves the board where it is.
const treasureIconBody = `{"result":{"available":false}}`

// treasurePackageInfoBody: the special pack list is empty (ResBlueMarbleSpecialPackList is a no-op on success).
const treasurePackageInfoBody = `{"result":{}}`

// TreasureSeasonArt maps a SeasonInfo picture field to the original file it shows, relative to the device files/ tree
// (roll-gate.md section 3). The client never loads season paths from its own files: it downloads every one from the
// static base (device 2026-11-02, all 404 under UIResource/), so Cherry serves the originals, read from the store like the butler portraits, at /cherry/treasure/<base name>. The season's own files (marble_main_*,
// marble_common_reward_*, 18 in all) are 1-bit transparent placeholders in both backups — the real ones were season
// downloads — so the fields take the original season-independent art instead: the board's round back button and the
// ticket, jewel and point nodes of animation/marble_ef. No original exists for SkinUrl (1) or BgImgUrl (20); the QUEST,
// help and ranking board pictures are Cherry-drawn (treasureDrawnFields).
var TreasureSeasonArt = map[uint64]string{
	14: "animation/marble_ef/point_node.png",               // RankingPointOneUrl (tag 94)
	16: "animation/marble_ef/ticket_node.png",              // TicketOneUrl (tags 91, 80)
	18: "animation/marble_ef/coin_node.png",                // CoinOneUrl (tag 90): the jewel
	21: "UIResource/UIImage_hd/marble_common_btn_back.png", // BackButtonUrl (tag 2)
}

// treasureArtFS is the original device files/ tree the season art is read from (the store, assets.DeviceFiles).
var treasureArtFS fs.FS = assets.DeviceFiles("")

// treasureArtPrefix is the route; the season paths are the same without the leading slash (the client prepends its
// static base and refuses nothing else, treasure-spec 9.2).
const treasureArtPrefix = "/cherry/treasure/"

// TreasureArtPath is the SeasonInfo path of an original art file: relative to the static base, no leading slash.
func TreasureArtPath(file string) string {
	return strings.TrimPrefix(treasureArtPrefix, "/") + path.Base(file)
}

// TreasureSeasonPicture is the SeasonInfo path of a picture field: the original art or a Cherry-drawn button, relative to
// the static base; "" for a field without art (the client draws nothing for an empty path).
func TreasureSeasonPicture(field uint64) string {
	if file, ok := TreasureSeasonArt[field]; ok {
		return TreasureArtPath(file)
	}
	if name, ok := treasureDrawnFields[field]; ok {
		return strings.TrimPrefix(treasureArtPrefix, "/") + name
	}
	return ""
}

// TreasureBoardPicture is the SeasonInfo path of the board picture art.board names in d (SkinUrl): relative to the static base,
// no leading slash; "" for a blank name. The v1 board's path carries the crc of the ring and the rates.
func TreasureBoardPicture(d *TreasureData) string {
	if f := treasureBoardFile(d); f != "" {
		return TreasureArtPath(f)
	}
	return ""
}

// TreasureBackgroundPicture is the SeasonInfo path of the background art.background names in d (BgImgUrl); "" for a blank name.
func TreasureBackgroundPicture(d *TreasureData) string {
	if f := treasureBgFile(d); f != "" {
		return TreasureArtPath(f)
	}
	return ""
}

// HandleTreasureArt serves GET /cherry/treasure/<name>: a Cherry-drawn picture, a season picture, or an original file the season
// art names.
func HandleTreasureArt(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, treasureArtPrefix)
	if r.Method != http.MethodGet {
		httpx.ServeNotFound(w)
		return
	}
	b, ok := treasureDrawnPicture(name)
	if !ok {
		b, ok = treasureSeasonPicture(name)
	}
	if ok {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(b)
		return
	}
	file := ""
	for _, f := range TreasureSeasonArt {
		if path.Base(f) == name {
			file = f
		}
	}
	if file == "" {
		httpx.ServeNotFound(w)
		return
	}
	b, err := fs.ReadFile(treasureArtFS, file)
	if err != nil {
		httpx.ServeNotFound(w)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	_, _ = w.Write(b)
}

// RegisterTreasureRoutes serves the two Treasure World billing answers above and the season art.
func RegisterTreasureRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/v4/bill/prod/treasure/icon", httpx.HandleJSONBody(treasureIconBody))
	mux.HandleFunc("/v4/bill/package2/bluemarble/info", httpx.HandleJSONBody(treasurePackageInfoBody))
	mux.HandleFunc(treasureArtPrefix, HandleTreasureArt)
}

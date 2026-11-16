package economy

import (
	"io/fs"
	"maps"
	"net/http"
	"path"
	"slices"
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
// static base (device 2026-11-02, all 404 under UIResource/), so Cherry serves the originals, read from the store like the
// butler portraits, at /cherry/treasure/<base name>. They are the server's season pictures as a device cached them
// (cache/season_master/ of the robo iOS donation, 2026-11-16; the field names match the file names); the board (SkinUrl,
// 1) and the background (BgImgUrl, 20) follow the data's art names (treasureBoardFile, treasureBgFile).
var TreasureSeasonArt = map[uint64]string{
	2:  treasureSeasonDir + "ranking_board_1598508720.png",      // RankingBoardUrl (tag 108)
	4:  treasureSeasonDir + "main_banner_nomal_1674008302.png",  // MainBannerNomalUrl
	5:  treasureSeasonDir + "main_banner_reward_1674008302.png", // MainBannerRewardUrl
	14: treasureSeasonDir + "ranking_point_one_1577958258.png",  // RankingPointOneUrl (tag 94)
	15: treasureSeasonDir + "ranking_point_two_1577958258.png",  // RankingPointTwoUrl
	16: treasureSeasonDir + "ticket_one_1598508720.png",         // TicketOneUrl (tags 91, 80)
	17: treasureSeasonDir + "ticket_two_1598508720.png",         // TicketTwoUrl
	18: treasureSeasonDir + "coin_one_1598508720.png",           // CoinOneUrl (tag 90): the jewel
	19: treasureSeasonDir + "coin_two_1598508720.png",           // CoinTwoUrl
	21: treasureSeasonDir + "back_button_1598508720.png",        // BackButtonUrl (tag 2)
	22: treasureSeasonDir + "quest_button_1598508720.png",       // QuestButtonUrl (tag 7)
	25: treasureSeasonDir + "helper_button_1598508720.png",      // HelperButtonUrl (tag 111)
}

// treasureSeasonDir is where the season pictures sit in the device files/ tree (R2 static/cache/season_master/).
const treasureSeasonDir = "cache/season_master/"

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

// HandleTreasureArt serves GET /cherry/treasure/<name>: a Cherry-drawn board or background, or an original file the season
// art names.
func HandleTreasureArt(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, treasureArtPrefix)
	if r.Method != http.MethodGet {
		httpx.ServeNotFound(w)
		return
	}
	if b, ok := treasureSeasonPicture(name); ok {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(b)
		return
	}
	file := ""
	for _, f := range append(slices.Collect(maps.Values(TreasureSeasonArt)), treasureBoardOriginal, treasureBgOriginal) {
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

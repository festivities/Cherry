package economy

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"cherry/internal/httpx"
)

// Economy Phase E: the Garden's floating event icon. The Garden asks GET /v4/home/integrated/info when it loads
// (NaDispatcherCommon::ResGardenEventTotalInfo 0x1b7be8c). result.home_icon must be an array: each row is read by
// sHomeEventIconData::Parse 0x1aee0dc (iconSeq asInt; iconId, iconName, linkTypeCode, linkDetail asString). A tap
// maps linkTypeCode, a link name, through GetLinkType and SceneChangeByLinkType. The picture is icon_effect/<iconId>
// (NaGardenEventManager::ConvertHomeEventIconFullAnimationKey 0x1f45f94): a symbol of the original
// animation/icon_effect.aniproj, which the client preloads. The icon shows only while its season is in force, so
// /admin/season forces it too.

// seasonIconJSON is calendars.json seasons[].icon.
//
//	id        the picture: a symbol name of animation/icon_effect.aniproj. A plain name; a "/" would name another project.
//	name      iconName
//	link      goSomewhere(<name>) with <name> in popupLinks: native scenes only (html and event are WebViews, refused)
//	detail    linkDetail, "" for the native scenes
type seasonIconJSON struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Link   string `json:"link"`
	Detail string `json:"detail"`
}

// homeEventIconRow is one result.home_icon row (sHomeEventIconData).
type homeEventIconRow struct {
	IconSeq      int    `json:"iconSeq"`
	IconID       string `json:"iconId"`
	IconName     string `json:"iconName"`
	LinkTypeCode string `json:"linkTypeCode"`
	LinkDetail   string `json:"linkDetail"`
}

// checkSeasonIcon validates a season's Garden icon at startup; each refusal names the season.
func checkSeasonIcon(season string, ic *seasonIconJSON) error {
	switch {
	case ic.ID == "" || strings.Contains(ic.ID, "/"):
		return fmt.Errorf("season %s: icon id %q must be a plain icon_effect symbol name", season, ic.ID)
	case ic.Name == "":
		return fmt.Errorf("season %s: icon name must not be empty", season)
	case !isNativeLink(ic.Link):
		return fmt.Errorf("season %s: icon link %q is not a native scene (goSomewhere(<name>) with <name> in %s)", season, ic.Link, strings.Join(popupLinks, ", "))
	}
	return nil
}

// HandleHomeIntegratedInfo serves GET /v4/home/integrated/info (exact path): one home_icon row while a season with
// an icon is in force, an empty list otherwise. result.bill_prod_icon is the unavailable package body (economy.go).
// result.gacha_target_icon is left out: the parser reads it only when it is non-empty (Json::Value::empty is true for
// a missing member), so no gacha target icon is shown.
func HandleHomeIntegratedInfo(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpx.ServeNotFound(w)
		return
	}
	rows := []homeEventIconRow{}
	if ic := scheduleAt(NowFunc()).Icon; ic != nil {
		rows = append(rows, homeEventIconRow{IconSeq: 1, IconID: ic.ID, IconName: ic.Name, LinkTypeCode: ic.Link, LinkDetail: ic.Detail})
	}
	icons, _ := json.Marshal(rows) // strings and ints only: cannot fail
	httpx.WriteJSON(w, http.StatusOK, `{"result":{"bill_prod_icon":`+unavailableHomeIcons+`,"home_icon":`+string(icons)+`}}`)
}

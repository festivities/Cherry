package web

import (
	"net/http"
	"strconv"
	"strings"

	"cherry/internal/httpx"
	"cherry/internal/lpn"
	"cherry/internal/store"
)

// Replies behind the home menu's More Games, More Shop and More Events popups, the Invitation tab, the Settings
// time-machine count and the friend session status. Keys and JSON types follow the native parsers
// (ResGameMenuList 0x1b40bc4, ResShopMenuList 0x1c1383c, ResEventMenuList 0x1c14310, ResInviteFriendLinkUrl
// 0x1a585a8, ResTimemachineGachaMagicPowderCount 0x1b2a01c, ResFriendSessionStatusWithAIDs 0x1b5d438); see
// rows-c-spec.md (audit 2026-10-28).

// inviteInfoBody leaves inviteUrl empty. NaFriendsBestFriendInviteCell::CButtonHandler (0x1ce06e4, tag 29) then shows
// "Invite Url is Empty" instead of sharing through LINE (PfSNSInviteBridge::InviteLineMessage). Cherry serves no
// invite link; choose one before the Invite button can share.
const inviteInfoBody = `{"result":{"inviteUrl":""}}`

const timemagicCountBody = `{"result":{"magicPowderCount":0,"stampCount":0}}`

// moreEventBody has no event rows. An event row would open a WebView on the NAVER event host, which is sunk here.
const moreEventBody = `{"result":[]}`

// roomPartyGameName is the More Games name of lp_rmchat. No home menu row carries that label.
const roomPartyGameName = "Room Party"

// moreShopLinks are the shops More Shop opens. Each is also a home menu row, entered with an empty link and extra
// as the home rows enter it. These five are the shops verified working in the lab.
var moreShopLinks = []string{
	"goSomewhere(interiorShop)",
	"goSomewhere(fashionShop)",
	"goSomewhere(minipetShop)",
	"goSomewhere(faceShop)",
	"goSomewhere(gachaShop)",
}

// moreGame is one More Games row. id is a JSON string: the client hashes it and sends it back as the playhome/games
// link. fileSize is a JSON number. Pictures and descriptions stay empty.
type moreGame struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Image      string `json:"image"`
	Flag       string `json:"flag"`
	GameType   string `json:"gameType"`
	Desc       string `json:"desc"`
	DispPeriod string `json:"dispPeriod"`
	FileSize   int    `json:"fileSize"`
}

// moreShop is one More Shop row. id is a JSON number. nMarkTimestamp is the extra argument of the row's link; it stays
// empty, as the home rows send an empty extra.
type moreShop struct {
	ID             int    `json:"id"`
	Name           string `json:"name"`
	Image          string `json:"image"`
	Flag           string `json:"flag"`
	LinkType       string `json:"linkType"`
	Link           string `json:"link"`
	NMarkTimestamp string `json:"nMarkTimestamp"`
	NMark          bool   `json:"nMark"`
	Delimiter      bool   `json:"delimiter"`
	Desc           string `json:"desc"`
}

// moreGameRows lists the games Cherry serves. Square takes its label from the home menu; the game id is the link.
func moreGameRows() []moreGame {
	labels := menuFile.Get().Labels
	return []moreGame{
		{ID: "lp_sq", Name: labels["goSomewhere(square)"], GameType: "gamelink"},
		{ID: "lp_rmchat", Name: roomPartyGameName, GameType: "gamelink"},
	}
}

// handleMoreGameList serves GET /v4/home/sublist/game/Android (the builder has no version or .nhn segment).
func handleMoreGameList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpx.ServeNotFound(w)
		return
	}
	httpx.WriteObj(w, struct {
		Result []moreGame `json:"result"`
	}{moreGameRows()})
}

// handleMoreShopList serves GET /v4/home/sublist/shop/10.1.0.0/Android.nhn?parentIconId= with the labels of the home rows.
func handleMoreShopList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpx.ServeNotFound(w)
		return
	}
	labels := menuFile.Get().Labels
	rows := make([]moreShop, len(moreShopLinks))
	for i, link := range moreShopLinks {
		rows[i] = moreShop{ID: i + 1, Name: labels[link], LinkType: link}
	}
	httpx.WriteObj(w, struct {
		Result []moreShop `json:"result"`
	}{rows})
}

// handleMoreEventList serves GET /v4/noti/event/<lang>/10.1.0.0/Android.nhn with no rows.
func handleMoreEventList(w http.ResponseWriter, r *http.Request) {
	lang, ok := strings.CutSuffix(strings.TrimPrefix(r.URL.Path, "/v4/noti/event/"), "/10.1.0.0/Android.nhn")
	if r.Method != http.MethodGet || !ok || lang == "" || strings.Contains(lang, "/") {
		httpx.ServeNotFound(w)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, moreEventBody)
}

// handleSessionStatus serves POST /v4/session/status. The body is a bare JSON array of avatar id strings (chunks of
// at most 200). The reply echoes each id in order; online = a live :10123 session (lpn.SessionOnline), busy is
// always false.
func handleSessionStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpx.ServeNotFound(w)
		return
	}
	raw, err := httpx.ReadNativeBody(r)
	var aids []string
	if err == nil {
		_, err = httpx.UnmarshalNativeJSON(raw, &aids)
	}
	if err != nil || aids == nil {
		httpx.WriteJSON(w, http.StatusBadRequest, httpx.BadRequestBody)
		return
	}
	type status struct {
		AvatarID string `json:"avatarId"`
		Busy     bool   `json:"busy"`
		Online   bool   `json:"online"`
	}
	result := make([]status, 0, len(aids))
	for _, aid := range aids {
		if !store.ValidAvatarID(aid) {
			httpx.WriteJSON(w, http.StatusBadRequest, httpx.BadRequestBody)
			return
		}
		n, _ := strconv.ParseUint(aid, 10, 64)
		result = append(result, status{AvatarID: aid, Online: lpn.SessionOnline(n)})
	}
	httpx.WriteObj(w, struct {
		Result []status `json:"result"`
	}{result})
}

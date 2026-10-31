// Package web is the HTTP layer: the mux, boot/patcher and guest/session routes,
// avatar creation and Closet, Diary/guestbook/friends, OBS media and Delete Avatar.
package web

import (
	"compress/gzip"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"log"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"cherry/data"
	"cherry/internal/assets"
	"cherry/internal/economy"
	"cherry/internal/httpx"
	"cherry/internal/itemcode"
	"cherry/internal/lpn"
	"cherry/internal/opdata"
	"cherry/internal/room"
	"cherry/internal/store"
	"cherry/internal/thumbs"
)

const setInitConfBody = `{"result":{"sessionServerInfo":"XPN:/p=XTCP;ip=session.play.naver.jp;port=10123","staticDomain":"https://play-static.line-scdn.net/","nationCode":"JP","isGdprNation":false,"snsLoginUIList":["LD_GUEST"],"snsSignUpUIList":["LD_GUEST"]}}`

// checkResource2Body is the checkresource2.json served when streaming is off. ch is the chat action icon version
// (arts_chatting_<ch>/ and cache/action/<ch>_...), read raw by the client, not a patcher category: "00005" names
// the santi icon files. Keep it a JSON string.
const checkResource2Body = `{"adr":{"10.1.0.0":"1"},"resource":{"1":{"animation":"531","he":"0","ch":"00005","sound":"530","subui":"0","tx":"543","squareui":"0","ui":"540"}}}`

const splashSkipBody = `{"result":{"update":false,"resourceID":-1,"resourceImageCount":0,"resourceImages":[]}}`

const emptyUpdateIniBody = `[]`

const tx543UpdateIniBody = `[start=1,end=1,size=454656,/arts_strings.ast:version=1492F278EC281078AC7F35479A85F197]`

const skinFileIniBody = `[empty]`

const eventFlagListBody = `{"result":[]}`

const snsTermsBody = `{"result":true}`

const termAllBody = `{"result":{"1":true}}`

const profileBody = `{"result":{"name":"guest","newbie":true}}`

// settingAllBody: notiConfig.onlineNotiYn must be true, or the client drops every session push silently
// (GetIsEnableOnlinePush 0x1933b84 in IsValidShowPushBannerPopup 0x20eb178; boss-mp-addendum 7), e.g. a boss invite.
const settingAllBody = `{"result":{"notiFlag":false,"changeCountry":false,"countryName":"","soundConfig":false,"notiConfig":{"onlineNotiYn":true,"ingameNotiYn":true,"squareInvtNotiYn":true},"privacyConfig":{},"roomSize":{"max":0,"cur":0}}}`

const friendLineBuddyBody = `{"result":{"nextCursor":"0","buddyList":[],"bookmarks":[]}}`

const (
	inviteMissionBody = `{"result":{"inviteCount":0,"achievementList":[]}}`
	inviteListBody    = `{"result":{"items":[]}}`
	badgeSetAckBody   = `{"result":{}}`
)

const badgeInfosBody = `{"result":{"NEWS":0,"CHAT":0,"NFRD":0,"IFRD":0,"GIFT":0,"POSTBOX":0,"CSET":0,"FACE":0,"ROOM":0,"ALERT":0}}`

const friendBrandBuddyBody = `{"result":[]}`

// homeLinks is the home menu in display order. The link type selects the client's screen and the row
// id is its position, so both stay in code; each row's label is operator data (data/menu.json). Link types
// that cannot work, are not implemented yet, or are real money/ads were removed on 2026-10-28 (user decision);
// PLAN "Home menu" -> "Removed MENU rows" lists each with its reason.
var homeLinks = []string{
	"goSomewhere(closet)",
	"goSomewhere(myfriends)",
	"goSomewhere(diary)",
	"goSomewhere(myRoom)",
	"goSomewhere(otherRoom)",
	"goSomewhere(interiorShop)",
	"goSomewhere(fashionShop)",
	"goSomewhere(gachaShop)",
	"goSomewhere(giftShop)",
	"goSomewhere(modelhouse)",
	"goSomewhere(faceShop)",
	"goSomewhere(profileedit)",
	"goSomewhere(addfriends)",
	"goSomewhere(invitation)",
	"goSomewhere(roomEdit)",
	"goSomewhere(setting)",
	"goSomewhere(square)",
	"goSomewhere(postbox)",
	"goSomewhere(randomRoom)",
	"goSomewhere(subMoreShop)",
	"goSomewhere(subMoreGacha)",
	"goSomewhere(subMoreEvent)",
	"goSomewhere(subMoreGame)",
	"goSomewhere(gachaCatg)",
	"goSomewhere(gachaCatgNotap)",
	"goSomewhere(gachaDetail)",
	"goSomewhere(gachaTop)",
	"goSomewhere(gachaNew)",
	"goSomewhere(minipetShop)",
	"goSomewhere(minipetBook)",
	"goSomewhere(viplounge)",
	"goSomewhere(remodelingShop)",
	"goSomewhere(quest)",
	"goSomewhere(attendance)",
	"goSomewhere(roompartyInvite)",
	"goSomewhere(cherrytip)",
	"goSomewhere(recommendPostList)",
	"goSomewhere(gachaMain)",
	"goSomewhere(gachaSearch)",
	"goSomewhere(gardenedit)",
	"goSomewhere(gardeneditcloud)",
	"goSomewhere(bag)",
	"goSomewhere(factory)",
	"goSomewhere(randomGarden)",
	"goSomewhere(butlerShop)",
	"goSomewhere(butlerBook)",
	"goSomewhere(butlerSkinSelect)",
	"goSomewhere(colorantShop)",
	"goSomewhere(colorantInven)",
	"goSomewhere(luckyspin)",
	"goSomewhere(jackpotspin)",
	"goSomewhere(loungeChat)",
}

// homeMenuIconPrefix is the picture path of a home menu row. It is RELATIVE on purpose: the client's
// ReqDownloadImage (NaDispatcherDownload 0x1b81360) fetches any URL without http(s):// from the static
// base (staticResourceURLWithPath), like the fishing pictures.
const homeMenuIconPrefix = "cherry/menu/"

// Menu is data/menu.json: the home menu labels and pictures, keyed by the link type of each row in homeLinks.
//
//	labels  {link type: name}: one non-empty name per home menu row; a key that no row uses is refused
//	icons   {link type: file}: optional; a file name in data/menuicons (a row without one keeps the placeholder)
type Menu struct {
	opdata.Provenance
	Labels map[string]string `json:"labels"`
	Icons  map[string]string `json:"icons"`
}

var menuFile = opdata.New("menu.json", data.MenuJSON, checkMenu, nil)

// streaming serves the game assets from R2 (internal/assets); nil while streaming is off.
var streaming *assets.Store

// LoadOperatorData loads the web layer's operator data files (menu.json, dressup.json) at startup, and
// opens the asset stream when streaming.json enables it.
func LoadOperatorData() error {
	for _, load := range []func() error{menuFile.Load, dressupFile.Load, assets.LoadItemConv} {
		if err := load(); err != nil {
			return err
		}
	}
	s, err := assets.Open()
	if err != nil {
		return err
	}
	streaming = s
	if err := s.FetchItemIndex(); err != nil {
		log.Printf("cherry: converted items index: %v", err)
	}
	return nil
}

func checkMenu(m *Menu) error {
	if err := m.CheckOrigin("mixed"); err != nil {
		return err
	}
	for _, link := range slices.Sorted(maps.Keys(m.Labels)) {
		if !slices.Contains(homeLinks, link) {
			return fmt.Errorf("label %q: no home menu row has that link type", link)
		}
	}
	for _, link := range homeLinks {
		name, ok := m.Labels[link]
		switch {
		case !ok:
			return fmt.Errorf("no label for the home menu row %s", link)
		case strings.TrimSpace(name) == "":
			return fmt.Errorf("label for %s must not be empty", link)
		}
	}
	for _, link := range slices.Sorted(maps.Keys(m.Icons)) {
		if !slices.Contains(homeLinks, link) {
			return fmt.Errorf("icon %q: no home menu row has that link type", link)
		}
		if _, err := fs.ReadFile(data.MenuIcons, "menuicons/"+m.Icons[link]); err != nil {
			return fmt.Errorf("icon for %s: %q is not a picture in data/menuicons", link, m.Icons[link])
		}
	}
	return nil
}

type homeIconRow struct {
	Name     string
	LinkType string
	Image    string // the picture path, "" when the row has none
}

// homeIconRows pairs each row with its label and picture in the menu file in use.
func homeIconRows() []homeIconRow {
	menu := menuFile.Get()
	rows := make([]homeIconRow, len(homeLinks))
	for i, link := range homeLinks {
		rows[i] = homeIconRow{Name: menu.Labels[link], LinkType: link}
		if file, ok := menu.Icons[link]; ok {
			rows[i].Image = homeMenuIconPrefix + file
		}
	}
	return rows
}

type homeIconEntry struct {
	ID             int    `json:"id"`
	Name           string `json:"name"`
	Image          string `json:"image"`
	Flag           string `json:"flag"`
	Link           string `json:"link"`
	NMarkTimestamp string `json:"nMarkTimestamp"`
	NMark          bool   `json:"nMark"`
	Delimiter      bool   `json:"delimiter"`
	LinkType       string `json:"linkType"`
	ShowMeOnly     bool   `json:"showMeOnly"`
}

type homeListExtResult struct {
	HomeIconList  []homeIconEntry `json:"homeIconList"`
	EventIconList []homeIconEntry `json:"eventIconList"`
}

func buildHomeListExtBody() string {
	rows := homeIconRows()
	icons := make([]homeIconEntry, len(rows))
	for i, row := range rows {
		icons[i] = homeIconEntry{ID: i + 1, Name: row.Name, Image: row.Image, LinkType: row.LinkType}
	}
	payload, _ := json.Marshal(struct {
		Result homeListExtResult `json:"result"`
	}{Result: homeListExtResult{HomeIconList: icons, EventIconList: []homeIconEntry{}}})
	return string(payload)
}

const storageDisplayBody = `{"result":false}`

const styleSlotListBody = `{"result":{"styleSlotResponses":[]}}`

// Empty questProgress maps to enum 0 (no active quest), avoiding the client's per-frame status retry.
const questStatusBody = `{"result":{"heart":0,"heartBase":0,"heartRewardCoin":0,"questProgress":"","toExpire":"0"}}`

// Parser-valid empty inventory counts; ResGetInventoryItemCountInfo @0x1b2afe0 defaults every field.
const invenCountsBody = `{"result":{}}`

const playDetailLPRmchatBody = `{"result":{"gameInfo":{"gameId":"lp_rmchat","executable":true,"underMaintenance":false,"minLinePlayVersion":"","startDate":"1577836800000","endDate":"4102444800000"}}}`

// ResPlayDetailWithGameID @0x1a84e5c needs a nonempty gameInfo object; dates are ms strings.
const playDetailLPSquareBody = `{"result":{"gameInfo":{"gameId":"lp_sq","executable":true,"underMaintenance":false,"minLinePlayVersion":"","startDate":"1577836800000","endDate":"4102444800000"}}}`

const artsStringsMD5 = "1492F278EC281078AC7F35479A85F197"

// artsStringsPath is the stock string table, read in place from the read-only archive
// (game assets are never committed). It must match artsStringsMD5, which update.ini
// advertises to the patcher.
var artsStringsPath = filepath.FromSlash(`D:/Dev/projects/Cherry/.opencode/line-play-artifacts/santi-backup-20260921/jp.naver.lineplay.android/files/stringtable/arts_strings.ast`)

// loadArtsStrings reads and MD5-checks the string table once; nil if missing or changed.
var loadArtsStrings = sync.OnceValue(func() []byte {
	b, err := os.ReadFile(artsStringsPath)
	if err != nil {
		log.Printf("cherry: arts_strings.ast unavailable: %v", err)
		return nil
	}
	if sum := md5.Sum(b); !strings.EqualFold(hex.EncodeToString(sum[:]), artsStringsMD5) {
		log.Printf("cherry: arts_strings.ast md5 mismatch at %s", artsStringsPath)
		return nil
	}
	return b
})

// NewMux returns the routes. ServeMux redirects a path with a ".." segment to its cleaned form before any route
// runs, so a streamed map pack request in the un-normalised form is answered first.
func NewMux() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/v4/account/guest/generate", handleGuestGenerate)
	mux.HandleFunc("/v4/createSession", handleCreateSession)
	mux.HandleFunc("/v4/checkSession", handleCheckSession)
	mux.HandleFunc("/v4/create/avatar", handleCreateAvatar)
	mux.HandleFunc("/v4/create/all/items", handleCreateAllItems)
	mux.HandleFunc("/v4/create/face/setitems/roll", handleCreateFaceSetItemsRoll)
	mux.HandleFunc("/v2/create/face/analyze", handleCreateFaceAnalyze)
	mux.HandleFunc("/v4/create/complete", handleCreateComplete)
	mux.HandleFunc("/arts_session/sckey.enc", lpn.HandleSckeyEnc)
	mux.HandleFunc("/cherry/fish/", economy.HandleFishImage)
	mux.HandleFunc("/cherry/fishing/", economy.HandleFishingItemImage)
	mux.HandleFunc("/cherry/butler/", economy.HandleButlerImage)
	mux.HandleFunc("/cherry/spin/", economy.HandleSpinImage)
	mux.HandleFunc("/cherry/dye/", economy.HandleDyeImage)
	mux.HandleFunc("/cherry/menu/", handleMenuIcon)
	mux.HandleFunc("/cherry/garden/items/", economy.HandleGatherImage)
	mux.HandleFunc("/v4/setInitConf", handleSetInitConf)
	mux.HandleFunc("/v4/resource/splash/", handleSplash)
	mux.HandleFunc("/notice/adr/checkresource2.json", handleCheckResource2)
	mux.HandleFunc("/v4/popup/isExists", economy.HandlePopupIsExists)
	mux.HandleFunc("/v4/popup/list", economy.HandlePopupList)
	mux.HandleFunc("/cherry/event/", economy.HandleEventPopupImage)
	mux.HandleFunc("/v4/eventFlag/flagList", httpx.HandleJSONBody(eventFlagListBody))
	mux.HandleFunc("/v4/social/terms/sns", handleSnsTerms)
	mux.HandleFunc("/v4/setting/term/all", httpx.HandleJSONBody(termAllBody))
	mux.HandleFunc("/v4/setting/all", httpx.HandleJSONBody(settingAllBody))
	mux.HandleFunc("/v4/sync/friends/", handleFriendSync)
	mux.HandleFunc("/v4/buddy/list/type/0", handleFriendSync)
	mux.HandleFunc("/v4/square/friends/search", handleFriendSearch)
	mux.HandleFunc("/v4/line/buddy/v4/list", httpx.HandleJSONBody(friendLineBuddyBody))
	mux.HandleFunc("/v4/badge/infos/", httpx.HandleJSONBody(badgeInfosBody))
	mux.HandleFunc("/v4/friends/invitation/mission/info", httpx.HandleJSONBody(inviteMissionBody))
	mux.HandleFunc("/v4/friends/invitation/list", httpx.HandleJSONBody(inviteListBody))
	mux.HandleFunc("/v4/recommend/code", handleRecommendCode)
	mux.HandleFunc("/v4/badge/IFRD/", handleBadgeSetAck)
	mux.HandleFunc("/v4/friend/bookmark/", handleFriendBookmark)
	mux.HandleFunc("/v4/r/friend/remove/", handleFriendRemove)
	mux.HandleFunc("/v4/friend/status/", handleFriendStatus)
	mux.HandleFunc("/v4/friend/apply/", handleFriendApply)
	mux.HandleFunc("/v4/friend/accept/", handleFriendAccept)
	mux.HandleFunc("/v4/pin/list", handlePinList)
	mux.HandleFunc("/v4/pin/add/", handlePinAdd)
	mux.HandleFunc("/v4/pin/delete", handlePinDelete)
	mux.HandleFunc("/v4/diary2/ext/checkExist/", handleDiaryCheckExist)
	mux.HandleFunc("/v4/diary2/intro/", handleDiaryIntro)
	mux.HandleFunc("/v4/diary2/ext/write", handleDiaryWrite)
	mux.HandleFunc("/v4/achieve/write", handleAchieveWrite)
	mux.HandleFunc("/v4/diary2/ext/unfold/photo/", handleDiaryPhotoAlbum)
	mux.HandleFunc("/v4/diary2/ext/unfold/", handleDiaryUnfold)
	mux.HandleFunc("/v4/diary2/ext/look/", handleDiaryLook)
	mux.HandleFunc("/v4/diary2/erase/", handleDiaryErase)
	mux.HandleFunc("/v4/guestbook/write", handleGuestbookWrite)
	mux.HandleFunc("/v4/guestbook/erase/", handleGuestbookErase)
	mux.HandleFunc("/v4/guestbook3/list/", handleGuestbookList)
	mux.HandleFunc("/v4/guestbook3/count/", handleGuestbookCount)
	mux.HandleFunc("/v4/brand/list", httpx.HandleJSONBody(friendBrandBuddyBody))
	mux.HandleFunc("/v4/quest/status", handleQuestStatus)
	mux.HandleFunc("/v4/heart/", handleHeart)
	mux.HandleFunc("/v4/noti/heart/receive/list/", handleHeartSenderList)
	mux.HandleFunc("/v4/inven/counts", httpx.HandleJSONBody(invenCountsBody))
	mux.HandleFunc("/lineplay/d/upload.nhn", handleDiaryImageUpload)
	mux.HandleFunc("/lineplay/d/download.nhn", handleDiaryImageDownload)
	mux.HandleFunc("/lineplay/d/download.nhn/", handleDiaryImageDownload)
	mux.HandleFunc("/lineplay/r/upload.nhn", handleRoomImageUpload)
	mux.HandleFunc("/lineplay/r/download.nhn", handleRoomImageDownload)
	mux.HandleFunc("/lineplay/r/delete.nhn", handleRoomImageDelete)
	mux.HandleFunc("/lineplay/pr/upload.nhn", handleProfileImageUpload)
	mux.HandleFunc("/lineplay/pr/", handleProfileImageDownload)
	mux.HandleFunc("/r/lineplay/pr/", handleProfileImageDownload)
	mux.HandleFunc("/v4/avatar/profile/image", handleProfileImageSave)
	mux.HandleFunc("/v4/home/list/ext/", handleHomeListExt)
	mux.HandleFunc("/v4/inven/closet/items/all", handleClosetItemsAll)
	mux.HandleFunc("/v4/storage/display", httpx.HandleJSONBody(storageDisplayBody))
	mux.HandleFunc("/v4/style/slot/list", httpx.HandleJSONBody(styleSlotListBody))
	mux.HandleFunc("/v4/avatar/save/v2", handleAvatarSaveV2)
	mux.HandleFunc("/v4/photozone/shop/info/", economy.HandlePhotoZoneShopInfo)
	mux.HandleFunc("/v4/r/badge/reset/", handleBadgeReset)
	mux.HandleFunc("/v4/faceshop/v2/shop/", economy.HandleFaceShopData)
	mux.HandleFunc("/v4/faceshop/saveAndPurchase/v2", economy.HandleFaceShopPurchase)
	mux.HandleFunc("/v4/shop/gift/list/", economy.HandleGiftShopList)
	mux.HandleFunc("/v4/gift/send", economy.HandleGiftSend)
	economy.RegisterEconomyRoutes(mux)
	economy.RegisterModelHouseRoutes(mux)
	economy.RegisterDyeRoutes(mux)
	economy.RegisterSpinRoutes(mux)
	economy.RegisterRemodelRoutes(mux)
	registerDeleteAccountRoutes(mux)
	mux.HandleFunc("/v4/items/dress/some", economy.HandleItemsSome)
	mux.HandleFunc("/v4/items/room/some", economy.HandleItemsSome)
	mux.HandleFunc("/v4/playhome/games/lp_rmchat", handlePlayDetailLPRmchat)
	mux.HandleFunc("/v4/playhome/games/lp_sq", httpx.HandleJSONBody(playDetailLPSquareBody))
	mux.HandleFunc("/v4/home/sublist/game/Android", handleMoreGameList)
	mux.HandleFunc("/v4/home/sublist/shop/10.1.0.0/Android.nhn", handleMoreShopList)
	mux.HandleFunc("/v4/noti/event/", handleMoreEventList)
	mux.HandleFunc("/v4/friends/invitation/info", httpx.HandleJSONBody(inviteInfoBody))
	mux.HandleFunc("/v4/timemagic/count", httpx.HandleJSONBody(timemagicCountBody))
	mux.HandleFunc("/v4/session/status", handleSessionStatus)
	mux.HandleFunc("/v4/inven/interior/items/all", room.InvenInteriorHandler(economy.RoomItemPrice))
	mux.HandleFunc("/v4/inven/use/list/interior/dividefloor", room.HandleInvenDivideFloor)
	mux.HandleFunc("/v4/room/save/new", room.HandleRoomSaveNew)
	mux.HandleFunc("/v4/room/preset/list", room.HandleRoomPresetList)
	mux.HandleFunc("/v4/room/preset/save/", room.HandleRoomPresetSave)
	mux.HandleFunc("/v4/room/preset/remove/", room.HandleRoomPresetRemove)
	mux.HandleFunc("/v4/room/preset/find/", room.HandleRoomPresetFind)
	mux.HandleFunc("/v4/pet/book/race/list/", room.HandlePetBook(economy.PetSpecies))
	mux.HandleFunc("/v4/pet/", room.HandlePet)
	mux.HandleFunc("/v4/room/groundlevel/status/", room.HandleGroundLevelStatus)
	mux.HandleFunc("/v4/room/random", lpn.HandleRoomRandom)
	mux.HandleFunc("/v4/room/random/hostaid", lpn.HandleRandomGardenHost)
	mux.HandleFunc("/v4/room/", lpn.HandleRoom)
	mux.HandleFunc("/v4/cherrytip/list/", economy.HandleCherryTipList)
	mux.HandleFunc("/v4/diary/profile/image/", handleDiaryProfileImage)
	mux.HandleFunc("/v4/diary/profile/", handleDiaryProfile)
	mux.HandleFunc("/v4/diary/wish/list/", handleWishList)
	mux.HandleFunc("/v4/diary/wish/save", handleWishSave)
	mux.HandleFunc("/v4/mileage/inven/items", handleMileageInven)
	mux.HandleFunc("/v4/profile/intro/", handleProfileIntroSave)
	mux.HandleFunc("/v4/profile/image", handleProfilePhotoSave)
	mux.HandleFunc("/v4/profile/", handleProfile)
	mux.HandleFunc("/v4/avatar/{aid}/profile", handleAvatarProfileSave)
	mux.HandleFunc("/v4/avatar/", handleAvatarInfo)
	mux.HandleFunc("/v4/banned/keyword", handleBannedKeyword)
	mux.HandleFunc("/v4/rcmdpost/list", handleRecommendPostList)
	mux.HandleFunc("/v4/storybook/list", httpx.HandleJSONBody(storyEmptyListBody))
	mux.HandleFunc("/v4/shop/storygacha/list", httpx.HandleJSONBody(storyEmptyListBody))
	mux.HandleFunc("/v4/line/event/invite/validate", httpx.HandleJSONBody(lineEventValidBody))
	mux.HandleFunc("/v4/line/event/invite/friend/list", httpx.HandleJSONBody(lineEventFriendListBody))
	mux.HandleFunc("/", handleRoot)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Some screens put their static base in front of an absolute picture URL (the boss result asked
		// "/https://play-static.line-scdn.net/cherry/fish/4901.png" and hung): serve the URL's own path.
		for _, scheme := range []string{"/https://", "/http://"} {
			if rest, ok := strings.CutPrefix(r.URL.Path, scheme); ok {
				if _, p, ok := strings.Cut(rest, "/"); ok {
					r.URL.Path, r.URL.RawPath = "/"+p, ""
				}
			}
		}
		if strings.Contains(r.URL.Path, "/../") && streaming.ServeRoute(w, r) {
			return
		}
		mux.ServeHTTP(w, r)
	})
}

// handleBadgeReset acknowledges POST /v4/r/badge/reset/<BADGE> (e.g. CSET, FACE after a
// Face Shop purchase); badges are not tracked, so this only clears the client's 404.
func handleBadgeReset(w http.ResponseWriter, r *http.Request) {
	badge := strings.TrimPrefix(r.URL.Path, "/v4/r/badge/reset/")
	if r.Method != http.MethodPost || badge == "" || strings.Contains(badge, "/") {
		httpx.ServeNotFound(w)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, `{"result":{}}`)
}

// handleMenuIcon serves GET /cherry/menu/<file>: a home menu picture embedded from data/menuicons.
func handleMenuIcon(w http.ResponseWriter, r *http.Request) {
	b, err := fs.ReadFile(data.MenuIcons, "menuicons/"+strings.TrimPrefix(r.URL.Path, "/cherry/menu/"))
	if err != nil || r.Method != http.MethodGet {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Write(b)
}

func handleHomeListExt(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpx.ServeNotFound(w)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, buildHomeListExtBody())
}

type closetInventoryItem struct {
	ItemCode       string `json:"itemCode"`
	InvenSeq       string `json:"invenSeq"`
	Count          int    `json:"count"`
	Price          int    `json:"price"`
	NewArrival     bool   `json:"newArrival"`
	SpecialEffects string `json:"specialEffects"`
	Grade          string `json:"grade"`
	DyeType        int    `json:"dyeType"`
	// ColorAndTransparencies is set for a dyed garment only (omitted otherwise, so undyed rows stay as they were).
	ColorAndTransparencies []store.DyeColor `json:"colorAndTransparencies,omitempty"`
}

type closetBasicFaceItem struct {
	ItemCode string `json:"itemCode"`
}

type closetItemsResult struct {
	BasicFaceList []closetBasicFaceItem `json:"basicFaceList"`
	InventoryList []closetInventoryItem `json:"inventoryList"`
}

func handleClosetItemsAll(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpx.ServeNotFound(w)
		return
	}
	store.AccountsMu.Lock()
	acc := store.Accounts[httpx.CookieValue(r, "AV_AUTH")]
	var codes []string
	var paid map[string]int64            // copy-on-write, safe to read after unlock
	var dyes map[string][]store.DyeColor // copy-on-write, safe to read after unlock
	if acc != nil {
		codes = store.AccountOwnedCodes(acc)
		paid = acc.PaidPrices
		dyes = acc.DyeColors
	}
	store.AccountsMu.Unlock()
	basicFaceItems := make([]closetBasicFaceItem, 0, len(codes))
	seenFaceCategories := make(map[string]struct{}, 5)
	for _, code := range codes {
		if store.IsBasicFaceItemCode(code) {
			category := code[2:4]
			if _, seen := seenFaceCategories[category]; !seen {
				basicFaceItems = append(basicFaceItems, closetBasicFaceItem{ItemCode: code})
				seenFaceCategories[category] = struct{}{}
			}
		}
	}
	order := store.ClosetOrder(codes)
	items := make([]closetInventoryItem, 0, len(order))
	for i, code := range order {
		price, grade := economy.ClosetPrice(paid, code) // faces and unclassified codes: price 0, grade N
		item := closetInventoryItem{ItemCode: code, InvenSeq: strconv.Itoa(i + 1), Count: 1, Price: int(price), Grade: grade}
		if colors, ok := dyes[code]; ok {
			item.DyeType, item.ColorAndTransparencies = store.DyeTypeTwoLayers, store.DyeLayers(colors)
		}
		items = append(items, item)
	}
	payload, _ := json.Marshal(struct {
		Result closetItemsResult `json:"result"`
	}{Result: closetItemsResult{BasicFaceList: basicFaceItems, InventoryList: items}})
	httpx.WriteJSON(w, http.StatusOK, string(payload))
}

func handlePlayDetailLPRmchat(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpx.ServeNotFound(w)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, playDetailLPRmchatBody)
}

func handleSnsTerms(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodPut {
		httpx.ServeNotFound(w)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, snsTermsBody)
}

func handleArtsStrings(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpx.ServeNotFound(w)
		return
	}
	ast := loadArtsStrings()
	if ast == nil {
		httpx.ServeNotFound(w)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", strconv.Itoa(len(ast)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(ast)
}

func handleSetInitConf(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpx.ServeNotFound(w)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, setInitConfBody)
}

func handleCheckResource2(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpx.ServeNotFound(w)
		return
	}
	if streaming != nil {
		streaming.WriteCheckResource2(w)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, checkResource2Body)
}

func handleSplash(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet || !validSplashPath(r.URL.Path) {
		httpx.ServeNotFound(w)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, splashSkipBody)
}

func handleGuestGenerate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpx.ServeNotFound(w)
		return
	}
	acc, err := store.NewAccount()
	if err != nil {
		httpx.WriteJSON(w, http.StatusInternalServerError, `{"errorCode":"500"}`)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, `{"result":{"provider":"lineplay","accessToken":"`+acc.AccessToken+`"}}`)
}

func handleCreateSession(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpx.ServeNotFound(w)
		return
	}
	acc := store.CurrentAccount(r)
	if acc == nil {
		httpx.ServeNotFound(w)
		return
	}
	token := store.RandomToken(256)
	store.AccountsMu.Lock()
	store.Accounts[token] = acc
	err := store.SaveAccountsLocked()
	if err != nil {
		delete(store.Accounts, token)
	}
	store.AccountsMu.Unlock()
	if err != nil {
		httpx.WriteJSON(w, http.StatusInternalServerError, `{"errorCode":"500"}`)
		return
	}
	w.Header().Add("Set-Cookie", avAuthSetCookie(token))
	httpx.WriteJSON(w, http.StatusOK, `{"result":`+sessionResultBody(acc)+`}`)
}

func handleCheckSession(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpx.ServeNotFound(w)
		return
	}
	store.AccountsMu.Lock()
	acc := store.Accounts[httpx.CookieValue(r, "AV_AUTH")]
	if acc == nil {
		store.AccountsMu.Unlock()
		httpx.WriteJSON(w, http.StatusNotFound, httpx.UnknownSessionBody)
		return
	}
	token := store.RandomToken(256)
	store.Accounts[token] = acc
	err := store.SaveAccountsLocked()
	if err != nil {
		delete(store.Accounts, token)
	}
	store.AccountsMu.Unlock()
	if err != nil {
		httpx.WriteJSON(w, http.StatusInternalServerError, `{"errorCode":"500"}`)
		return
	}
	w.Header().Add("Set-Cookie", avAuthSetCookie(token))
	httpx.WriteJSON(w, http.StatusOK, fmt.Sprintf(`{"Timestamp":"%d","result":%s}`, time.Now().Unix(), sessionResultBody(acc)))
}

const maxCreateAvatarBody = httpx.MaxJSONBody

type createAvatarRequest struct {
	Name       string            `json:"name"`
	AvatarType string            `json:"avatarType"`
	NationCode string            `json:"nationCode"`
	SkinColor  json.RawMessage   `json:"skinColor"`
	ItemCodes  []json.RawMessage `json:"itemCodes"`
}

// itemCodesFromJSON accepts the client's item code strings and the legacy
// numeric base/skin codes; anything else (objects/arrays/bools/null) is
// rejected before the account is touched. On rejection it also reports the
// JSON kind of the offending element for diagnostics, never its value.
func itemCodesFromJSON(raws []json.RawMessage) ([]string, string, bool) {
	codes := make([]string, 0, len(raws))
	for _, raw := range raws {
		if string(raw) == "null" {
			return nil, "null", false
		}
		var s string
		if err := json.Unmarshal(raw, &s); err == nil {
			codes = append(codes, s)
			continue
		}
		var n json.Number
		if err := json.Unmarshal(raw, &n); err == nil {
			codes = append(codes, n.String())
			continue
		}
		return nil, jsonValueKind(raw), false
	}
	return codes, "", true
}

// fitSpecies drops the creation codes the sex cannot wear, instead of failing the creation: an ANIMAL keeps no hair or
// human face (itemcode.HumanOnly), a human keeps no animal face, ear or tail part (itemcode.AnimalOnly). The creation
// catalogs in data/dressup.json offer each sex only its own parts, so a real client should not send any of these.
func fitSpecies(gender string, codes []string) []string {
	out := make([]string, 0, len(codes))
	for _, c := range codes {
		if gender == "ANIMAL" && itemcode.HumanOnly(c) || gender != "ANIMAL" && itemcode.AnimalOnly(c) {
			continue
		}
		out = append(out, c)
	}
	return out
}

// jsonValueKind names the top-level JSON kind of raw without decoding it.
func jsonValueKind(raw json.RawMessage) string {
	s := strings.TrimSpace(string(raw))
	if s == "" {
		return "empty"
	}
	switch s[0] {
	case '{':
		return "object"
	case '[':
		return "array"
	case '"':
		return "string"
	case 't', 'f':
		return "bool"
	case 'n':
		return "null"
	default:
		return "number"
	}
}

// logCreateAvatarReject emits the diagnostic line for a rejected
// /v4/create/avatar request. It must never carry request headers, cookie,
// name, item codes or raw JSON. bodyBytes is bounded by maxJSONBody+1.
func logCreateAvatarReject(stage string, bodyBytes int, detail string) {
	if detail == "" {
		log.Printf("CREATE avatar rejected stage=%s bytes=%d", stage, bodyBytes)
		return
	}
	log.Printf("CREATE avatar rejected stage=%s bytes=%d %s", stage, bodyBytes, detail)
}

func handleCreateAvatar(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpx.ServeNotFound(w)
		return
	}
	body := io.Reader(r.Body)
	if strings.EqualFold(strings.TrimSpace(r.Header.Get("Content-Encoding")), "gzip") {
		zr, err := gzip.NewReader(r.Body)
		if err != nil {
			logCreateAvatarReject("gzip", 0, "")
			httpx.WriteJSON(w, http.StatusBadRequest, httpx.BadRequestBody)
			return
		}
		defer zr.Close()
		body = zr
	}
	raw, err := io.ReadAll(io.LimitReader(body, httpx.MaxJSONBody+1))
	if err != nil {
		logCreateAvatarReject("read", len(raw), "")
		httpx.WriteJSON(w, http.StatusBadRequest, httpx.BadRequestBody)
		return
	}
	if len(raw) > httpx.MaxJSONBody {
		logCreateAvatarReject("size", len(raw), "")
		httpx.WriteJSON(w, http.StatusBadRequest, httpx.BadRequestBody)
		return
	}
	var req createAvatarRequest
	nativeTerminator, err := httpx.UnmarshalNativeJSON(raw, &req)
	if err != nil {
		logCreateAvatarReject("json", len(raw), fmt.Sprintf("nativeTerminator=%t", nativeTerminator))
		httpx.WriteJSON(w, http.StatusBadRequest, httpx.BadRequestBody)
		return
	}
	gender := normalizeAvatarType(req.AvatarType)
	if gender == "" {
		logCreateAvatarReject("avatarType", len(raw), "typeLen="+strconv.Itoa(len(req.AvatarType)))
		httpx.WriteJSON(w, http.StatusBadRequest, httpx.BadRequestBody)
		return
	}
	itemCodes, badKind, ok := itemCodesFromJSON(req.ItemCodes)
	if !ok {
		logCreateAvatarReject("itemCodes", len(raw), fmt.Sprintf("count=%d firstType=%s", len(req.ItemCodes), badKind))
		httpx.WriteJSON(w, http.StatusBadRequest, httpx.BadRequestBody)
		return
	}
	itemCodes = fitSpecies(gender, itemCodes)
	skin, ok := skinColorString(req.SkinColor)
	if !ok {
		logCreateAvatarReject("skinColor", len(raw), "")
		httpx.WriteJSON(w, http.StatusBadRequest, httpx.BadRequestBody)
		return
	}
	if skin == "" {
		skin = "1"
	}

	store.AccountsMu.Lock()
	acc := store.Accounts[httpx.CookieValue(r, "AV_AUTH")]
	store.AccountsMu.Unlock()
	if acc == nil {
		httpx.WriteJSON(w, http.StatusNotFound, httpx.UnknownSessionBody)
		return
	}
	token := store.RandomToken(256)
	store.AccountsMu.Lock()
	previous, previousID := *acc, store.NextAvatarID
	if acc.Aid == "0" {
		// libgame's Room Party cells blank any friend whose avatarNo is a single character (NaRoomPartySearchCell/NormalCell::InitUI), so aids start at 10.
		store.NextAvatarID = max(store.NextAvatarID, store.MinAvatarID-1) + 1
		if strconv.FormatUint(store.NextAvatarID, 10) == store.FriendAID {
			store.NextAvatarID++
		}
		acc.Aid = strconv.FormatUint(store.NextAvatarID, 10)
	}
	acc.Name = req.Name
	acc.Gender = gender
	acc.Skin = skin
	acc.Country = req.NationCode
	acc.InventoryCodes = store.AppendUniqueItemCodes(store.AccountInventoryCodes(acc), itemCodes)
	acc.ItemCodes = append([]string(nil), itemCodes...)
	aid, sessionKey := acc.Aid, acc.SessionKey
	store.Accounts[token] = acc
	err = store.SaveAccountsLocked()
	if err != nil {
		*acc, store.NextAvatarID = previous, previousID
		delete(store.Accounts, token)
	}
	store.AccountsMu.Unlock()
	if err != nil {
		httpx.WriteJSON(w, http.StatusInternalServerError, `{"errorCode":"500"}`)
		return
	}

	w.Header().Add("Set-Cookie", avAuthSetCookie(token))
	payload, _ := json.Marshal(struct {
		Result store.AvatarResult `json:"result"`
	}{store.AvatarResult{
		AvatarID:    aid,
		Name:        req.Name,
		Gender:      req.AvatarType,
		SessionKey:  sessionKey,
		AvatarCode:  "ac",
		Items:       store.AvatarItemsFromCodes(itemCodes),
		PetProfiles: []string{},
	}})
	httpx.WriteJSON(w, http.StatusOK, string(payload))
}

// skinColorString accepts only a scalar string or number (or absent/null);
// objects, arrays and bools are rejected before the account is touched.
func skinColorString(raw json.RawMessage) (string, bool) {
	if len(raw) == 0 || string(raw) == "null" {
		return "", true
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s, true
	}
	var n json.Number
	if err := json.Unmarshal(raw, &n); err == nil {
		return n.String(), true
	}
	return "", false
}

func handleCreateComplete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpx.ServeNotFound(w)
		return
	}
	economy.HandleWelcomeGift(w, r)
}

func handleAvatarSaveV2(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut {
		httpx.ServeNotFound(w)
		return
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, httpx.MaxJSONBody+1))
	if err != nil || len(raw) > httpx.MaxJSONBody {
		httpx.WriteJSON(w, http.StatusBadRequest, httpx.BadRequestBody)
		return
	}
	var req []store.AvatarSaveItem
	if _, err := httpx.UnmarshalNativeJSON(raw, &req); err != nil || req == nil {
		httpx.WriteJSON(w, http.StatusBadRequest, httpx.BadRequestBody)
		return
	}
	for _, item := range req {
		if item.ItemCode == nil || *item.ItemCode == "" {
			httpx.WriteJSON(w, http.StatusBadRequest, httpx.BadRequestBody)
			return
		}
		if len(item.InvenSeq) > 0 {
			var seq int
			if strings.TrimSpace(string(item.InvenSeq)) == "null" || json.Unmarshal(item.InvenSeq, &seq) != nil {
				httpx.WriteJSON(w, http.StatusBadRequest, httpx.BadRequestBody)
				return
			}
		}
	}

	store.AccountsMu.Lock()
	acc := store.Accounts[httpx.CookieValue(r, "AV_AUTH")]
	if acc == nil {
		store.AccountsMu.Unlock()
		httpx.WriteJSON(w, http.StatusNotFound, httpx.UnknownSessionBody)
		return
	}
	serials := store.ItemSerials(store.AccountOwnedCodes(acc))
	itemCodes := make([]string, 0, len(req))
	for _, item := range req {
		code := *item.ItemCode
		if isSkinItemCode(code) || store.IsAvatarBaseItem(acc.Gender, code) {
			continue
		}
		if _, owned := serials[code]; !owned {
			store.AccountsMu.Unlock()
			httpx.WriteJSON(w, http.StatusBadRequest, httpx.BadRequestBody)
			return
		}
		itemCodes = append(itemCodes, code)
	}
	previous := *acc
	acc.ItemCodes = itemCodes
	err = store.SaveAccountsLocked()
	if err != nil {
		*acc = previous
		store.AccountsMu.Unlock()
		httpx.WriteJSON(w, http.StatusInternalServerError, `{"errorCode":"500"}`)
		return
	}
	info := store.AvatarInfoForAccount(store.AccountSnapshot{
		Aid: acc.Aid, Name: acc.Name, Gender: acc.Gender, Skin: acc.Skin,
		Country: acc.Country, ItemCodes: append([]string(nil), acc.ItemCodes...),
		InventoryCodes: store.AccountOwnedCodes(acc), DyeColors: acc.DyeColors,
	})
	store.AccountsMu.Unlock()
	payload, _ := json.Marshal(struct {
		Result store.AvatarInfoResult `json:"result"`
	}{Result: info})
	httpx.WriteJSON(w, http.StatusOK, string(payload))
}

func isSkinItemCode(code string) bool {
	return len(code) == 6 && strings.HasPrefix(code, "SKN") && httpx.IsAllDigits(code[3:])
}

func handleAvatarInfo(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpx.ServeNotFound(w)
		return
	}
	id, ok := strings.CutPrefix(r.URL.Path, "/v4/avatar/")
	if !ok || id == "" || strings.Contains(id, "/") || !httpx.IsAllDigits(id) {
		httpx.ServeNotFound(w)
		return
	}
	info := store.AvatarInfoResult{
		AvatarID:    id,
		Gender:      "FEMALE",
		SType:       "NORMAL",
		Skin:        "1",
		Country:     "JP",
		Items:       []store.AvatarItem{},
		PetProfiles: []string{},
	}
	if acc, ok := store.AccountByAvatarID(id); ok {
		info = store.AvatarInfoForAccount(acc)
	} else if id == store.FriendAID {
		info.Name = store.FriendName()
		info.Items = store.AvatarItemsFromCodes(workableLook("FEMALE"))
	} else if sex, ok := store.ActiveProfiles().CloneAvatars[id]; ok {
		// The four clone avatars the create flow asks for (NaCreateLayer::InitCloneAvatars); their
		// synthesized appearance is operator data (profiles.json). The authentic ones are not in the archives.
		info.Name = store.ActiveProfiles().CloneName
		info.Gender = sex
		info.Items = store.AvatarItemsFromCodes(workableLook(sex))
	}
	payload, _ := json.Marshal(struct {
		Result store.AvatarInfoResult `json:"result"`
	}{Result: info})
	httpx.WriteJSON(w, http.StatusOK, string(payload))
}

func handleRoot(w http.ResponseWriter, r *http.Request) {
	// A generated landscape has no archive folder, so its item files are answered before the stream could look.
	if economy.ServeLandscapeFile(w, r) {
		return
	}
	// A streamed file (or dp.png) wins; a miss falls through to the generated thumbnail below.
	if streaming.ServeRoute(w, r) {
		return
	}
	// Streaming off: the static-base craft art and photozone frames are read in place from the archive
	// (streaming on: R2 alone answers them).
	if streaming == nil && economy.ServeGardenCraftArt(w, r) {
		return
	}
	// A converted iOS-only item (cmd/cherry-assets convert) answers from the local generated tree when R2 did not.
	if assets.ServeGeneratedItem(w, r) {
		return
	}
	// A dyeable garment's dye preview mask that the stream lacks (economy.ServeDyeMask); the dp.png generator follows.
	if economy.ServeDyeMask(w, r) {
		return
	}
	if r.Method == http.MethodGet {
		if kind, id, ok := thumbs.ParseDpPath(r.URL.Path); ok {
			thumbs.ServeDpPNG(w, kind, id)
			return
		}
	}
	if r.Method == http.MethodGet && economy.ServeAttendanceSkin(w, r.URL.Path) {
		return
	}
	if r.Method == http.MethodGet && isUpdateIniPath(r.URL.Path) {
		body := emptyUpdateIniBody
		if isTx543UpdateIniPath(r.URL.Path) {
			body = tx543UpdateIniBody
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(body))
		return
	}
	if r.Method == http.MethodGet && isSkinFileIniPath(r.URL.Path) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(skinFileIniBody))
		return
	}
	if r.Method == http.MethodGet && strings.Contains(r.URL.Path, "arts_strings.ast") {
		handleArtsStrings(w, r)
		return
	}
	httpx.ServeNotFound(w)
}

func validSplashPath(path string) bool {
	rest, ok := strings.CutPrefix(path, "/v4/resource/splash/")
	if !ok || rest == "" {
		return false
	}
	parts := strings.Split(rest, "/")
	if len(parts) != 3 {
		return false
	}
	for _, p := range parts {
		if p == "" {
			return false
		}
	}
	return httpx.IsAllDigits(parts[0]) && httpx.IsAllDigits(parts[1])
}

func isSkinFileIniPath(path string) bool {
	return strings.HasPrefix(path, "/arts_diaryskin_") && strings.HasSuffix(path, "/skin_file.ini")
}

func isUpdateIniPath(path string) bool {
	name := path
	if i := strings.LastIndex(path, "/"); i >= 0 {
		name = path[i+1:]
	}
	switch name {
	case "update_en.ini", "update_jp.ini", "update_ja.ini":
	default:
		return false
	}
	dir := path[:len(path)-len(name)]
	prefixes := []string{
		"/arts_animation_ini_00531/",
		"/arts_sound_ini_00530/",
		"/arts_tx_ini_00540/",
		"/arts_tx_ini_00541/",
		"/arts_tx_ini_00542/",
		"/arts_tx_ini_00543/",
		"/arts_ui_ini_00540/",
		"/arts_subui_ini_00000/",
	}
	for _, p := range prefixes {
		if dir == p {
			return true
		}
	}
	return false
}

func isTx543UpdateIniPath(path string) bool {
	switch path {
	case "/arts_tx_ini_00543/update_en.ini", "/arts_tx_ini_00543/update_jp.ini":
		return true
	}
	return false
}

func sessionResultBody(acc *store.Account) string {
	store.AccountsMu.Lock()
	sessionKey, mid, avatarUserID, aid := acc.SessionKey, acc.Mid, acc.AvatarUserID, acc.Aid
	store.AccountsMu.Unlock()
	return fmt.Sprintf(`{"sessionKey":"%s","mid":"%s","avatarUserId":"%s","aid":"%s","lineId":"%s","lineName":"","termAge":false}`,
		sessionKey, mid, avatarUserID, aid, guestLineID(aid))
}

// guestLineID is the lineId of a guest account. The client checks saves for the LINE ID with find(lineId) and
// no empty guard (NaDiaryProfileEditView::SubmitChanges 0x1e13ff4, NaDiaryIntroEditView::Submit 0x1e13d14,
// NaClosetStylingNamePopup 0x1ff24a8), and find("") always hits, so an empty lineId refuses every save. No
// reader treats a non-empty value as a LINE-linked account (lineid-spec.md, 2026-10-28). It is longer than
// any text those fields accept (status 50, nickname 20 runes), so it can never match. lineName stays "":
// creation would pre-fill the nickname with it.
func guestLineID(aid string) string {
	return fmt.Sprintf("cherry-guest-%010s-%s", aid, strings.Repeat("0", 40))
}

func avAuthSetCookie(token string) string {
	return `AV_AUTH="` + token + `"; Path=/`
}

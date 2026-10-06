package main

import (
	"compress/gzip"
	"crypto/rand"
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

const setInitConfBody = `{"result":{"sessionServerInfo":"XPN:/p=XTCP;ip=session.play.naver.jp;port=10123","staticDomain":"https://play-static.line-scdn.net/","nationCode":"JP","isGdprNation":false,"snsLoginUIList":["LD_GUEST"],"snsSignUpUIList":["LD_GUEST"]}}`

const checkResource2Body = `{"adr":{"10.1.0.0":"1"},"resource":{"1":{"animation":"531","he":"0","ch":"0","sound":"530","subui":"0","tx":"543","squareui":"0","ui":"540"}}}`

const splashSkipBody = `{"result":{"update":false,"resourceID":-1,"resourceImageCount":0,"resourceImages":[]}}`

const emptyUpdateIniBody = `[]`

const tx543UpdateIniBody = `[start=1,end=1,size=454656,/arts_strings.ast:version=1492F278EC281078AC7F35479A85F197]`

const skinFileIniBody = `[empty]`

const popupIsExistsBody = `{"result":false}`

const eventFlagListBody = `{"result":[]}`

const snsTermsBody = `{"result":true}`

const termAllBody = `{"result":{"1":true}}`

const profileBody = `{"result":{"name":"guest","newbie":true}}`

const notFoundBody = `{"errorCode":"404","errorMessage":"cherry: unknown route"}`

const unknownSessionBody = `{"errorCode":"404","errorMessage":"cherry: unknown session"}`

const badRequestBody = `{"errorCode":"400","errorMessage":"cherry: bad request"}`

const createCompleteBody = `{"result":{"status":true,"rewardCoin":300}}`

const settingAllBody = `{"result":{"notiFlag":false,"changeCountry":false,"countryName":"","soundConfig":false,"notiConfig":{},"privacyConfig":{},"roomSize":{"max":0,"cur":0}}}`

const friendLineBuddyBody = `{"result":{"nextCursor":"0","buddyList":[],"bookmarks":[]}}`

const badgeInfosBody = `{"result":{"NEWS":0,"CHAT":0,"NFRD":0,"IFRD":0,"GIFT":0,"POSTBOX":0,"CSET":0,"FACE":0,"ROOM":0,"ALERT":0}}`

const friendBrandBuddyBody = `{"result":[]}`

type homeIconRow struct {
	Name     string
	LinkType string
}

var homeIconRows = []homeIconRow{
	{"Closet", "goSomewhere(closet)"},
	{"Friends", "goSomewhere(myfriends)"},
	{"Diary", "goSomewhere(diary)"},
	{"Browser", "openWithBrowser"},
	{"HTML", "html"},
	{"Events", "event"},
	{"Game Link", "gamelink"},
	{"Game Link Custom", "gamelink(custom)"},
	{"Game Link Embedded", "gamelink(embedded)"},
	{"My Home", "goSomewhere(myHome)"},
	{"My Room", "goSomewhere(myRoom)"},
	{"Other Room", "goSomewhere(otherRoom)"},
	{"Interior Shop", "goSomewhere(interiorShop)"},
	{"Fashion Shop", "goSomewhere(fashionShop)"},
	{"Brand Shop", "goSomewhere(brandShop)"},
	{"Brand Shop Ex", "goSomewhere(brandShopEx)"},
	{"Gacha Shop", "goSomewhere(gachaShop)"},
	{"Gift Shop", "goSomewhere(giftShop)"},
	{"Model House", "goSomewhere(modelhouse)"},
	{"Face Shop", "goSomewhere(faceShop)"},
	{"Badge Shop", "goSomewhere(badgeShop)"},
	{"Gem Shop", "goSomewhere(gemShop)"},
	{"Heart Shop", "goSomewhere(extraHeartShop)"},
	{"Cash Shop", "goSomewhere(cashShop)"},
	{"Profile Edit", "goSomewhere(profileedit)"},
	{"Notice", "goSomewhere(notice)"},
	{"Add Friends", "goSomewhere(addfriends)"},
	{"Invitation", "goSomewhere(invitation)"},
	{"Lounge Chat", "goSomewhere(loungeChat)"},
	{"Today Members", "goSomewhere(todayMember)"},
	{"Room Edit", "goSomewhere(roomEdit)"},
	{"Settings", "goSomewhere(setting)"},
	{"Square", "goSomewhere(square)"},
	{"Postbox", "goSomewhere(postbox)"},
	{"Random Room", "goSomewhere(randomRoom)"},
	{"Lucky Spin", "goSomewhere(luckyspin)"},
	{"More Shop", "goSomewhere(subMoreShop)"},
	{"More Gacha", "goSomewhere(subMoreGacha)"},
	{"More Events", "goSomewhere(subMoreEvent)"},
	{"More Games", "goSomewhere(subMoreGame)"},
	{"Gacha Category", "goSomewhere(gachaCatg)"},
	{"Gacha Category No Tap", "goSomewhere(gachaCatgNotap)"},
	{"Gacha Detail", "goSomewhere(gachaDetail)"},
	{"Gacha Top", "goSomewhere(gachaTop)"},
	{"Gacha New", "goSomewhere(gachaNew)"},
	{"Mailbox", "goSomewhere(mailbox)"},
	{"Jackpot Spin", "goSomewhere(jackpotspin)"},
	{"Minipet Shop", "goSomewhere(minipetShop)"},
	{"Minipet Book", "goSomewhere(minipetBook)"},
	{"VIP Lounge", "goSomewhere(viplounge)"},
	{"VIP Enter Popup", "goSomewhere(vipenterpopup)"},
	{"VIP Detail", "goSomewhere(vipdetail)"},
	{"Fashion Quest", "goSomewhere(fashionQuest)"},
	{"Enter Square", "goSomewhere(enterSquare)"},
	{"Golden Chance", "goSomewhere(goldenChance)"},
	{"Go To Friend", "goSomewhere(gotoFriend)"},
	{"Play Pass Popup", "goSomewhere(playpasspopup)"},
	{"Remodeling Shop", "goSomewhere(remodelingShop)"},
	{"Quest", "goSomewhere(quest)"},
	{"Attendance", "goSomewhere(attendance)"},
	{"Room Party Invite", "goSomewhere(roompartyInvite)"},
	{"Avatar Chat", "goSomewhere(avatarChat)"},
	{"Diary Detail", "goSomewhere(diaryDetail)"},
	{"Cherry Tip", "goSomewhere(cherrytip)"},
	{"Video Play", "goSomewhere(videoplay)"},
	{"Event Ranking", "goSomewhere(eventRanking)"},
	{"Package Day 1", "goSomewhere(billPackageDay1)"},
	{"Package Day 30", "goSomewhere(billPackageDay30)"},
	{"Bill Package", "goSomewhere(billPackage)"},
	{"Package Item High", "goSomewhere(billPackageItemHigh)"},
	{"Package Item Low", "goSomewhere(billPackageItemLow)"},
	{"Recommended Posts", "goSomewhere(recommendPostList)"},
	{"Fashionista", "goSomewhere(fashionista)"},
	{"Fashionista List", "goSomewhere(fashionistaList)"},
	{"Circle", "goSomewhere(circle)"},
	{"Circle Fashion Quest", "goSomewhere(circleFashionQuest)"},
	{"Gacha Main", "goSomewhere(gachaMain)"},
	{"Gacha Search", "goSomewhere(gachaSearch)"},
	{"Gacha Keyword", "goSomewhere(gachaKeyword)"},
	{"Comic Guide", "goSomewhere(ComicGuide)"},
	{"More Comic Guide", "goSomewhere(MoreComicGuide)"},
	{"Time Magic", "goSomewhere(timemagic)"},
	{"Item Trade", "goSomewhere(ItemTrade)"},
	{"Story Gacha", "goSomewhere(storygacha)"},
	{"Story Gacha List", "goSomewhere(storygachaList)"},
	{"Story Gacha Detail", "goSomewhere(storygachaDetail)"},
	{"Square Collab Event", "goSomewhere(SquareCollaboEvent)"},
	{"Line Game Event", "goSomewhere(lineGameEvent)"},
	{"Store List", "goSomewhere(storeList)"},
	{"Store Detail", "goSomewhere(storeDetail)"},
	{"Event Lotto", "goSomewhere(eventLottoDetail)"},
	{"Treasure World", "goSomewhere(treasureWorld)"},
	{"Gift Shop Detail", "goSomewhere(giftShopDetail)"},
	{"Collection", "goSomewhere(collectionmain)"},
	{"Collection Detail", "goSomewhere(collectiondetail)"},
	{"Garden Edit", "goSomewhere(gardenedit)"},
	{"Garden Edit Cloud", "goSomewhere(gardeneditcloud)"},
	{"Bag", "goSomewhere(bag)"},
	{"Factory", "goSomewhere(factory)"},
	{"My Garden", "goSomewhere(myGarden)"},
	{"Other Garden", "goSomewhere(otherGarden)"},
	{"Random Garden", "goSomewhere(randomGarden)"},
	{"Garden Buff Info", "goSomewhere(gardenbuffInfo)"},
	{"Marble Board", "goSomewhere(marbleboardWebview)"},
	{"Bill Package", "goSomewhere(billPkg)"},
	{"Colorant Shop", "goSomewhere(colorantShop)"},
	{"Colorant Base Shop", "goSomewhere(colorantBaseShop)"},
	{"Colorant Inventory", "goSomewhere(colorantInven)"},
	{"Butler Shop", "goSomewhere(butlerShop)"},
	{"Butler Book", "goSomewhere(butlerBook)"},
	{"Butler Skin Select", "goSomewhere(butlerSkinSelect)"},
	{"Storage", "goSomewhere(storage)"},
	{"Image Popup", "goSomewhere(imagePopupDetail)"},
	{"Fishing Boss Ranking", "goSomewhere(fishingBossRanking)"},
	{"Fishing Exchange", "goSomewhere(fishingExchange)"},
	{"Minigame Ranking", "goSomewhere(minigameRanking)"},
	{"Riding Pet Info", "goSomewhere(ridingpetplayinfo)"},
	{"Riding Pet Gacha", "goSomewhere(ridingpetGacha)"},
	{"Season Pass", "goSomewhere(seasonPass)"},
	{"Free Gems", "goSomewhere(rcvFreeGem)"},
	{"Home Ad", "goSomewhere(homeAd)"},
	{"Find Magic", "goSomewhere(findMagic)"},
	{"Survey", "goSomewhere(survey)"},
	{"Square Web View", "goSomewhere(squarewebview)"},
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
	icons := make([]homeIconEntry, len(homeIconRows))
	for i, row := range homeIconRows {
		icons[i] = homeIconEntry{ID: i + 1, Name: row.Name, LinkType: row.LinkType}
	}
	payload, _ := json.Marshal(struct {
		Result homeListExtResult `json:"result"`
	}{Result: homeListExtResult{HomeIconList: icons, EventIconList: []homeIconEntry{}}})
	return string(payload)
}

var homeListExtBody = buildHomeListExtBody()

var curatedGrantCodes = []string{"CUHA0036Z", "CUON004TV", "CUSH00267", "CUAH004JH"}

// InitAvatarBaseItem @0x29a7f84 (called from AvActorManager::Initialize).
// Index 0=MALE, 1=FEMALE. Numeric ids → CU codes via squareNumericItem inverse.
var avatarBaseItems = map[string][]string{
	"MALE":   {"CUTO0011X", "CUPA000LO", "CUSH0009Q", "CUHA00001"},
	"FEMALE": {"CUTO0011X", "CUPA000LO", "CUSH0009Q", "CUHA00004"},
}

func itemSlot(code string) string {
	if len(code) < 4 {
		return ""
	}
	return code[2:4]
}

func isAvatarBaseItem(code string) bool {
	for _, list := range avatarBaseItems {
		for _, b := range list {
			if b == code {
				return true
			}
		}
	}
	return false
}

func appearanceItemCodes(gender string, equipped []string) []string {
	have := map[string]bool{}
	for _, c := range equipped {
		s := itemSlot(c)
		have[s] = true
		if s == "ON" {
			have["TO"], have["PA"] = true, true
		}
	}
	base := avatarBaseItems[gender]
	if base == nil {
		base = avatarBaseItems["FEMALE"]
	}
	out := append([]string(nil), equipped...)
	for _, c := range base {
		if have[itemSlot(c)] {
			continue
		}
		out = append(out, c)
		have[itemSlot(c)] = true
	}
	return out
}

const storageDisplayBody = `{"result":false}`

const styleSlotListBody = `{"result":{"styleSlotResponses":[]}}`

const recycleConfigBody = `{"result":{"config":[]}}`

// Empty questProgress maps to enum 0 (no active quest), avoiding the client's per-frame status retry.
const questStatusBody = `{"result":{"heart":0,"heartBase":0,"heartRewardCoin":0,"questProgress":"","toExpire":"0"}}`

// Parser-valid empty inventory counts; ResGetInventoryItemCountInfo @0x1b2afe0 defaults every field.
const invenCountsBody = `{"result":{}}`

const itemsSomeBody = `{"result":[]}`

const playDetailLPRmchatBody = `{"result":{"gameInfo":{"gameId":"lp_rmchat","executable":true,"underMaintenance":false,"minLinePlayVersion":"","startDate":"1577836800000","endDate":"4102444800000"}}}`

// ResPlayDetailWithGameID @0x1a84e5c needs a nonempty gameInfo object; dates are ms strings.
const playDetailLPSquareBody = `{"result":{"gameInfo":{"gameId":"lp_sq","executable":true,"underMaintenance":false,"minLinePlayVersion":"","startDate":"1577836800000","endDate":"4102444800000"}}}`

const artsStringsMD5 = "1492F278EC281078AC7F35479A85F197"

//go:embed testdata/arts_strings.ast
var artsStringsAST []byte

func newMux() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/v4/account/guest/generate", handleGuestGenerate)
	mux.HandleFunc("/v4/createSession", handleCreateSession)
	mux.HandleFunc("/v4/checkSession", handleCheckSession)
	mux.HandleFunc("/v4/create/avatar", handleCreateAvatar)
	mux.HandleFunc("/v4/create/all/items", handleCreateAllItems)
	mux.HandleFunc("/v4/create/face/setitems/roll", handleCreateFaceSetItemsRoll)
	mux.HandleFunc("/v2/create/face/analyze", handleCreateFaceAnalyze)
	mux.HandleFunc("/v4/create/complete", handleCreateComplete)
	mux.HandleFunc("/arts_session/sckey.enc", handleSckeyEnc)
	mux.HandleFunc("/v4/setInitConf", handleSetInitConf)
	mux.HandleFunc("/v4/resource/splash/", handleSplash)
	mux.HandleFunc("/notice/adr/checkresource2.json", handleCheckResource2)
	mux.HandleFunc("/v4/popup/isExists", handleJSONBody(popupIsExistsBody))
	mux.HandleFunc("/v4/eventFlag/flagList", handleJSONBody(eventFlagListBody))
	mux.HandleFunc("/v4/social/terms/sns", handleSnsTerms)
	mux.HandleFunc("/v4/setting/term/all", handleJSONBody(termAllBody))
	mux.HandleFunc("/v4/setting/all", handleJSONBody(settingAllBody))
	mux.HandleFunc("/v4/sync/friends/", handleFriendSync)
	mux.HandleFunc("/v4/buddy/list/type/0", handleFriendSync)
	mux.HandleFunc("/v4/square/friends/search", handleFriendSearch)
	mux.HandleFunc("/v4/line/buddy/v4/list", handleJSONBody(friendLineBuddyBody))
	mux.HandleFunc("/v4/badge/infos/", handleJSONBody(badgeInfosBody))
	mux.HandleFunc("/v4/friend/bookmark/", handleFriendBookmark)
	mux.HandleFunc("/v4/r/friend/remove/", handleFriendRemove)
	mux.HandleFunc("/v4/friend/status/", handleFriendStatus)
	mux.HandleFunc("/v4/friend/apply/", handleFriendApply)
	mux.HandleFunc("/v4/friend/accept/", handleFriendAccept)
	mux.HandleFunc("/v4/diary2/ext/checkExist/", handleDiaryCheckExist)
	mux.HandleFunc("/v4/diary2/intro/", handleDiaryIntro)
	mux.HandleFunc("/v4/diary2/ext/write", handleDiaryWrite)
	mux.HandleFunc("/v4/diary2/ext/unfold/photo/", handleDiaryPhotoAlbum)
	mux.HandleFunc("/v4/diary2/ext/unfold/", handleDiaryUnfold)
	mux.HandleFunc("/v4/diary2/ext/look/", handleDiaryLook)
	mux.HandleFunc("/v4/diary2/erase/", handleDiaryErase)
	mux.HandleFunc("/v4/guestbook/write", handleGuestbookWrite)
	mux.HandleFunc("/v4/guestbook/erase/", handleGuestbookErase)
	mux.HandleFunc("/v4/guestbook3/list/", handleGuestbookList)
	mux.HandleFunc("/v4/guestbook3/count/", handleGuestbookCount)
	mux.HandleFunc("/v4/brand/list", handleJSONBody(friendBrandBuddyBody))
	mux.HandleFunc("/v4/quest/status", handleJSONBody(questStatusBody))
	mux.HandleFunc("/v4/inven/counts", handleJSONBody(invenCountsBody))
	mux.HandleFunc("/lineplay/d/upload.nhn", handleDiaryImageUpload)
	mux.HandleFunc("/lineplay/d/download.nhn", handleDiaryImageDownload)
	mux.HandleFunc("/lineplay/d/download.nhn/", handleDiaryImageDownload)
	mux.HandleFunc("/lineplay/r/upload.nhn", handleRoomImageUpload)
	mux.HandleFunc("/lineplay/r/download.nhn", handleRoomImageDownload)
	mux.HandleFunc("/lineplay/r/delete.nhn", handleRoomImageDelete)
	mux.HandleFunc("/v4/home/list/ext/", handleHomeListExt)
	mux.HandleFunc("/v4/inven/closet/items/all", handleClosetItemsAll)
	mux.HandleFunc("/v4/storage/display", handleJSONBody(storageDisplayBody))
	mux.HandleFunc("/v4/style/slot/list", handleJSONBody(styleSlotListBody))
	mux.HandleFunc("/v4/inven/recycle/cfg", handleJSONBody(recycleConfigBody))
	mux.HandleFunc("/v4/avatar/save/v2", handleAvatarSaveV2)
	mux.HandleFunc("/v4/items/dress/some", handleItemsSome)
	mux.HandleFunc("/v4/items/room/some", handleItemsSome)
	mux.HandleFunc("/v4/playhome/games/lp_rmchat", handlePlayDetailLPRmchat)
	mux.HandleFunc("/v4/playhome/games/lp_sq", handleJSONBody(playDetailLPSquareBody))
	mux.HandleFunc("/v4/inven/interior/items/all", handleInvenInterior)
	mux.HandleFunc("/v4/inven/use/list/interior/dividefloor", handleInvenDivideFloor)
	mux.HandleFunc("/v4/room/save/new", handleRoomSaveNew)
	mux.HandleFunc("/v4/room/preset/list", handleRoomPresetList)
	mux.HandleFunc("/v4/room/preset/save/", handleRoomPresetSave)
	mux.HandleFunc("/v4/room/preset/remove/", handleRoomPresetRemove)
	mux.HandleFunc("/v4/room/preset/find/", handleRoomPresetFind)
	mux.HandleFunc("/v4/pet/", handlePet)
	mux.HandleFunc("/v4/room/", handleRoom)
	mux.HandleFunc("/v4/profile/", handleProfile)
	mux.HandleFunc("/v4/avatar/", handleAvatarInfo)
	mux.HandleFunc("/", handleRoot)
	return mux
}

func handleJSONBody(body string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			serveNotFound(w)
			return
		}
		writeJSON(w, http.StatusOK, body)
	}
}

func handleHomeListExt(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		serveNotFound(w)
		return
	}
	writeJSON(w, http.StatusOK, homeListExtBody)
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
		serveNotFound(w)
		return
	}
	accountsMu.Lock()
	acc := accounts[cookieValue(r, "AV_AUTH")]
	var codes []string
	if acc != nil {
		codes = accountOwnedCodes(acc)
	}
	accountsMu.Unlock()
	basicFaceItems := make([]closetBasicFaceItem, 0, len(codes))
	faceCodes := make([]string, 0, len(codes))
	seenFaceCategories := make(map[string]struct{}, 5)
	wearableCodes := make([]string, 0, len(codes))
	for _, code := range codes {
		if isBasicFaceItemCode(code) {
			category := code[2:4]
			if _, seen := seenFaceCategories[category]; !seen {
				basicFaceItems = append(basicFaceItems, closetBasicFaceItem{ItemCode: code})
				seenFaceCategories[category] = struct{}{}
			}
			faceCodes = append(faceCodes, code)
		} else {
			wearableCodes = append(wearableCodes, code)
		}
	}
	items := make([]closetInventoryItem, 0, len(codes))
	for i, code := range wearableCodes {
		items = append(items, closetInventoryItem{ItemCode: code, InvenSeq: strconv.Itoa(i + 1), Count: 1})
	}
	for i, code := range faceCodes {
		items = append(items, closetInventoryItem{ItemCode: code, InvenSeq: strconv.Itoa(len(wearableCodes) + i + 1), Count: 1})
	}
	payload, _ := json.Marshal(struct {
		Result closetItemsResult `json:"result"`
	}{Result: closetItemsResult{BasicFaceList: basicFaceItems, InventoryList: items}})
	writeJSON(w, http.StatusOK, string(payload))
}

func handlePlayDetailLPRmchat(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		serveNotFound(w)
		return
	}
	writeJSON(w, http.StatusOK, playDetailLPRmchatBody)
}

func handleItemsSome(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		serveNotFound(w)
		return
	}
	writeJSON(w, http.StatusOK, itemsSomeBody)
}

func handleSnsTerms(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodPut {
		serveNotFound(w)
		return
	}
	writeJSON(w, http.StatusOK, snsTermsBody)
}

func handleArtsStrings(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		serveNotFound(w)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", strconv.Itoa(len(artsStringsAST)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(artsStringsAST)
}

func handleSetInitConf(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		serveNotFound(w)
		return
	}
	writeJSON(w, http.StatusOK, setInitConfBody)
}

func handleCheckResource2(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		serveNotFound(w)
		return
	}
	writeJSON(w, http.StatusOK, checkResource2Body)
}

func handleSplash(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet || !validSplashPath(r.URL.Path) {
		serveNotFound(w)
		return
	}
	writeJSON(w, http.StatusOK, splashSkipBody)
}

func handleGuestGenerate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		serveNotFound(w)
		return
	}
	acc, err := newAccount()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, `{"errorCode":"500"}`)
		return
	}
	writeJSON(w, http.StatusOK, `{"result":{"provider":"lineplay","accessToken":"`+acc.accessToken+`"}}`)
}

func handleCreateSession(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		serveNotFound(w)
		return
	}
	acc := currentAccount(r)
	if acc == nil {
		serveNotFound(w)
		return
	}
	token := randomToken(256)
	accountsMu.Lock()
	accounts[token] = acc
	err := saveAccountsLocked()
	if err != nil {
		delete(accounts, token)
	}
	accountsMu.Unlock()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, `{"errorCode":"500"}`)
		return
	}
	w.Header().Add("Set-Cookie", avAuthSetCookie(token))
	writeJSON(w, http.StatusOK, `{"result":`+sessionResultBody(acc)+`}`)
}

func handleCheckSession(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		serveNotFound(w)
		return
	}
	accountsMu.Lock()
	acc := accounts[cookieValue(r, "AV_AUTH")]
	if acc == nil {
		accountsMu.Unlock()
		writeJSON(w, http.StatusNotFound, unknownSessionBody)
		return
	}
	token := randomToken(256)
	accounts[token] = acc
	err := saveAccountsLocked()
	if err != nil {
		delete(accounts, token)
	}
	accountsMu.Unlock()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, `{"errorCode":"500"}`)
		return
	}
	w.Header().Add("Set-Cookie", avAuthSetCookie(token))
	writeJSON(w, http.StatusOK, fmt.Sprintf(`{"Timestamp":"%d","result":%s}`, time.Now().Unix(), sessionResultBody(acc)))
}

const maxJSONBody = 1 << 20

const maxCreateAvatarBody = maxJSONBody

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

// The native client appends one NUL after its JSON body. Accept that exact
// terminator while leaving every other trailing byte to json.Unmarshal.
func unmarshalNativeJSON(raw []byte, dst any) (bool, error) {
	nativeTerminator := len(raw) > 0 && raw[len(raw)-1] == 0
	if nativeTerminator {
		raw = raw[:len(raw)-1]
	}
	return nativeTerminator, json.Unmarshal(raw, dst)
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
		serveNotFound(w)
		return
	}
	body := io.Reader(r.Body)
	if strings.EqualFold(strings.TrimSpace(r.Header.Get("Content-Encoding")), "gzip") {
		zr, err := gzip.NewReader(r.Body)
		if err != nil {
			logCreateAvatarReject("gzip", 0, "")
			writeJSON(w, http.StatusBadRequest, badRequestBody)
			return
		}
		defer zr.Close()
		body = zr
	}
	raw, err := io.ReadAll(io.LimitReader(body, maxJSONBody+1))
	if err != nil {
		logCreateAvatarReject("read", len(raw), "")
		writeJSON(w, http.StatusBadRequest, badRequestBody)
		return
	}
	if len(raw) > maxJSONBody {
		logCreateAvatarReject("size", len(raw), "")
		writeJSON(w, http.StatusBadRequest, badRequestBody)
		return
	}
	var req createAvatarRequest
	nativeTerminator, err := unmarshalNativeJSON(raw, &req)
	if err != nil {
		logCreateAvatarReject("json", len(raw), fmt.Sprintf("nativeTerminator=%t", nativeTerminator))
		writeJSON(w, http.StatusBadRequest, badRequestBody)
		return
	}
	gender := normalizeAvatarType(req.AvatarType)
	if gender == "" {
		logCreateAvatarReject("avatarType", len(raw), "typeLen="+strconv.Itoa(len(req.AvatarType)))
		writeJSON(w, http.StatusBadRequest, badRequestBody)
		return
	}
	itemCodes, badKind, ok := itemCodesFromJSON(req.ItemCodes)
	if !ok {
		logCreateAvatarReject("itemCodes", len(raw), fmt.Sprintf("count=%d firstType=%s", len(req.ItemCodes), badKind))
		writeJSON(w, http.StatusBadRequest, badRequestBody)
		return
	}
	skin, ok := skinColorString(req.SkinColor)
	if !ok {
		logCreateAvatarReject("skinColor", len(raw), "")
		writeJSON(w, http.StatusBadRequest, badRequestBody)
		return
	}
	if skin == "" {
		skin = "1"
	}

	accountsMu.Lock()
	acc := accounts[cookieValue(r, "AV_AUTH")]
	accountsMu.Unlock()
	if acc == nil {
		writeJSON(w, http.StatusNotFound, unknownSessionBody)
		return
	}
	token := randomToken(256)
	accountsMu.Lock()
	previous, previousID := *acc, nextAvatarID
	if acc.aid == "0" {
		// libgame's Room Party cells blank any friend whose avatarNo is a single character (NaRoomPartySearchCell/NormalCell::InitUI), so aids start at 10.
		nextAvatarID = max(nextAvatarID, minAvatarID-1) + 1
		if strconv.FormatUint(nextAvatarID, 10) == friendAID {
			nextAvatarID++
		}
		acc.aid = strconv.FormatUint(nextAvatarID, 10)
	}
	acc.name = req.Name
	acc.gender = gender
	acc.skin = skin
	acc.country = req.NationCode
	acc.inventoryCodes = appendUniqueItemCodes(accountInventoryCodes(acc), itemCodes)
	acc.itemCodes = append([]string(nil), itemCodes...)
	aid, sessionKey := acc.aid, acc.sessionKey
	accounts[token] = acc
	err = saveAccountsLocked()
	if err != nil {
		*acc, nextAvatarID = previous, previousID
		delete(accounts, token)
	}
	accountsMu.Unlock()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, `{"errorCode":"500"}`)
		return
	}

	w.Header().Add("Set-Cookie", avAuthSetCookie(token))
	payload, _ := json.Marshal(struct {
		Result avatarResult `json:"result"`
	}{avatarResult{
		AvatarID:    aid,
		Name:        req.Name,
		Gender:      req.AvatarType,
		SessionKey:  sessionKey,
		AvatarCode:  "ac",
		Items:       avatarItemsFromCodes(itemCodes),
		PetProfiles: []string{},
	}})
	writeJSON(w, http.StatusOK, string(payload))
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
		serveNotFound(w)
		return
	}
	writeJSON(w, http.StatusOK, createCompleteBody)
}

type avatarSaveItem struct {
	ItemCode *string         `json:"itemCode"`
	InvenSeq json.RawMessage `json:"invenSeq"`
}

func handleAvatarSaveV2(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut {
		serveNotFound(w)
		return
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, maxJSONBody+1))
	if err != nil || len(raw) > maxJSONBody {
		writeJSON(w, http.StatusBadRequest, badRequestBody)
		return
	}
	var req []avatarSaveItem
	if _, err := unmarshalNativeJSON(raw, &req); err != nil || req == nil {
		writeJSON(w, http.StatusBadRequest, badRequestBody)
		return
	}
	for _, item := range req {
		if item.ItemCode == nil || *item.ItemCode == "" {
			writeJSON(w, http.StatusBadRequest, badRequestBody)
			return
		}
		if len(item.InvenSeq) > 0 {
			var seq int
			if strings.TrimSpace(string(item.InvenSeq)) == "null" || json.Unmarshal(item.InvenSeq, &seq) != nil {
				writeJSON(w, http.StatusBadRequest, badRequestBody)
				return
			}
		}
	}

	accountsMu.Lock()
	acc := accounts[cookieValue(r, "AV_AUTH")]
	if acc == nil {
		accountsMu.Unlock()
		writeJSON(w, http.StatusNotFound, unknownSessionBody)
		return
	}
	serials := itemSerials(accountOwnedCodes(acc))
	itemCodes := make([]string, 0, len(req))
	for _, item := range req {
		code := *item.ItemCode
		if isSkinItemCode(code) || isAvatarBaseItem(code) {
			continue
		}
		if _, owned := serials[code]; !owned {
			accountsMu.Unlock()
			writeJSON(w, http.StatusBadRequest, badRequestBody)
			return
		}
		itemCodes = append(itemCodes, code)
	}
	previous := *acc
	acc.itemCodes = itemCodes
	err = saveAccountsLocked()
	if err != nil {
		*acc = previous
		accountsMu.Unlock()
		writeJSON(w, http.StatusInternalServerError, `{"errorCode":"500"}`)
		return
	}
	info := avatarInfoForAccount(accountSnapshot{
		aid: acc.aid, name: acc.name, gender: acc.gender, skin: acc.skin,
		country: acc.country, itemCodes: append([]string(nil), acc.itemCodes...),
		inventoryCodes: accountOwnedCodes(acc),
	})
	accountsMu.Unlock()
	payload, _ := json.Marshal(struct {
		Result avatarInfoResult `json:"result"`
	}{Result: info})
	writeJSON(w, http.StatusOK, string(payload))
}

func isSkinItemCode(code string) bool {
	return len(code) == 6 && strings.HasPrefix(code, "SKN") && isAllDigits(code[3:])
}

// tutorialAvatarTypes maps the four avatar IDs requested by
// NaCreateLayer::InitCloneAvatars to a synthesized appearance. The sex
// assignment is a deterministic stand-in; the authentic per-ID appearances are
// not in the archives.
var tutorialAvatarTypes = map[string]string{
	"1151262555912366100": "MALE",
	"1151262590612422506": "FEMALE",
	"1151262602112441105": "ANIMAL",
	"1151262571612391402": "MALE",
}

func handleAvatarInfo(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		serveNotFound(w)
		return
	}
	id, ok := strings.CutPrefix(r.URL.Path, "/v4/avatar/")
	if !ok || id == "" || strings.Contains(id, "/") || !isAllDigits(id) {
		serveNotFound(w)
		return
	}
	info := avatarInfoResult{
		AvatarID:    id,
		Gender:      "FEMALE",
		SType:       "NORMAL",
		Skin:        "1",
		Country:     "JP",
		Items:       []avatarItem{},
		PetProfiles: []string{},
	}
	if acc, ok := accountByAvatarID(id); ok {
		info = avatarInfoForAccount(acc)
	} else if id == friendAID {
		info.Name = friendName
		info.Items = avatarItemsFromCodes(workableLook("FEMALE"))
	} else if sex, ok := tutorialAvatarTypes[id]; ok {
		info.Name = "cherry"
		info.Gender = sex
		info.Items = avatarItemsFromCodes(workableLook(sex))
	}
	payload, _ := json.Marshal(struct {
		Result avatarInfoResult `json:"result"`
	}{Result: info})
	writeJSON(w, http.StatusOK, string(payload))
}

// accountSnapshot is a copy of the fields /v4/avatar/<id> serves, taken under
// accountsMu so the handler never reads fields while an avatar mutation writes.
type accountSnapshot struct {
	aid            string
	name           string
	gender         string
	skin           string
	country        string
	itemCodes      []string
	inventoryCodes []string
}

func avatarInfoForAccount(acc accountSnapshot) avatarInfoResult {
	gender, skin := acc.gender, acc.skin
	if gender == "" {
		gender = "FEMALE"
	}
	if skin == "" {
		skin = "1"
	}
	return avatarInfoResult{
		AvatarID: acc.aid, Name: acc.name, Gender: gender, SType: "NORMAL", Skin: skin,
		Country: acc.country, Items: avatarItemsFromInventory(appearanceItemCodes(gender, acc.itemCodes), acc.inventoryCodes),
		PetProfiles: []string{},
	}
}

func accountInventoryCodes(acc *account) []string {
	if acc.inventoryCodes != nil {
		return acc.inventoryCodes
	}
	return acc.itemCodes
}

func accountOwnedCodes(acc *account) []string {
	if acc == nil {
		return nil
	}
	return appendUniqueItemCodes(accountInventoryCodes(acc), curatedGrantCodes)
}

func isBasicFaceItemCode(code string) bool {
	if len(code) < 4 {
		return false
	}
	switch code[2:4] {
	case "EY", "MO", "EB", "NO", "HE":
		return true
	default:
		return false
	}
}

func appendUniqueItemCodes(existing, additions []string) []string {
	codes := make([]string, 0, len(existing)+len(additions))
	seen := make(map[string]struct{}, len(existing)+len(additions))
	for _, code := range append(append([]string(nil), existing...), additions...) {
		if _, ok := seen[code]; ok {
			continue
		}
		seen[code] = struct{}{}
		codes = append(codes, code)
	}
	return codes
}

func itemSerials(codes []string) map[string]string {
	serials := make(map[string]string, len(codes))
	for i, code := range codes {
		if _, exists := serials[code]; !exists {
			serials[code] = strconv.Itoa(i + 1)
		}
	}
	return serials
}

func avatarItemsFromInventory(codes, inventory []string) []avatarItem {
	wearableCodes := make([]string, 0, len(inventory))
	for _, code := range inventory {
		if !isBasicFaceItemCode(code) {
			wearableCodes = append(wearableCodes, code)
		}
	}
	serials := itemSerials(wearableCodes)
	items := make([]avatarItem, 0, len(codes))
	for _, code := range codes {
		serial := serials[code]
		if serial == "" {
			serial = "0"
		}
		items = append(items, avatarItem{CD: code, InvenSeq: serial, ColorAndTransparencies: []any{}})
	}
	return items
}

func accountByAvatarID(id string) (accountSnapshot, bool) {
	accountsMu.Lock()
	defer accountsMu.Unlock()
	for _, acc := range accounts {
		if acc.aid == id {
			return accountSnapshot{
				aid:            acc.aid,
				name:           acc.name,
				gender:         acc.gender,
				skin:           acc.skin,
				country:        acc.country,
				itemCodes:      appearanceItemCodes(acc.gender, acc.itemCodes),
				inventoryCodes: accountOwnedCodes(acc),
			}, true
		}
	}
	return accountSnapshot{}, false
}

func handleRoot(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		if kind, id, ok := parseDpPath(r.URL.Path); ok {
			serveDpPNG(w, kind, id)
			return
		}
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
	serveNotFound(w)
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
	return isAllDigits(parts[0]) && isAllDigits(parts[1])
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

func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

type account struct {
	accessToken    string
	sessionKey     string
	mid            string
	avatarUserID   string
	aid            string
	name           string
	gender         string
	skin           string
	country        string
	itemCodes      []string
	inventoryCodes []string
	roomItems      []roomItem
	nextRoomSeq    int64
	rooms          map[string]roomLayout
	presets        map[string]roomPreset
	pets           []petItem
}

// avatarItem is the object form parsed by sDataAvatar::SetData @0x1c0a39c.
type avatarItem struct {
	CD                     string `json:"cd"`
	InvenSeq               string `json:"invenSeq"`
	DyeType                int    `json:"dyeType"`
	ColorAndTransparencies []any  `json:"colorAndTransparencies"`
}

func avatarItemsFromCodes(codes []string) []avatarItem {
	items := make([]avatarItem, 0, len(codes))
	for _, cd := range codes {
		items = append(items, avatarItem{CD: cd, InvenSeq: "0", ColorAndTransparencies: []any{}})
	}
	return items
}

type avatarResult struct {
	AvatarID    string       `json:"avatarId"`
	Name        string       `json:"name"`
	Gender      string       `json:"gender"`
	SessionKey  string       `json:"sessionKey"`
	AvatarCode  string       `json:"avatarCode"`
	Items       []avatarItem `json:"items"`
	PetProfiles []string     `json:"petProfiles"`
}

type avatarInfoResult struct {
	AvatarID    string       `json:"avatarId"`
	Name        string       `json:"name"`
	Gender      string       `json:"gender"`
	SType       string       `json:"sType"`
	Skin        string       `json:"skin"`
	Country     string       `json:"country"`
	Items       []avatarItem `json:"items"`
	PetProfiles []string     `json:"petProfiles"`
}

const minAvatarID = 10

var (
	accountsMu       sync.Mutex
	accounts         = make(map[string]*account)
	latestAcc        *account
	nextAvatarID     uint64
	accountStorePath string
)

func newAccount() (*account, error) {
	acc := &account{
		accessToken:  randomToken(32),
		sessionKey:   randomToken(32),
		mid:          "1",
		avatarUserID: "3001",
		aid:          "0",
	}
	accountsMu.Lock()
	accounts[acc.accessToken] = acc
	previous := latestAcc
	latestAcc = acc
	err := saveAccountsLocked()
	if err != nil {
		delete(accounts, acc.accessToken)
		latestAcc = previous
	}
	accountsMu.Unlock()
	return acc, err
}

func currentAccount(r *http.Request) *account {
	accountsMu.Lock()
	defer accountsMu.Unlock()
	token := cookieValue(r, "accessToken")
	if token == "" {
		token = cookieValue(r, "cc") // Native createSession sends its guest access token as cc.
	}
	if token != "" {
		return accounts[token]
	}
	return latestAcc
}

func sessionResultBody(acc *account) string {
	accountsMu.Lock()
	sessionKey, mid, avatarUserID, aid := acc.sessionKey, acc.mid, acc.avatarUserID, acc.aid
	accountsMu.Unlock()
	return fmt.Sprintf(`{"sessionKey":"%s","mid":"%s","avatarUserId":"%s","aid":"%s","lineId":"","lineName":"","termAge":false}`,
		sessionKey, mid, avatarUserID, aid)
}

func avAuthSetCookie(token string) string {
	return `AV_AUTH="` + token + `"; Path=/`
}

func cookieValue(r *http.Request, name string) string {
	for _, part := range strings.Split(r.Header.Get("Cookie"), ";") {
		key, value, ok := strings.Cut(strings.TrimSpace(part), "=")
		if ok && key == name {
			return strings.Trim(value, `"`)
		}
	}
	return ""
}

func randomToken(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return base64.StdEncoding.EncodeToString(b)
}

func serveNotFound(w http.ResponseWriter) {
	writeJSON(w, http.StatusNotFound, notFoundBody)
}

func writeJSON(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(body))
}

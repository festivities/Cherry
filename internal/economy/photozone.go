package economy

import (
	"encoding/json"
	"net/http"
	"strings"

	"cherry/internal/httpx"
)

// Comic photozone (diary "record the changes?" YES -> NaShowComicViewPopup ->
// NaDiaryComicView::OnOpenUI @0x1da44cc -> ReqPhotoZoneShopInfo @0x1b8a0bc).
//   GET /v4/photozone/shop/info/<shopId>/<lang>   parser ResPhotoZoneShopInfo @0x1c1acdc
// shopId is the second popup id; "comic_shop" -> view type 1 (ConvertIdToViewType
// @0x1da43b4). The client renders the comic itself (SaveComicLayerToPNG), uploads
// it through OBS (existing /lineplay/d/upload.nhn) and posts via the normal diary
// write route (UploadToDiary @0x1e96a08, type 0), so nothing else is served.
// Frames are the 50 pre-cached dirs under files/cache/photozone/frame/<dir>/
// (animation.aniproj bg.png myavatar.png otheravatar.png thumbnail.png); the
// client loads them from disk and only downloads files that are missing.
// Action thumbs are UIResource/UIImage_hd/action_comic_{a,f,m}<id>.png; the ids
// below exist for all three genders.

var PhotoZoneFrames = []string{
	"0-2", "031", "032", "033", "034", "035", "036", "037", "038", "039-2",
	"040", "041", "042", "043", "044", "045", "046", "047", "048", "049",
	"050", "051", "052", "053", "054", "055", "056", "057", "058", "059",
	"060", "061", "062", "063", "064", "065", "066", "067-1", "068", "069-1", "070-2",
	"1", "2", "3", "4", "5", "6", "7", "8", "9",
}

var photoZoneActions = []int{
	159, 161, 162, 163, 166, 167, 198, 199, 200, 201, 202, 203, 204, 205,
	233, 234, 235, 236, 237, 238, 353, 354, 355, 356, 357, 358, 359, 360, 361, 362,
}

func photoZoneShopInfoBody(shopID string) ([]byte, error) {
	frames := make([]map[string]any, 0, len(PhotoZoneFrames))
	for _, dir := range PhotoZoneFrames {
		frames = append(frames, map[string]any{
			"frameId":           dir,
			"flagDisplayYn":     false,
			"displayEndDate":    "0", // atoll()
			"displayImagePath":  "photozone/frame/" + dir,
			"displayImagefiles": "animation.aniproj|bg.png|myavatar.png|otheravatar.png|thumbnail.png",
		})
	}
	actions := make([]map[string]any, 0, len(photoZoneActions))
	for _, id := range photoZoneActions {
		actions = append(actions, map[string]any{"actionId": id})
	}
	// Free: shopFlagYn false skips the SALE price UI; originPrice 0 passes the
	// balance check (Cash/Gem >= 0), so the camera button needs no purchase call.
	return json.Marshal(map[string]any{"result": map[string]any{
		"categoryId": "comic", "shopId": shopID, "shopType": "COMIC", "subjectType": 0,
		"shopFlag": "", "payType": "COIN", "originPrice": 0, "discountAmount": 0,
		"totalPrice": 0, "shopName": "Comic", "shopFlagYn": false, "endDate": "",
		"frameList": frames, "actionList": actions, "petActionList": []string{},
	}})
}

func HandlePhotoZoneShopInfo(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpx.ServeNotFound(w)
		return
	}
	shopID, lang, ok := strings.Cut(strings.TrimPrefix(r.URL.Path, "/v4/photozone/shop/info/"), "/")
	if !ok || shopID != "comic_shop" || lang == "" || strings.Contains(lang, "/") {
		httpx.ServeNotFound(w)
		return
	}
	body, err := photoZoneShopInfoBody(shopID)
	if err != nil {
		httpx.WriteJSON(w, http.StatusInternalServerError, httpx.SaveFailedBody)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, string(body))
}

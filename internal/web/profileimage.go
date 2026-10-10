package web

import (
	"encoding/json"
	"net/http"
	"regexp"
	"strings"

	"cherry/internal/httpx"
	"cherry/internal/store"
)

// Profile picture ("Take profile picture"): the client uploads two images to
// /lineplay/pr/upload.nhn (ReqObsProfileUploadURL @0x1bbb1c8), then sends
// POST /v4/avatar/profile/image {imageUrl, wbImageUrl, bgUrl}
// (ReqAvatarProfileUpload @0x1bc685c; the reply is only checked for HTTP success,
// ResAvatarProfileUpload @0x1a83b78). The paths come back through the profile row
// keys obsProfileImagePath / obsWholeBodyProfileImagePath / bg (sDataProfileWithAvatarID::ParseJson
// @0x1b6f000) and buddyRow.obsProfileImagePath.

var (
	profilePathRE = regexp.MustCompile(`^/lineplay/pr/[\x21-\x7e]{1,300}$`)
	profileBgRE   = regexp.MustCompile(`^profilebg/[0-9]{1,4}\.jpg$`)
)

func handleProfileImageSave(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost && r.Method != http.MethodPut {
		httpx.ServeNotFound(w)
		return
	}
	raw, err := httpx.ReadNativeBody(r)
	var req struct {
		ImageURL   string `json:"imageUrl"`
		WbImageURL string `json:"wbImageUrl"`
		BgURL      string `json:"bgUrl"`
	}
	if err == nil {
		_, err = httpx.UnmarshalNativeJSON(raw, &req)
	}
	if err != nil || !profilePathRE.MatchString(req.ImageURL) || strings.Contains(req.ImageURL, "..") ||
		req.WbImageURL != "" && (!profilePathRE.MatchString(req.WbImageURL) || strings.Contains(req.WbImageURL, "..")) ||
		req.BgURL != "" && !profileBgRE.MatchString(req.BgURL) {
		httpx.WriteJSON(w, http.StatusBadRequest, httpx.BadRequestBody)
		return
	}
	store.AccountsMu.Lock()
	defer store.AccountsMu.Unlock()
	acc := store.Accounts[httpx.CookieValue(r, "AV_AUTH")]
	if acc == nil {
		httpx.WriteJSON(w, http.StatusNotFound, httpx.UnknownSessionBody)
		return
	}
	// The oid is <userid>_<ctime>: only the requester's own uploads may become their picture.
	own := "/lineplay/pr/" + acc.Aid + "_"
	if !strings.HasPrefix(req.ImageURL, own) || req.WbImageURL != "" && !strings.HasPrefix(req.WbImageURL, own) {
		httpx.WriteJSON(w, http.StatusBadRequest, httpx.BadRequestBody)
		return
	}
	previous := acc.ProfileImage
	acc.ProfileImage = store.ProfileImage{Image: req.ImageURL, WholeBody: req.WbImageURL, Bg: req.BgURL}
	if err := store.SaveAccountsLocked(); err != nil {
		acc.ProfileImage = previous
		httpx.WriteJSON(w, http.StatusInternalServerError, httpx.SaveFailedBody)
		return
	}
	body, _ := json.Marshal(map[string]any{"result": true})
	httpx.WriteJSON(w, http.StatusOK, string(body))
}

package web

import (
	"encoding/json"
	"net/http"
	"slices"
	"strings"

	"cherry/internal/httpx"
	"cherry/internal/store"
)

// My Pins (Friends scene, My Pins tab; NaDispatcherFriend::ReqMyPinSyncList @0x1babff8,
// ReqMyPinAddData @0x1bac188, ReqMyPinRemoveData @0x1bce134). Pins belong to the requester
// (Account.Pins, in pin order). A pinned account that no longer exists (Delete Avatar) is skipped
// at read time, so it vanishes from every list.
//
//	GET  /v4/pin/list      -> {"result":{"pinList":[<row>...]}}  (ResMyPinSyncList @0x1b1f968)
//	GET  /v4/pin/add/<aid> -> {"result":true}                    (ResMyPinAddData @0x1b11790)
//	POST /v4/pin/delete    body "[id,id]" (raw numbers)          (ResMyPinRemoveData @0x1a86cb8)

// pinRow is one sDataMyPin element. Every key is read as a string except status (asInt).
type pinRow struct {
	AvatarNo           string `json:"avatarNo"`
	AvatarName         string `json:"avatarName"`
	ProfileImageURL    string `json:"profileImageUrl"`
	ObsProfileImageURL string `json:"obsProfileImageUrl"`
	ProfileBgURL       string `json:"profileBgUrl"`
	Status             int    `json:"status"`
	BadgeIcon          string `json:"badgeIcon"`
	UpdateDate         string `json:"updateDate"`
	BirthDayTime       string `json:"birthDayTime"`
}

func handlePinList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpx.ServeNotFound(w)
		return
	}
	owner, pins, ok := requesterPins(r)
	if !ok {
		httpx.WriteJSON(w, http.StatusNotFound, httpx.UnknownSessionBody)
		return
	}
	rows := []pinRow{}
	for _, aid := range pins {
		row, ok := pinRowFor(aid)
		if !ok {
			continue
		}
		store.SocialMu.Lock()
		row.Status = friendRelation(owner, true, aid)
		store.SocialMu.Unlock()
		rows = append(rows, row)
	}
	httpx.WriteObj(w, map[string]any{"result": map[string]any{"pinList": rows}})
}

func handlePinAdd(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpx.ServeNotFound(w)
		return
	}
	actor, ok := store.AccountForRequest(r)
	if !ok {
		httpx.WriteJSON(w, http.StatusNotFound, httpx.UnknownSessionBody)
		return
	}
	aid := strings.Trim(strings.TrimPrefix(r.URL.Path, "/v4/pin/add/"), "/")
	if !store.ValidAvatarID(aid) {
		httpx.ServeNotFound(w)
		return
	}
	// Self and unknown accounts get result false: the client shows no error popup for it (11218, flag 0).
	if aid == actor.Aid || !store.KnownSocialAvatar(aid) {
		httpx.WriteJSON(w, http.StatusOK, `{"result":false}`)
		return
	}
	found, err := editPins(httpx.CookieValue(r, "AV_AUTH"), func(pins []string) []string {
		if slices.Contains(pins, aid) {
			return pins
		}
		return append(slices.Clone(pins), aid)
	})
	writePinResult(w, found, err)
}

func handlePinDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpx.ServeNotFound(w)
		return
	}
	raw, err := httpx.ReadNativeBody(r)
	var ids []json.Number
	if err == nil {
		_, err = httpx.UnmarshalNativeJSON(raw, &ids)
	}
	if err != nil || ids == nil || len(ids) > 100 {
		httpx.WriteJSON(w, http.StatusBadRequest, httpx.BadRequestBody)
		return
	}
	drop := make(map[string]bool, len(ids))
	for _, id := range ids {
		if !store.ValidAvatarID(id.String()) {
			httpx.WriteJSON(w, http.StatusBadRequest, httpx.BadRequestBody)
			return
		}
		drop[id.String()] = true
	}
	found, err := editPins(httpx.CookieValue(r, "AV_AUTH"), func(pins []string) []string {
		kept := []string{}
		for _, aid := range pins {
			if !drop[aid] {
				kept = append(kept, aid)
			}
		}
		return kept
	})
	writePinResult(w, found, err)
}

func writePinResult(w http.ResponseWriter, found bool, err error) {
	switch {
	case !found:
		httpx.WriteJSON(w, http.StatusNotFound, httpx.UnknownSessionBody)
	case err != nil:
		httpx.WriteJSON(w, http.StatusInternalServerError, httpx.SaveFailedBody)
	default:
		httpx.WriteJSON(w, http.StatusOK, `{"result":true}`)
	}
}

// requesterPins returns the requester's aid and a copy of its pins; ok is false without a session.
func requesterPins(r *http.Request) (aid string, pins []string, ok bool) {
	store.AccountsMu.Lock()
	defer store.AccountsMu.Unlock()
	acc := store.Accounts[httpx.CookieValue(r, "AV_AUTH")]
	if acc == nil {
		return "", nil, false
	}
	return acc.Aid, slices.Clone(acc.Pins), true
}

// editPins replaces the pins of the account behind token with edit(pins) and saves them. found is
// false when no account has that token. Lock order: accountsMu only.
func editPins(token string, edit func([]string) []string) (found bool, err error) {
	store.AccountsMu.Lock()
	defer store.AccountsMu.Unlock()
	acc := store.Accounts[token]
	if acc == nil {
		return false, nil
	}
	prev := acc.Pins
	acc.Pins = edit(prev)
	if err := store.SaveAccountsLocked(); err != nil {
		acc.Pins = prev
		return true, err
	}
	return true, nil
}

// pinRowFor fills a row like the friend sync does: name, OBS profile path and background. The
// synthetic friend always exists; ok is false for an account that was deleted.
// ponytail: updateDate is always "0" (no pin time is stored).
func pinRowFor(aid string) (pinRow, bool) {
	row := pinRow{AvatarNo: aid, UpdateDate: "0"}
	if aid == store.FriendAID {
		row.AvatarName = store.FriendName()
		return row, true
	}
	acc, ok := store.AccountByAvatarID(aid)
	if !ok {
		return pinRow{}, false
	}
	row.AvatarName, row.ObsProfileImageURL, row.ProfileBgURL = acc.Name, acc.ProfileImage.Image, acc.ProfileImage.Bg
	return row, true
}

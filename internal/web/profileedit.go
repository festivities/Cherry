package web

import (
	"log"
	"net/http"
	"strings"
	"unicode"
	"unicode/utf8"

	"cherry/internal/httpx"
	"cherry/internal/store"
)

// Diary profile edit page (NaDiaryProfileEditView, spec rows-b item 4). Verified keys and types:
// GET diary/profile/<aid> is read by SetDiaryProfileData (0x1d5dc64); the POST body is built by
// ReqDiaryProfileSave (0x1bd6ba4); the save reply ResDiaryProfileSave (0x1a7867c) never reads its body
// and posts 11318 on success. Mileage (ResMileageInvenURL) and the photo list (SetDiaryProfileImgData)
// are answered empty.

// diaryOwnerStatus is the status value sent for every profile. SetDiaryProfileData keeps only 1..5 and
// the enum's meaning is not decoded; 1 is a guess that passes the gate.
const diaryOwnerStatus = 1

// diaryProfileMaxRunes caps each diary profile text field. The client's own input limit is not decoded.
const diaryProfileMaxRunes = 32

// profileStatusMaxRunes caps the Status Message (the edit view's counter reads 6/50).
const profileStatusMaxRunes = 50

// diaryProfileText accepts valid UTF-8 of at most diaryProfileMaxRunes characters with no control characters.
func diaryProfileText(s string) bool { return profileTextOK(s, diaryProfileMaxRunes) }

// profileTextOK accepts valid UTF-8 of at most maxRunes characters with no control characters.
func profileTextOK(s string, maxRunes int) bool {
	if !utf8.ValidString(s) || utf8.RuneCountInString(s) > maxRunes {
		return false
	}
	for _, c := range s {
		if unicode.IsControl(c) {
			return false
		}
	}
	return true
}

// profileIntro is the Status Message that GET /v4/profile/<aid> returns as result.intro.
func profileIntro(acc store.AccountSnapshot) string {
	if acc.DiaryProfile == nil {
		return ""
	}
	return acc.DiaryProfile.StatusMessage
}

// nicknameMaxRunes is the creation limit (NaCreateLayer::CheckValidNickName 0x1828498 rejects more than 20
// characters, error 48). The server applies the same rule to a later edit.
const nicknameMaxRunes = 20

// validNickname applies the creation rules: not empty and not only spaces (IsEmptyNickName 0x18282d8 looks at
// spaces only), at most nicknameMaxRunes characters, valid UTF-8 with no control characters.
func validNickname(s string) bool {
	return strings.Trim(s, " ") != "" && profileTextOK(s, nicknameMaxRunes)
}

// renameSocialCopies rewrites the nickname copied into aid's diary posts and guestbook rows at write time, so an
// edited nickname shows there too. Callers hold AccountsMu (lock order AccountsMu → SocialMu). A failed social save
// only logs: the account rename already stands and the copies are cosmetic.
func renameSocialCopies(aid, name string) {
	store.SocialMu.Lock()
	defer store.SocialMu.Unlock()
	changed := false
	for i := range store.DiaryPosts {
		if store.DiaryPosts[i].AvatarID == aid && store.DiaryPosts[i].Nickname != name {
			store.DiaryPosts[i].Nickname, changed = name, true
		}
	}
	for i := range store.GuestbookEntries {
		if store.GuestbookEntries[i].GuestAvtNo == aid && store.GuestbookEntries[i].GuestNickname != name {
			store.GuestbookEntries[i].GuestNickname, changed = name, true
		}
	}
	if changed {
		if err := store.SaveSocialLocked(); err != nil {
			log.Printf("cherry: nickname copies not saved aid=%s: %v", aid, err)
		}
	}
}

// handleAvatarProfileSave serves POST /v4/avatar/<aid>/profile (ReqAvatarProfileUpdateWithAvatarID,
// libgame 0x1bc6a90). The body is a flat JSON object of strings: name (the nickname), intro (the Status
// Message; the client strips newlines), bg and badgeIconNo. name and intro are stored; bg and badgeIconNo
// are ignored. The reply ResAvatarProfileUpdateWithAvatarID (0x1b2d61c) reads result as a bool.
func handleAvatarProfileSave(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpx.ServeNotFound(w)
		return
	}
	raw, err := httpx.ReadNativeBody(r)
	var req struct {
		Name  string `json:"name"`
		Intro string `json:"intro"`
	}
	if err == nil {
		_, err = httpx.UnmarshalNativeJSON(raw, &req)
	}
	if err != nil || req.Name != "" && !validNickname(req.Name) || !profileTextOK(req.Intro, profileStatusMaxRunes) {
		httpx.WriteJSON(w, http.StatusBadRequest, httpx.BadRequestBody)
		return
	}
	store.AccountsMu.Lock()
	defer store.AccountsMu.Unlock()
	acc := store.SessionAccountLocked(r)
	if acc == nil {
		httpx.WriteJSON(w, http.StatusNotFound, httpx.UnknownSessionBody)
		return
	}
	if acc.Aid != r.PathValue("aid") {
		httpx.ServeNotFound(w)
		return
	}
	previousName, previous := acc.Name, acc.DiaryProfile
	next := store.DiaryProfile{}
	if previous != nil {
		next = *previous
	}
	next.StatusMessage = req.Intro
	name := previousName
	if req.Name != "" { // the stock client always sends one (an empty nickname is refused before sending)
		name = req.Name
	}
	acc.Name, acc.DiaryProfile = name, &next
	if err := store.SaveAccountsLocked(); err != nil {
		acc.Name, acc.DiaryProfile = previousName, previous
		httpx.WriteJSON(w, http.StatusInternalServerError, httpx.SaveFailedBody)
		return
	}
	if name != previousName {
		renameSocialCopies(acc.Aid, name)
	}
	httpx.WriteJSON(w, http.StatusOK, `{"result":true}`)
}

// handleDiaryProfile serves GET /v4/diary/profile/<aid>. Every viewer except the owner gets a secret field
// blanked; its flag stays set so the client keeps masking it.
func handleDiaryProfile(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpx.ServeNotFound(w)
		return
	}
	aid := strings.Trim(strings.TrimPrefix(r.URL.Path, "/v4/diary/profile/"), "/")
	if !store.ValidAvatarID(aid) {
		httpx.ServeNotFound(w)
		return
	}
	var name string
	var dp store.DiaryProfile
	if aid == store.FriendAID {
		name = store.FriendName()
	} else {
		snap, ok := store.AccountByAvatarID(aid)
		if !ok {
			httpx.ServeNotFound(w)
			return
		}
		name = snap.Name
		if snap.DiaryProfile != nil {
			dp = *snap.DiaryProfile
		}
	}
	viewer, viewerOK := store.AccountForRequest(r)
	own := viewerOK && viewer.Aid == aid
	shown := func(value string, secret bool) string {
		if secret && !own {
			return ""
		}
		return value
	}
	profileJSON(w, map[string]any{
		"avatarId": aid, "nickname": name, "status": diaryOwnerStatus, "created": true,
		"location": shown(dp.Location, dp.LocationSecret), "locationSecret": dp.LocationSecret,
		"sex": shown(dp.Sex, dp.SexSecret), "sexSecret": dp.SexSecret,
		"birthDay": shown(dp.BirthDay, dp.BirthDaySecret), "birthDaySecret": dp.BirthDaySecret,
		"bloodType": shown(dp.BloodType, dp.BloodTypeSecret), "bloodTypeSecret": dp.BloodTypeSecret,
		"job": shown(dp.Job, dp.JobSecret), "jobSecret": dp.JobSecret,
	})
}

// handleDiaryProfileImage serves GET /v4/diary/profile/image/<aid> with an empty imageList. The account's
// avatar picture (Account.ProfileImage) is not used: the client's photo thumbnails are not verified to
// resolve a /lineplay/pr/ path, and the photo save has no store yet.
func handleDiaryProfileImage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpx.ServeNotFound(w)
		return
	}
	aid := strings.Trim(strings.TrimPrefix(r.URL.Path, "/v4/diary/profile/image/"), "/")
	if !store.KnownSocialAvatar(aid) {
		httpx.ServeNotFound(w)
		return
	}
	profileJSON(w, map[string]any{"imageList": []any{}})
}

// handleMileageInven serves GET /v4/mileage/inven/items with no items. Any failure here (even a 404)
// makes the profile edit page show its "Unable to connect" retry screen (ErrMileageInvenURL posts 11625).
func handleMileageInven(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpx.ServeNotFound(w)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, `{"result":{"items":[],"totalCount":"0","nextCursor":"","previousCursor":""}}`)
}

// handleProfileIntroSave serves POST /v4/profile/intro/. The body replaces the account's whole profile
// record. A wrong JSON type or an invalid text field is a 400, which the client shows as an error popup;
// it never reaches the retry screen (ErrDiaryProfileSave posts 11319).
func handleProfileIntroSave(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpx.ServeNotFound(w)
		return
	}
	raw, err := httpx.ReadNativeBody(r)
	var req struct {
		Location        string `json:"location"`
		LocationSecret  bool   `json:"locationSecret"`
		Sex             string `json:"sex"`
		SexSecret       bool   `json:"sexSecret"`
		BirthDay        string `json:"birthDay"`
		BirthDaySecret  bool   `json:"birthDaySecret"`
		BloodType       string `json:"bloodType"`
		BloodTypeSecret bool   `json:"bloodTypeSecret"`
		Job             string `json:"job"`
		JobSecret       bool   `json:"jobSecret"`
	}
	if err == nil {
		_, err = httpx.UnmarshalNativeJSON(raw, &req)
	}
	if err != nil || !diaryProfileText(req.Location) || !diaryProfileText(req.Sex) || !diaryProfileText(req.BirthDay) ||
		!diaryProfileText(req.BloodType) || !diaryProfileText(req.Job) {
		httpx.WriteJSON(w, http.StatusBadRequest, httpx.BadRequestBody)
		return
	}
	store.AccountsMu.Lock()
	defer store.AccountsMu.Unlock()
	acc := store.SessionAccountLocked(r)
	if acc == nil {
		httpx.WriteJSON(w, http.StatusNotFound, httpx.UnknownSessionBody)
		return
	}
	previous := acc.DiaryProfile
	next := &store.DiaryProfile{
		Location: req.Location, LocationSecret: req.LocationSecret,
		Sex: req.Sex, SexSecret: req.SexSecret,
		BirthDay: req.BirthDay, BirthDaySecret: req.BirthDaySecret,
		BloodType: req.BloodType, BloodTypeSecret: req.BloodTypeSecret,
		Job: req.Job, JobSecret: req.JobSecret,
	}
	if previous != nil {
		next.StatusMessage = previous.StatusMessage // the Status Message has its own route; keep it
	}
	acc.DiaryProfile = next
	if err := store.SaveAccountsLocked(); err != nil {
		acc.DiaryProfile = previous
		httpx.WriteJSON(w, http.StatusInternalServerError, httpx.SaveFailedBody)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, `{"result":true}`)
}

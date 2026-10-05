package main

import (
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

type savedGuestbookEntry struct {
	HostAvtNo                    string `json:"hostAvtNo"`
	GuestHistNo                  string `json:"guestHistNo"`
	GuestAvtNo                   string `json:"guestAvtNo"`
	GuestAvtLocale               string `json:"guestAvtLocale"`
	GuestNickname                string `json:"guestNickname"`
	GuestProfile                 string `json:"guestProfile"`
	GuestBg                      string `json:"guestBg"`
	GuestWholeProfile            string `json:"guestWholeProfile"`
	ObsProfileImagePath          string `json:"obsProfileImagePath"`
	ObsWholeBodyProfileImagePath string `json:"obsWholeBodyProfileImagePath"`
	Content                      string `json:"content"`
	RegYmdt                      string `json:"regYmdt"`
	Secret                       bool   `json:"secret"`
	Blind                        bool   `json:"blind"`
	IsPenalty                    bool   `json:"isPenalty"`
	GuestIsExist                 bool   `json:"guestIsExist"`
	Reply                        []any  `json:"reply"`
}

type guestbookRow struct {
	GuestHistNo             string `json:"guestHistNo"`
	GuestAvtNo              string `json:"guestAvtNo"`
	GuestAvtLocale          string `json:"guestAvtLocale"`
	GuestNickname           string `json:"guestNickname"`
	GuestProfile            string `json:"guestProfile"`
	GuestBg                 string `json:"guestBg"`
	GuestWholeProfile       string `json:"guestWholeProfile"`
	ObsProfileImagePath     string `json:"obsProfileImagePath"`
	ObsWholeBodyProfilePath string `json:"obsWholeBodyProfileImagePath"`
	Content                 string `json:"content"`
	RegYmdt                 string `json:"regYmdt"`
	Secret                  bool   `json:"secret"`
	Blind                   bool   `json:"blind"`
	IsPenalty               bool   `json:"isPenalty"`
	GuestIsExist            bool   `json:"guestIsExist"`
	Reply                   []any  `json:"reply"`
}

func (e savedGuestbookEntry) row() guestbookRow {
	reply := e.Reply
	if reply == nil {
		reply = []any{}
	}
	return guestbookRow{
		GuestHistNo: e.GuestHistNo, GuestAvtNo: e.GuestAvtNo, GuestAvtLocale: e.GuestAvtLocale,
		GuestNickname: e.GuestNickname, GuestProfile: e.GuestProfile, GuestBg: e.GuestBg,
		GuestWholeProfile: e.GuestWholeProfile, ObsProfileImagePath: e.ObsProfileImagePath,
		ObsWholeBodyProfilePath: e.ObsWholeBodyProfileImagePath, Content: e.Content, RegYmdt: e.RegYmdt,
		Secret: e.Secret, Blind: e.Blind, IsPenalty: e.IsPenalty, GuestIsExist: e.GuestIsExist, Reply: reply,
	}
}

func handleGuestbookWrite(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		serveNotFound(w)
		return
	}
	actor, ok := accountForRequest(r)
	if !ok {
		writeJSON(w, http.StatusNotFound, unknownSessionBody)
		return
	}
	raw, err := readNativeBody(r)
	var req struct {
		HostAvtNo  string `json:"hostAvtNo"`
		GuestAvtNo string `json:"guestAvtNo"`
		Content    string `json:"content"`
		Secret     *bool  `json:"secret"`
	}
	if err != nil {
		writeJSON(w, http.StatusBadRequest, badRequestBody)
		return
	}
	if _, err := unmarshalNativeJSON(raw, &req); err != nil || !validAvatarID(req.HostAvtNo) || !validAvatarID(req.GuestAvtNo) || req.GuestAvtNo != actor.aid || req.Secret == nil || strings.TrimSpace(req.Content) == "" || len(req.Content) > 4000 || !utf8.ValidString(req.Content) {
		writeJSON(w, http.StatusBadRequest, badRequestBody)
		return
	}
	if !knownSocialAvatar(req.HostAvtNo) || !knownSocialAvatar(req.GuestAvtNo) {
		serveNotFound(w)
		return
	}

	socialMu.Lock()
	if nextGuestHistNo == ^uint64(0) {
		socialMu.Unlock()
		writeJSON(w, http.StatusInternalServerError, saveFailedBody)
		return
	}
	previousSeq := nextGuestHistNo
	nextGuestHistNo++
	locale := actor.country
	if locale == "" {
		locale = "JP"
	}
	nickname := actor.name
	if nickname == "" {
		nickname = "guest"
	}
	entry := savedGuestbookEntry{
		HostAvtNo: req.HostAvtNo, GuestHistNo: strconv.FormatUint(nextGuestHistNo, 10),
		GuestAvtNo: actor.aid, GuestAvtLocale: locale, GuestNickname: nickname,
		GuestProfile: "", GuestBg: "", GuestWholeProfile: "", ObsProfileImagePath: "",
		ObsWholeBodyProfileImagePath: "", Content: req.Content,
		RegYmdt: strconv.FormatInt(time.Now().UnixMilli(), 10), Secret: *req.Secret,
		Blind: false, IsPenalty: false, GuestIsExist: true, Reply: []any{},
	}
	guestbookEntries = append([]savedGuestbookEntry{entry}, guestbookEntries...)
	err = saveSocialLocked()
	if err != nil {
		guestbookEntries = guestbookEntries[1:]
		nextGuestHistNo = previousSeq
	}
	socialMu.Unlock()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, saveFailedBody)
		return
	}
	data, err := json.Marshal(map[string]any{"result": entry.row()})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, saveFailedBody)
		return
	}
	writeJSON(w, http.StatusOK, string(data))
}

func handleGuestbookList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		serveNotFound(w)
		return
	}
	host := strings.Trim(strings.TrimPrefix(r.URL.Path, "/v4/guestbook3/list/"), "/")
	if !validAvatarID(host) || !knownSocialAvatar(host) {
		serveNotFound(w)
		return
	}
	lastSeq, size, ok := cursorPage(r)
	if !ok {
		writeJSON(w, http.StatusBadRequest, badRequestBody)
		return
	}
	actor, actorOK := accountForRequest(r)
	socialMu.Lock()
	entries := make([]savedGuestbookEntry, 0)
	for _, entry := range guestbookEntries {
		if entry.HostAvtNo != host || (lastSeq >= 0 && guestbookSeq(entry.GuestHistNo) >= uint64(lastSeq)) {
			continue
		}
		if entry.Secret && (!actorOK || (actor.aid != host && actor.aid != entry.GuestAvtNo)) {
			continue
		}
		entries = append(entries, entry)
	}
	socialMu.Unlock()
	sort.SliceStable(entries, func(i, j int) bool {
		return guestbookSeq(entries[i].GuestHistNo) > guestbookSeq(entries[j].GuestHistNo)
	})
	lastData := len(entries) <= size
	if len(entries) > size {
		entries = entries[:size]
	}
	items := make([]guestbookRow, 0, len(entries))
	for _, entry := range entries {
		items = append(items, entry.row())
	}
	data, err := json.Marshal(map[string]any{"result": map[string]any{"lastData": lastData, "items": items}})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, saveFailedBody)
		return
	}
	writeJSON(w, http.StatusOK, string(data))
}

func handleGuestbookCount(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		serveNotFound(w)
		return
	}
	host := strings.Trim(strings.TrimPrefix(r.URL.Path, "/v4/guestbook3/count/"), "/")
	if !validAvatarID(host) || !knownSocialAvatar(host) {
		serveNotFound(w)
		return
	}
	socialMu.Lock()
	count := 0
	for _, entry := range guestbookEntries {
		if entry.HostAvtNo == host {
			count++
		}
	}
	socialMu.Unlock()
	data, _ := json.Marshal(map[string]any{"result": map[string]any{"hostAvtNo": host, "count": count}})
	writeJSON(w, http.StatusOK, string(data))
}

func handleGuestbookErase(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		serveNotFound(w)
		return
	}
	actor, ok := accountForRequest(r)
	if !ok {
		writeJSON(w, http.StatusNotFound, unknownSessionBody)
		return
	}
	rest := strings.Trim(strings.TrimPrefix(r.URL.Path, "/v4/guestbook/erase/"), "/")
	parts := strings.Split(rest, "/")
	if len(parts) != 2 || !validAvatarID(parts[0]) || !validAvatarID(parts[1]) || !knownSocialAvatar(parts[0]) {
		serveNotFound(w)
		return
	}
	raw, err := readNativeBody(r)
	var body any
	if err != nil {
		writeJSON(w, http.StatusBadRequest, badRequestBody)
		return
	}
	if _, err := unmarshalNativeJSON(raw, &body); err != nil || body != nil {
		writeJSON(w, http.StatusBadRequest, badRequestBody)
		return
	}
	socialMu.Lock()
	index := -1
	for i, entry := range guestbookEntries {
		if entry.HostAvtNo == parts[0] && entry.GuestHistNo == parts[1] {
			if actor.aid != parts[0] && actor.aid != entry.GuestAvtNo {
				socialMu.Unlock()
				serveNotFound(w)
				return
			}
			index = i
			break
		}
	}
	if index >= 0 {
		previous := append([]savedGuestbookEntry(nil), guestbookEntries...)
		guestbookEntries = append(guestbookEntries[:index], guestbookEntries[index+1:]...)
		err = saveSocialLocked()
		if err != nil {
			guestbookEntries = previous
		}
	}
	socialMu.Unlock()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, saveFailedBody)
		return
	}
	writeJSON(w, http.StatusOK, `{"result":true}`)
}

func guestbookSeq(value string) uint64 {
	seq, _ := strconv.ParseUint(value, 10, 64)
	return seq
}

func validAvatarID(id string) bool {
	return len(id) > 0 && len(id) <= 20 && isAllDigits(id)
}

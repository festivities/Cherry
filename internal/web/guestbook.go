package web

import (
	"encoding/json"
	"net/http"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"cherry/internal/httpx"
	"cherry/internal/store"
)

func handleGuestbookWrite(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpx.ServeNotFound(w)
		return
	}
	actor, ok := store.AccountForRequest(r)
	if !ok {
		httpx.WriteJSON(w, http.StatusNotFound, httpx.UnknownSessionBody)
		return
	}
	raw, err := httpx.ReadNativeBody(r)
	var req struct {
		HostAvtNo  string `json:"hostAvtNo"`
		GuestAvtNo string `json:"guestAvtNo"`
		Content    string `json:"content"`
		Secret     *bool  `json:"secret"`
	}
	if err != nil {
		httpx.WriteJSON(w, http.StatusBadRequest, httpx.BadRequestBody)
		return
	}
	if _, err := httpx.UnmarshalNativeJSON(raw, &req); err != nil || !store.ValidAvatarID(req.HostAvtNo) || !store.ValidAvatarID(req.GuestAvtNo) || req.GuestAvtNo != actor.Aid || req.Secret == nil || strings.TrimSpace(req.Content) == "" || len(req.Content) > 4000 || !utf8.ValidString(req.Content) {
		httpx.WriteJSON(w, http.StatusBadRequest, httpx.BadRequestBody)
		return
	}
	if !store.KnownSocialAvatar(req.HostAvtNo) || !store.KnownSocialAvatar(req.GuestAvtNo) {
		httpx.ServeNotFound(w)
		return
	}

	store.SocialMu.Lock()
	if store.NextGuestHistNo == ^uint64(0) {
		store.SocialMu.Unlock()
		httpx.WriteJSON(w, http.StatusInternalServerError, httpx.SaveFailedBody)
		return
	}
	previousSeq := store.NextGuestHistNo
	store.NextGuestHistNo++
	locale := actor.Country
	if locale == "" {
		locale = "JP"
	}
	nickname := actor.Name
	if nickname == "" {
		nickname = "guest"
	}
	entry := store.SavedGuestbookEntry{
		HostAvtNo: req.HostAvtNo, GuestHistNo: strconv.FormatUint(store.NextGuestHistNo, 10),
		GuestAvtNo: actor.Aid, GuestAvtLocale: locale, GuestNickname: nickname,
		GuestProfile: "", GuestBg: "", GuestWholeProfile: "", ObsProfileImagePath: "",
		ObsWholeBodyProfileImagePath: "", Content: req.Content,
		RegYmdt: strconv.FormatInt(time.Now().UnixMilli(), 10), Secret: *req.Secret,
		Blind: false, IsPenalty: false, GuestIsExist: true, Reply: []store.GuestbookReply{},
	}
	store.GuestbookEntries = append([]store.SavedGuestbookEntry{entry}, store.GuestbookEntries...)
	err = store.SaveSocialLocked()
	if err != nil {
		store.GuestbookEntries = store.GuestbookEntries[1:]
		store.NextGuestHistNo = previousSeq
	}
	store.SocialMu.Unlock()
	if err != nil {
		httpx.WriteJSON(w, http.StatusInternalServerError, httpx.SaveFailedBody)
		return
	}
	data, err := json.Marshal(map[string]any{"result": entry.Row()})
	if err != nil {
		httpx.WriteJSON(w, http.StatusInternalServerError, httpx.SaveFailedBody)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, string(data))
}

func handleGuestbookList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpx.ServeNotFound(w)
		return
	}
	host := strings.Trim(strings.TrimPrefix(r.URL.Path, "/v4/guestbook3/list/"), "/")
	if !store.ValidAvatarID(host) || !store.KnownSocialAvatar(host) {
		httpx.ServeNotFound(w)
		return
	}
	lastSeq, size, ok := cursorPage(r)
	if !ok {
		httpx.WriteJSON(w, http.StatusBadRequest, httpx.BadRequestBody)
		return
	}
	actor, actorOK := store.AccountForRequest(r)
	store.SocialMu.Lock()
	entries := make([]store.SavedGuestbookEntry, 0)
	for _, entry := range store.GuestbookEntries {
		if entry.HostAvtNo != host || (lastSeq >= 0 && guestbookSeq(entry.GuestHistNo) >= uint64(lastSeq)) {
			continue
		}
		if entry.Secret && (!actorOK || (actor.Aid != host && actor.Aid != entry.GuestAvtNo)) {
			continue
		}
		entries = append(entries, entry)
	}
	store.SocialMu.Unlock()
	sort.SliceStable(entries, func(i, j int) bool {
		return guestbookSeq(entries[i].GuestHistNo) > guestbookSeq(entries[j].GuestHistNo)
	})
	lastData := len(entries) <= size
	if len(entries) > size {
		entries = entries[:size]
	}
	items := make([]store.GuestbookRow, 0, len(entries))
	for _, entry := range entries {
		items = append(items, entry.Row())
	}
	data, err := json.Marshal(map[string]any{"result": map[string]any{"lastData": lastData, "items": items}})
	if err != nil {
		httpx.WriteJSON(w, http.StatusInternalServerError, httpx.SaveFailedBody)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, string(data))
}

func handleGuestbookCount(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpx.ServeNotFound(w)
		return
	}
	host := strings.Trim(strings.TrimPrefix(r.URL.Path, "/v4/guestbook3/count/"), "/")
	if !store.ValidAvatarID(host) || !store.KnownSocialAvatar(host) {
		httpx.ServeNotFound(w)
		return
	}
	store.SocialMu.Lock()
	count := 0
	for _, entry := range store.GuestbookEntries {
		if entry.HostAvtNo == host {
			count++
		}
	}
	store.SocialMu.Unlock()
	data, _ := json.Marshal(map[string]any{"result": map[string]any{"hostAvtNo": host, "count": count}})
	httpx.WriteJSON(w, http.StatusOK, string(data))
}

func handleGuestbookErase(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpx.ServeNotFound(w)
		return
	}
	actor, ok := store.AccountForRequest(r)
	if !ok {
		httpx.WriteJSON(w, http.StatusNotFound, httpx.UnknownSessionBody)
		return
	}
	rest := strings.Trim(strings.TrimPrefix(r.URL.Path, "/v4/guestbook/erase/"), "/")
	parts := strings.Split(rest, "/")
	if len(parts) != 2 || !store.ValidAvatarID(parts[0]) || !store.ValidAvatarID(parts[1]) || !store.KnownSocialAvatar(parts[0]) {
		httpx.ServeNotFound(w)
		return
	}
	raw, err := httpx.ReadNativeBody(r)
	var body any
	if err != nil {
		httpx.WriteJSON(w, http.StatusBadRequest, httpx.BadRequestBody)
		return
	}
	if _, err := httpx.UnmarshalNativeJSON(raw, &body); err != nil || body != nil {
		httpx.WriteJSON(w, http.StatusBadRequest, httpx.BadRequestBody)
		return
	}
	store.SocialMu.Lock()
	index := -1
	for i, entry := range store.GuestbookEntries {
		if entry.HostAvtNo == parts[0] && entry.GuestHistNo == parts[1] {
			if actor.Aid != parts[0] && actor.Aid != entry.GuestAvtNo {
				store.SocialMu.Unlock()
				httpx.ServeNotFound(w)
				return
			}
			index = i
			break
		}
	}
	if index >= 0 {
		previous := append([]store.SavedGuestbookEntry(nil), store.GuestbookEntries...)
		store.GuestbookEntries = append(store.GuestbookEntries[:index], store.GuestbookEntries[index+1:]...)
		err = store.SaveSocialLocked()
		if err != nil {
			store.GuestbookEntries = previous
		}
	}
	store.SocialMu.Unlock()
	if err != nil {
		httpx.WriteJSON(w, http.StatusInternalServerError, httpx.SaveFailedBody)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, `{"result":true}`)
}

// handleGuestbookReplyWrite is POST /v4/guestbookreply/write (spec 2.2). The reply goes into entry parentHistNo of
// hostAvtNo's guestbook. Only the host and that entry's guest writer may reply; anyone else gets 404, the same answer
// as a secret entry they cannot see. The body's guestAvtNo is not read: the entry itself decides who the guest is.
func handleGuestbookReplyWrite(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpx.ServeNotFound(w)
		return
	}
	actor, ok := store.AccountForRequest(r)
	if !ok {
		httpx.WriteJSON(w, http.StatusNotFound, httpx.UnknownSessionBody)
		return
	}
	raw, err := httpx.ReadNativeBody(r)
	if err != nil {
		httpx.WriteJSON(w, http.StatusBadRequest, httpx.BadRequestBody)
		return
	}
	var req struct {
		HostAvtNo    string `json:"hostAvtNo"`
		ReplyAvtNo   string `json:"replyAvtNo"`
		ParentHistNo string `json:"parentHistNo"`
		Content      string `json:"content"`
	}
	if _, err := httpx.UnmarshalNativeJSON(raw, &req); err != nil || !store.ValidAvatarID(req.HostAvtNo) || req.ReplyAvtNo != actor.Aid || !httpx.IsAllDigits(req.ParentHistNo) || strings.TrimSpace(req.Content) == "" || len(req.Content) > 4000 || !utf8.ValidString(req.Content) {
		httpx.WriteJSON(w, http.StatusBadRequest, httpx.BadRequestBody)
		return
	}
	locale := actor.Country
	if locale == "" {
		locale = "JP"
	}
	nickname := actor.Name
	if nickname == "" {
		nickname = "guest"
	}

	store.SocialMu.Lock()
	index := -1
	for i, entry := range store.GuestbookEntries {
		if entry.HostAvtNo == req.HostAvtNo && entry.GuestHistNo == req.ParentHistNo {
			index = i
			break
		}
	}
	if index < 0 || (actor.Aid != req.HostAvtNo && actor.Aid != store.GuestbookEntries[index].GuestAvtNo) {
		store.SocialMu.Unlock()
		httpx.ServeNotFound(w)
		return
	}
	if store.NextReplyHistNo == ^uint64(0) {
		store.SocialMu.Unlock()
		httpx.WriteJSON(w, http.StatusInternalServerError, httpx.SaveFailedBody)
		return
	}
	store.NextReplyHistNo++
	reply := store.GuestbookReply{
		ReplyHistNo: strconv.FormatUint(store.NextReplyHistNo, 10), ParentHistNo: req.ParentHistNo,
		ReplyAvtNo: actor.Aid, ReplyAvtLocale: locale, ReplyNickname: nickname, Content: req.Content,
		RegYmdt: strconv.FormatInt(time.Now().UnixMilli(), 10),
	}
	prev := store.GuestbookEntries[index].Reply
	store.GuestbookEntries[index].Reply = append(slices.Clone(prev), reply)
	err = store.SaveSocialLocked()
	if err != nil {
		store.GuestbookEntries[index].Reply = prev
		store.NextReplyHistNo--
	}
	store.SocialMu.Unlock()
	if err != nil {
		httpx.WriteJSON(w, http.StatusInternalServerError, httpx.SaveFailedBody)
		return
	}
	data, err := json.Marshal(map[string]any{"result": reply})
	if err != nil {
		httpx.WriteJSON(w, http.StatusInternalServerError, httpx.SaveFailedBody)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, string(data))
}

// replyIndexLocked finds a guestbook reply by replyHistNo: the entry index and the reply index, -1 and -1 when none.
// Caller holds SocialMu.
func replyIndexLocked(replyHistNo string) (entry, reply int) {
	for i, e := range store.GuestbookEntries {
		for j, rep := range e.Reply {
			if rep.ReplyHistNo == replyHistNo {
				return i, j
			}
		}
	}
	return -1, -1
}

// handleGuestbookReplyErase is GET /v4/guestbookreply/erase/<host>/<replyHistNo>. The reply's author or the host may
// erase it; a reply that no longer exists answers true (idempotent). Anyone else gets 400.
func handleGuestbookReplyErase(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpx.ServeNotFound(w)
		return
	}
	actor, ok := store.AccountForRequest(r)
	if !ok {
		httpx.WriteJSON(w, http.StatusNotFound, httpx.UnknownSessionBody)
		return
	}
	parts, ok := diaryPathParts(r.URL.Path, "/v4/guestbookreply/erase/", 2)
	if !ok {
		httpx.ServeNotFound(w)
		return
	}
	host := parts[0]
	store.SocialMu.Lock()
	entry, reply := replyIndexLocked(parts[1])
	var err error
	if entry >= 0 && store.GuestbookEntries[entry].HostAvtNo == host {
		prev := store.GuestbookEntries[entry].Reply
		if actor.Aid != host && actor.Aid != prev[reply].ReplyAvtNo {
			store.SocialMu.Unlock()
			httpx.WriteJSON(w, http.StatusBadRequest, httpx.BadRequestBody)
			return
		}
		store.GuestbookEntries[entry].Reply = slices.Delete(slices.Clone(prev), reply, reply+1)
		err = store.SaveSocialLocked()
		if err != nil {
			store.GuestbookEntries[entry].Reply = prev
		}
	}
	store.SocialMu.Unlock()
	if err != nil {
		httpx.WriteJSON(w, http.StatusInternalServerError, httpx.SaveFailedBody)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, `{"result":true}`)
}

// handleGuestbookReplyIsErased is GET /v4/guestbookreply/isErased/<a>/<replyHistNo>: true once the reply is gone.
func handleGuestbookReplyIsErased(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpx.ServeNotFound(w)
		return
	}
	parts, ok := diaryPathParts(r.URL.Path, "/v4/guestbookreply/isErased/", 2)
	if !ok {
		httpx.ServeNotFound(w)
		return
	}
	store.SocialMu.Lock()
	entry, _ := replyIndexLocked(parts[1])
	store.SocialMu.Unlock()
	httpx.WriteJSON(w, http.StatusOK, `{"result":`+strconv.FormatBool(entry < 0)+`}`)
}

// handleGuestbookIsErased is GET /v4/guestbook/isErased/<a>/<guestHistNo>: true once the entry is gone. Entry numbers
// are unique across hosts, so the host segment is only checked for shape.
func handleGuestbookIsErased(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpx.ServeNotFound(w)
		return
	}
	parts, ok := diaryPathParts(r.URL.Path, "/v4/guestbook/isErased/", 2)
	if !ok {
		httpx.ServeNotFound(w)
		return
	}
	store.SocialMu.Lock()
	exists := slices.ContainsFunc(store.GuestbookEntries, func(e store.SavedGuestbookEntry) bool { return e.GuestHistNo == parts[1] })
	store.SocialMu.Unlock()
	httpx.WriteJSON(w, http.StatusOK, `{"result":`+strconv.FormatBool(!exists)+`}`)
}

func guestbookSeq(value string) uint64 {
	seq, _ := strconv.ParseUint(value, 10, 64)
	return seq
}

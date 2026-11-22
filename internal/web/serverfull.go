package web

import (
	"encoding/json"
	"log"
	"net/http"
	"strconv"
	"sync"
	"time"

	"cherry/internal/httpx"
	"cherry/internal/lpn"
)

// A full server (limits.max-players, lpn.ServerFull) refuses a new player at app start with the client's own maintenance
// popup, which shows server text and keeps the login (spec %TEMP%\opencode\cherry\serverfull-20261122\serverfull-spec.md,
// main-agent checked in IDA: ErrCheckConfiguration 0x1a81e48, CheckMaintenance 0x204c570, ErrCheckSessionWithToken
// 0x1b6a62c):
//  1. GET /v4/setInitConf answers 503 (any errorCode but 1017, which is the region popup). The client posts 10036 (its
//     "Network Error" box, unless a schedule popup is up) and calls CheckMaintenance, which GETs notice.json2.
//  2. GET http://<static>/img/read/notice/notice.json2 (plain port; this mux sees /notice/notice.json2) answers the
//     maintenance JSON. The client shows NaScheduleErrPopup: title string 10079 "Maintenance in progress." (fixed),
//     message.ko / .ja / .en by device language (others get en), and the start ~ end line.
//  3. Before showing it the client GETs /v4/maintenance/whitelist/lineplay; an empty list keeps the popup up (a matching
//     udid or aid entry would replace it with "Are you the staff of the LINE PLAY?").
// The login is never checked, so avcookie.na stays. In-game clients fetch notice.json2 too (after avatar info, Garden
// updates, pushes, foreground), and neither request carries an identity, so the notice goes only to the address refused
// at step 1, once, within fullNoticeTTL. ponytail: a player behind the same shared address who fetches the notice first
// takes it (that one sees the popup, the refused one the stock Network Error box); a per-device key needs a client change.

const (
	fullNoticeTTL = 10 * time.Second
	// The popup window. The client shows it only while its own clock is inside [start, end] (CheckDuration 0x200cb8c), and a
	// shown popup stays valid until end within that run, so start leaves room for a slow device clock and end stays short.
	fullNoticeBefore = 10 * time.Minute
	fullNoticeAfter  = 5 * time.Minute
)

// fullNoticeText is the popup's reason line per client language key (Cherry-made text).
var fullNoticeText = map[string]string{
	"en": "The server is full right now. Please try again in a few minutes.",
	"ko": "서버가 지금 가득 찼습니다. 잠시 후 다시 시도해 주세요.",
	"ja": "ただいまサーバーが満員です。しばらく経ってから、もう一度お試しください。",
}

// fullNotices holds the addresses refused at setInitConf until they fetch the notice or fullNoticeTTL passes.
var fullNotices = struct {
	sync.Mutex
	until map[string]time.Time
}{until: map[string]time.Time{}}

func rememberFullNotice(addr string, now time.Time) {
	fullNotices.Lock()
	defer fullNotices.Unlock()
	for a, t := range fullNotices.until {
		if now.After(t) {
			delete(fullNotices.until, a)
		}
	}
	fullNotices.until[addr] = now.Add(fullNoticeTTL)
}

// takeFullNotice reports whether addr was refused within fullNoticeTTL, and forgets it.
func takeFullNotice(addr string, now time.Time) bool {
	fullNotices.Lock()
	defer fullNotices.Unlock()
	t, ok := fullNotices.until[addr]
	delete(fullNotices.until, addr)
	return ok && !now.After(t)
}

// fullNoticeBody is the notice.json2 reply (ResMaintenanceWithCachePolicy 0x1aa2b10: root keys, no result wrapper; seq
// asInt, the rest asString; start and end are epoch milliseconds, which the client divides by 1000).
func fullNoticeBody(now time.Time) string {
	ms := func(t time.Time) string { return strconv.FormatInt(t.UnixMilli(), 10) }
	b, _ := json.Marshal(map[string]any{
		"seq": 1, "target": "all", "type": "maint", "displayType": "popup",
		"start": ms(now.Add(-fullNoticeBefore)), "end": ms(now.Add(fullNoticeAfter)),
		"message": fullNoticeText,
	})
	return string(b)
}

// handleSetInitConf serves GET /v4/setInitConf, the first request of every app start (SplashScene::setupPatcher).
func handleSetInitConf(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpx.ServeNotFound(w)
		return
	}
	if lpn.ServerFull() {
		addr := httpx.RemoteHost(r.RemoteAddr)
		rememberFullNotice(addr, time.Now())
		log.Printf("cherry: setInitConf refused: server full")
		httpx.WriteJSON(w, http.StatusServiceUnavailable, `{"errorCode":"503","errorMessage":"cherry: server full"}`)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, setInitConfBody)
}

// handleMaintenanceNotice serves GET notice/notice.json2: the server-full popup for an address just refused at
// setInitConf, else 404 = no maintenance (ErrMaintenanceWithCachePolicy 0x1a50d4c does nothing), as before.
func handleMaintenanceNotice(w http.ResponseWriter, r *http.Request) {
	now := time.Now()
	if r.Method != http.MethodGet || !takeFullNotice(httpx.RemoteHost(r.RemoteAddr), now) {
		httpx.ServeNotFound(w)
		return
	}
	log.Printf("cherry: server-full notice served")
	httpx.WriteJSON(w, http.StatusOK, fullNoticeBody(now))
}

// handleMaintenanceWhitelist serves GET /v4/maintenance/whitelist/lineplay (ReqWhiteListInLinePlay 0x1b90058, sent when the
// maintenance popup shows; ResWhiteListInLinePlay 0x1b44620 reads result as an array). Nobody is staff, so the popup stays.
func handleMaintenanceWhitelist(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpx.ServeNotFound(w)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, `{"result":[]}`)
}

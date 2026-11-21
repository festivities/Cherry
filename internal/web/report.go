package web

import (
	"errors"
	"log"
	"net/http"
	"strconv"
	"time"

	"cherry/internal/economy"
	"cherry/internal/httpx"
	"cherry/internal/store"
)

// Player reports over HTTP: POST /v4/report (1:1 and Lounge chat, categoryId chat, chat_img or tchat) and the four diary
// routes (diarysocial.go). The reporter is the account of the request's AV_AUTH, never a body field. A report the store
// stores, and one the daily cap refuses, both answer {"result":true}, which is what the client treats as success; the cap
// refusal is logged as a count line only. Report text is never logged.

// chatReportSources maps the categoryId of POST /v4/report to its report source.
var chatReportSources = map[string]string{"chat": store.ReportChat, "chat_img": store.ReportChatImg, "tchat": store.ReportTChat}

// handleChatReport answers POST /v4/report.
func handleChatReport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpx.ServeNotFound(w)
		return
	}
	acc, ok := store.AccountForRequest(r)
	if !ok {
		httpx.WriteJSON(w, http.StatusNotFound, httpx.UnknownSessionBody)
		return
	}
	body, ok := reportBody(w, r)
	if !ok {
		return
	}
	source, ok := chatReportSources[reportText(body["categoryId"])]
	if !ok {
		httpx.WriteJSON(w, http.StatusBadRequest, httpx.BadRequestBody)
		return
	}
	fileReport(w, store.ReportIn{
		Source: source, Reporter: acc.Aid, Target: reportText(body["targetAvatarId"]), Reason: reportText(body["reportType"]),
		Message: reportText(body["reportMsg"]), RefID: reportText(body["refId"]), RefDetail: reportText(body["refDtl"]),
	})
}

// reportBody reads a report route's JSON object. A body that does not read answers 400 and ok is false.
func reportBody(w http.ResponseWriter, r *http.Request) (map[string]any, bool) {
	raw, err := httpx.ReadNativeBody(r)
	if err != nil {
		httpx.WriteJSON(w, http.StatusBadRequest, httpx.BadRequestBody)
		return nil, false
	}
	var body map[string]any
	if _, err := httpx.UnmarshalNativeJSON(raw, &body); err != nil {
		httpx.WriteJSON(w, http.StatusBadRequest, httpx.BadRequestBody)
		return nil, false
	}
	return body, true
}

// reportText is a report field as text: a string as it is, a number as its decimal digits (the diary routes send their ids
// either way), and "" for anything else or a missing key.
func reportText(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	}
	return ""
}

// fileReport stores in (the store checks it) and writes the client's answer: 400 for a report the store refuses as invalid,
// 500 when the write fails, and {"result":true} otherwise.
func fileReport(w http.ResponseWriter, in store.ReportIn) {
	stored, err := store.FileReport(in, economy.GameDayStart(time.Now()))
	switch {
	case errors.Is(err, store.ErrBadInput):
		log.Printf("cherry: report refused source=%s: %v", in.Source, err)
		httpx.WriteJSON(w, http.StatusBadRequest, httpx.BadRequestBody)
		return
	case err != nil:
		log.Printf("cherry: report not stored source=%s: %v", in.Source, err)
		httpx.WriteJSON(w, http.StatusInternalServerError, httpx.SaveFailedBody)
		return
	case !stored:
		log.Printf("cherry: report over the daily cap, not stored source=%s", in.Source)
	}
	httpx.WriteJSON(w, http.StatusOK, `{"result":true}`)
}

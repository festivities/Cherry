package web

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"cherry/internal/economy"
	"cherry/internal/httpx"
	"cherry/internal/store"
)

// Hearts quest HTTP (economy/hearts.go): quest/status, heart/haveHeart and heart/receivable,
// the receiver's sender list, and the heart fields of the profile. Wire notes:
// %TEMP%\opencode\cherry\phaseDE-20261022\hearts.md.

// handleQuestStatus is GET /v4/quest/status. The heart block is the caller's own account
// (AV_AUTH); an unknown session gets the zero body.
func handleQuestStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpx.ServeNotFound(w)
		return
	}
	acc, _ := store.AccountForRequest(r)
	now := economy.NowFunc()
	left, known := economy.HeartsLeft(acc.Aid, now)
	if !known {
		httpx.WriteJSON(w, http.StatusOK, questStatusBody)
		return
	}
	sent, _ := economy.HeartsSentToday(acc.Aid, now)
	progress := "ONGOING"
	if sent >= economy.HeartQuestCount() {
		progress = "COMPLETED"
	}
	rewardCoin := economy.HeartRewardGems()
	if economy.HeartRewardCash() > 0 && economy.HeartRewardGems() == 0 {
		rewardCoin = economy.HeartRewardCash()
	}
	body, _ := json.Marshal(map[string]any{"result": map[string]any{
		"heart":           left,
		"heartBase":       economy.HeartBase(),
		"heartRewardCoin": rewardCoin,
		"questProgress":   progress,
		"toExpire":        strconv.FormatInt(economy.HeartResetSeconds(now), 10),
	}})
	httpx.WriteJSON(w, http.StatusOK, string(body))
}

// handleAdmobInit serves GET /v4/admob/0 so QuestCode 118 ("Get Free Gems") populates cleanly
// (RestCount=0, BaseCount=1, RewardAmount=2000) without video ads or per-frame polling.
func handleAdmobInit(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpx.ServeNotFound(w)
		return
	}
	nextReset := strconv.FormatInt(economy.QuestResetMillis(economy.NowFunc()), 10)
	body, _ := json.Marshal(map[string]any{"result": map[string]any{
		"pid":               0,
		"viewable":          false,
		"remainingCount":    0,
		"maxCount":          0,
		"nextViewTimeStamp": nextReset,
		"gainCirclePoint":   0,
		"coppaApply":        false,
		"senderName":        "",
		"linkType":          "",
		"reward":            []any{},
	}})
	httpx.WriteJSON(w, http.StatusOK, string(body))
}

// handleHeart serves GET /v4/heart/haveHeart/<aid> and /v4/heart/receivable/<aid>, both
// {"result":<bool>}. The caller is the sender (AV_AUTH); <aid> is the other avatar.
func handleHeart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpx.ServeNotFound(w)
		return
	}
	viewer, _ := store.AccountForRequest(r)
	kind, target, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, "/v4/heart/"), "/")
	var result bool
	switch kind {
	case "haveHeart":
		result = economy.HaveHeart(viewer.Aid, economy.NowFunc())
	case "receivable":
		result = economy.HeartReceivable(viewer.Aid, strings.Trim(target, "/"))
	default:
		httpx.ServeNotFound(w)
		return
	}
	body, _ := json.Marshal(map[string]bool{"result": result})
	httpx.WriteJSON(w, http.StatusOK, string(body))
}

// handleHeartSenderList serves GET /v4/noti/heart/receive/list/<aid>?timestamp=... as
// {"result":[...]}: the accounts that hearted <aid> today (oldest first, at most 50). The
// timestamp query is ignored. Rows match ResDiaryHeartSenderListWithTimeStamp: strings except
// heartReceivable (bool). An empty list is [].
func handleHeartSenderList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpx.ServeNotFound(w)
		return
	}
	aid := strings.Trim(strings.TrimPrefix(r.URL.Path, "/v4/noti/heart/receive/list/"), "/")
	rows := []map[string]any{}
	for _, sender := range economy.HeartSenders(aid, economy.NowFunc()) {
		acc, ok := store.AccountByAvatarID(sender)
		if !ok {
			continue
		}
		img, _, bg := acc.ProfileImage.Fields()
		rows = append(rows, map[string]any{
			"avatarId": sender, "name": acc.Name, "avatarProfile": img,
			"obsProfileImagePath": img, "regDate": "", "introduce": "",
			"heartReceivable": true, "profileBackground": bg,
		})
	}
	body, _ := json.Marshal(map[string]any{"result": rows})
	httpx.WriteJSON(w, http.StatusOK, string(body))
}

// heartProfileFields are the heart fields of /v4/profile/<aid> as the viewer sees them.
// haveExtraHeartBalance stays 0, so the client's non-JP extra-heart gate never blocks a send.
func heartProfileFields(viewerAid, aid string) map[string]any {
	now := economy.NowFunc()
	left, _ := economy.HeartsLeft(viewerAid, now)
	return map[string]any{
		"heartCount":            economy.HeartsReceived(aid),
		"haveHeart":             economy.HaveHeart(viewerAid, now),
		"heartReceivable":       economy.HeartReceivable(viewerAid, aid),
		"haveHeartBalance":      left,
		"haveExtraHeartBalance": 0,
	}
}

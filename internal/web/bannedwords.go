package web

import (
	"encoding/json"
	"net/http"

	"cherry/internal/economy"
	"cherry/internal/httpx"
)

// Chat bad-word list (NaDispatcherChat::ReqChatBadWordWithCachePolicy, libgame 0x1bba258; GET built by
// chatBadWordURL 0x1acdd14 = baseHostName(1) + "banned/keyword"). The reply ResChatBadWordWithCachePolicy
// (0x1c0a360) hands root.result to NaDataManager::SetInhibitionWordList (0x1c0a170), which needs an array of
// strings and lowercases each one. A missing or failed reply leaves the list empty, which passes every input.

// handleBannedKeyword serves GET /v4/banned/keyword: the operator's banned word list (economy.BannedWords, empty by
// default). The list never holds a blank word: an empty entry matches every input (find("") returns 0) and loops
// CheckInhibitionWord. The server masks the same words itself (economy.MaskBannedWords).
func handleBannedKeyword(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpx.ServeNotFound(w)
		return
	}
	body, _ := json.Marshal(struct {
		Result []string `json:"result"`
	}{economy.BannedWords()})
	httpx.WriteJSON(w, http.StatusOK, string(body))
}

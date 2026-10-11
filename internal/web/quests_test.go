package web

import (
	"net/http"
	"testing"

	"cherry/internal/store"
)

// TestDiaryWriteMarksDiaryQuest checks the diary progress trigger: a successful post marks the
// "Write in your Diary" quest (9004) for the author, and nothing else.
func TestDiaryWriteMarksDiaryQuest(t *testing.T) {
	installSocialTestAccounts(t, map[string]*store.Account{
		"social-test-token": {AccessToken: "social-test-token", Aid: "1", Name: "Cherry"},
	})
	store.ResetSocial()
	t.Cleanup(store.ResetSocial)

	resp := socialTestRequest(t, http.MethodPost, "/v4/diary2/ext/write", `{"avatarId":"1","content":"quest","openState":"public","images":[{"imageUrl":""}]}` /* stock text-only post shape */)
	if resp.Code != http.StatusOK {
		t.Fatalf("write = %d %s", resp.Code, resp.Body.String())
	}
	store.AccountsMu.Lock()
	defer store.AccountsMu.Unlock()
	acc := store.AccountByAidLocked("1")
	if acc == nil || acc.Quests == nil || len(acc.Quests.Done) != 1 || acc.Quests.Done[0] != 9004 {
		t.Fatalf("diary quest progress = %+v, want done [9004]", acc)
	}
}

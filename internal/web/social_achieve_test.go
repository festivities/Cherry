package web

import (
	"bytes"
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"cherry/internal/store"
)

func TestAchieveShareToDiary(t *testing.T) {
	installSocialTestAccounts(t, map[string]*store.Account{
		"social-test-token": {AccessToken: "social-test-token", Aid: "1", Name: "Cherry"},
		"other-test-token":  {AccessToken: "other-test-token", Aid: "1002", Name: "Other"},
	})
	store.ResetSocial()
	t.Cleanup(store.ResetSocial)
	store.SocialMu.Lock()
	store.SocialStorePath = filepath.Join(t.TempDir(), "social.json")
	store.SocialMu.Unlock()

	const share = `{"avatarId":"1","achieveType":"spin","achieveTypeLevel":"R","achieveDataType":"gem","achieveData":"","achieveLinkValue":"3","achieveDataDetail":"Lucky Spin"}`
	wrote := socialTestRequest(t, http.MethodPost, "/v4/achieve/write", share)
	var reply struct {
		Result struct {
			DiaryNo string `json:"diaryNo"`
		} `json:"result"`
	}
	if wrote.Code != http.StatusOK || json.Unmarshal(wrote.Body.Bytes(), &reply) != nil || reply.Result.DiaryNo == "" {
		t.Fatalf("achieve write = %d %s", wrote.Code, wrote.Body.String())
	}

	unfold := serve(t, http.MethodGet, "/v4/diary2/ext/unfold/1/?lastSeq=-1&size=10")
	for _, want := range []string{
		`"diaryNo":"` + reply.Result.DiaryNo + `"`, `"imageTypeKind":"achieve"`, `"openState":"public"`,
		`"title":"Lucky Spin"`, `"achieveType":"spin"`, `"achieveTypeLevel":"R"`, `"achieveDataType":"gem"`,
		`"achieveLinkValue":"3"`, `"achieveDataDetail":"Lucky Spin"`,
	} {
		if !bytes.Contains(unfold.Body.Bytes(), []byte(want)) {
			t.Fatalf("unfold misses %s: %d %s", want, unfold.Code, unfold.Body.String())
		}
	}
	look := serve(t, http.MethodGet, "/v4/diary2/ext/look/1/"+reply.Result.DiaryNo)
	if look.Code != http.StatusOK || !bytes.Contains(look.Body.Bytes(), []byte(`"achieveType":"spin"`)) || !bytes.Contains(look.Body.Bytes(), []byte(`"achieveDataDetail":"Lucky Spin"`)) {
		t.Fatalf("look = %d %s", look.Code, look.Body.String())
	}

	if other := socialTestRequestAs(t, "other-test-token", http.MethodPost, "/v4/achieve/write", share); other.Code != http.StatusBadRequest {
		t.Fatalf("other account avatarId = %d %s, want 400", other.Code, other.Body.String())
	}
	if anon := serve(t, http.MethodPost, "/v4/achieve/write"); anon.Code != http.StatusNotFound {
		t.Fatalf("no session = %d %s, want 404", anon.Code, anon.Body.String())
	}
	if bad := socialTestRequest(t, http.MethodPost, "/v4/achieve/write", strings.Replace(share, `"spin"`, `"fishing"`, 1)); bad.Code != http.StatusBadRequest {
		t.Fatalf("unknown achieveType = %d %s, want 400", bad.Code, bad.Body.String())
	}
	store.SocialMu.Lock()
	count, path := len(store.DiaryPosts), store.SocialStorePath
	store.SocialMu.Unlock()
	if count != 1 {
		t.Fatalf("rejected shares stored posts: %d posts, want 1", count)
	}

	if err := store.LoadSocialFrom(path); err != nil {
		t.Fatal(err)
	}
	store.SocialMu.Lock()
	reloaded := store.DiaryPosts[0]
	store.SocialMu.Unlock()
	if reloaded.DiaryNo != reply.Result.DiaryNo || reloaded.AchieveType != "spin" || reloaded.ImageTypeKind != "achieve" {
		t.Fatalf("reloaded post = %+v", reloaded)
	}

	// The dye share sends its colours as the detail; the post shows the garment instead.
	if dye := socialTestRequest(t, http.MethodPost, "/v4/achieve/write", `{"avatarId":"1","achieveType":"colorshop_complete","achieveTypeLevel":"","achieveDataType":"item","achieveData":"CUTO0019U","achieveLinkValue":"","achieveDataDetail":"#E53935_#FFFFFF"}`); dye.Code != http.StatusOK {
		t.Fatalf("dye share = %d %s, want 200", dye.Code, dye.Body.String())
	}
	store.SocialMu.Lock()
	defer store.SocialMu.Unlock()
	for _, p := range store.DiaryPosts {
		if p.AchieveType == "colorshop_complete" && p.Title != "Top 0019U" {
			t.Fatalf("dye post title = %q, want the garment's name", p.Title)
		}
	}
}

package web

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"cherry/internal/httpx"
	"cherry/internal/room"
	"cherry/internal/store"
)

func petGet(t *testing.T, token, target string) map[string]any {
	t.Helper()
	rec := decorReq(t, token, "GET", target, "")
	if rec.Code != 200 {
		t.Fatalf("%s: %d %s", target, rec.Code, rec.Body)
	}
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestPetCodeRoundTrip(t *testing.T) {
	if got := room.PetItemCode(16); got != "PUPE0000G" {
		t.Fatalf("code %s", got)
	}
	if got := room.PetSkinID("PUPE0000G"); got != 326400016 {
		t.Fatalf("id %d", got)
	}
}

func TestPetRoutes(t *testing.T) {
	acc := &store.Account{Aid: "9201", Name: "Pets"}
	installSocialTestAccounts(t, map[string]*store.Account{"ptok": acc})

	rep := petGet(t, "ptok", "/v4/pet/inven/represent?avatarId=9201")["result"].([]any)
	if len(rep) != 1 {
		t.Fatalf("represent %v", rep)
	}
	row := rep[0].(map[string]any)
	if row["skinCode"] != "PUPE0000G" || row["petId"] != "920101" || row["avatarId"] != "9201" || row["skinId"] != float64(326400016) {
		t.Fatalf("row %v", row)
	}
	if got := petGet(t, "ptok", "/v4/pet/inven/represent?avatarId=1")["result"].([]any); len(got) != 0 {
		t.Fatalf("unknown avatar %v", got)
	}
	if mc := petGet(t, "ptok", "/v4/pet/room/arrange/maxcount")["result"].(map[string]any)["maxCount"]; mc != float64(room.PetMaxArrange) {
		t.Fatalf("maxcount %v", mc)
	}
	list := petGet(t, "ptok", "/v4/pet/room/arrange/list/9201/LEVEL_1")["result"].(map[string]any)["list"].([]any)
	if len(list) != 2 || list[0].(map[string]any)["groundLevel"] != "LEVEL_1" {
		t.Fatalf("arranged %v", list)
	}
	rl := petGet(t, "ptok", "/v4/pet/room/arrange/represent/list/9201/LEVEL_2")["result"].(map[string]any)
	if len(rl["arrangedPetList"].(map[string]any)["list"].([]any)) != 0 || rl["hostRepresentPetList"] == nil || rl["guestRepresentPetList"] == nil {
		t.Fatalf("room list %v", rl)
	}

	if rec := decorReq(t, "ptok", "POST", "/v4/pet/inven/change/represent", "[920104]"); rec.Code != 200 {
		t.Fatalf("change %d", rec.Code)
	}
	rep = petGet(t, "ptok", "/v4/pet/inven/represent?avatarId=9201")["result"].([]any)
	if len(rep) != 1 || rep[0].(map[string]any)["petId"] != "920104" {
		t.Fatalf("after change %v", rep)
	}
	if rec := decorReq(t, "ptok", "POST", "/v4/pet/inven/change/represent", "[77]"); rec.Code != 400 {
		t.Fatalf("bad id %d", rec.Code)
	}
	decorReq(t, "ptok", "POST", "/v4/pet/inven/remove/represent", "null")
	if rep = petGet(t, "ptok", "/v4/pet/inven/represent?avatarId=9201")["result"].([]any); len(rep) != 0 {
		t.Fatalf("after remove %v", rep)
	}
}

func TestPetUnknownSession(t *testing.T) {
	installSocialTestAccounts(t, map[string]*store.Account{"ptok": {Aid: "9202"}})
	for _, p := range []string{"/v4/pet/inven/represent?avatarId=1", "/v4/pet/room/arrange/maxcount", "/v4/pet/room/arrange/list/1/LEVEL_1"} {
		if rec := decorReq(t, "nope", "GET", p, ""); rec.Code != 404 || rec.Body.String() != httpx.UnknownSessionBody {
			t.Fatalf("%s: %d %s", p, rec.Code, rec.Body)
		}
	}
}

func TestPetPersistence(t *testing.T) {
	acc := &store.Account{AccessToken: "ptok", SessionKey: "k", Aid: "9203", Name: "P"}
	installSocialTestAccounts(t, map[string]*store.Account{"ptok": acc})
	store.AccountsMu.Lock()
	store.AccountStorePath = filepath.Join(t.TempDir(), "accounts.json")
	store.NextAvatarID = 9203
	store.AccountsMu.Unlock()
	decorReq(t, "ptok", "POST", "/v4/pet/inven/change/represent", "[920303]")
	if err := store.LoadAccountsFrom(store.AccountStorePath); err != nil {
		t.Fatal(err)
	}
	store.AccountsMu.Lock()
	defer store.AccountsMu.Unlock()
	var got *store.Account
	for _, a := range store.Accounts {
		got = a
	}
	if got == nil || len(got.Pets) != len(room.PetShowcaseNums) {
		t.Fatalf("reloaded %+v", got)
	}
	for _, p := range got.Pets {
		if p.Rep != (p.ID == 920303) {
			t.Fatalf("rep flags %+v", got.Pets)
		}
	}
}

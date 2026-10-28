package web

import (
	"encoding/json"
	"net/http"
	"reflect"
	"testing"

	"cherry/internal/store"
)

func TestRoomRandomNeverReturnsRequester(t *testing.T) {
	installSocialTestAccounts(t, map[string]*store.Account{
		"rr-me": {AccessToken: "rr-me", SessionKey: "sk", Aid: "1201", Name: "Me"},
		"rr-a":  {AccessToken: "rr-a", SessionKey: "sk", Aid: "1202", Name: "Ann"},
		"rr-b":  {AccessToken: "rr-b", SessionKey: "sk", Aid: "1203", Name: "Bo"},
	})
	seen := map[string]bool{}
	for range 200 {
		rec := socialTestRequestAs(t, "rr-me", http.MethodGet, "/v4/room/random", "")
		if rec.Code != http.StatusOK {
			t.Fatalf("room random = %d %s", rec.Code, rec.Body.String())
		}
		var body struct {
			Result map[string]json.RawMessage `json:"result"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		var host string
		if err := json.Unmarshal(body.Result["hostAvatarId"], &host); err != nil {
			t.Fatal(err)
		}
		if host != "1202" && host != "1203" {
			t.Fatalf("random host = %q, want another account (not 1201)", host)
		}
		if _, ok := body.Result["room"]; !ok {
			t.Fatalf("random reply has no room key: %s", rec.Body.String())
		}
		if _, ok := body.Result["roomInfo"]; ok {
			t.Fatalf("random reply uses roomInfo, the client reads room: %s", rec.Body.String())
		}
		seen[host] = true
	}
	if !seen["1202"] || !seen["1203"] {
		t.Errorf("200 draws only picked %v", seen)
	}
}

func TestRoomRandomWithoutOtherAccount(t *testing.T) {
	installSocialTestAccounts(t, map[string]*store.Account{
		"rr-solo": {AccessToken: "rr-solo", SessionKey: "sk", Aid: "1211", Name: "Solo"},
	})
	rec := socialTestRequestAs(t, "rr-solo", http.MethodGet, "/v4/room/random", "")
	if rec.Code != http.StatusInternalServerError || rec.Body.String() != `{"errorCode":"500","errorMessage":"cherry: no other room"}` {
		t.Fatalf("room random with no other account = %d %s", rec.Code, rec.Body.String())
	}
}

// The random reply carries the same roomInfo object as a visit (only the key differs), and the
// host's pets load through the represent list for the host's aid.
func TestRoomRandomMatchesVisitLayout(t *testing.T) {
	installSocialTestAccounts(t, map[string]*store.Account{
		"rr-me":   {AccessToken: "rr-me", SessionKey: "sk", Aid: "1301", Name: "Me"},
		"rr-host": {AccessToken: "rr-host", SessionKey: "sk", Aid: "1302", Name: "Host"},
	})
	random := socialTestRequestAs(t, "rr-me", http.MethodGet, "/v4/room/random", "")
	visit := socialTestRequestAs(t, "rr-me", http.MethodGet, "/v4/room/enter/1302/LEVEL_1", "")
	if random.Code != http.StatusOK || visit.Code != http.StatusOK {
		t.Fatalf("random = %d, visit = %d %s", random.Code, visit.Code, visit.Body.String())
	}
	var got struct {
		Result struct {
			HostAvatarID      string         `json:"hostAvatarId"`
			Room              map[string]any `json:"room"`
			OwnMaxGroundLevel string         `json:"ownMaxGroundLevel"`
			AvailMax          string         `json:"availMaxGroundLevel"`
		} `json:"result"`
	}
	if err := json.Unmarshal(random.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	var want struct {
		Result struct {
			RoomInfo map[string]any `json:"roomInfo"`
		} `json:"result"`
	}
	if err := json.Unmarshal(visit.Body.Bytes(), &want); err != nil {
		t.Fatal(err)
	}
	if got.Result.HostAvatarID != "1302" || got.Result.OwnMaxGroundLevel != "LEVEL_2" || got.Result.AvailMax != "LEVEL_2" {
		t.Errorf("random reply header = %+v", got.Result)
	}
	if !reflect.DeepEqual(got.Result.Room, want.Result.RoomInfo) {
		t.Errorf("random room = %v, visit roomInfo = %v", got.Result.Room, want.Result.RoomInfo)
	}
	if rec := socialTestRequestAs(t, "rr-me", http.MethodGet, "/v4/pet/room/arrange/represent/list/1302/LEVEL_1", ""); rec.Code != http.StatusOK {
		t.Errorf("host pet list = %d %s", rec.Code, rec.Body.String())
	}
}

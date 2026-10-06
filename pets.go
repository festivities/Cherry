package main

import (
	"encoding/json"
	"io"
	"maps"
	"net/http"
	"slices"
	"strconv"
	"strings"
)

// Minipet display/arrangement subset (libgame.so 10.1.0.0): NaDispatcherPet
// represent/arrange parsers (ResRepresentPetInfo @0x1b64e78, ResPetArrangeList
// @0x1bfb678, ResPetRoomList @0x1bfc2c4, ResPetLimitInfo @0x1a7e040). Growth,
// feeding, calls, shop and the Pet Book (pet/book/*) are not implemented.

const petMaxArrange = 8

type petItem struct {
	ID   int64  `json:"id"`
	Cd   string `json:"cd"`
	Name string `json:"name"`
	Rep  bool   `json:"rep,omitempty"`  // follows the owner (Garden, My Room)
	Room string `json:"room,omitempty"` // "LEVEL_n" when arranged on that My Room floor
}

// ponytail: lab showcase grant, six renderable pets (complete skeleton/texture/
// behavior in files/item/pet): numeric ids 326400016..021 = P,U,"PE",base36 n.
var petShowcaseNums = []int{16, 17, 18, 19, 20, 21}

// petItemCode is the inverse of SbItemTable::GetIntIdx(string) @0x2af0904 for
// pet skins: attribute P (digit 3), type U (digit 2), category PE (idx 64), n base36.
func petItemCode(n int) string {
	s := strings.ToUpper(strconv.FormatInt(int64(n), 36))
	return "PUPE" + strings.Repeat("0", max(0, 5-len(s))) + s
}

// petSkinID decodes PUPE+base36 back to the numeric item id (3264xxxxx).
func petSkinID(cd string) int {
	if len(cd) != 9 {
		return 0
	}
	n, err := strconv.ParseInt(cd[4:], 36, 64)
	if err != nil {
		return 0
	}
	return 326400000 + int(n)
}

// ensurePetsLocked grants the showcase pets once; pet 1 follows the owner, 2-3
// are arranged on LEVEL_1. Caller holds accountsMu.
func ensurePetsLocked(acc *account) error {
	if len(acc.pets) > 0 {
		return nil
	}
	pets := make([]petItem, 0, len(petShowcaseNums))
	for i, n := range petShowcaseNums {
		p := petItem{ID: petBaseID(acc.aid) + int64(i), Cd: petItemCode(n), Name: "Minipet " + strconv.Itoa(i+1)}
		switch i {
		case 0:
			p.Rep = true
		case 1, 2:
			p.Room = defaultLevel
		}
		pets = append(pets, p)
	}
	prev := acc.pets
	acc.pets = pets
	if err := saveAccountsLocked(); err != nil {
		acc.pets = prev
		return err
	}
	return nil
}

// petAccount mirrors roomAccount: caller unlocks accountsMu iff non-nil.
func petAccount(w http.ResponseWriter, r *http.Request) *account {
	accountsMu.Lock()
	acc := accounts[cookieValue(r, "AV_AUTH")]
	if acc == nil {
		accountsMu.Unlock()
		writeJSON(w, http.StatusNotFound, unknownSessionBody)
		return nil
	}
	if err := ensurePetsLocked(acc); err != nil {
		accountsMu.Unlock()
		writeJSON(w, http.StatusInternalServerError, `{"errorCode":"500"}`)
		return nil
	}
	return acc
}

// petsOfAid returns a copy of the pets of the account with that aid (seeding it).
func petsOfAid(aid string) []petItem {
	accountsMu.Lock()
	defer accountsMu.Unlock()
	for _, acc := range accounts {
		if acc.aid == aid {
			if ensurePetsLocked(acc) != nil {
				return nil
			}
			return slices.Clone(acc.pets)
		}
	}
	return nil
}

// petRepRow is the ResRepresentPetInfo element (ints as ints, ids as strings:
// jsoncpp asInt throws on strings).
func petRepRow(aid string, p petItem, arranged bool) map[string]any {
	return map[string]any{
		"raceId": 1, "catgCd": "PET", "itemNo": 1, "itemCode": p.Cd, "itemName": p.Name,
		"localeCd": "en", "raceNm": "Minipet", "linkPosX": 0, "linkPosY": 0, "linkScale": 100,
		"dispProbability": 0, "petId": strconv.FormatInt(p.ID, 10), "petName": p.Name,
		"avatarId": aid, "skinSerial": strconv.FormatInt(p.ID, 10), "skinId": petSkinID(p.Cd),
		"skinCode": p.Cd, "receiveHeart": 0, "level": 1, "petSleepingYn": false, "arrangedYn": arranged,
	}
}

// petArrangeRow is the ResPetArrangeList / arrangedPetList element.
func petArrangeRow(aid string, p petItem) map[string]any {
	row := petRepRow(aid, p, true)
	delete(row, "dispProbability")
	delete(row, "receiveHeart")
	delete(row, "arrangedYn")
	maps.Copy(row, map[string]any{
		"exposureYn": true, "exp": 0, "maxExp": 100, "petTouchableYn": true, "callPayCode": "",
		"callPrice": 0, "defaultCallYn": false, "defaultCallRemainTimestamp": "0",
		"finalLevelYn": false, "groundLevel": p.Room,
	})
	return row
}

func petJSON(w http.ResponseWriter, v any) {
	body, _ := json.Marshal(map[string]any{"result": v})
	writeJSON(w, http.StatusOK, string(body))
}

func arrangedRows(aid string, pets []petItem, level string) []map[string]any {
	rows := []map[string]any{}
	for _, p := range pets {
		if p.Room == level {
			rows = append(rows, petArrangeRow(aid, p))
		}
	}
	return rows
}

// handlePet serves /v4/pet/...: inven/represent, inven/{change,remove}/represent,
// room/arrange/{maxcount,list/<aid>/LEVEL_n,represent/list/<aid>/LEVEL_n}.
func handlePet(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/v4/pet/")
	parts := strings.Split(path, "/")
	get := r.Method == http.MethodGet
	switch {
	case get && path == "inven/represent":
		if petAccount(w, r) == nil {
			return
		}
		accountsMu.Unlock()
		aid := r.URL.Query().Get("avatarId")
		rows := []map[string]any{}
		for _, p := range petsOfAid(aid) {
			if p.Rep {
				rows = append(rows, petRepRow(aid, p, false))
			}
		}
		petJSON(w, rows)
	case get && path == "room/arrange/maxcount":
		if petAccount(w, r) == nil {
			return
		}
		accountsMu.Unlock()
		petJSON(w, map[string]any{"maxCount": petMaxArrange})
	case get && len(parts) == 5 && parts[0] == "room" && parts[1] == "arrange" && parts[2] == "list" && roomLevelValid(parts[4]):
		if petAccount(w, r) == nil {
			return
		}
		accountsMu.Unlock()
		petJSON(w, map[string]any{"list": arrangedRows(parts[3], petsOfAid(parts[3]), parts[4])})
	case get && len(parts) == 6 && parts[0] == "room" && parts[1] == "arrange" && parts[2] == "represent" && parts[3] == "list" && roomLevelValid(parts[5]):
		if petAccount(w, r) == nil {
			return
		}
		accountsMu.Unlock()
		// ponytail: represent pets spawn through inven/represent; host/guest lists stay empty to avoid duplicates.
		petJSON(w, map[string]any{
			"arrangedPetList":      map[string]any{"list": arrangedRows(parts[4], petsOfAid(parts[4]), parts[5])},
			"hostRepresentPetList": []any{}, "guestRepresentPetList": []any{},
		})
	case r.Method == http.MethodPost && (path == "inven/change/represent" || path == "inven/remove/represent"):
		petSetRepresent(w, r, path == "inven/change/represent")
	default:
		serveNotFound(w)
	}
}

// petSetRepresent: change body is a JSON array of petIds (ReqRepresentPetChange
// @0x1bd09d0 wraps "[ids]"), remove body is "null". Single representative pet.
func petSetRepresent(w http.ResponseWriter, r *http.Request, change bool) {
	raw, _ := io.ReadAll(io.LimitReader(r.Body, 4096))
	var ids []json.Number
	if change && (json.Unmarshal(raw, &ids) != nil || len(ids) == 0) {
		writeJSON(w, http.StatusBadRequest, `{"errorCode":"400"}`)
		return
	}
	acc := petAccount(w, r)
	if acc == nil {
		return
	}
	defer accountsMu.Unlock()
	pets := slices.Clone(acc.pets)
	found := false
	for i := range pets {
		want := change && ids[0].String() == strconv.FormatInt(pets[i].ID, 10)
		found = found || want
		pets[i].Rep = want
	}
	if change && !found {
		writeJSON(w, http.StatusBadRequest, `{"errorCode":"400"}`)
		return
	}
	prev := acc.pets
	acc.pets = pets
	if err := saveAccountsLocked(); err != nil {
		acc.pets = prev
		writeJSON(w, http.StatusInternalServerError, `{"errorCode":"500"}`)
		return
	}
	petJSON(w, map[string]any{})
}

// petBaseID makes pet ids unique per owner (aid*100+1, ...): petId is the actor id, so
// two players' pets in one Garden or party must not share it.
func petBaseID(aid string) int64 {
	if n, err := strconv.ParseInt(aid, 10, 64); err == nil && n > 0 && n < 1<<50 {
		return n*100 + 1
	}
	return 1001
}

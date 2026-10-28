package room

import (
	"encoding/json"
	"io"
	"maps"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"cherry/internal/httpx"
	"cherry/internal/store"
)

// Minipet display/arrangement subset (libgame.so 10.1.0.0): NaDispatcherPet
// represent/arrange parsers (ResRepresentPetInfo @0x1b64e78, ResPetArrangeList
// @0x1bfb678, ResPetRoomList @0x1bfc2c4, ResPetLimitInfo @0x1a7e040). Growth,
// feeding, calls and shop are not implemented; the Pet Book list is HandlePetBook (pet/book/race/list/*),
// the Pet Book detail pages (pet/book/race/detail, pet/book/race/info) are not.

const PetMaxArrange = 8

// ponytail: lab showcase grant, six renderable pets (complete skeleton/texture/
// behavior in files/item/pet): numeric ids 326400016..021 = P,U,"PE",base36 n.
var PetShowcaseNums = []int{16, 17, 18, 19, 20, 21}

// PetItemCode is the inverse of SbItemTable::GetIntIdx(string) @0x2af0904 for
// pet skins: attribute P (digit 3), type U (digit 2), category PE (idx 64), n base36.
func PetItemCode(n int) string {
	s := strings.ToUpper(strconv.FormatInt(int64(n), 36))
	return "PUPE" + strings.Repeat("0", max(0, 5-len(s))) + s
}

// PetSkinID decodes PUPE+base36 back to the numeric item id (3264xxxxx).
func PetSkinID(cd string) int {
	if len(cd) != 9 {
		return 0
	}
	n, err := strconv.ParseInt(cd[4:], 36, 64)
	if err != nil {
		return 0
	}
	return 326400000 + int(n)
}

// EnsurePetsLocked grants the showcase pets once; pet 1 follows the owner, 2-3
// are arranged on LEVEL_1. Caller holds accountsMu.
func EnsurePetsLocked(acc *store.Account) error {
	if len(acc.Pets) > 0 || !store.LabAids[acc.Aid] {
		return nil
	}
	pets := make([]store.PetItem, 0, len(PetShowcaseNums))
	for i, n := range PetShowcaseNums {
		p := store.PetItem{ID: petBaseID(acc.Aid) + int64(i), Cd: PetItemCode(n), Name: PetName(i + 1)}
		switch i {
		case 0:
			p.Rep = true
		case 1, 2:
			p.Room = DefaultLevel
		}
		pets = append(pets, p)
	}
	prev := acc.Pets
	acc.Pets = pets
	if err := store.SaveAccountsLocked(); err != nil {
		acc.Pets = prev
		return err
	}
	return nil
}

// NewPetLocked builds a pet of item code cd for acc (not appended): the id is the
// next free one in the owner's aid*100+n range. Caller holds accountsMu.
func NewPetLocked(acc *store.Account, cd string) store.PetItem {
	id := petBaseID(acc.Aid)
	for _, p := range acc.Pets {
		id = max(id, p.ID+1)
	}
	return store.PetItem{ID: id, Cd: cd, Name: PetName(len(acc.Pets) + 1)}
}

// petAccount mirrors roomAccount: caller unlocks accountsMu iff non-nil.
func petAccount(w http.ResponseWriter, r *http.Request) *store.Account {
	store.AccountsMu.Lock()
	acc := store.Accounts[httpx.CookieValue(r, "AV_AUTH")]
	if acc == nil {
		store.AccountsMu.Unlock()
		httpx.WriteJSON(w, http.StatusNotFound, httpx.UnknownSessionBody)
		return nil
	}
	if err := EnsurePetsLocked(acc); err != nil {
		store.AccountsMu.Unlock()
		httpx.WriteJSON(w, http.StatusInternalServerError, `{"errorCode":"500"}`)
		return nil
	}
	return acc
}

// PetsOfAid returns a copy of the pets of the account with that aid (seeding it).
func PetsOfAid(aid string) []store.PetItem {
	store.AccountsMu.Lock()
	defer store.AccountsMu.Unlock()
	for _, acc := range store.Accounts {
		if acc.Aid == aid {
			if EnsurePetsLocked(acc) != nil {
				return nil
			}
			return slices.Clone(acc.Pets)
		}
	}
	return nil
}

// petRepRow is the ResRepresentPetInfo element (ints as ints, ids as strings:
// jsoncpp asInt throws on strings).
func petRepRow(aid string, p store.PetItem, arranged bool) map[string]any {
	return map[string]any{
		"raceId": 1, "catgCd": "PET", "itemNo": 1, "itemCode": p.Cd, "itemName": p.Name,
		"localeCd": "en", "raceNm": "Minipet", "linkPosX": 0, "linkPosY": 0, "linkScale": 100,
		"dispProbability": 0, "petId": strconv.FormatInt(p.ID, 10), "petName": p.Name,
		"avatarId": aid, "skinSerial": strconv.FormatInt(p.ID, 10), "skinId": PetSkinID(p.Cd),
		"skinCode": p.Cd, "receiveHeart": 0, "level": 1, "petSleepingYn": false, "arrangedYn": arranged,
	}
}

// petArrangeRow is the ResPetArrangeList / arrangedPetList element.
func petArrangeRow(aid string, p store.PetItem) map[string]any {
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
	httpx.WriteJSON(w, http.StatusOK, string(body))
}

func arrangedRows(aid string, pets []store.PetItem, level string) []map[string]any {
	rows := []map[string]any{}
	for _, p := range pets {
		if p.Room == level {
			rows = append(rows, petArrangeRow(aid, p))
		}
	}
	return rows
}

// HandlePet serves /v4/pet/...: inven/represent, inven/{change,remove}/represent,
// room/arrange/{maxcount,list/<aid>/LEVEL_n,represent/list/<aid>/LEVEL_n}.
func HandlePet(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/v4/pet/")
	parts := strings.Split(path, "/")
	get := r.Method == http.MethodGet
	switch {
	case get && path == "inven/represent":
		if petAccount(w, r) == nil {
			return
		}
		store.AccountsMu.Unlock()
		aid := r.URL.Query().Get("avatarId")
		rows := []map[string]any{}
		for _, p := range PetsOfAid(aid) {
			if p.Rep {
				rows = append(rows, petRepRow(aid, p, false))
			}
		}
		petJSON(w, rows)
	case get && path == "room/arrange/maxcount":
		if petAccount(w, r) == nil {
			return
		}
		store.AccountsMu.Unlock()
		petJSON(w, map[string]any{"maxCount": PetMaxArrange})
	case get && len(parts) == 5 && parts[0] == "room" && parts[1] == "arrange" && parts[2] == "list" && RoomLevelValid(parts[4]):
		if petAccount(w, r) == nil {
			return
		}
		store.AccountsMu.Unlock()
		petJSON(w, map[string]any{"list": arrangedRows(parts[3], PetsOfAid(parts[3]), parts[4])})
	case get && len(parts) == 6 && parts[0] == "room" && parts[1] == "arrange" && parts[2] == "represent" && parts[3] == "list" && RoomLevelValid(parts[5]):
		if petAccount(w, r) == nil {
			return
		}
		store.AccountsMu.Unlock()
		// ponytail: represent pets spawn through inven/represent; host/guest lists stay empty to avoid duplicates.
		petJSON(w, map[string]any{
			"arrangedPetList":      map[string]any{"list": arrangedRows(parts[4], PetsOfAid(parts[4]), parts[5])},
			"hostRepresentPetList": []any{}, "guestRepresentPetList": []any{},
		})
	case r.Method == http.MethodPost && (path == "inven/change/represent" || path == "inven/remove/represent"):
		petSetRepresent(w, r, path == "inven/change/represent")
	default:
		httpx.ServeNotFound(w)
	}
}

// petSetRepresent: change body is a JSON array of petIds (ReqRepresentPetChange
// @0x1bd09d0 wraps "[ids]"), remove body is "null". Single representative pet.
func petSetRepresent(w http.ResponseWriter, r *http.Request, change bool) {
	raw, _ := io.ReadAll(io.LimitReader(r.Body, 4096))
	var ids []json.Number
	if change && (json.Unmarshal(raw, &ids) != nil || len(ids) == 0) {
		httpx.WriteJSON(w, http.StatusBadRequest, `{"errorCode":"400"}`)
		return
	}
	acc := petAccount(w, r)
	if acc == nil {
		return
	}
	defer store.AccountsMu.Unlock()
	pets := slices.Clone(acc.Pets)
	found := false
	for i := range pets {
		want := change && ids[0].String() == strconv.FormatInt(pets[i].ID, 10)
		found = found || want
		pets[i].Rep = want
	}
	if change && !found {
		httpx.WriteJSON(w, http.StatusBadRequest, `{"errorCode":"400"}`)
		return
	}
	prev := acc.Pets
	acc.Pets = pets
	if err := store.SaveAccountsLocked(); err != nil {
		acc.Pets = prev
		httpx.WriteJSON(w, http.StatusInternalServerError, `{"errorCode":"500"}`)
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

// PetSpecies is one sellable Minipet of the Minipet Shop catalog as the Pet Book lists it: the item code,
// the numeric item id and the client's race category (UDOG, UCAT, UTOY or URIDE). economy.PetSpecies supplies it.
type PetSpecies struct {
	Code string
	ID   int64
	Catg string
}

// petBookRow is one ResPetBookList element (sDataPetInfo). Key types are IDA-checked (0x1c34340, rows-d 4.2):
// asInt keys are numbers, asString keys strings, asBool keys bools. Keys Cherry has no source for stay zero.
type petBookRow struct {
	RaceID                      int    `json:"raceId"`
	CatgCd                      string `json:"catgCd"`
	ItemNo                      int    `json:"itemNo"`
	ItemCode                    string `json:"itemCode"`
	ItemSaleType                string `json:"itemSaleType"`
	LocaleCd                    string `json:"localeCd"`
	RaceNm                      string `json:"raceNm"`
	PetID                       string `json:"petId"`
	PetMemo                     string `json:"petMemo"`
	AvatarID                    string `json:"avatarId"`
	FirstVisited                string `json:"firstVisited"`
	FirstLevelUp                string `json:"firstLevelUp"`
	LastVisited                 string `json:"lastVisited"`
	InitActivityRemainTimestamp string `json:"initActivityRemainTimestamp"`
	CallPayCode                 string `json:"callPayCode"`
	ExposureYn                  bool   `json:"exposureYn"`
	ArrangedYn                  bool   `json:"arrangedYn"`
	DefaultCallYn               bool   `json:"defaultCallYn"`
	DefaultCallRemainTimestamp  string `json:"defaultCallRemainTimestamp"`
	GiftItemID                  int    `json:"giftItemId"`
	GiftItemCode                string `json:"giftItemCode"`
	PetSleepingYn               bool   `json:"petSleepingYn"`
	FinalLevelYn                bool   `json:"finalLevelYn"`
	CoppaStatus                 int    `json:"coppaStatus"`
	PetName                     string `json:"petName"`
	Exp                         int    `json:"exp"`
	MaxExp                      int    `json:"maxExp"`
	Level                       int    `json:"level"`
	ReceiveHeart                int    `json:"receiveHeart"`
	SkinSerial                  string `json:"skinSerial"`
	SkinID                      int    `json:"skinId"`
	SkinCode                    string `json:"skinCode"`
	LinkPosX                    int    `json:"linkPosX"`
	LinkPosY                    int    `json:"linkPosY"`
	LinkScale                   int    `json:"linkScale"`
	FirstVisitYn                bool   `json:"firstVisitYn"`
	RepresentYn                 bool   `json:"representYn"`
	InteriorOwnYn               bool   `json:"interiorOwnYn"`
	PaidCallCount               int    `json:"paidCallCount"`
	MaxPaidCallCount            int    `json:"maxPaidCallCount"`
	CallPrice                   int    `json:"callPrice"`
}

// petBookOwned is an account's own pet (the MYPET tab): the same level and experience Cherry already serves
// on the represent and arrange lists.
func petBookOwned(aid string, p store.PetItem, catg string) petBookRow {
	return petBookRow{
		RaceID: PetSkinID(p.Cd), CatgCd: catg, ItemCode: p.Cd, LocaleCd: "en",
		PetID: strconv.FormatInt(p.ID, 10), AvatarID: aid, PetName: p.Name,
		ExposureYn: true, ArrangedYn: p.Room != "", RepresentYn: p.Rep, DefaultCallRemainTimestamp: "0",
		Exp: 0, MaxExp: 100, Level: 1, SkinSerial: strconv.FormatInt(p.ID, 10),
		SkinID: PetSkinID(p.Cd), SkinCode: p.Cd, LinkScale: 100,
	}
}

// petBookSpecies is a catalog Minipet in a race tab: the account may not own it, so no pet id, avatar or
// name; level and experience stay 0 and the max experience 100 (neutral placeholders, no Cherry source).
func petBookSpecies(sp PetSpecies) petBookRow {
	return petBookRow{
		RaceID: int(sp.ID), CatgCd: sp.Catg, ItemCode: sp.Code, LocaleCd: "en",
		ExposureYn: true, MaxExp: 100, SkinID: int(sp.ID), SkinCode: sp.Code, LinkScale: 100,
	}
}

// HandlePetBook serves GET pet/book/race/list/<category> (NaDispatcherPet::ReqPetBookList → ResPetBookList
// 0x1c34340: result.list is an array of rows). The client's five tabs are MYPET, UDOG, UCAT, UTOY and URIDE
// (NaPetBook static init, sub_1511374). MYPET lists the account's pets; a race tab lists the Minipet Shop
// catalog pets of that category (economy.PetSpecies). Any other category gets an empty list: an error reply
// would leave the client's spinner up (11684 never hides it, 11683 does).
func HandlePetBook(species func() []PetSpecies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			httpx.ServeNotFound(w)
			return
		}
		category := strings.TrimPrefix(r.URL.Path, "/v4/pet/book/race/list/")
		all := species()
		list := []petBookRow{}
		if category == "MYPET" {
			acc := petAccount(w, r)
			if acc == nil {
				return
			}
			catg := map[string]string{}
			for _, sp := range all {
				catg[sp.Code] = sp.Catg
			}
			for _, p := range acc.Pets {
				list = append(list, petBookOwned(acc.Aid, p, catg[p.Cd]))
			}
			store.AccountsMu.Unlock()
		} else {
			for _, sp := range all {
				if sp.Catg == category {
					list = append(list, petBookSpecies(sp))
				}
			}
		}
		petJSON(w, map[string]any{"list": list})
	}
}

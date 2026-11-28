package economy

import (
	"encoding/json"
	"hash/fnv"
	"log"
	"maps"
	"math/rand/v2"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"cherry/internal/httpx"
	"cherry/internal/itemcode"
	"cherry/internal/room"
	"cherry/internal/store"
)

// The Minipet loop (original strings 1188-1282; spec pet-place-spec*.md): a Minipet Shop item (a RUPI room item,
// catIdx 29) is bought into the room and placed. Its pet (a PUPE code) shows in that room on the placed item's floor:
// a free call (every FreeCallMinutes) or a paid call (PaidCallGems) starts a visit of VisitMinutes; the owner pets it
// once per visit (TLUP 101/4), and at maxExp (the pet's cuddles) the pet is adopted and a present joins the closet.
// The pairs and the numbers are data/pets.json (`item`, `minipet`); Cherry-made.

const (
	errPetNotPlaced = "600202" // popup 2201: "pet room item has not been placed"
	errPetCooldown  = "600203" // popup 2199: the free call is not available yet
	errPetPayGems   = "600601" // popup 2025: the paid call was refused (Gems)
)

// presentPick picks one present (tests replace it).
var presentPick = rand.IntN

// miniPetIndex is the pairing of the loaded pets.json; rebuilt when the file is replaced.
type miniPetIndex struct {
	src    *PetCatalog
	pets   []string          // pet codes that have an item, sorted
	itemOf map[string]string // pet code -> RUPI item code
	petOf  map[string]string // RUPI item code -> pet code
}

var (
	miniPetIdxMu sync.Mutex
	miniPetIdx   *miniPetIndex
)

func miniPets() *miniPetIndex {
	cat := ActivePets()
	miniPetIdxMu.Lock()
	defer miniPetIdxMu.Unlock()
	if miniPetIdx == nil || miniPetIdx.src != cat {
		idx := &miniPetIndex{src: cat, itemOf: map[string]string{}, petOf: map[string]string{}}
		for code, spec := range cat.Pets {
			if spec.Item != "" {
				idx.pets = append(idx.pets, code)
				idx.itemOf[code] = spec.Item
				idx.petOf[spec.Item] = code
			}
		}
		slices.Sort(idx.pets)
		miniPetIdx = idx
	}
	return miniPetIdx
}

// MiniPetItem is the RUPI room item that places pet, if the pet is paired.
func MiniPetItem(pet string) (string, bool) {
	item, ok := miniPets().itemOf[pet]
	return item, ok
}

// MiniPetOfItem is the pet a RUPI room item places, if it is a paired Minipet item.
func MiniPetOfItem(item string) (string, bool) {
	pet, ok := miniPets().petOf[item]
	return pet, ok
}

// RoomItemCategory returns the Room Edit categoryCode for cd: for a paired Minipet RUPI item,
// the pet's catalog category plus "INT" (UDOGINT, UCATINT, UTOYINT, URIDEINT); otherwise room.RoomCategoryCode.
func RoomItemCategory(cd string) string {
	if pet, ok := MiniPetOfItem(cd); ok {
		if it := loadCatalog().byCode[pet]; it != nil && it.Catg != "" {
			return it.Catg + "INT"
		}
	}
	return room.RoomCategoryCode(cd)
}

// miniPetMaxExp is the friendship a pet needs for adoption: its cuddles, 100 where the catalog has none.
func miniPetMaxExp(pet string) int64 {
	if c := ActivePets().Pets[pet].Cuddles; c > 0 {
		return int64(c)
	}
	return 100
}

// miniPetPrice is the Shop price of a pet's item: the pet's own price and currency (ItemPrice of the PUPE code).
// ponytail: sell-back of an owned Minipet item still uses the interior price (ItemPrice of the RUPI code).
func miniPetPrice(pet string) (int64, bool) {
	price, cash, _, _ := ItemPrice(pet)
	return price, cash
}

// minipetState is the account's state for pet (zero until its first call).
func minipetState(acc *store.Account, pet string) store.MiniPetState {
	for _, st := range acc.MiniPets {
		if st.Cd == pet {
			return st
		}
	}
	return store.MiniPetState{Cd: pet}
}

// setMiniPetState stores st for its pet in a new slice (the previous one may be shared with a copy).
func setMiniPetState(acc *store.Account, st store.MiniPetState) {
	states := slices.Clone(acc.MiniPets)
	if i := slices.IndexFunc(states, func(s store.MiniPetState) bool { return s.Cd == st.Cd }); i >= 0 {
		states[i] = st
	} else {
		states = append(states, st)
	}
	acc.MiniPets = states
}

// ownedPet is the pet the account owns (bought as a Minipet before, or adopted), if any.
func ownedPet(acc *store.Account, pet string) (store.PetItem, bool) {
	i := slices.IndexFunc(acc.Pets, func(p store.PetItem) bool { return p.Cd == pet })
	if i < 0 {
		return store.PetItem{}, false
	}
	return acc.Pets[i], true
}

// minipetID is the petId of pet for acc: the owned pet's id, else the visit id room.MiniPetVisitID gives.
func minipetID(acc *store.Account, pet string) int64 {
	if p, ok := ownedPet(acc, pet); ok {
		return p.ID
	}
	return room.MiniPetVisitID(acc.Aid, pet)
}

// minipetByID is the paired pet whose petId for acc is id.
func minipetByID(acc *store.Account, id int64) (string, bool) {
	for _, pet := range miniPets().pets {
		if minipetID(acc, pet) == id {
			return pet, true
		}
	}
	return "", false
}

// placedLevel is the My Room floor (LEVEL_n) acc has item placed on, or "" when it is not placed.
func placedLevel(acc *store.Account, item string) string {
	for _, lvl := range slices.Sorted(maps.Keys(acc.Rooms)) {
		if slices.ContainsFunc(acc.Rooms[lvl].Placed, func(p store.RoomPlaced) bool { return p.Cd == item }) {
			return lvl
		}
	}
	return ""
}

// minipetAccount takes the requester's account with AccountsMu held. nil means the reply is written and the lock is free.
func minipetAccount(w http.ResponseWriter, r *http.Request) *store.Account {
	store.AccountsMu.Lock()
	acc := store.AccountByTokenLocked(httpx.CookieValue(r, "AV_AUTH"))
	if acc == nil {
		store.AccountsMu.Unlock()
		httpx.WriteJSON(w, http.StatusNotFound, httpx.UnknownSessionBody)
		return nil
	}
	if err := room.EnsurePetsLocked(acc); err != nil {
		store.AccountsMu.Unlock()
		httpx.WriteJSON(w, http.StatusInternalServerError, `{"errorCode":"500"}`)
		return nil
	}
	return acc
}

// startVisit opens a visit of the configured length at now, clears the petting of the previous one
// and stamps FirstVisit on the pet's first visit.
func startVisit(st store.MiniPetState, now time.Time) store.MiniPetState {
	st.VisitEnd = now.Add(time.Duration(ActivePets().Minipet.VisitMinutes) * time.Minute).Unix()
	st.Petted = false
	if st.FirstVisit == 0 {
		st.FirstVisit = now.Unix()
	}
	return st
}

// remainMillis returns the remaining cooldown in milliseconds until nextCall, clamped to 0.
func remainMillis(nextCall int64, now time.Time) int64 {
	return max(nextCall-now.Unix(), 0) * 1000
}

// miniPetCallReply is the reply of a call (ResPetFreeCall / ResPetCall shape).
func miniPetCallReply(acc *store.Account, level string, paid bool, price, remainMs int64) map[string]any {
	return map[string]any{
		"resultCode": 0, "balance": acc.Gems, "balanceUnit": "GEM", "purchasedCallFlag": paid,
		"defaultCallRemainTimestamp": strconv.FormatInt(max(remainMs, 0), 10), "exposureYn": true, "firstVisitYn": false,
		"callPayCode": "GEM", "callPrice": price, "groundLevel": level,
	}
}

// writeMiniPetResult writes {"result": v} with status 200.
func writeMiniPetResult(w http.ResponseWriter, v any) {
	body, _ := json.Marshal(map[string]any{"result": v})
	httpx.WriteJSON(w, http.StatusOK, string(body))
}

// miniPetCall serves GET room/call/<petId> for a paired pet. The item must be placed and the free call's cooldown over;
// the call starts a visit. It reports false, writing nothing, when no paired pet has that id (the owned-pet call answers).
func miniPetCall(w http.ResponseWriter, r *http.Request, id string) bool {
	n, err := strconv.ParseInt(id, 10, 64)
	if err != nil {
		return false
	}
	acc := minipetAccount(w, r)
	if acc == nil {
		return true
	}
	pet, ok := minipetByID(acc, n)
	if !ok {
		store.AccountsMu.Unlock()
		return false
	}
	defer store.AccountsMu.Unlock()
	item, _ := MiniPetItem(pet)
	level := placedLevel(acc, item)
	if level == "" {
		shopError(w, r, nil, errPetNotPlaced, "pet room item has not been placed")
		return true
	}
	now := NowFunc()
	st := minipetState(acc, pet)
	if now.Unix() < st.NextCall {
		shopError(w, r, nil, errPetCooldown, "free call not available yet")
		return true
	}
	prev := *acc
	st = startVisit(st, now)
	st.NextCall = now.Add(time.Duration(ActivePets().Minipet.FreeCallMinutes) * time.Minute).Unix()
	setMiniPetState(acc, st)
	if err := store.SaveLocked(nil, acc); err != nil {
		*acc = prev
		httpx.WriteJSON(w, http.StatusInternalServerError, httpx.SaveFailedBody)
		return true
	}
	writeMiniPetResult(w, miniPetCallReply(acc, level, false, 0, remainMillis(st.NextCall, now)))
	return true
}

// miniPetPaidCall serves POST book/prch/call: a paid call (Gems only) starts a visit at once and does not touch the
// free call's cooldown.
func miniPetPaidCall(w http.ResponseWriter, r *http.Request) {
	raw, err := httpx.ReadNativeBody(r)
	var req struct {
		CoinUnit string          `json:"coinUnit"`
		PetID    json.RawMessage `json:"petId"`
	}
	if err == nil {
		_, err = httpx.UnmarshalNativeJSON(raw, &req)
	}
	if err != nil {
		httpx.WriteJSON(w, http.StatusBadRequest, httpx.BadRequestBody)
		return
	}
	id, perr := strconv.ParseInt(strings.Trim(string(req.PetID), `"`), 10, 64)
	acc := minipetAccount(w, r)
	if acc == nil {
		return
	}
	defer store.AccountsMu.Unlock()
	pet, ok := minipetByID(acc, id)
	if perr != nil || !ok {
		shopError(w, r, raw, errUnknown, "unknown pet")
		return
	}
	item, _ := MiniPetItem(pet)
	level := placedLevel(acc, item)
	if level == "" {
		shopError(w, r, raw, errPetNotPlaced, "pet room item has not been placed")
		return
	}
	if req.CoinUnit != "GEM" && req.CoinUnit != "" {
		shopError(w, r, raw, errUnknown, "gems only")
		return
	}
	rules := ActivePets().Minipet
	now := NowFunc()
	prev := *acc
	lines, err := store.SpendLocked(acc, store.LedgerDeltas{Gems: -rules.PaidCallGems}, "minipet:call:"+pet)
	if err != nil {
		*acc = prev
		shopError(w, r, raw, errPetPayGems, "insufficient balance")
		return
	}
	st := startVisit(minipetState(acc, pet), now)
	setMiniPetState(acc, st)
	if err := store.SaveLocked(lines, acc); err != nil {
		*acc = prev
		httpx.WriteJSON(w, http.StatusInternalServerError, httpx.SaveFailedBody)
		return
	}
	writeMiniPetResult(w, miniPetCallReply(acc, level, true, rules.PaidCallGems, remainMillis(st.NextCall, now)))
}

// miniPetRows are the arrange rows of the Minipets placed on level in owner's room. exposureYn is true for an owned pet
// and during a visit; defaultCallYn when the free call is open; petTouchableYn while the visit's petting is unused.
func miniPetRows(owner *store.Account, level string, now time.Time) []map[string]any {
	rows := []map[string]any{}
	idx := miniPets()
	for _, pet := range idx.pets {
		item := idx.itemOf[pet]
		if placedLevel(owner, item) != level {
			continue
		}
		st := minipetState(owner, pet)
		_, owned := ownedPet(owner, pet)
		visit := st.VisitEnd > now.Unix()
		maxExp := miniPetMaxExp(pet)
		exp := st.Exp
		if owned {
			exp = max(exp, maxExp)
		}
		remain := int64(0)
		if !owned {
			remain = remainMillis(st.NextCall, now)
		}
		rows = append(rows, room.MiniPetArrangeRow(room.MiniPetArrange{
			Aid: owner.Aid, Item: item, Level: level, Pet: pet, Name: PetDisplayName(pet), PetID: minipetID(owner, pet),
			Exp: exp, MaxExp: maxExp, CallRemain: remain, Exposed: owned || visit,
			Touchable: visit && !st.Petted, CanCall: !owned && remain == 0, Final: owned || exp >= maxExp,
		}))
	}
	return rows
}

// miniPetArrange serves the arrange list of the room of aid: the Minipet rows, then the owned pets Cherry arranged
// before the loop. represent selects the represent list's envelope.
func miniPetArrange(w http.ResponseWriter, r *http.Request, aid, level string, represent bool) {
	acc := minipetAccount(w, r)
	if acc == nil {
		return
	}
	rows := []map[string]any{}
	if owner := store.AccountByAidLocked(aid); owner != nil && room.EnsurePetsLocked(owner) == nil {
		rows = miniPetRows(owner, level, NowFunc())
		rows = append(rows, room.LegacyArrangeRows(owner.Aid, slices.Clone(owner.Pets), level, func(cd string) bool {
			item, paired := MiniPetItem(cd)
			return paired && placedLevel(owner, item) != ""
		})...)
	}
	store.AccountsMu.Unlock()
	if represent {
		writeMiniPetResult(w, map[string]any{"arrangedPetList": map[string]any{"list": rows},
			"hostRepresentPetList": []any{}, "guestRepresentPetList": []any{}})
		return
	}
	writeMiniPetResult(w, map[string]any{"list": rows})
}

// miniPetSkin serves default/skin?interiorItemCode=<RUPI>: the pet on a paired item (ResAdditionalDPInfo), else an
// empty result (the silent no-op).
func miniPetSkin(w http.ResponseWriter, r *http.Request) {
	if minipetAccount(w, r) == nil {
		return
	}
	store.AccountsMu.Unlock()
	item := r.URL.Query().Get("interiorItemCode")
	pet, ok := MiniPetOfItem(item)
	if !ok {
		writeMiniPetResult(w, map[string]any{})
		return
	}
	writeMiniPetResult(w, map[string]any{"itemCode": item, "skinCode": pet, "linkPosX": 0, "linkPosY": 0,
		"linkScale": 100, "petName": PetDisplayName(pet), "specialEffects": ""})
}

// MiniPetStrokeReply is the QuestActionCommand 112 that answers one petting (lpn encodes it). Gift is set when this
// petting adopted the pet.
type MiniPetStrokeReply struct {
	PetID, Exp, MaxExp, Gained int64
	Final, LevelUp             bool
	Gift                       *MiniPetGift
}

// MiniPetGift is the adoption present and the pet's identity for the adoption popup.
type MiniPetGift struct {
	Item, ItemName, Grade string
	Pet, PetName          string
}

// MiniPetStroke records one petting of the pet petID by the owner aid during its visit (TLUP 101/4, target_pet). It
// reports false, with nothing changed, when that pet is not visiting for aid, was petted in this visit, or is not
// paired and placed. At maxExp an unowned pet is adopted: it joins the owned pets and a present joins the closet.
func MiniPetStroke(aid string, petID int64) (MiniPetStrokeReply, bool) {
	now := NowFunc()
	store.AccountsMu.Lock()
	defer store.AccountsMu.Unlock()
	acc := store.AccountByAidLocked(aid)
	if acc == nil {
		return MiniPetStrokeReply{}, false
	}
	pet, ok := minipetByID(acc, petID)
	if !ok {
		return MiniPetStrokeReply{}, false
	}
	item, _ := MiniPetItem(pet)
	st := minipetState(acc, pet)
	if placedLevel(acc, item) == "" || st.VisitEnd <= now.Unix() || st.Petted {
		return MiniPetStrokeReply{}, false
	}
	rules := ActivePets().Minipet
	maxExp := miniPetMaxExp(pet)
	prev := *acc
	st.Exp = min(st.Exp+rules.GainedExp, maxExp)
	st.Petted = true
	reply := MiniPetStrokeReply{PetID: petID, Exp: st.Exp, MaxExp: maxExp, Gained: rules.GainedExp, Final: st.Exp >= maxExp}
	if _, owned := ownedPet(acc, pet); reply.Final && !owned {
		name := PetDisplayName(pet)
		present := rules.Presents[presentPick(len(rules.Presents))]
		acc.Pets = append(slices.Clone(acc.Pets), store.PetItem{ID: petID, Cd: pet, Name: name, Memo: st.Memo})
		acc.InventoryCodes = store.AppendUniqueItemCodes(store.AccountInventoryCodes(acc), []string{present})
		st.Gift = present
		st.AdoptedAt = now.Unix()
		if st.FirstVisit == 0 {
			st.FirstVisit = now.Unix()
		}
		reply.LevelUp = true
		reply.Gift = &MiniPetGift{Item: present, ItemName: ItemName(present), Grade: "NORMAL", Pet: pet, PetName: name}
	}
	setMiniPetState(acc, st)
	if err := store.SaveLocked(nil, acc); err != nil {
		*acc = prev
		return MiniPetStrokeReply{}, false
	}
	return reply, true
}

// miniPetBuy serves purchase/interior/buy for Minipet items (RUPI codes): each item is granted as a room item at its
// pet's price. An item the account already holds is refused (a Minipet item cannot be bought twice).
func miniPetBuy(w http.ResponseWriter, r *http.Request, raw []byte, req shopBuyReq, give string) {
	if len(req.Items) == 0 || len(req.Items) > maxShopBatch || give != "" && give != "null" && give != "0" {
		shopError(w, r, raw, errUnknown, "bad product")
		return
	}
	c := loadCatalog()
	codes := make([]string, 0, len(req.Items))
	var total int64
	cash := false
	for i, it := range req.Items {
		pet, ok := MiniPetOfItem(it.ItemCode)
		if !ok || c.minipetItem(it.ItemCode) == nil {
			shopError(w, r, raw, errUnknown, "unknown item")
			return
		}
		price, c2 := miniPetPrice(pet)
		if cp, ok := lenientInt(it.Price); !ok || cp != price {
			shopError(w, r, raw, errUpdated, "shop updated")
			return
		}
		if i > 0 && c2 != cash {
			shopError(w, r, raw, errUnknown, "unknown item")
			return
		}
		cash = c2
		total += price
		codes = append(codes, it.ItemCode)
	}
	store.AccountsMu.Lock()
	defer store.AccountsMu.Unlock()
	acc := store.AccountByTokenLocked(httpx.CookieValue(r, "AV_AUTH"))
	if acc == nil {
		httpx.WriteJSON(w, http.StatusNotFound, httpx.UnknownSessionBody)
		return
	}
	if err := room.EnsureRoomLocked(acc); err != nil {
		httpx.WriteJSON(w, http.StatusInternalServerError, httpx.SaveFailedBody)
		return
	}
	owned := map[string]bool{}
	for _, it := range acc.RoomItems {
		owned[it.Cd] = true
	}
	for i, code := range codes {
		if owned[code] || slices.Contains(codes[:i], code) {
			shopError(w, r, raw, errOwned, "already owned")
			return
		}
	}
	deltas := store.LedgerDeltas{Gems: -total}
	if cash {
		deltas = store.LedgerDeltas{Cash: -total}
	}
	previous := *acc
	lines, err := store.SpendLocked(acc, deltas, "shop:minipet:"+strings.Join(codes, ","))
	if err != nil {
		shopError(w, r, raw, errNoMoney, "insufficient balance")
		return
	}
	items := slices.Clone(acc.RoomItems)
	seq := max(acc.NextRoomSeq, store.FirstRoomSeq)
	for _, it := range items {
		seq = max(seq, it.Seq+1)
	}
	for _, code := range codes {
		items = append(items, store.RoomItem{Seq: seq, Cd: code})
		seq++
	}
	acc.RoomItems, acc.NextRoomSeq = items, seq
	if err := store.SaveLocked(lines, acc); err != nil {
		*acc = previous
		httpx.WriteJSON(w, http.StatusInternalServerError, httpx.SaveFailedBody)
		return
	}
	balance := acc.Gems
	if cash {
		balance = acc.Cash
	}
	writeMiniPetResult(w, map[string]any{"balance": balance, "resultCode": 0, "buyItems": codes})
}

// HandlePet serves /v4/pet/ for the Minipet loop: the paired pets' call, paid call, arrange lists,
// default skin, Pet Book list and Pet Book detail. Every other path, and a call of an owned pet that
// has no room item, is room.HandlePet's.
func HandlePet(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/v4/pet/")
	parts := strings.Split(path, "/")
	get := r.Method == http.MethodGet
	switch {
	case get && len(parts) == 3 && parts[0] == "room" && parts[1] == "call":
		if miniPetCall(w, r, parts[2]) {
			return
		}
	case r.Method == http.MethodPost && path == "book/prch/call":
		miniPetPaidCall(w, r)
		return
	case get && len(parts) == 5 && parts[0] == "room" && parts[1] == "arrange" && parts[2] == "list" && room.RoomLevelValid(parts[4]):
		miniPetArrange(w, r, parts[3], parts[4], false)
		return
	case get && len(parts) == 6 && parts[0] == "room" && parts[1] == "arrange" && parts[2] == "represent" && parts[3] == "list" && room.RoomLevelValid(parts[5]):
		miniPetArrange(w, r, parts[4], parts[5], true)
		return
	case get && path == "book/race/info/default/skin":
		miniPetSkin(w, r)
		return
	case get && path == "book/race/detail":
		miniPetBookDetail(w, r)
		return
	case r.Method == http.MethodPost && path == "book/change/memo":
		miniPetChangeMemo(w, r)
		return
	case get && len(parts) == 3 && parts[0] == "gift" && parts[1] == "list":
		miniPetCollectionList(w, r, parts[2])
		return
	}
	room.HandlePet(w, r)
}

func speciesMap(all []room.PetSpecies) map[string]room.PetSpecies {
	byCode := make(map[string]room.PetSpecies, len(all))
	for _, sp := range all {
		byCode[sp.Code] = sp
	}
	return byCode
}

func ownedRoomItemSet(acc *store.Account) map[string]bool {
	if acc == nil {
		return nil
	}
	owned := make(map[string]bool, len(acc.RoomItems))
	for _, it := range acc.RoomItems {
		owned[it.Cd] = true
	}
	return owned
}

// miniPetGiftOf returns the clothing item code gifted by pet on adoption: the recorded MiniPetState.Gift
// when present, else a deterministic pick from ActivePets().Minipet.Presents for already-adopted pets
// predating the Gift field, or "" for pets not yet adopted.
func miniPetGiftOf(acc *store.Account, pet string) string {
	if acc == nil {
		return ""
	}
	if st := minipetState(acc, pet); st.Gift != "" {
		return st.Gift
	}
	if _, owned := ownedPet(acc, pet); !owned {
		return ""
	}
	presents := ActivePets().Minipet.Presents
	if len(presents) == 0 {
		return ""
	}
	h := fnv.New32a()
	_, _ = h.Write([]byte(acc.Aid))
	_, _ = h.Write([]byte{':'})
	_, _ = h.Write([]byte(pet))
	return presents[int(h.Sum32())%len(presents)]
}

func miniPetGiftFields(acc *store.Account, pet string) (int, string) {
	code := miniPetGiftOf(acc, pet)
	if code == "" {
		return 0, ""
	}
	id, _ := itemcode.ID(code)
	return int(id), code
}

// miniPetFirstVisitMillis formats FirstVisit (or AdoptedAt) as Unix milliseconds for ResPetBookList
// (which parses atoll(firstVisited)/1000 into NaPetBookDetailPopup::SetFirstVisitTime).
func miniPetFirstVisitMillis(st store.MiniPetState) string {
	ts := st.FirstVisit
	if ts <= 0 {
		ts = st.AdoptedAt
	}
	if ts <= 0 {
		return ""
	}
	return strconv.FormatInt(ts*1000, 10)
}

func miniPetDateStr(unixSec int64) string {
	if unixSec <= 0 {
		return ""
	}
	return time.Unix(unixSec, 0).UTC().Format("2006-01-02")
}

// miniPetOwnedBookRow builds the PetBookRow for an account's own pet (MYPET tab, race tabs and detail).
func miniPetOwnedBookRow(acc *store.Account, p store.PetItem, sp *room.PetSpecies, rules MiniPetRules) room.PetBookRow {
	raceID := room.PetSkinID(p.Cd)
	catgCd := ""
	raceNm := PetDisplayName(p.Cd)
	if sp != nil {
		raceID = int(sp.ID)
		catgCd = sp.Catg
		if sp.Name != "" {
			raceNm = sp.Name
		}
	}
	if catgCd == "" {
		catgCd = PetCategoryCode(p.Cd)
	}
	if raceNm == "" {
		raceNm = "Minipet"
	}
	petName := p.Name
	if petName == "" {
		petName = raceNm
	}
	petID := strconv.FormatInt(p.ID, 10)
	maxExp := miniPetMaxExp(p.Cd)
	st := minipetState(acc, p.Cd)
	giftID, giftCode := miniPetGiftFields(acc, p.Cd)
	row := room.PetBookRow{
		RaceID:                     raceID,
		CatgCd:                     catgCd,
		LocaleCd:                   "en",
		RaceNm:                     raceNm,
		PetID:                      petID,
		PetMemo:                    p.Memo,
		AvatarID:                   acc.Aid,
		FirstVisited:               miniPetFirstVisitMillis(st),
		PetName:                    petName,
		SkinSerial:                 petID,
		SkinID:                     room.PetSkinID(p.Cd),
		SkinCode:                   p.Cd,
		LinkScale:                  100,
		Level:                      1,
		RepresentYn:                p.Rep,
		InteriorOwnYn:              true,
		CallPayCode:                "GEM",
		CallPrice:                  int(rules.PaidCallGems),
		MaxPaidCallCount:           10,
		DefaultCallRemainTimestamp: "0",
		Exp:                        int(max(st.Exp, maxExp)),
		MaxExp:                     int(maxExp),
		FinalLevelYn:               true,
		GiftItemID:                 giftID,
		GiftItemCode:               giftCode,
	}
	if row.PetMemo == "" {
		row.PetMemo = st.Memo
	}
	if item, ok := MiniPetItem(p.Cd); ok {
		placed := placedLevel(acc, item) != ""
		row.ItemCode = item
		row.ArrangedYn = placed
		row.ExposureYn = placed || p.Room != ""
	} else {
		row.ItemCode = p.Cd
		row.ArrangedYn = p.Room != ""
		row.ExposureYn = p.Room != ""
		row.DefaultCallYn = p.Room == ""
	}
	return row
}

// miniPetSpeciesBookRow builds the PetBookRow for a catalog Minipet (race tabs and unowned detail).
func miniPetSpeciesBookRow(acc *store.Account, sp room.PetSpecies, ownedItems map[string]bool, rules MiniPetRules, now time.Time) room.PetBookRow {
	itemCode := sp.Item
	if itemCode == "" {
		itemCode = sp.Code
	}
	if acc != nil {
		ownsItem := sp.Item != "" && ownedItems[sp.Item]
		arranged := sp.Item != "" && placedLevel(acc, sp.Item) != ""
		ownedPetRow, owned := ownedPet(acc, sp.Code)
		st := minipetState(acc, sp.Code)
		if ownsItem || arranged || owned || st.Exp > 0 || st.VisitEnd > 0 || st.NextCall > 0 {
			petID := strconv.FormatInt(minipetID(acc, sp.Code), 10)
			visit := st.VisitEnd > now.Unix()
			remain := int64(0)
			if !owned {
				remain = remainMillis(st.NextCall, now)
			}
			exposure := owned || visit
			level := 0
			exp := st.Exp
			final := st.Exp >= sp.MaxExp
			petName := sp.Name
			var giftID int
			var giftCode string
			if owned {
				level = 1
				exp = max(exp, sp.MaxExp)
				final = true
				if ownedPetRow.Name != "" {
					petName = ownedPetRow.Name
				}
				giftID, giftCode = miniPetGiftFields(acc, sp.Code)
			}
			memo := st.Memo
			if owned && ownedPetRow.Memo != "" {
				memo = ownedPetRow.Memo
			}
			return room.PetBookRow{
				RaceID:                     int(sp.ID),
				CatgCd:                     sp.Catg,
				ItemCode:                   itemCode,
				LocaleCd:                   "en",
				RaceNm:                     sp.Name,
				PetID:                      petID,
				PetMemo:                    memo,
				AvatarID:                   acc.Aid,
				FirstVisited:               miniPetFirstVisitMillis(st),
				PetName:                    petName,
				SkinSerial:                 petID,
				SkinID:                     int(sp.ID),
				SkinCode:                   sp.Code,
				LinkScale:                  100,
				CallPayCode:                "GEM",
				InteriorOwnYn:              ownsItem || owned,
				ArrangedYn:                 arranged || (owned && ownedPetRow.Room != ""),
				ExposureYn:                 exposure,
				DefaultCallYn:              arranged && !exposure && remain == 0,
				DefaultCallRemainTimestamp: strconv.FormatInt(remain, 10),
				Exp:                        int(exp),
				MaxExp:                     int(sp.MaxExp),
				FinalLevelYn:               final,
				GiftItemID:                 giftID,
				GiftItemCode:               giftCode,
				Level:                      level,
				RepresentYn:                owned && ownedPetRow.Rep,
				CallPrice:                  int(rules.PaidCallGems),
				MaxPaidCallCount:           10,
			}
		}
	}
	// NaPetBookCell::InitUI / SetPetState / NaPetBookPhotoCell::InitUI compare skinSerial == "0" to
	// render unowned pets as greyed silhouettes labeled with raceNm instead of full-color with blank petName.
	return room.PetBookRow{
		RaceID:      int(sp.ID),
		CatgCd:      sp.Catg,
		ItemCode:    itemCode,
		LocaleCd:    "en",
		RaceNm:      sp.Name,
		PetID:       "0",
		SkinSerial:  "0",
		CallPayCode: "GEM",
		ExposureYn:  true,
		MaxExp:      int(sp.MaxExp),
		SkinID:      int(sp.ID),
		SkinCode:    sp.Code,
		LinkScale:   100,
	}
}

// petBookRaceRank orders race-tab rows so the player's own pets appear at the top instead of buried
// behind dozens of unowned species.
func petBookRaceRank(row room.PetBookRow) int {
	switch {
	case row.RepresentYn:
		return 0
	case row.Level > 0:
		return 1
	case row.ArrangedYn:
		return 2
	case row.InteriorOwnYn:
		return 3
	case row.SkinSerial != "" && row.SkinSerial != "0":
		return 4
	default:
		return 5
	}
}

// HandlePetBook serves GET /v4/pet/book/race/list/<category> with Minipet ownership, placement,
// visit and cooldown state.
func HandlePetBook(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpx.ServeNotFound(w)
		return
	}
	category := strings.TrimPrefix(r.URL.Path, "/v4/pet/book/race/list/")
	all := PetSpecies()
	rules := ActivePets().Minipet
	list := []room.PetBookRow{}
	if category == "MYPET" {
		acc := minipetAccount(w, r)
		if acc == nil {
			return
		}
		byCode := speciesMap(all)
		for _, p := range acc.Pets {
			var sp *room.PetSpecies
			if s, ok := byCode[p.Cd]; ok {
				sp = &s
			}
			list = append(list, miniPetOwnedBookRow(acc, p, sp, rules))
		}
		store.AccountsMu.Unlock()
	} else {
		store.AccountsMu.Lock()
		acc := store.AccountByTokenLocked(httpx.CookieValue(r, "AV_AUTH"))
		if acc != nil {
			_ = room.EnsurePetsLocked(acc)
		}
		now := NowFunc()
		ownedItems := ownedRoomItemSet(acc)
		seen := make(map[string]bool, len(all))
		for _, sp := range all {
			if sp.Catg != category {
				continue
			}
			seen[sp.Code] = true
			list = append(list, miniPetSpeciesBookRow(acc, sp, ownedItems, rules, now))
		}
		if acc != nil {
			for _, p := range acc.Pets {
				if seen[p.Cd] || PetCategoryCode(p.Cd) != category {
					continue
				}
				seen[p.Cd] = true
				list = append(list, miniPetOwnedBookRow(acc, p, nil, rules))
			}
		}
		store.AccountsMu.Unlock()
		slices.SortStableFunc(list, func(a, b room.PetBookRow) int {
			return petBookRaceRank(a) - petBookRaceRank(b)
		})
	}
	writeMiniPetResult(w, map[string]any{"list": list})
}

func petSpeciesFor(byCode map[string]room.PetSpecies, pet string) room.PetSpecies {
	if sp, ok := byCode[pet]; ok {
		return sp
	}
	item, _ := MiniPetItem(pet)
	catg := ""
	if it := loadCatalog().byCode[pet]; it != nil {
		catg = it.Catg
	}
	return room.PetSpecies{
		Code: pet, ID: int64(room.PetSkinID(pet)), Catg: catg,
		Item: item, Name: PetDisplayName(pet), MaxExp: miniPetMaxExp(pet),
	}
}

func hasMiniPetActivity(acc *store.Account, pet, item string, ownedItems map[string]bool) bool {
	ownsItem := item != "" && ownedItems[item]
	arranged := item != "" && placedLevel(acc, item) != ""
	st := minipetState(acc, pet)
	return ownsItem || arranged || st.Exp > 0 || st.VisitEnd > 0 || st.NextCall > 0
}

// miniPetBookDetail serves GET /v4/pet/book/race/detail?petId=<id>.
func miniPetBookDetail(w http.ResponseWriter, r *http.Request) {
	acc := minipetAccount(w, r)
	if acc == nil {
		return
	}
	defer store.AccountsMu.Unlock()
	id, err := strconv.ParseInt(r.URL.Query().Get("petId"), 10, 64)
	if err != nil {
		httpx.WriteJSON(w, http.StatusBadRequest, `{"errorCode":"400"}`)
		return
	}
	all := PetSpecies()
	byCode := speciesMap(all)
	rules := ActivePets().Minipet
	speciesOf := func(cd string) *room.PetSpecies {
		if s, ok := byCode[cd]; ok {
			return &s
		}
		return nil
	}
	if i := slices.IndexFunc(acc.Pets, func(p store.PetItem) bool { return p.ID == id }); i >= 0 {
		p := acc.Pets[i]
		writeMiniPetResult(w, miniPetOwnedBookRow(acc, p, speciesOf(p.Cd), rules))
		return
	}
	if pet, ok := minipetByID(acc, id); ok {
		writeMiniPetResult(w, miniPetSpeciesBookRow(acc, petSpeciesFor(byCode, pet), ownedRoomItemSet(acc), rules, NowFunc()))
		return
	}
	for _, other := range store.AllAccountsLocked() {
		if other == acc {
			continue
		}
		if i := slices.IndexFunc(other.Pets, func(p store.PetItem) bool { return p.ID == id }); i >= 0 {
			p := other.Pets[i]
			writeMiniPetResult(w, miniPetOwnedBookRow(other, p, speciesOf(p.Cd), rules))
			return
		}
	}
	if id >= 1_000_000_000_000_000 {
		ownerAid := strconv.FormatInt((id-1_000_000_000_000_000)/100_000_000, 10)
		if other := store.AccountByAidLocked(ownerAid); other != nil && other != acc {
			if pet, ok := minipetByID(other, id); ok {
				ownedItems := ownedRoomItemSet(other)
				sp := petSpeciesFor(byCode, pet)
				if hasMiniPetActivity(other, pet, sp.Item, ownedItems) {
					writeMiniPetResult(w, miniPetSpeciesBookRow(other, sp, ownedItems, rules, NowFunc()))
					return
				}
			}
		}
	}
	httpx.WriteJSON(w, http.StatusBadRequest, `{"errorCode":"400"}`)
}

// miniPetChangeMemo serves POST /v4/pet/book/change/memo to update the owner's Pet Book memo.
func miniPetChangeMemo(w http.ResponseWriter, r *http.Request) {
	raw, err := httpx.ReadNativeBody(r)
	var req struct {
		PetID   json.RawMessage `json:"petId"`
		PetMemo string          `json:"petMemo"`
	}
	if err == nil {
		_, err = httpx.UnmarshalNativeJSON(raw, &req)
	}
	if err != nil {
		httpx.WriteJSON(w, http.StatusBadRequest, httpx.BadRequestBody)
		return
	}
	id, err := strconv.ParseInt(strings.Trim(string(req.PetID), `"`), 10, 64)
	if err != nil || id <= 0 {
		httpx.WriteJSON(w, http.StatusBadRequest, httpx.BadRequestBody)
		return
	}
	memo := strings.NewReplacer("\r", "", "\n", "").Replace(req.PetMemo)
	if utf8.RuneCountInString(memo) > 50 {
		httpx.WriteJSON(w, http.StatusBadRequest, `{"errorCode":"400","errorMessage":"memo too long"}`)
		return
	}
	memo = MaskBannedWords(memo)
	acc := minipetAccount(w, r)
	if acc == nil {
		return
	}
	defer store.AccountsMu.Unlock()
	prev := *acc
	if i := slices.IndexFunc(acc.Pets, func(p store.PetItem) bool { return p.ID == id }); i >= 0 {
		pets := slices.Clone(acc.Pets)
		pets[i].Memo = memo
		acc.Pets = pets
		if _, paired := MiniPetItem(pets[i].Cd); paired {
			st := minipetState(acc, pets[i].Cd)
			st.Memo = memo
			setMiniPetState(acc, st)
		}
	} else if pet, ok := minipetByID(acc, id); ok {
		item, _ := MiniPetItem(pet)
		if !hasMiniPetActivity(acc, pet, item, ownedRoomItemSet(acc)) {
			httpx.WriteJSON(w, http.StatusBadRequest, `{"errorCode":"600101","errorMessage":"pet not found"}`)
			return
		}
		st := minipetState(acc, pet)
		st.Memo = memo
		setMiniPetState(acc, st)
	} else {
		httpx.WriteJSON(w, http.StatusBadRequest, `{"errorCode":"600101","errorMessage":"pet not found"}`)
		return
	}
	if err := store.SaveLocked(nil, acc); err != nil {
		*acc = prev
		httpx.WriteJSON(w, http.StatusInternalServerError, httpx.SaveFailedBody)
		return
	}
	writeMiniPetResult(w, true)
}

// petCollectionRow matches NaDispatcherPet::ResPetCollectionInfo (0x1b65db0): result is a JSON array of
// 15-field objects rendered by NaPetCollectionBook (3 columns per row) and NaPetCollectionGiftPopup.
type petCollectionRow struct {
	GatherYn         bool   `json:"gatherYn"`
	RaceCategoryCode string `json:"raceCategoryCode"`
	RaceID           int    `json:"raceId"`
	Level            int    `json:"level"`
	GiftItemID       int    `json:"giftItemId"`
	GiftItemCode     string `json:"giftItemCode"`
	ItemGradeCode    string `json:"itemGradeCode"`
	ItemName         string `json:"itemName"`
	ItemType         string `json:"itemType"`
	Description      string `json:"description"`
	RegisterDate     string `json:"registerDate"`
	AvatarID         string `json:"avatarId"`
	PetID            string `json:"petId"`
	PetName          string `json:"petName"`
	PetSkinItemCode  string `json:"petSkinItemCode"`
}

func validPetCategory(cat string) bool {
	return cat == "UDOG" || cat == "UCAT" || cat == "UTOY" || cat == "URIDE"
}

func makePetCollectionRow(acc *store.Account, cat, code, fallbackName string, raceID int) petCollectionRow {
	ownedRow, owned := ownedPet(acc, code)
	if !owned {
		return petCollectionRow{
			GatherYn:         false,
			RaceCategoryCode: cat,
			RaceID:           raceID,
			Level:            0,
			PetSkinItemCode:  code,
		}
	}
	petName := ownedRow.Name
	if petName == "" {
		petName = fallbackName
	}
	if petName == "" {
		petName = "Minipet"
	}
	giftID, giftCode := miniPetGiftFields(acc, code)
	_, _, grade, _ := ItemPrice(giftCode)
	st := minipetState(acc, code)
	return petCollectionRow{
		GatherYn:         true,
		RaceCategoryCode: cat,
		RaceID:           raceID,
		Level:            1,
		GiftItemID:       giftID,
		GiftItemCode:     giftCode,
		ItemGradeCode:    grade,
		ItemName:         ItemName(giftCode),
		ItemType:         itemTypeOf(giftCode),
		Description:      "Present from your pet " + petName,
		RegisterDate:     miniPetDateStr(st.AdoptedAt),
		AvatarID:         acc.Aid,
		PetID:            strconv.FormatInt(ownedRow.ID, 10),
		PetName:          petName,
		PetSkinItemCode:  code,
	}
}

// miniPetCollectionList serves GET /v4/pet/gift/list/<UDOG|UCAT|UTOY|URIDE> for NaPetCollectionBook.
func miniPetCollectionList(w http.ResponseWriter, r *http.Request, category string) {
	acc := minipetAccount(w, r)
	if acc == nil {
		return
	}
	if !validPetCategory(category) {
		store.AccountsMu.Unlock()
		writeMiniPetResult(w, []petCollectionRow{})
		return
	}
	all := PetSpecies()
	rows := []petCollectionRow{}
	seen := make(map[string]bool, len(all))
	for _, sp := range all {
		if sp.Catg != category {
			continue
		}
		seen[sp.Code] = true
		rows = append(rows, makePetCollectionRow(acc, category, sp.Code, sp.Name, int(sp.ID)))
	}
	for _, p := range acc.Pets {
		if seen[p.Cd] || PetCategoryCode(p.Cd) != category {
			continue
		}
		seen[p.Cd] = true
		rows = append(rows, makePetCollectionRow(acc, category, p.Cd, PetDisplayName(p.Cd), room.PetSkinID(p.Cd)))
	}
	store.AccountsMu.Unlock()
	slices.SortStableFunc(rows, func(a, b petCollectionRow) int {
		switch {
		case a.GatherYn && !b.GatherYn:
			return -1
		case !a.GatherYn && b.GatherYn:
			return 1
		default:
			return 0
		}
	})
	writeMiniPetResult(w, rows)
}

// MigrateOwnedPetItems grants the paired RUPI room item (marked store.GiftPaid so it cannot be recycled
// for Gems) to every account that already owns a paired PUPE pet without its room item (e.g. pets bought
// before the Minipet placement loop). Idempotent: accounts that already hold their paired room items are
// untouched. cmd/cherry calls it once at startup after operator data loads.
func MigrateOwnedPetItems() error {
	store.AccountsMu.Lock()
	defer store.AccountsMu.Unlock()
	var needRoom []*store.Account
	var affected []*store.Account
	seen := map[*store.Account]bool{}
	for _, acc := range store.AllAccountsLocked() {
		if seen[acc] || len(acc.Pets) == 0 {
			seen[acc] = true
			continue
		}
		seen[acc] = true
		owned := ownedRoomItemSet(acc)
		missing := false
		for _, p := range acc.Pets {
			if item, paired := MiniPetItem(p.Cd); paired && !owned[item] {
				missing = true
				break
			}
		}
		if !missing {
			continue
		}
		affected = append(affected, acc)
		if len(acc.RoomItems) == 0 {
			needRoom = append(needRoom, acc)
		}
	}
	if len(affected) == 0 {
		return nil
	}
	// Pass 1: seed starter room kits on affected accounts whose RoomItems are still empty before adding
	// paired RUPI items (EnsureRoomLocked only seeds when len(acc.RoomItems) == 0).
	for _, acc := range needRoom {
		if err := room.EnsureRoomLocked(acc); err != nil {
			return err
		}
	}
	type change struct {
		acc  *store.Account
		prev store.Account
	}
	var changes []change
	var grantedTotal int
	restore := func() {
		for _, c := range changes {
			*c.acc = c.prev
		}
	}
	for _, acc := range affected {
		owned := ownedRoomItemSet(acc)
		var missing []string
		for _, p := range acc.Pets {
			if item, paired := MiniPetItem(p.Cd); paired && !owned[item] {
				owned[item] = true
				missing = append(missing, item)
			}
		}
		if len(missing) == 0 {
			continue
		}
		changes = append(changes, change{acc, *acc})
		items := slices.Clone(acc.RoomItems)
		seq := max(acc.NextRoomSeq, store.FirstRoomSeq)
		for _, it := range items {
			seq = max(seq, it.Seq+1)
		}
		for _, code := range missing {
			items = append(items, store.RoomItem{Seq: seq, Cd: code, Paid: store.GiftPaid})
			seq++
		}
		acc.RoomItems, acc.NextRoomSeq = items, seq
		grantedTotal += len(missing)
	}
	if len(changes) == 0 {
		return nil
	}
	named := make([]*store.Account, 0, len(changes))
	for _, c := range changes {
		named = append(named, c.acc)
	}
	if err := store.SaveLocked(nil, named...); err != nil {
		restore()
		return err
	}
	log.Printf("cherry: pet room item migration: granted %d paired room items across %d accounts", grantedTotal, len(changes))
	return nil
}

package economy

import (
	"encoding/json"
	"maps"
	"math/rand/v2"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"cherry/internal/httpx"
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

// startVisit opens a visit of the configured length at now and clears the petting of the previous one.
func startVisit(st store.MiniPetState, now time.Time) store.MiniPetState {
	st.VisitEnd = now.Add(time.Duration(ActivePets().Minipet.VisitMinutes) * time.Minute).Unix()
	st.Petted = false
	return st
}

// miniPetCallReply is the reply of a call (ResPetFreeCall / ResPetCall shape).
func miniPetCallReply(acc *store.Account, level string, paid bool, price, remain int64) map[string]any {
	return map[string]any{
		"resultCode": 0, "balance": acc.Gems, "balanceUnit": "GEM", "purchasedCallFlag": paid,
		"defaultCallRemainTimestamp": strconv.FormatInt(max(remain, 0), 10), "exposureYn": true, "firstVisitYn": false,
		"callPayCode": "", "callPrice": price, "groundLevel": level,
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
	writeMiniPetResult(w, miniPetCallReply(acc, level, false, 0, st.NextCall-now.Unix()))
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
	if req.CoinUnit != "GEM" {
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
	writeMiniPetResult(w, miniPetCallReply(acc, level, true, rules.PaidCallGems, st.NextCall-now.Unix()))
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
		remain := max(st.NextCall-now.Unix(), 0)
		rows = append(rows, room.MiniPetArrangeRow(room.MiniPetArrange{
			Aid: owner.Aid, Item: item, Level: level, Pet: pet, Name: PetDisplayName(pet), PetID: minipetID(owner, pet),
			Exp: st.Exp, MaxExp: maxExp, CallRemain: remain, Exposed: owned || visit,
			Touchable: visit && !st.Petted, CanCall: remain == 0, Final: st.Exp >= maxExp,
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
		acc.Pets = append(slices.Clone(acc.Pets), store.PetItem{ID: petID, Cd: pet, Name: name})
		acc.InventoryCodes = store.AppendUniqueItemCodes(store.AccountInventoryCodes(acc), []string{present})
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

// HandlePet serves /v4/pet/ for the Minipet loop: the paired pets' call, paid call, arrange lists and default skin.
// Every other path, and a call of an owned pet that has no room item, is room.HandlePet's.
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
	}
	room.HandlePet(w, r)
}

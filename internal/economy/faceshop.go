package economy

import (
	"encoding/json"
	"io"
	"maps"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"cherry/data"
	"cherry/internal/assets"
	"cherry/internal/httpx"
	"cherry/internal/itemcode"
	"cherry/internal/store"
)

// Face Shop (goSomewhere(faceShop) -> scene 3/316 NaFaceShop). Static research:
//   GET  /v4/faceshop/v2/shop/<shopId>?shopLocation=HOME|SQUARE
//        ReqFaceShopDataWithShopID @0x1b9a068; parser sDataFaceShop::SetParserJSON @0x2ada478
//   GET  /v4/voucher/own/count/faceshop        ResFaceShopOwnVoucherCount @0x1b2c6a4
//   POST /v4/faceshop/saveAndPurchase/v2       ReqFaceShopSaveNPurchase @0x1be1f0c,
//        ResFaceShopSaveNPurchase @0x1c0b780
// Shop data is one catalog per avatar type; the single price is shop.discountedPrice.

// FaceCatalog is the 1,173 device-renderable human face codes (HE14/EB200/EY507/NO54/MO398). The converted human
// faces (429 CU HE/EB/EY/NO/MO custom items in the converted index) join it through faceCatalog. The animal parts are
// AnimalFaceCatalog (data/animal_face_codes.txt: the 114 original CA EY/NO/MO faces, then the 24 FA, 19 EA and 15 TL
// originals of the 10.1 backup) plus the converted CA parts (animalFaceCatalog). Animal accounts only.
var (
	FaceCatalog          = strings.Fields(data.FaceCodes)
	FaceCatalogSet       = codeSet(FaceCatalog)
	AnimalFaceCatalog    = strings.Fields(data.AnimalFaceCodes)
	AnimalFaceCatalogSet = codeSet(AnimalFaceCatalog)
)

func codeSet(codes []string) map[string]struct{} {
	m := make(map[string]struct{}, len(codes))
	for _, c := range codes {
		m[c] = struct{}{}
	}
	return m
}

// faceShopCategoryKey maps an item category (code[2:4]) to the shop-data key
// suffix per avatar type prefix (M/F: FACE BROW EYE NOSE MOUTH; native list
// offsets @0x1b01db0: HE=38 EB=40 EY=41 NO=42 MO=43).
var faceShopCategoryKey = map[string]string{"HE": "FACE", "EB": "BROW", "EY": "EYE", "NO": "NOSE", "MO": "MOUTH"}

// animalFaceShopCategoryKey: the animal slots, from the "A" branch of sDataFaceShop::SetParserJSON (AFACE = cat 39
// FA, AEYE = 41 EY, ANOSE = 42 NO, AMOUTH = 43 MO, AEAR = 57 EA, ATAIL = 58 TL; NaFaceShop::GetCateItemList).
var animalFaceShopCategoryKey = map[string]string{"FA": "FACE", "EY": "EYE", "NO": "NOSE", "MO": "MOUTH", "EA": "EAR", "TL": "TAIL"}

var faceShopSlots = []string{"HE", "EB", "EY", "NO", "MO"}

// animalWearSlots are the animal skin, ear and tail slots, in the order the client's animal branch of
// NaFaceShop::DoBuyFaceItem (0x1c08448) reads them: FA, EA, TL. Their changed codes travel in invenItemList.
var animalWearSlots = []string{"FA", "EA", "TL"}

// isHumanFace: a CU code in one of the five Face Shop slots.
func isHumanFace(code string) bool {
	if len(code) != 9 || code[:2] != "CU" {
		return false
	}
	_, ok := faceShopCategoryKey[code[2:4]]
	return ok
}

// isAnimalPart: a CA code in one of the six animal Face Shop slots (faces, skin, ears, tail).
func isAnimalPart(code string) bool {
	if len(code) != 9 || code[:2] != "CA" {
		return false
	}
	_, ok := animalFaceShopCategoryKey[code[2:4]]
	return ok
}

// isAnimalWear: a CA skin, ear or tail code, the parts a purchase changes through invenItemList.
func isAnimalWear(code string) bool {
	return isAnimalPart(code) && slices.Contains(animalWearSlots, code[2:4])
}

// animalBaseWear is the default animal item of a slot: CAFA0005O for FA, none for EA or TL (the ANIMAL base list).
func animalBaseWear(slot string) string {
	for _, c := range store.AvatarBaseItems["ANIMAL"] {
		if store.ItemSlot(c) == slot {
			return c
		}
	}
	return ""
}

// The index kinds converted codes are listed under: human faces in custom; animal parts in custom (faces, skin) and
// dress (ears and tails, the iOS folders they came from; cmd/cherry-assets convert). Both sets are checked for an
// animal part, so the kind never has to match the slot.
var (
	humanKinds  = []string{"custom"}
	animalKinds = []string{"custom", "dress"}
)

// convertedFaces lists the converted items of the given kinds that keep accepts, per kind in the order given, each
// kind in id order. assets.GeneratedItems is nil while itemconv.json is off, so none are listed then.
func convertedFaces(keep func(string) bool, kinds []string) []string {
	var codes []string
	for _, kind := range kinds {
		for _, id := range assets.GeneratedItems().IDs(kind) {
			if code, ok := itemcode.Code(id); ok && keep(code) {
				codes = append(codes, code)
			}
		}
	}
	return codes
}

// faceCatalog is every human face the Face Shop lists: FaceCatalog, then the converted human faces.
func faceCatalog() []string {
	return append(slices.Clone(FaceCatalog), convertedFaces(isHumanFace, humanKinds)...)
}

// animalFaceCatalog is every animal part the Face Shop lists: the original animal parts, then the converted ones.
func animalFaceCatalog() []string {
	return append(slices.Clone(AnimalFaceCatalog), convertedFaces(isAnimalPart, animalKinds)...)
}

// faceForSale reports whether a face or animal part can be bought now: a catalog code (human or animal), or a
// converted code the index lists. Which species may buy it is checked against the account in HandleFaceShopPurchase.
func faceForSale(code string) bool {
	if _, ok := FaceCatalogSet[code]; ok {
		return true
	}
	if _, ok := AnimalFaceCatalogSet[code]; ok {
		return true
	}
	id, ok := itemcode.ID(code)
	if !ok {
		return false
	}
	kinds := humanKinds
	switch {
	case isAnimalPart(code):
		kinds = animalKinds
	case !isHumanFace(code):
		return false
	}
	for _, kind := range kinds {
		if assets.GeneratedItems().Has(kind, id) {
			return true
		}
	}
	return false
}

type faceShopItem struct {
	CD             string `json:"cd"`
	SellingIcon    string `json:"sellingIconType"`
	SpecialEffects string `json:"specialEffects"`
}

type faceShopCategory struct {
	Items []faceShopItem `json:"items"`
}

type faceShopInfo struct {
	ID              int     `json:"id"`
	Name            string  `json:"name"`
	CpID            string  `json:"cpId"`
	ItemCd          string  `json:"itemCd"`
	Discount        bool    `json:"discount"`
	DcRate          float64 `json:"dcRate"`
	Price           int     `json:"price"`
	DiscountedPrice int     `json:"discountedPrice"`
	ShopType        string  `json:"shopType"`
	DisplayCoinUnit string  `json:"displayCoinUnit"`
}

type faceShopCD struct {
	CD string `json:"cd"`
}

type faceShopItemCode struct {
	ItemCode string `json:"itemCode"`
}

func faceShopAvatarType(gender string) string {
	switch gender {
	case "MALE":
		return "M"
	case "ANIMAL":
		return "A"
	}
	return "F"
}

// faceShopBody builds {"result":{...}}. Key types follow the native parsers:
// avatarType string; each category object with items[] of {cd,sellingIconType,
// specialEffects} strings; SET.items array; shop ints/bool/float/strings; pGown
// array of {cd}; basicFaceList array of {itemCode}. Animal accounts list their
// parts under AFACE/AEYE/ANOSE/AMOUTH/AEAR/ATAIL (originals first, then converted).
func faceShopBody(gender string, shopID int) []byte {
	avatarType := faceShopAvatarType(gender)
	result := map[string]any{"avatarType": avatarType}
	slots, list := faceShopCategoryKey, faceCatalog()
	if avatarType == "A" {
		slots, list = animalFaceShopCategoryKey, animalFaceCatalog()
	}
	cats := map[string]*faceShopCategory{}
	for slot, key := range slots {
		cat := &faceShopCategory{Items: []faceShopItem{}}
		cats[slot] = cat
		result[avatarType+key] = cat
	}
	for _, code := range list {
		cat := cats[code[2:4]]
		cat.Items = append(cat.Items, faceShopItem{CD: code})
	}
	// ponytail: no face sets (SET tab empty); set codes/previews unverified.
	result["SET"] = faceShopCategory{Items: []faceShopItem{}}
	result["shop"] = faceShopInfo{ID: shopID, Name: "Face Shop", DisplayCoinUnit: "G"}
	// pGown: the try-on gown. Base clothes are renderable by construction and are
	// stripped from the purchase request by NaFaceShop::CheckValidSendItem.
	base := store.AvatarBaseItems[gender]
	if base == nil {
		base = store.AvatarBaseItems["FEMALE"]
	}
	gown := []faceShopCD{}
	for _, c := range base {
		if s := store.ItemSlot(c); s == "TO" || s == "PA" || s == "SH" {
			gown = append(gown, faceShopCD{CD: c})
		}
	}
	result["pGown"] = gown
	// Empty: ReplaceAvatarFaceItem then leaves the preview avatar's current faces.
	result["basicFaceList"] = []faceShopItemCode{}
	payload, _ := json.Marshal(struct {
		Result map[string]any `json:"result"`
	}{result})
	return payload
}

func HandleFaceShopData(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpx.ServeNotFound(w)
		return
	}
	id, ok := strings.CutPrefix(r.URL.Path, "/v4/faceshop/v2/shop/")
	if !ok || id == "" || len(id) > 9 || !httpx.IsAllDigits(id) {
		httpx.ServeNotFound(w)
		return
	}
	store.AccountsMu.Lock()
	acc := store.Accounts[httpx.CookieValue(r, "AV_AUTH")]
	gender := ""
	if acc != nil {
		gender = acc.Gender
	}
	store.AccountsMu.Unlock()
	if acc == nil {
		httpx.WriteJSON(w, http.StatusNotFound, httpx.UnknownSessionBody)
		return
	}
	shopID := 0
	for _, d := range id {
		shopID = shopID*10 + int(d-'0')
	}
	httpx.WriteJSON(w, http.StatusOK, string(faceShopBody(gender, shopID)))
}

// Economy: catalog shop.price stays 0 and every purchase that changes a face part
// or the skin tone consumes one Face Shop ticket (voucher), whatever useVoucher says;
// tickets are bought with Gems (economy.go). With 0 tickets the client itself only
// offers the voucher shop (NaFaceShop::CheckVoucherPurchase), so the server check is
// a backstop. The skin tone rides in saveItemList as SKN%03d and is stored as acc.Skin in the
// client's own create form (three digits; live accounts hold "000" and "001", so SKN000 is a tone).
// Cherry's code and data name no skin range, so any SKN%03d is accepted.
//
// Animal skin, ear and tail (ANIMAL accounts): the client sends no SKN pair. A changed slot arrives as a
// string in invenItemList (DoBuyFaceItem's animal branch compares the preview with the worn set at open and pushes
// each changed TL, EA or FA code). saveItemList carries the preview's whole equipped set without the base items
// (CheckValidSendItem), so a return to the default skin CAFA0005O shows only in invenItemList. Returning to the
// default is free (the base item is never bought); any other new code is a purchase like a face.

// skinCode parses a skin pseudo code to the stored acc.Skin form ("SKN002" -> "002").
// Web's isSkinItemCode (save/v2, which ignores skins) takes the same shape.
func skinCode(code string) (string, bool) {
	if len(code) != 6 || !strings.HasPrefix(code, "SKN") || !httpx.IsAllDigits(code[3:]) {
		return "", false
	}
	return code[3:], true
}

// sameSkin compares skins by number: create stores the client's "000" form, older and test accounts hold "2".
func sameSkin(a, b string) bool {
	n, errA := strconv.Atoi(a)
	m, errB := strconv.Atoi(b)
	return a == b || errA == nil && errB == nil && n == m
}

type faceShopPurchaseReq struct {
	SaveItemList  []store.AvatarSaveItem `json:"saveItemList"`
	InvenItemList []json.RawMessage      `json:"invenItemList"`
}

const errAlreadyPurchasedBody = `{"errorCode":"61009","errorMessage":"cherry: item already purchased"}`

type faceShopPurchaseResult struct {
	store.AvatarInfoResult
	Coin int `json:"coin"`
	Cash int `json:"cash"`
}

func HandleFaceShopPurchase(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpx.ServeNotFound(w)
		return
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, httpx.MaxJSONBody+1))
	if err != nil || len(raw) > httpx.MaxJSONBody {
		httpx.WriteJSON(w, http.StatusBadRequest, httpx.BadRequestBody)
		return
	}
	var req faceShopPurchaseReq
	if _, err := httpx.UnmarshalNativeJSON(raw, &req); err != nil || req.SaveItemList == nil {
		httpx.WriteJSON(w, http.StatusBadRequest, httpx.BadRequestBody)
		return
	}
	// invenItemList: integers are accepted and ignored; strings must be animal skin, ear or tail codes.
	var wear []string
	for _, v := range req.InvenItemList {
		var n int64
		if json.Unmarshal(v, &n) == nil {
			continue
		}
		var code string
		if json.Unmarshal(v, &code) != nil || !isAnimalWear(code) {
			httpx.WriteJSON(w, http.StatusBadRequest, httpx.BadRequestBody)
			return
		}
		wear = append(wear, code)
	}
	wearBySlot := make(map[string]string, len(wear)) // slot -> changed code
	for _, code := range wear {
		if prev, dup := wearBySlot[code[2:4]]; dup && prev != code {
			httpx.WriteJSON(w, http.StatusBadRequest, httpx.BadRequestBody)
			return
		}
		wearBySlot[code[2:4]] = code
	}
	// Same shape rules as save/v2; only face-category codes and the skin are applied (the
	// preview avatar wears a try-on gown, so its clothes list is not authoritative).
	faces := make(map[string]string, 5) // category -> new code
	skin := ""                          // new skin from a SKN%03d code, "" when the request has none
	var caSave []string                 // CA skin/ear/tail codes in saveItemList: worn context, animals only
	for _, item := range req.SaveItemList {
		if item.ItemCode == nil || *item.ItemCode == "" {
			httpx.WriteJSON(w, http.StatusBadRequest, httpx.BadRequestBody)
			return
		}
		if len(item.InvenSeq) > 0 {
			var seq int
			if strings.TrimSpace(string(item.InvenSeq)) == "null" || json.Unmarshal(item.InvenSeq, &seq) != nil {
				httpx.WriteJSON(w, http.StatusBadRequest, httpx.BadRequestBody)
				return
			}
		}
		code := *item.ItemCode
		if s, ok := skinCode(code); ok {
			// Two skins in one request are ambiguous.
			if skin != "" && skin != s {
				httpx.WriteJSON(w, http.StatusBadRequest, httpx.BadRequestBody)
				return
			}
			skin = s
			continue
		}
		if isAnimalWear(code) {
			caSave = append(caSave, code)
			continue
		}
		if !store.IsBasicFaceItemCode(code) {
			continue
		}
		if !faceForSale(code) {
			httpx.WriteJSON(w, http.StatusBadRequest, httpx.BadRequestBody)
			return
		}
		if prev, dup := faces[code[2:4]]; dup && prev != code {
			httpx.WriteJSON(w, http.StatusBadRequest, httpx.BadRequestBody)
			return
		}
		faces[code[2:4]] = code
	}

	store.AccountsMu.Lock()
	acc := store.Accounts[httpx.CookieValue(r, "AV_AUTH")]
	if acc == nil {
		store.AccountsMu.Unlock()
		httpx.WriteJSON(w, http.StatusNotFound, httpx.UnknownSessionBody)
		return
	}
	// Species before any spend: CA animal parts only for ANIMAL accounts, CU human faces only for the others.
	for _, code := range slices.Concat(slices.Collect(maps.Values(faces)), wear, caSave) {
		if strings.HasPrefix(code, "CA") != (acc.Gender == "ANIMAL") {
			store.AccountsMu.Unlock()
			httpx.WriteJSON(w, http.StatusBadRequest, httpx.BadRequestBody)
			return
		}
	}
	// An animal skin, ear or tail must be listed, or already owned or worn (as the starter parts are).
	owned := store.AccountInventoryCodes(acc)
	for _, code := range wear {
		if !faceForSale(code) && !slices.Contains(owned, code) && !slices.Contains(acc.ItemCodes, code) {
			store.AccountsMu.Unlock()
			httpx.WriteJSON(w, http.StatusBadRequest, httpx.BadRequestBody)
			return
		}
	}
	previous := *acc
	// The request carries the preview avatar's full face list; the client cannot tell new
	// from owned (DoBuyFaceItem @0x1c08448). A face neither worn nor owned is a purchase
	// (one ticket). A change made only of owned faces and the same skin is refused like the stock server most
	// likely did: errorCode 61009 -> "Item already purchased." (ErrCommonShopPurchase
	// @0x1aa9cdc), nothing equipped or spent; owned faces are switched in Closet > Makeup.
	current := acc.Skin
	if current == "" {
		current = "1" // the default AvatarInfoForAccount reports
	}
	// A new skin is a change of its own: it spends the ticket and lifts the 61009 refusal.
	changed, rebuy := skin != "" && !sameSkin(skin, current), false
	for _, code := range faces {
		worn := slices.Contains(acc.ItemCodes, code)
		changed = changed || !worn && !slices.Contains(owned, code)
		rebuy = rebuy || !worn && slices.Contains(owned, code)
	}
	// An animal part is bought like a face, except the default base item (never bought): returning to it is free.
	for _, code := range wear {
		if code == animalBaseWear(code[2:4]) {
			continue
		}
		worn := slices.Contains(acc.ItemCodes, code)
		changed = changed || !worn && !slices.Contains(owned, code)
		rebuy = rebuy || !worn && slices.Contains(owned, code)
	}
	if rebuy && !changed {
		store.AccountsMu.Unlock()
		httpx.WriteJSON(w, http.StatusBadRequest, errAlreadyPurchasedBody)
		return
	}
	var lines []store.LedgerLine
	if changed {
		var err error
		if lines, err = store.SpendLocked(acc, store.LedgerDeltas{FaceTickets: -ActivePrices().FaceTicketCost}, "faceshop purchase"); err != nil {
			store.AccountsMu.Unlock()
			httpx.WriteJSON(w, http.StatusBadRequest, errNotEnoughBody)
			return
		}
	}
	if skin != "" && !sameSkin(skin, current) {
		acc.Skin = skin // before SaveAccountsLocked, so the rollback below restores it too
	}
	// replace: the code each changed slot wears now ("" = the default base, so the stored item is dropped).
	replace := maps.Clone(faces)
	granted := make([]string, 0, len(faces)+len(wear))
	for _, slot := range faceShopSlots {
		if code, ok := faces[slot]; ok {
			granted = append(granted, code)
		}
	}
	for _, slot := range animalWearSlots {
		code, ok := wearBySlot[slot]
		if !ok {
			continue
		}
		if code == animalBaseWear(slot) {
			replace[slot] = ""
			continue
		}
		replace[slot] = code
		granted = append(granted, code)
	}
	acc.InventoryCodes = store.AppendUniqueItemCodes(store.AccountInventoryCodes(acc), granted)
	equipped := make([]string, 0, len(acc.ItemCodes)+len(granted))
	placed := make(map[string]bool, len(granted))
	for _, code := range acc.ItemCodes {
		slot := store.ItemSlot(code)
		if nc, ok := replace[slot]; ok && (store.IsBasicFaceItemCode(code) || isAnimalWear(code)) {
			if nc != "" && !placed[nc] {
				equipped = append(equipped, nc)
				placed[nc] = true
			}
			continue
		}
		equipped = append(equipped, code)
	}
	for _, nc := range granted {
		if !placed[nc] {
			equipped = append(equipped, nc)
		}
	}
	acc.ItemCodes = equipped
	if err := store.SaveAccountsLocked(); err != nil {
		*acc = previous
		store.AccountsMu.Unlock()
		httpx.WriteJSON(w, http.StatusInternalServerError, `{"errorCode":"500"}`)
		return
	}
	store.FlushLedgerLocked(lines)
	coin, cash := int(acc.Gems), int(acc.Cash)
	info := store.AvatarInfoForAccount(store.AccountSnapshot{
		Aid: acc.Aid, Name: acc.Name, Gender: acc.Gender, Skin: acc.Skin,
		Country: acc.Country, ItemCodes: append([]string(nil), acc.ItemCodes...),
		InventoryCodes: store.AccountOwnedCodes(acc),
	})
	store.AccountsMu.Unlock()
	payload, _ := json.Marshal(struct {
		Result faceShopPurchaseResult `json:"result"`
	}{faceShopPurchaseResult{info, coin, cash}})
	httpx.WriteJSON(w, http.StatusOK, string(payload))
}

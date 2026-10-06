package main

import (
	_ "embed"
	"encoding/json"
	"io"
	"net/http"
	"slices"
	"strings"
)

// Face Shop (goSomewhere(faceShop) -> scene 3/316 NaFaceShop). Static research:
//   GET  /v4/faceshop/v2/shop/<shopId>?shopLocation=HOME|SQUARE
//        ReqFaceShopDataWithShopID @0x1b9a068; parser sDataFaceShop::SetParserJSON @0x2ada478
//   GET  /v4/voucher/own/count/faceshop        ResFaceShopOwnVoucherCount @0x1b2c6a4
//   POST /v4/faceshop/saveAndPurchase/v2       ReqFaceShopSaveNPurchase @0x1be1f0c,
//        ResFaceShopSaveNPurchase @0x1c0b780
// Shop data is one catalog per avatar type; the single price is shop.discountedPrice.

//go:embed data/face_codes.txt
var faceCodesTxt string

// faceCatalog is the 1,173 device-renderable face codes (HE14/EB200/EY507/NO54/MO398).
var (
	faceCatalog    = strings.Fields(faceCodesTxt)
	faceCatalogSet = func() map[string]struct{} {
		m := make(map[string]struct{}, len(faceCatalog))
		for _, c := range faceCatalog {
			m[c] = struct{}{}
		}
		return m
	}()
)

// faceShopCategoryKey maps an item category (code[2:4]) to the shop-data key
// suffix per avatar type prefix (M/F: FACE BROW EYE NOSE MOUTH; native list
// offsets @0x1b01db0: HE=38 EB=40 EY=41 NO=42 MO=43).
var faceShopCategoryKey = map[string]string{"HE": "FACE", "EB": "BROW", "EY": "EYE", "NO": "NOSE", "MO": "MOUTH"}

var faceShopSlots = []string{"HE", "EB", "EY", "NO", "MO"}

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
// array of {cd}; basicFaceList array of {itemCode}. Animal accounts get empty
// category lists because no verified animal face codes exist.
func faceShopBody(gender string, shopID int) []byte {
	avatarType := faceShopAvatarType(gender)
	result := map[string]any{"avatarType": avatarType}
	if avatarType == "A" {
		for _, key := range []string{"AFACE", "AEYE", "AMOUTH", "ANOSE", "ATAIL", "AEAR"} {
			result[key] = faceShopCategory{Items: []faceShopItem{}}
		}
	} else {
		cats := map[string]*faceShopCategory{}
		for slot, key := range faceShopCategoryKey {
			cat := &faceShopCategory{Items: []faceShopItem{}}
			cats[slot] = cat
			result[avatarType+key] = cat
		}
		for _, code := range faceCatalog {
			cat := cats[code[2:4]]
			cat.Items = append(cat.Items, faceShopItem{CD: code})
		}
	}
	// ponytail: no face sets (SET tab empty); set codes/previews unverified.
	result["SET"] = faceShopCategory{Items: []faceShopItem{}}
	result["shop"] = faceShopInfo{ID: shopID, Name: "Face Shop", DisplayCoinUnit: "G"}
	// pGown: the try-on gown. Base clothes are renderable by construction and are
	// stripped from the purchase request by NaFaceShop::CheckValidSendItem.
	base := avatarBaseItems[gender]
	if base == nil {
		base = avatarBaseItems["FEMALE"]
	}
	gown := []faceShopCD{}
	for _, c := range base {
		if s := itemSlot(c); s == "TO" || s == "PA" || s == "SH" {
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

func handleFaceShopData(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		serveNotFound(w)
		return
	}
	id, ok := strings.CutPrefix(r.URL.Path, "/v4/faceshop/v2/shop/")
	if !ok || id == "" || len(id) > 9 || !isAllDigits(id) {
		serveNotFound(w)
		return
	}
	accountsMu.Lock()
	acc := accounts[cookieValue(r, "AV_AUTH")]
	gender := ""
	if acc != nil {
		gender = acc.gender
	}
	accountsMu.Unlock()
	if acc == nil {
		writeJSON(w, http.StatusNotFound, unknownSessionBody)
		return
	}
	shopID := 0
	for _, d := range id {
		shopID = shopID*10 + int(d-'0')
	}
	writeJSON(w, http.StatusOK, string(faceShopBody(gender, shopID)))
}

// Economy: catalog shop.price stays 0 and every purchase that changes a face part
// consumes one Face Shop ticket (voucher), whatever useVoucher says; tickets are
// bought with Gems (economy.go). With 0 tickets the client itself only offers the
// voucher shop (NaFaceShop::CheckVoucherPurchase), so the server check is a backstop.

type faceShopPurchaseReq struct {
	SaveItemList  []avatarSaveItem  `json:"saveItemList"`
	InvenItemList []json.RawMessage `json:"invenItemList"`
}

const errAlreadyPurchasedBody = `{"errorCode":"61009","errorMessage":"cherry: item already purchased"}`

type faceShopPurchaseResult struct {
	avatarInfoResult
	Coin int `json:"coin"`
	Cash int `json:"cash"`
}

func handleFaceShopPurchase(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		serveNotFound(w)
		return
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, maxJSONBody+1))
	if err != nil || len(raw) > maxJSONBody {
		writeJSON(w, http.StatusBadRequest, badRequestBody)
		return
	}
	var req faceShopPurchaseReq
	if _, err := unmarshalNativeJSON(raw, &req); err != nil || req.SaveItemList == nil {
		writeJSON(w, http.StatusBadRequest, badRequestBody)
		return
	}
	for _, v := range req.InvenItemList {
		var n int64
		if json.Unmarshal(v, &n) != nil {
			writeJSON(w, http.StatusBadRequest, badRequestBody)
			return
		}
	}
	// Same shape rules as save/v2; only face-category codes are applied (the
	// preview avatar wears a try-on gown, so its clothes list is not authoritative).
	faces := make(map[string]string, 5) // category -> new code
	for _, item := range req.SaveItemList {
		if item.ItemCode == nil || *item.ItemCode == "" {
			writeJSON(w, http.StatusBadRequest, badRequestBody)
			return
		}
		if len(item.InvenSeq) > 0 {
			var seq int
			if strings.TrimSpace(string(item.InvenSeq)) == "null" || json.Unmarshal(item.InvenSeq, &seq) != nil {
				writeJSON(w, http.StatusBadRequest, badRequestBody)
				return
			}
		}
		code := *item.ItemCode
		if !isBasicFaceItemCode(code) {
			continue
		}
		if _, known := faceCatalogSet[code]; !known {
			writeJSON(w, http.StatusBadRequest, badRequestBody)
			return
		}
		if prev, dup := faces[code[2:4]]; dup && prev != code {
			writeJSON(w, http.StatusBadRequest, badRequestBody)
			return
		}
		faces[code[2:4]] = code
	}

	accountsMu.Lock()
	acc := accounts[cookieValue(r, "AV_AUTH")]
	if acc == nil {
		accountsMu.Unlock()
		writeJSON(w, http.StatusNotFound, unknownSessionBody)
		return
	}
	previous := *acc
	// The request carries the preview avatar's full face list; the client cannot tell new
	// from owned (DoBuyFaceItem @0x1c08448). A face neither worn nor owned is a purchase
	// (one ticket). A change made only of owned faces is refused like the stock server most
	// likely did: errorCode 61009 -> "Item already purchased." (ErrCommonShopPurchase
	// @0x1aa9cdc), nothing equipped or spent; owned faces are switched in Closet > Makeup.
	owned := accountInventoryCodes(acc)
	changed, rebuy := false, false
	for _, code := range faces {
		worn := slices.Contains(acc.itemCodes, code)
		changed = changed || !worn && !slices.Contains(owned, code)
		rebuy = rebuy || !worn && slices.Contains(owned, code)
	}
	if rebuy && !changed {
		accountsMu.Unlock()
		writeJSON(w, http.StatusBadRequest, errAlreadyPurchasedBody)
		return
	}
	var lines []ledgerLine
	if changed {
		var err error
		if lines, err = spendLocked(acc, ledgerDeltas{FaceTickets: -1}, "faceshop purchase"); err != nil {
			accountsMu.Unlock()
			writeJSON(w, http.StatusBadRequest, errNotEnoughBody)
			return
		}
	}
	granted := make([]string, 0, len(faces))
	for _, slot := range faceShopSlots {
		if code, ok := faces[slot]; ok {
			granted = append(granted, code)
		}
	}
	acc.inventoryCodes = appendUniqueItemCodes(accountInventoryCodes(acc), granted)
	equipped := make([]string, 0, len(acc.itemCodes)+len(faces))
	placed := make(map[string]bool, len(faces))
	for _, code := range acc.itemCodes {
		if isBasicFaceItemCode(code) {
			if nc, ok := faces[code[2:4]]; ok {
				if !placed[nc] {
					equipped = append(equipped, nc)
					placed[nc] = true
				}
				continue
			}
		}
		equipped = append(equipped, code)
	}
	for _, nc := range granted {
		if !placed[nc] {
			equipped = append(equipped, nc)
		}
	}
	acc.itemCodes = equipped
	if err := saveAccountsLocked(); err != nil {
		*acc = previous
		accountsMu.Unlock()
		writeJSON(w, http.StatusInternalServerError, `{"errorCode":"500"}`)
		return
	}
	flushLedgerLocked(lines)
	coin, cash := int(acc.gems), int(acc.cash)
	info := avatarInfoForAccount(accountSnapshot{
		aid: acc.aid, name: acc.name, gender: acc.gender, skin: acc.skin,
		country: acc.country, itemCodes: append([]string(nil), acc.itemCodes...),
		inventoryCodes: accountOwnedCodes(acc),
	})
	accountsMu.Unlock()
	payload, _ := json.Marshal(struct {
		Result faceShopPurchaseResult `json:"result"`
	}{faceShopPurchaseResult{info, coin, cash}})
	writeJSON(w, http.StatusOK, string(payload))
}

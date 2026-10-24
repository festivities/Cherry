package lpn

import (
	"log"
	"strconv"
	"time"

	"cherry/internal/economy"
)

// Garden Atelier (craft, Economy Phase DE): craft_info 29 -> hc 34, theme_info 30 -> 35, design_info 31
// -> 36, design_open 32 -> 37, make_material 33 -> 38, make_clothes_info 34 -> 39, make_clothes 35 -> 40,
// theme_complete_reward 37 -> 44, craft shop 40 -> 47 and buy 41 -> 48. Field layouts and client rules:
// spec section 12 (12.1 layouts, 12.2 corrections, 12.5 byte example) in
// %TEMP%\opencode\cherry\phaseDE-20261022\gather-craft-spec.md. Recipes, stamina and prices come from
// economy/atelier.go (data/atelier.json).

// craftNoDate is int64 -1: the client's no-date branch for valid and begin/end times (spec 12.2, 18.7).
const craftNoDate = ^uint64(0)

// craftGrade is ItemCodeExtra.item_grade. It is a string on the wire (spec 12.1).
const craftGrade = "1"

// craftLockedType is CraftOpenCondition.type for a design whose blueprint is not bought. The client
// opens its "use Smiles" popup only for this type, with value as the price (spec 19.2).
const craftLockedType = 1

// gardenCraftMsg reports whether msgid is an Atelier request answered here.
func gardenCraftMsg(msgid uint16) bool {
	switch msgid {
	case 29, 30, 31, 32, 33, 34, 35, 37, 40, 41:
		return true
	}
	return false
}

func pbRepeated(b []byte, field uint64, items [][]byte) []byte {
	for _, it := range items {
		b = pbLen(b, field, it)
	}
	return b
}

// craftPlayer is PlayerCraftInfo: stamina and its timer, and the Smile balance as p_point (the same
// number the Bag shows on hc 30). Cash is 0: the Atelier never uses Gems or Cash.
func craftPlayer(s economy.AtelierSnap) []byte {
	b := pbVar(nil, 1, 1)                 // level
	b = pbVar(b, 2, 0)                    // exp_current
	b = pbVar(b, 3, 10)                   // exp_first
	b = pbVar(b, 4, 100)                  // exp_last
	b = pbVar(b, 5, 0)                    // making_count
	b = pbVar(b, 6, uint64(s.Stamina))    // stamina
	b = pbVar(b, 7, uint64(s.StaminaMax)) // stamina_max
	b = pbVar(b, 8, uint64(s.RemainSec))  // stamina_remain_sec
	b = pbVar(b, 9, uint64(s.ChargeSec))  // stamina_charge_sec
	b = pbVar(b, 10, uint64(s.Smiles))    // p_point: Smiles
	return pbVar(b, 11, 0)                // cash
}

// craftInfoBody is hc 34: the state, every theme and the theme the Atelier opens on.
func craftInfoBody(s economy.AtelierSnap, themes []economy.AtelierTheme, materialTheme int64) []byte {
	b := pbVar(nil, 1, 0)
	b = pbLen(b, 2, craftPlayer(s))
	for _, t := range themes {
		b = pbLen(b, 3, craftTheme(t))
	}
	return pbVar(b, 4, uint64(materialTheme))
}

// craftTheme is CraftThemeInfo. The progress is 0 of 1 (process_max is never 0: the client divides by
// it), the banner and icon are client paths, and there is no date and no reward.
func craftTheme(t economy.AtelierTheme) []byte {
	b := pbVar(nil, 1, uint64(t.ID))
	b = pbLen(b, 2, []byte(t.Name))
	b = pbLen(b, 3, []byte(t.Information))
	b = pbVar(b, 4, 0)  // flag
	b = pbVar(b, 5, 0)  // begin
	b = pbVar(b, 6, 0)  // end
	b = pbVar(b, 7, 1)  // require_level
	b = pbVar(b, 8, 0)  // process_current
	b = pbVar(b, 9, 1)  // process_max
	b = pbVar(b, 11, 0) // complete_reward_state
	b = pbLen(b, 12, []byte(t.Banner))
	b = pbLen(b, 13, []byte(t.Icon))
	b = pbVar(b, 14, 0)           // create_date
	b = pbLen(b, 15, nil)         // complete_reward_img_url (empty)
	b = pbVar(b, 16, craftNoDate) // begin_time
	b = pbVar(b, 17, craftNoDate) // end_time
	b = pbVar(b, 18, 0)           // create_time
	return pbVar(b, 20, 0)        // mission_id
}

// craftItemCodeExtra is ItemCodeExtra for a clothing product: the code, grade "1", no effects, the name.
func craftItemCodeExtra(code, name string) []byte {
	b := pbLen(nil, 1, []byte(code))
	b = pbLen(b, 2, []byte(craftGrade))
	b = pbLen(b, 3, nil)
	b = pbLen(b, 4, []byte(name))
	b = pbVar(b, 5, 0)
	return pbVar(b, 6, 0)
}

// craftProduct is CraftProductClothes: one item per make, rate 100, grade 1.
func craftProduct(d economy.AtelierDesign, s economy.AtelierSnap) []byte {
	b := pbLen(nil, 1, craftItemCodeExtra(d.ProductClothes, d.ProductName))
	b = pbVar(b, 2, 1)                                     // count
	b = pbVar(b, 3, 100)                                   // rate
	b = pbVar(b, 4, squareBool(s.Owned[d.ProductClothes])) // made_history: already in the Closet
	return pbVar(b, 5, 1)                                  // make_grade
}

// craftMakeRows is one CraftMakeMaterial per input: count is what the player holds, require what the
// make needs (the client gates its make button on count < require, spec 12.1 item 5).
func craftMakeRows(d economy.AtelierDesign, s economy.AtelierSnap) [][]byte {
	rows := make([][]byte, 0, len(d.Inputs))
	for _, in := range d.Inputs {
		r := pbVar(nil, 1, uint64(in.Item))
		r = pbVar(r, 3, uint64(s.Counts[in.Item]))
		r = pbVar(r, 4, uint64(in.Count))
		r = pbVar(r, 5, uint64(d.ID))
		r = pbVar(r, 6, 0)
		rows = append(rows, pbVar(r, 7, 0))
	}
	return rows
}

// craftOpenCondition is CraftOpenCondition {type, value}: a locked design (blueprint not bought) is type
// craftLockedType with the Smile price; a free or bought design is type 0.
func craftOpenCondition(d economy.AtelierDesign, s economy.AtelierSnap) []byte {
	if s.Unlocked(d) {
		return pbVar(pbVar(nil, 1, 0), 2, 0)
	}
	return pbVar(pbVar(nil, 1, craftLockedType), 2, uint64(d.Price))
}

// craftMaterialDesign is CraftDesignMaterial: one product per make; enough_material says whether the
// Bag covers one make.
func craftMaterialDesign(d economy.AtelierDesign, s economy.AtelierSnap) []byte {
	b := pbVar(nil, 1, uint64(d.ID))
	b = pbVar(b, 2, 0)
	b = pbVar(b, 3, 0)
	b = pbLen(b, 4, craftOpenCondition(d, s))
	b = pbVar(b, 5, uint64(d.ProductMaterial))
	b = pbVar(b, 6, 1)
	b = pbVar(b, 7, squareBool(s.Enough(d.Inputs, 1)))
	b = pbVar(b, 8, uint64(d.Stamina))
	b = pbVar(b, 9, 0)
	b = pbVar(b, 10, craftNoDate)
	return pbVar(b, 11, craftNoDate)
}

// craftMaterialDetail is CraftDesignMaterialDetail (the make page of a material design).
func craftMaterialDetail(d economy.AtelierDesign, s economy.AtelierSnap) []byte {
	b := pbVar(nil, 1, uint64(d.ID))
	b = pbVar(b, 2, uint64(d.ProductMaterial))
	b = pbVar(b, 3, 1)
	b = pbRepeated(b, 4, craftMakeRows(d, s))
	return pbVar(b, 5, uint64(d.Stamina))
}

// craftClothesDesign is CraftDesignClothes: the one clothing product and whether one make is covered.
func craftClothesDesign(d economy.AtelierDesign, s economy.AtelierSnap) []byte {
	b := pbVar(nil, 1, uint64(d.ID))
	b = pbVar(b, 2, 0)
	b = pbVar(b, 3, 0)
	b = pbLen(b, 4, craftOpenCondition(d, s))
	b = pbLen(b, 5, craftProduct(d, s))
	b = pbVar(b, 6, squareBool(s.Enough(d.Inputs, 1)))
	b = pbVar(b, 7, uint64(d.Stamina))
	b = pbVar(b, 8, craftNoDate)
	return pbVar(b, 9, craftNoDate)
}

// craftClothesDetail is CraftDesignClothesDetail (the make page of a clothes design, rate powder none).
func craftClothesDetail(d economy.AtelierDesign, s economy.AtelierSnap) []byte {
	b := pbVar(nil, 1, uint64(d.ID))
	b = pbLen(b, 2, craftProduct(d, s))
	b = pbRepeated(b, 3, craftMakeRows(d, s))
	return pbVar(b, 5, uint64(d.Stamina))
}

// craftThemeMaterial is CraftThemeDetailMaterial: the theme's material designs.
func craftThemeMaterial(t economy.AtelierTheme, designs []economy.AtelierDesign, s economy.AtelierSnap) []byte {
	b := pbVar(nil, 1, uint64(t.ID))
	b = pbLen(b, 2, []byte(t.Name))
	b = pbLen(b, 3, []byte(t.Information))
	for _, d := range designs {
		b = pbLen(b, 5, craftMaterialDesign(d, s))
	}
	return b
}

// craftThemeClothes is CraftThemeDetailClothes: the theme's clothes designs and one top entry (the
// banner, grade 1).
func craftThemeClothes(t economy.AtelierTheme, designs []economy.AtelierDesign, s economy.AtelierSnap) []byte {
	b := pbVar(nil, 1, uint64(t.ID))
	for _, d := range designs {
		b = pbLen(b, 2, craftClothesDesign(d, s))
	}
	top := pbLen(nil, 1, []byte(t.Banner))
	return pbLen(b, 3, pbVar(top, 2, 1))
}

// craftAssets is one AssetItem per distinct product and input of designs; the client joins rows to
// them by item id.
func craftAssets(designs []economy.AtelierDesign) [][]byte {
	var out [][]byte
	seen := map[string]bool{}
	add := func(key string, asset []byte) {
		if !seen[key] {
			seen[key] = true
			out = append(out, asset)
		}
	}
	for _, d := range designs {
		if d.Material() {
			if g, ok := economy.GatherMaterialByID(d.ProductMaterial); ok {
				add("m"+strconv.FormatInt(g.Item, 10), gardenAssetItem(g))
			}
		} else if a, ok := craftClothesAsset(d); ok {
			add("c"+d.ProductClothes, a)
		}
		for _, in := range d.Inputs {
			if g, ok := economy.GatherMaterialByID(in.Item); ok {
				add("m"+strconv.FormatInt(g.Item, 10), gardenAssetItem(g))
			}
		}
	}
	return out
}

// craftClothesAsset is the AssetItem of a clothing product (its catalog id and picture); ok is false
// when the code is not in the catalog.
func craftClothesAsset(d economy.AtelierDesign) ([]byte, bool) {
	id, img, ok := economy.ClothingItem(d.ProductClothes)
	if !ok {
		return nil, false
	}
	a := pbVar(nil, 1, uint64(id))
	a = pbVar(a, 2, 0)
	a = pbLen(a, 3, []byte(d.ProductName))
	a = pbVar(a, 4, 1)
	a = pbLen(a, 5, []byte(d.ProductClothes))
	a = pbLen(a, 6, []byte(img))
	a = pbLen(a, 7, []byte("Made in the Atelier."))
	return pbVar(pbVar(a, 9, craftNoDate), 10, craftNoDate), true
}

// craftRefused is a reply that carries only a result and the requested id, for a request the
// Atelier does not know (the stock client never sends one).
func craftRefused(msgid uint16, id uint64) []gardenMsg {
	return []gardenMsg{{msgid, pbVar(pbVar(nil, 1, economy.AtelierInvalid), 2, id)}}
}

// gardenCraftReply answers one Atelier request with the frames to send, in order.
func gardenCraftReply(msgid uint16, viewer uint64, data []byte, now time.Time, logger *log.Logger) []gardenMsg {
	aid := strconv.FormatUint(viewer, 10)
	a := economy.ActiveAtelier()
	id, _ := protobufUintField(data, 1)
	switch msgid {
	case 29: // craft_info {} -> hc 34
		return []gardenMsg{{34, craftInfoBody(economy.AtelierStatus(aid, now), a.Themes, a.FirstMaterialTheme())}}
	case 30: // craft_theme_info {theme_id} -> hc 35: one branch, by the theme's kind
		t, ok := a.Theme(int64(id))
		if !ok {
			return craftRefused(35, id)
		}
		designs, s := a.DesignsOf(t.ID), economy.AtelierStatus(aid, now)
		body := pbVar(nil, 1, 0)
		if t.Kind == economy.AtelierKindMaterial {
			body = pbLen(body, 2, craftThemeMaterial(t, designs, s))
		} else {
			body = pbLen(body, 3, craftThemeClothes(t, designs, s))
		}
		return []gardenMsg{{35, pbRepeated(body, 4, craftAssets(designs))}}
	case 31: // craft_design_info {design_id} -> hc 36: one branch, by the product
		d, ok := a.Design(int64(id))
		if !ok {
			return craftRefused(36, id)
		}
		s := economy.AtelierStatus(aid, now)
		body := pbVar(pbVar(nil, 1, 0), 2, id)
		if d.Material() {
			body = pbLen(body, 3, craftMaterialDetail(d, s))
		} else {
			body = pbLen(body, 4, craftClothesDetail(d, s))
		}
		return []gardenMsg{{36, pbRepeated(body, 5, craftAssets([]economy.AtelierDesign{d}))}}
	case 32: // craft_design_open {design_id} -> hc 37: buys the blueprint with Smiles
		code, s := economy.AtelierOpenDesign(aid, int64(id), now)
		logger.Printf("ATELIER blueprint aid=%s design=%d result=%d smiles=%d", aid, id, code, s.Smiles)
		body := pbVar(pbVar(nil, 1, uint64(code)), 2, id)
		d, ok := a.Design(int64(id))
		if !ok {
			return []gardenMsg{{37, body}}
		}
		if d.Material() {
			body = pbLen(body, 3, craftMaterialDesign(d, s))
		} else {
			body = pbLen(body, 4, craftClothesDesign(d, s))
		}
		body = pbVar(body, 5, 1) // design_category
		body = pbLen(body, 6, []byte(a.ProductName(d)+" Blueprint"))
		body = pbVar(body, 7, uint64(s.Smiles)) // p_point: the Smile balance after the purchase (the client shows it as the new count)
		body = pbRepeated(body, 8, craftAssets([]economy.AtelierDesign{d}))
		return []gardenMsg{{37, pbVar(body, 9, uint64(d.Theme))}}
	case 33: // craft_make_product_material {design_id, count, before_design_id*} -> hc 38
		// before_design_id (field 3) is repeated, packed or not; v1 has no design links, so it is ignored.
		count, _ := protobufUintField(data, 2)
		code, s := economy.AtelierMakeMaterial(aid, int64(id), int64(count), now)
		logger.Printf("ATELIER make-material aid=%s design=%d count=%d result=%d stamina=%d", aid, id, count, code, s.Stamina)
		if code == economy.AtelierOK {
			MarkQuest(viewer, "atelier-craft")
		}
		body := pbVar(pbVar(nil, 1, uint64(code)), 2, id)
		d, ok := a.Design(int64(id))
		if !ok || !d.Material() {
			return []gardenMsg{{38, body}}
		}
		if code == economy.AtelierOK {
			body = pbLen(body, 3, pbVar(pbVar(nil, 1, uint64(d.ProductMaterial)), 2, count))
		}
		theme, _ := a.Theme(d.Theme)
		body = pbLen(body, 4, craftPlayer(s))
		body = pbLen(body, 5, craftThemeMaterial(theme, a.DesignsOf(theme.ID), s))
		body = pbRepeated(body, 6, craftAssets([]economy.AtelierDesign{d}))
		return []gardenMsg{{38, pbLen(body, 7, craftMaterialDetail(d, s))}}
	case 34: // craft_make_product_clothes_info {design_id, item_code, rate_powder} -> hc 39
		code, _, _ := pbBytesField(data, 2)
		powder, _ := protobufUintField(data, 3)
		res := economy.AtelierClothesChoice(int64(id), string(code), int64(powder))
		body := pbVar(pbVar(nil, 1, uint64(res)), 2, id)
		d, ok := a.Design(int64(id))
		if !ok || d.Material() {
			return []gardenMsg{{39, body}}
		}
		body = pbLen(body, 3, craftClothesDetail(d, economy.AtelierStatus(aid, now)))
		body = pbRepeated(body, 4, craftAssets([]economy.AtelierDesign{d}))
		body = pbLen(body, 5, []byte(d.ProductClothes))
		itemID, _, _ := economy.ClothingItem(d.ProductClothes)
		return []gardenMsg{{39, pbVar(body, 6, uint64(itemID))}}
	case 35: // craft_make_product_clothes {design_id, retry, item_code, rate_powder} -> hc 40
		retry, _ := protobufUintField(data, 2)
		code, _, _ := pbBytesField(data, 3)
		powder, _ := protobufUintField(data, 4)
		res, s := int64(economy.AtelierRetry), economy.AtelierStatus(aid, now) // retries are not in v1
		if retry == 0 {
			if res = economy.AtelierClothesChoice(int64(id), string(code), int64(powder)); res == economy.AtelierOK {
				res, s = economy.AtelierMakeClothes(aid, int64(id), now)
				if res == economy.AtelierOK {
					MarkQuest(viewer, "atelier-craft")
				}
			}
		}
		logger.Printf("ATELIER make-clothes aid=%s design=%d result=%d stamina=%d", aid, id, res, s.Stamina)
		body := pbVar(pbVar(nil, 1, uint64(res)), 2, id)
		d, ok := a.Design(int64(id))
		if !ok || d.Material() {
			return []gardenMsg{{40, body}}
		}
		theme, _ := a.Theme(d.Theme)
		body = pbLen(body, 3, craftItemCodeExtra(d.ProductClothes, a.ProductName(d)))
		body = pbVar(body, 4, 1) // make_grade
		body = pbLen(body, 5, craftPlayer(s))
		body = pbLen(body, 6, craftThemeClothes(theme, a.DesignsOf(theme.ID), s))
		body = pbLen(body, 7, craftTheme(theme))
		body = pbVar(body, 8, 0)  // retry_price
		body = pbVar(body, 9, 0)  // retry_current_count
		body = pbVar(body, 10, 0) // retry_max_count
		body = pbVar(body, 12, 0) // retry_type
		return []gardenMsg{{40, pbLen(body, 13, craftClothesDetail(d, s))}}
	case 37: // theme_complete_reward {theme_id, mission_id} -> hc 44: theme rewards are not in v1
		body := pbVar(nil, 1, economy.AtelierRewardNone)
		if t, ok := a.Theme(int64(id)); ok {
			body = pbLen(body, 4, craftTheme(t))
		}
		return []gardenMsg{{44, body}}
	case 40: // craft shop info {} -> hc 47: an empty shop
		return []gardenMsg{{47, pbVar(nil, 1, 0)}}
	case 41: // craft shop buy -> hc 48: no shop data (the Atelier shop is not in v1)
		return []gardenMsg{{48, pbVar(nil, 1, economy.AtelierShopNone)}}
	}
	return nil
}

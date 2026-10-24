package lpn

import (
	"math/rand/v2"
	"strconv"

	"cherry/internal/economy"
)

// Square fishing (Economy Phase D slice 2): one fishing spot in the global Square room, and the
// cast flow begin 27 -> 30, start 29 -> 32, finish 30 -> 33, end 28 -> 31. Replies go to the
// fisher only. Field numbers and the spot's graphic/tile rules come from
// %TEMP%\opencode\cherry\phaseD-20261011\gathering.txt (sections 4, 7.2, 9 and Resolution).
const (
	// NpcAppearance.NpcIndex of a fishing spot = 0x3010007 (50397191). NaWorldInteriorManager::Create
	// switches on NpcIndex - 0x3010000 (50397184), so 0x3000007 fell through and no spot was built.
	// NaWorldFishing registers the spot (msg 12390 -> AddFishingIterior) only for 0x3010007 too.
	squareSpotNpcIndex = 0x3010007
	squareSpotSerial   = 1
	// The spot is hosted by the chair RUCH002F0 (interior item 120403132) at tile (106,92). Linking
	// (NaFishingSpotInterior::LinkInteriorObject) calls SetInteractionTypeManual(14,1,true) on that
	// object, so taps and the 10082 gate see type 14. Kind = (InteriorID % 1e7) / 1e5 = 4, which the
	// gate also needs (CInObject::GetType). Game px for tile (106,92), N=160:
	// X=40*(wx-wy+N), Y=20*(2N-1-wx-wy). The avatar spawns at (93,80) = (6920,2920).
	squareSpotInteriorID = 120403132
	squareSpotPX         = 6960
	squareSpotPY         = 2420

	squareFishOK     = 393220 // success result for every fishing reply
	squareFishMiss   = 0      // any result other than squareFishOK takes the client's fail branch
	squareRewardFish = 1      // rc_finish RewardType: the fish card (NaWorldFishingResultPopup::ShowRewardFish)
	squareHitTimeMs  = 3000   // rc_start HitTime, milliseconds [I]
	squareDifficult  = 2000   // rc_start Difficult, milliseconds [I]

	// rc_player_status_all (outbound 10) carries the busy state. NaWorldManager::SetAvatarBusyState
	// sends msg 12422 for state 20 (NaFishingLayer::RegisterFishingPoint(aid): the fisher's bobber,
	// stored at NaFishingLayer+672) and msg 12423 for state 0 (remove). NaWorldFishing::Casting ends
	// the cast when that bobber is missing, which is the 2 ms cr_end after a start (device, 2026-10-11).
	squareRcPlayerStatus = 10
	squareStatusFishing  = 20
	squareStatusIdle     = 0
)

// squareBusyStatus is rc_player_status_all {Aid 1, Status 2}; it goes to the whole room, the fisher included.
func squareBusyStatus(aid, status uint64) []byte {
	return pbVar(pbVar(nil, 1, aid), 2, status)
}

// squareFish is the catch the server chose at the last start. It is paid once, by the next finish
// with Result true, and any begin or end clears it.
type squareFish struct{ def economy.FishDef }

const (
	// The fish shop (NaWorldInteriorManager::Create case 1 -> NaFishShopInterior). It links to the
	// host object whose item id is InteriorID (FindInteiorByItemIndex, first match): RUGO0049C
	// (122005520) is the only copy of that item on the Square map, at tile (115,94). Linking sets
	// that object's interaction type to 39, and a tap there sends msg 39 -> cr_open_shop_req (87).
	squareShopNpcIndex   = 0x3010001
	squareShopSerial     = 2
	squareShopInteriorID = 122005520
	squareShopPX         = 7240 // X=40*(115-94+160)
	squareShopPY         = 2200 // Y=20*(319-115-94)
)

// squareInteriorNpc is one _SQInteriorNpc (rc_room_enter_res NpcList entry) for a map host object.
func squareInteriorNpc(serial, npcIndex, interiorID, px, py uint64) []byte {
	npcAppearance := pbVar(nil, 1, npcIndex)             // _SQNpcAppearance.NpcIndex
	interior := pbLen(nil, 1, npcAppearance)             // _SQInteriorAppearance.Apperance
	interior = pbVar(interior, 3, 1)                     // SizeX [I]
	interior = pbVar(interior, 4, 1)                     // SizeY [I]
	interior = pbVar(interior, 5, interiorID)            // InteriorID: the host map object
	npc := pbVar(nil, 1, serial)                         // _SQInteriorNpc.Serial
	npc = pbLen(npc, 2, interior)                        // Appearance
	npc = pbLen(npc, 3, pbVar(pbVar(nil, 1, px), 2, py)) // PositionInfo (_SQMovePoint Px/Py)
	npc = pbVar(npc, 10, px)                             // Px
	npc = pbVar(npc, 11, py)                             // Py
	npc = pbVar(npc, 42, 1)                              // IsShow
	npc = pbVar(npc, 43, 1)                              // Attackable [I]
	npc = pbVar(npc, 44, 1)                              // IsVisible
	return npc
}

// squareFishingSpotList is rc_room_enter_res.NpcList (field 2): NPCEnumList{NpcList: [NPCEnum{
// InteriorNpc: _SQInteriorNpc}, ...]}, the fishing spot and the fish shop. Every IsInitialized on
// this path returns true (no required fields).
func squareFishingSpotList() []byte {
	list := pbLen(nil, 1, pbLen(nil, 2, squareInteriorNpc(squareSpotSerial, squareSpotNpcIndex, squareSpotInteriorID, squareSpotPX, squareSpotPY)))
	return append(list, pbLen(nil, 1, pbLen(nil, 2, squareInteriorNpc(squareShopSerial, squareShopNpcIndex, squareShopInteriorID, squareShopPX, squareShopPY)))...)
}

// squareLang is a client Lang message (en = 1, ja = 3); the client picks its language. Cherry's
// item names are English, so ja repeats them.
func squareLang(s string) []byte {
	return pbLen(pbLen(nil, 1, []byte(s)), 3, []byte(s))
}

// squareShopRow is one ShopItem. PriceType 1 is Gems. Count is 1 (the client's row loop stops at
// Count -1). Dates stay unset (0), so IsSaleItem is false. BillingCode and BillingText are never set.
func squareShopRow(it economy.FishingItem, displayIdx uint64) []byte {
	url := []byte(economy.FishingItemImageURL(it.ID))
	row := pbVar(nil, 1, uint64(it.ID))                  // ShopItemId
	row = pbVar(row, 2, displayIdx)                      // DisplayIdx
	row = pbVar(row, 3, uint64(it.ID))                   // ItemId
	row = pbLen(row, 4, squareLang(it.Name))             // Name
	row = pbVar(row, 5, 1)                               // PriceType: 1 = Gems
	row = pbVar(row, 6, uint64(it.Price))                // Price
	row = pbVar(row, 7, 1)                               // Count
	row = pbLen(row, 12, url)                            // ImageUrl
	row = pbLen(row, 14, squareLang(squareItemDesc(it))) // Description
	row = pbLen(row, 15, url)                            // DpImgUrl
	return pbVar(row, 24, uint64(it.Kind))               // ItemType: 1 bait, 2 rod
}

// squareInvenItemPlain is an InvenItem with only the fields the shop reads for owned counts: ItemId
// (1), Count (3), ItemType (7: 1 bait, 2 rod) and MaxCount (9). MaxCount = Count marks the item as
// owned, so FishingShopInfo::SetData sets the "cannot buy more" flag. No strings: names, pictures
// and the period are not sent to the bag cell (the bag crashed on them, see gathering.txt).
func squareInvenItemPlain(it economy.FishingItem, count uint64) []byte {
	b := pbVar(nil, 1, uint64(it.ID))
	b = pbVar(b, 3, count)
	b = pbVar(b, 7, uint64(it.Kind))
	return pbVar(b, 9, count)
}

// squareBagShowsOwned controls the bag rows in rc_open_inven_res (116). false: the bag is empty,
// the fallback that never crashed on device. true (experiment, 2026-10-11): owned rows as
// InvenItemClient with EMPTY ImageUrl and PlayingImageUrl, so the bag cell takes its failed-indicator
// path and starts no picture download. Flip to false to revert.
const squareBagShowsOwned = false

// squareAsset is an AssetItemClient, the Asset (field 1) of a bag row. Name and Description are plain
// strings. ImageUrl (5) and PlayingImageUrl (6) are sent as explicit empty strings: NaCommonDownloadSprite
// then takes its failed-indicator path (InitWithPath 0x20d0f58, length check) and no download is queued.
// Every field is sent, because an unset field keeps the default instance's placeholder text. Zero dates
// hide the period banner (NaFishingBagPopup::OnTouchCell shows it only when item+48 != 0).
func squareAsset(it economy.FishingItem) []byte {
	a := pbVar(nil, 1, uint64(it.ID))           // ItemId
	a = pbLen(a, 2, []byte(it.Name))            // Name
	a = pbLen(a, 4, []byte(squareItemDesc(it))) // Description
	a = pbLen(a, 5, nil)                        // ImageUrl: empty
	a = pbLen(a, 6, nil)                        // PlayingImageUrl: empty
	a = pbVar(a, 7, 1)                          // MaxCount
	a = pbVar(a, 8, uint64(it.Kind))            // ItemType: 1 bait, 2 rod
	a = pbVar(a, 9, 0)                          // ValidStartDateMs: no period
	return pbVar(a, 10, 0)                      // ValidEndDateMs: no period
}

// squareInvenItemClient is an InvenItemClient bag row: Asset (1, message) and Count (2, int32).
func squareInvenItemClient(it economy.FishingItem, count uint64) []byte {
	return pbVar(pbLen(nil, 1, squareAsset(it)), 2, count)
}

// squareOwned returns every shop item the account owns, rods and bait.
func squareOwned(aid string) []economy.FishingItem {
	var out []economy.FishingItem
	for _, kind := range []int64{economy.FishKindRod, economy.FishKindBait} {
		owned, _ := economy.FishingBag(aid, kind)
		out = append(out, owned...)
	}
	return out
}

func squareItemDesc(it economy.FishingItem) string {
	return "Adds +" + strconv.FormatInt(it.RareBonus, 10) + "% rare catch chance while in use."
}

func squareBool(b bool) uint64 {
	if b {
		return 1
	}
	return 0
}

// handleFishing answers the four fishing requests for a session already in the room.
func (s *squareSession) handleFishing(frame []byte, msgid uint16, data []byte) error {
	aid := s.aid
	aidText := strconv.FormatUint(aid, 10)
	switch msgid {
	case 27: // cr_begin_fishing_req -> rc_begin_fishing_all (30)
		serial, _ := protobufUintField(data, 1)
		s.fish = nil
		body := pbVar(nil, 1, aid)
		body = pbVar(body, 2, squareFishOK)
		body = pbVar(body, 3, serial) // echo the spot serial
		body = pbVar(body, 4, 0)      // RetryGem: no retry cost
		s.logger.Printf("SQUARE fishing begin aid=%d fish=- gems=0 result=%d", aid, squareFishOK)
		return s.reply(frame, 30, body)
	case 29: // cr_start_fishing_req -> rc_start_fishing_all (32)
		def := economy.ChooseFishWithBonus(rand.IntN, economy.FishingGearBonus(aidText))
		s.fish = &squareFish{def: def}
		body := pbVar(nil, 1, aid)
		body = pbVar(body, 2, squareFishOK)
		body = pbVar(body, 3, squareHitTimeMs)
		body = pbVar(body, 4, squareBool(def.Rare))
		body = pbVar(body, 5, squareDifficult)
		body = pbVar(body, 6, 0) // RareGrade: 1000 would force a rare bite, none here
		s.logger.Printf("SQUARE fishing start aid=%d fish=%d gems=%d result=%d", aid, def.ID, def.Gems, squareFishOK)
		s.room.broadcast(s.logger, nil, squareRcPlayerStatus, squareBusyStatus(aid, squareStatusFishing))
		s.fishing = true
		return s.reply(frame, 32, body)
	case 30: // cr_finish_fishing_req (bool Result = hooked) -> rc_finish_fishing_all (33)
		hooked, _ := protobufUintField(data, 1)
		pending := s.fish
		s.fish = nil
		if hooked == 0 || pending == nil {
			body := pbVar(nil, 1, aid)
			body = pbVar(body, 2, squareFishMiss)
			s.logger.Printf("SQUARE fishing finish aid=%d fish=- gems=0 result=%d", aid, squareFishMiss)
			return s.reply(frame, 33, body)
		}
		def := pending.def
		gems, ok := economy.FishCatch(aidText, def, economy.NowFunc())
		result := uint64(squareFishOK)
		if !ok {
			result = squareFishMiss // the save failed: no credit, and the client shows its fail branch
		} else {
			MarkQuest(aid, "square-fish") // any hooked catch counts, even one past the Gem cap
		}
		s.logger.Printf("SQUARE fishing finish aid=%d fish=%d gems=%d result=%d", aid, def.ID, gems, result)
		body := pbVar(nil, 1, aid)
		body = pbVar(body, 2, result)
		body = pbVar(body, 3, squareRewardFish)
		body = pbVar(body, 4, uint64(gems))
		body = pbVar(body, 6, uint64(def.ID))
		body = pbLen(body, 7, []byte(economy.FishCardName(def, gems)))
		body = pbVar(body, 8, uint64(def.Level))
		body = pbLen(body, 9, []byte(economy.FishImageURL(def.ID)))
		body = pbVar(body, 10, squareBool(def.Rare))
		body = pbVar(body, 15, 1) // Count
		body = pbLen(body, 16, []byte(economy.FishImageURL(def.ID)))
		return s.reply(frame, 33, body)
	case 28: // cr_end_fishing_req -> rc_end_fishing_all (31)
		s.fish, s.fishing = nil, false
		body := pbVar(nil, 1, aid)
		body = pbVar(body, 2, squareFishOK)
		s.logger.Printf("SQUARE fishing end aid=%d fish=- gems=0 result=%d", aid, squareFishOK)
		s.room.broadcast(s.logger, nil, squareRcPlayerStatus, squareBusyStatus(aid, squareStatusIdle))
		return s.reply(frame, 31, body)
	case 25: // cr_fish_collection_req {PageNo} -> rc_fish_collection_res (28): the fishing book
		page, _ := protobufUintField(data, 1)
		page = min(max(page, 1), squareFishPages)
		body, caught := squareFishCollection(aidText, page)
		s.logger.Printf("SQUARE fishing book aid=%d page=%d caught=%d", aid, page, caught)
		return s.reply(frame, 28, body)
	case 26: // cr_complete_collection_req -> rc_complete_collection_res (29): no page rewards yet
		s.logger.Printf("SQUARE fishing book reward refused aid=%d", aid)
		return s.reply(frame, 29, pbVar(nil, 1, 0))
	case 45: // cr_start_fishing_boss_try_req -> rc_start_fishing_boss_try_res (57): boss rooms are not served
		s.logger.Printf("SQUARE fishing boss refused aid=%d", aid)
		return s.reply(frame, 57, pbVar(pbVar(nil, 1, 0), 2, 0))
	case 56: // cr_fish_boss_collection_req {PageNo} -> rc_fish_boss_collection_res (75): empty boss book (no boss catches)
		s.logger.Printf("SQUARE fishing boss book aid=%d empty", aid)
		return s.reply(frame, 75, nil)
	case 59: // cr_inven_item_list_req -> rc_inven_item_list_res (78): RodList stays empty (rods live in the bag)
		return s.reply(frame, 78, nil)
	case 87: // cr_open_shop_req -> rc_open_shop_res (115): Gem-priced rods and bait, real Gem and Cash balances
		gems, cash, _ := economy.Balances(aidText)
		body := pbVar(pbVar(nil, 1, squareFishOK), 2, 1) // ShopId
		for i, it := range economy.FishingShopItems() {
			body = pbLen(body, 3, squareShopRow(it, uint64(i+1)))
		}
		// InvenItems (4) = what the account owns: the shop popup shows Count as "owned" (dword 17
		// of the InvenItem, copied to ShopItem+36 by FishingShopInfo::SetData 0x1e365d0).
		owned := squareOwned(aidText)
		for _, it := range owned {
			body = pbLen(body, 4, squareInvenItemPlain(it, 1))
		}
		body = pbVar(body, 5, uint64(gems))
		body = pbVar(body, 6, uint64(cash))
		s.logger.Printf("SQUARE fishing shop aid=%d items=%d owned=%d gems=%d cash=%d", aid, len(economy.FishingShopItems()), len(owned), gems, cash)
		return s.reply(frame, 115, body)
	case 88: // cr_open_inven_req {BagType: 1 bait, 2 rod} -> rc_open_inven_res (116): owned rows if the switch is on
		bagType, _ := protobufUintField(data, 1)
		owned, inUse := economy.FishingBag(aidText, int64(bagType))
		body := pbVar(pbVar(nil, 1, squareFishOK), 3, bagType)
		shown := 0
		if squareBagShowsOwned {
			for _, it := range owned {
				body = pbLen(body, 2, squareInvenItemClient(it, 1)) // InvenItems: InvenItemClient rows
				shown++
			}
		}
		s.logger.Printf("SQUARE fishing bag aid=%d type=%d owned=%d inUse=%d shown=%d", aid, bagType, len(owned), inUse, shown)
		return s.reply(frame, 116, body)
	case 89: // cr_buy_item_req {ShopItemId, Count} -> rc_buy_item_res (117): Gems only
		id, _ := protobufUintField(data, 1)
		status, gems := economy.BuyFishingItem(aidText, int64(id))
		s.logger.Printf("SQUARE fishing buy aid=%d item=%d status=%d gems=%d", aid, id, status, gems)
		switch status {
		case economy.FishBuyOK:
			body := pbVar(nil, 2, id) // ItemId
			body = pbVar(body, 3, 1)  // Count bought
			body = pbVar(body, 4, 1)  // TotalCount owned
			return s.reply(frame, 117, body)
		case economy.FishBuyOwned:
			return s.reply(frame, 117, pbVar(nil, 1, 393237)) // "Can't buy more than your retention limit."
		case economy.FishBuyGems:
			return s.reply(frame, 117, pbVar(nil, 1, 393236)) // shows "This item is currently unavailable."
		case economy.FishBuyUnknown:
			return s.reply(frame, 117, pbVar(nil, 1, 655376)) // "This item is currently unavailable."
		default:
			return s.reply(frame, 117, pbVar(nil, 1, 10130)) // save failed: "Network Error."
		}
	case 90: // cr_equip_item_req -> rc_equip_item_res (118): refused, the bag is not shown (see 88)
		id, _ := protobufUintField(data, 1)
		s.logger.Printf("SQUARE fishing equip refused aid=%d item=%d (bag display unsupported)", aid, id)
		return s.reply(frame, 118, pbVar(nil, 1, 655376)) // "This item is currently unavailable."
	case 91: // cr_get_equip_item_req -> rc_get_equip_item_res (119): nothing listed (no chat-bar rows)
		s.logger.Printf("SQUARE fishing gear aid=%d listed=0", aid)
		return s.reply(frame, 119, nil)
	}
	return nil
}

// squareFishPages is the page count of the fishing book (MaxPage). Every Cherry fish fits on page 1.
const squareFishPages = 1

// squareFishCollection builds rc_fish_collection_res (28) for the book. Entries follow
// economy.FishCatalog, ItemIndex is the 1-based position (the client sorts by it). A fish the
// account has not caught sends only its id and count 0, which the client draws as a locked slot
// (name, level and picture are withheld). RewardCoin stays 0 and PossibleComplete/CompleteAlready
// are false. The preview picture is the rare fish's, because the client downloads any URL it does
// not have cached and an empty one would start a request for "". CurrentVersion is 1 + total
// catches, so the client's cache is refreshed after every catch.
func squareFishCollection(aidText string, page uint64) ([]byte, int) {
	counts, total := economy.FishCollection(aidText)
	var coll []byte
	caught := 0
	for i, f := range economy.FishCatalog() {
		n := counts[f.ID]
		entry := pbVar(nil, 1, uint64(i+1))
		entry = pbVar(entry, 2, uint64(f.ID))
		entry = pbVar(entry, 3, uint64(n))
		if n > 0 {
			caught++
			entry = pbLen(entry, 4, []byte(f.Name))
			entry = pbLen(entry, 5, []byte(economy.FishImageURL(f.ID)))
			entry = pbVar(entry, 6, uint64(f.Level))
			entry = pbLen(entry, 7, []byte(economy.FishImageURL(f.ID)))
		}
		coll = pbLen(coll, 1, entry)
	}
	out := pbVar(nil, 1, squareFishOK)
	out = pbVar(out, 2, squareFishPages)
	out = pbVar(out, 3, page)
	out = pbVar(out, 4, uint64(1+total))
	out = pbVar(out, 6, 0)
	out = pbLen(out, 8, coll)
	out = pbVar(out, 9, 0)
	out = pbVar(out, 10, 0)
	return pbLen(out, 12, []byte(economy.FishImageURL(4201))), caught
}

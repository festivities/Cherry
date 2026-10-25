package lpn

import (
	"strconv"
	"time"

	"cherry/internal/economy"
)

// Square Ride Point exchange shop (popup 117; wire notes %TEMP%\opencode\cherry\phaseDE-20261022\
// racing-exchange-spec.md, sections 2 to 7). The shop NPC is a second _SQInteriorNpc with NpcIndex
// 0x3010035, serial 5, AniEventType 2 (the pet-exchange bubble that opens popup 117). It links to the
// root floor RUGO002IM (122003262) at tile (84,85); the host rules are in square_racing.go. The popup
// sends cr61 {ShopType1} (rc80 {ExchangeShop1[], Balance2[]}) and cr62 {ShopId1, ItemId2} (rc81). Only
// Ride Points (coin 5) are spent. A refusal is rc81 Result 1, which the client shows as "Network Error.".
const (
	squareRideShopNpcIndex    = 0x3010035
	squareRideShopNpcSerial   = 5
	squareRideShopInteriorID  = 122003262
	squareRideShopPX          = 6360 // X=40*(84-85+160)
	squareRideShopPY          = 3000 // Y=20*(2*160-1-84-85)
	squareRideShopNpcEvent    = 2    // AniEventType (field 76): the pet-exchange bubble, popup 117
	squareRideShopID          = 1    // ShopId: the only shop
	squareRideShopType        = 1    // ShopType
	squareRideShopCoinType    = 5    // CoinType: Ride Points (the popup reads only this balance)
	squareRideShopRefused     = 1    // rc81 Result for any refusal
	squareRideShopBought      = 0    // rc81 Result for a bought row
	squareRideShopOwnedStatus = 2    // ExchangeShopItemData Status: the buy button is disabled
)

// squareRideShopNpcEntry is one rc_room_enter_res NpcList entry: the Ride Point exchange shop.
func squareRideShopNpcEntry() []byte {
	npc := squareInteriorNpc(squareRideShopNpcSerial, squareRideShopNpcIndex, squareRideShopInteriorID, squareRideShopPX, squareRideShopPY)
	npc = pbVar(npc, 63, 1)                      // ViewIconStatus, as the Boss Coin office [I]
	npc = pbVar(npc, 75, 1)                      // AniButtonVisible
	npc = pbVar(npc, 76, squareRideShopNpcEvent) // AniEventType 2: popup 117
	return pbLen(nil, 1, pbLen(nil, 2, npc))
}

// squareRideShopRow is one ExchangeShopItemData. ItemId is the row id the client sends back in cr62.
// RemainCount 0 hides the stock node (no stock in v1). Status 2 disables the button of an owned item.
func squareRideShopRow(r economy.RideShopEntry, order int) []byte {
	row := pbVar(nil, 1, uint64(r.ID))                                     // ItemId
	row = pbVar(row, 2, uint64(economy.RideShopItemType(r.ItemCode)))      // ItemType: 1 clothing, 2 room
	row = pbLen(row, 3, []byte(r.ItemCode))                                // ItemCode
	row = pbVar(row, 4, 1)                                                 // ItemCount
	row = pbVar(row, 5, uint64(order))                                     // ItemOrder
	row = pbVar(row, 7, uint64(r.Price))                                   // Price, Ride Points
	row = pbLen(row, 13, []byte(economy.ItemName(r.ItemCode)))             // ItemName
	row = pbLen(row, 14, []byte(economy.ExchangeItemImageURL(r.ItemCode))) // ImgUrl: relative, the client prefixes its static base
	row = pbVar(row, 16, 0)                                                // RemainCount
	status := uint64(0)
	if r.Owned {
		status = squareRideShopOwnedStatus
	}
	return pbVar(row, 17, status) // Status
}

// squareRideShopList is rc_exchange_shop_list_res (80): the one shop with the rows on sale, and the
// Ride Points as its only balance entry.
func squareRideShopList(aid string, now time.Time) []byte {
	rows, points := economy.RideShopFor(aid, now)
	begin, end := economy.RideShopWindowAt(now)
	shop := pbVar(nil, 1, squareRideShopID)
	shop = pbVar(shop, 2, squareRideShopType)
	shop = pbVar(shop, 3, uint64(begin.UnixMilli())) // BeginDate, epoch ms (the header prints it)
	shop = pbVar(shop, 4, uint64(end.UnixMilli()))   // EndDate
	shop = pbVar(shop, 5, squareRideShopCoinType)
	for i, r := range rows {
		shop = pbLen(shop, 7, squareRideShopRow(r, i+1))
	}
	body := pbLen(nil, 1, shop)
	return pbLen(body, 2, pbVar(pbVar(nil, 1, squareRideShopCoinType), 2, uint64(points)))
}

// handleRideShop answers the Ride Point exchange shop requests of a session in the room.
func (s *squareSession) handleRideShop(frame []byte, msgid uint16, data []byte) error {
	aid := s.aid
	aidText := strconv.FormatUint(aid, 10)
	now := economy.NowFunc()
	switch msgid {
	case 61: // cr_exchange_shop_list_req {ShopType1} -> rc_exchange_shop_list_res (80)
		s.logger.Printf("SQUARE racing exchange list aid=%d", aid)
		return s.reply(frame, 80, squareRideShopList(aidText, now))
	case 62: // cr_exchange_shop_purchase_req {ShopId1, ItemId2} -> rc_exchange_shop_purchase_res (81)
		shop, _ := protobufUintField(data, 1)
		item, _ := protobufUintField(data, 2)
		row, points, ok := economy.RideShopBuy(aidText, int64(item), now)
		if !ok {
			s.logger.Printf("SQUARE racing exchange refused aid=%d row=%d points=%d", aid, item, points)
			body := pbVar(nil, 1, squareRideShopRefused)
			body = pbVar(body, 2, shop) // echo
			body = pbVar(body, 3, item) // echo
			body = pbVar(body, 4, squareRideShopCoinType)
			body = pbVar(body, 5, uint64(row.Price))
			body = pbVar(body, 6, uint64(points))
			return s.reply(frame, 81, body)
		}
		s.logger.Printf("SQUARE racing exchange buy aid=%d row=%d item=%s price=%d points=%d", aid, row.ID, row.ItemCode, row.Price, points)
		body := pbVar(nil, 1, squareRideShopBought)
		body = pbVar(body, 2, squareRideShopID)
		body = pbVar(body, 3, uint64(row.ID))
		body = pbVar(body, 4, squareRideShopCoinType)
		body = pbVar(body, 5, uint64(row.Price))
		body = pbVar(body, 6, uint64(points)) // Balance: Ride Points left
		reward := pbVar(nil, 1, 1)            // ViewType 1: the fashion reveal (room items too, as the office) [I]
		reward = pbLen(reward, 3, []byte(row.ItemCode))
		reward = pbVar(reward, 4, 1)
		reward = pbLen(reward, 7, []byte(economy.ItemName(row.ItemCode)))
		reward = pbLen(reward, 10, []byte(economy.ExchangeItemImageURL(row.ItemCode)))
		return s.reply(frame, 81, pbLen(body, 7, reward)) // Reward7
	}
	return nil
}

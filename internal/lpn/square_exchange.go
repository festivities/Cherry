package lpn

import (
	"strconv"
	"time"

	"cherry/internal/economy"
)

// Square Boss Coin exchange office (economy/exchange.go; wire notes %TEMP%\opencode\cherry\
// phaseDE-20261022\exchange-spec.md, sections 0 to 5 and 7). The office NPC is _SQInteriorNpc NpcIndex
// 0x3010034 (NaWorldInteriorManager::Create case 52). It links to the unique root floor object RUGO002IG
// (interior item 122003256) at tile (89,90), near the Square spawn: the client keeps root floors (kind
// 0x14, like the fish shop's host) in its object cache, while a tile deco such as RUTD001LS was dropped
// with its parent and the bubble never showed (device 2026-10-25, exchange-spec section 9). Tapping it pushes popup 116, which sends cr52 GroupId 1 (rc68 list). A row is bought with
// cr53 GachaId (rc69, result 0 = done). Every other result shows the client's "Network Error." box.
const (
	squareExchangeNpcIndex   = 0x3010034
	squareExchangeSerial     = 3
	squareExchangeInteriorID = 122003256
	squareExchangePX         = 6360 // X=40*(89-90+160)
	squareExchangePY         = 2800 // Y=20*(2*160-1-89-90)
	squareExchangeCoinType   = 3    // Boss Coins: the client's "Not enough Boss Coins." pre-check is for coin 3 (spec 5)
	squareExchangeCashType   = 2    // Cash: the header's second balance (SetTopCurrency 0x1db7474, spec 10.4) [I]
	// squareExchangeChest is GachaData.gacha_type 3, the bronze chest: its confirm reads "Open the bronze
	// chest with %s Boss Coin?" (string 1859). Type 1 (gold) asks for cash (1857), 2 (silver) shows a
	// silver title over the coin-3 copper box (spec 10.1).
	squareExchangeChest = 3
	// squareExchangeFail is the rc69 result for a refused buy. The client shows box 10130 for any result
	// other than 0 except 1048577..1048583, which it shows as billing text (never sent).
	squareExchangeFail = 10130
)

// squareExchangeNpcEntry is one rc_room_enter_res NpcList entry: the exchange office.
func squareExchangeNpcEntry() []byte {
	npc := squareInteriorNpc(squareExchangeSerial, squareExchangeNpcIndex, squareExchangeInteriorID, squareExchangePX, squareExchangePY)
	// ViewIconStatus (63) non-zero: the office draws its bubble only then (ShowAnimationButton 0x186194c;
	// the fish shop has no such check). 1 is the value used; only zero vs non-zero is known.
	npc = pbVar(npc, 63, 1)
	return pbLen(nil, 1, pbLen(nil, 2, npc))
}

// squareExchangeRow is a GachaData row (rc68 gacha_group_list[].gacha_list): the row id is the gacha id
// the client sends back, the price is Boss Coins, and one GachaItemData gives the item. The item picture
// is the absolute URL the popup loads (no base URL is added on the client); discount dates stay unset.
func squareExchangeRow(r economy.ExchangeRow) []byte {
	item := pbVar(nil, 1, uint64(economy.ExchangeItemID(r.ItemCode))) // item_id: the numeric id
	item = pbLen(item, 3, []byte(r.ItemCode))                         // item_code: the client's icon (SetItemDp)
	item = pbVar(item, 4, 1)                                          // item_count
	item = pbLen(item, 9, []byte(economy.ItemName(r.ItemCode)))       // item_name
	item = pbLen(item, 10, []byte(economy.ExchangeItemImageURL(r.ItemCode)))
	row := pbVar(nil, 1, uint64(r.ID))          // gacha_id
	row = pbVar(row, 5, squareExchangeChest)    // gacha_type: the chest (tab name, title art, confirm text)
	row = pbVar(row, 6, squareExchangeCoinType) // coin_type
	row = pbVar(row, 7, uint64(r.Price))        // price
	return pbLen(row, 12, item)                 // item_list
}

// squareExchangeList is rc_gacha_list_res (68): one group (the office's title and the rows on sale now)
// and the account's Boss Coins and Cash as the header's cost entries (without a Cash entry the header
// keeps its layout default "C 0"). The group is never empty (standing rows are required by the file).
// It serves the same group whatever GroupId the client asked for.
func squareExchangeList(aid string, now time.Time) ([]byte, int) {
	e := economy.ActiveExchange()
	rows := economy.ExchangeRowsFor(aid, now)
	begin, end := economy.ExchangeWindowAt(now)
	group := pbVar(nil, 1, 1)  // group_id: the client's GroupId 1
	group = pbVar(group, 2, 1) // ver [I]
	// The period line is always drawn (Title::RefreshUI 0x1db7624): epoch ms, 0 would read 1970.
	group = pbVar(group, 3, uint64(begin.UnixMilli()))
	group = pbVar(group, 4, uint64(end.UnixMilli()))
	group = pbLen(group, 6, []byte(e.Title))
	group = pbLen(group, 7, []byte(e.TitleDesc))
	for _, r := range rows {
		group = pbLen(group, 5, squareExchangeRow(r))
	}
	_, cash, _ := economy.Balances(aid)
	body := pbLen(nil, 1, group)
	body = pbLen(body, 2, pbVar(pbVar(nil, 1, squareExchangeCoinType), 2, uint64(economy.BossCoinBalance(aid))))
	body = pbLen(body, 2, pbVar(pbVar(nil, 1, squareExchangeCashType), 2, uint64(cash)))
	return pbVar(body, 3, 0), len(rows) // result: not read by the client's list path
}

// handleExchange answers the exchange office requests for a session in the room.
func (s *squareSession) handleExchange(frame []byte, msgid uint16, data []byte) error {
	aid := s.aid
	aidText := strconv.FormatUint(aid, 10)
	now := economy.NowFunc()
	switch msgid {
	case 52: // cr_gacha_list_req {GroupId} -> rc_gacha_list_res (68)
		body, rows := squareExchangeList(aidText, now)
		s.logger.Printf("SQUARE exchange list aid=%d rows=%d coins=%d", aid, rows, economy.BossCoinBalance(aidText))
		return s.reply(frame, 68, body)
	case 53: // cr_gacha_purchase_req {GachaId} -> rc_gacha_purchase_res (69)
		id, _ := protobufUintField(data, 1)
		row, balance, ok := economy.ExchangeBuy(aidText, int64(id), now)
		if !ok {
			s.logger.Printf("SQUARE exchange buy refused aid=%d row=%d balance=%d", aid, id, balance)
			return s.reply(frame, 69, pbVar(nil, 1, squareExchangeFail))
		}
		s.logger.Printf("SQUARE exchange buy aid=%d row=%d coins=%d balance=%d item=%s", aid, row.ID, row.Price, balance, row.ItemCode)
		body := pbVar(nil, 1, 0)                        // result: 0 = success
		body = pbVar(body, 2, uint64(row.ID))           // gacha_id
		body = pbVar(body, 3, squareExchangeCoinType)   // coin_type
		body = pbVar(body, 4, uint64(row.Price))        // price
		body = pbVar(body, 5, uint64(balance))          // balance: Boss Coins left
		reward := pbVar(nil, 1, 1)                      // view_type [I]
		reward = pbLen(reward, 3, []byte(row.ItemCode)) // view_item_code
		reward = pbVar(reward, 4, 1)                    // view_count
		reward = pbLen(reward, 7, []byte(economy.ItemName(row.ItemCode)))
		reward = pbLen(reward, 10, []byte(economy.ExchangeItemImageURL(row.ItemCode))) // img_url
		return s.reply(frame, 69, pbLen(body, 6, reward))                              // reward: RewardItemInfo
	}
	return nil
}

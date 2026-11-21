package lpn

import "cherry/internal/economy"

// squareGatedTheme is the one Square type that may answer a request (user, 2026-11-17: fishing and Treasure World are
// on Treasure Island only). Fishing, the boss try, lobby, battle and book, and the Boss Coin office belong to Treasure
// Island (6); the Ride Point shop and the Riding Pet race to LINE City (1). The boss lobby entry (36) is not gated: an
// invitee accepts from wherever the session is, and a lobby exists only in the Treasure Island room (its try, 45 and 54,
// is gated there).
func squareGatedTheme(msgid uint16) (uint64, bool) {
	switch msgid {
	case 25, 26, 27, 28, 29, 30, 59, 87, 88, 89, 90, 91, // fishing: book, cast, rods, shop, bag, buy, equip, gear
		35, 37, 45, 46, 47, 48, 49, 50, 54, 55, 56, 57, 58, 60, // boss: invite, picker, try, lobby, battle, book
		52, 53: // Boss Coin office: list, buy
		return squareThemeID, true
	case 61, 62, 63, 64, 65, 66, 68, 71, 72, 76, 79, 82, 86, 94: // Ride Point shop and the Riding Pet race
		return squareCityThemeID, true
	}
	return 0, false
}

// refuseSquare answers a gated request sent from another Square with the refusal that request already has. A request
// with no refusal reply is logged and ignored.
func (s *squareSession) refuseSquare(frame []byte, msgid uint16) error {
	s.logger.Printf("SQUARE msgid=%d refused aid=%d theme=%d", msgid, s.aid, s.square.Theme)
	fail := func(reply uint16) error { // a fishing step fails with the result every fishing reply uses for that
		return s.reply(frame, reply, pbVar(pbVar(nil, 1, s.aid), 2, squareFishMiss))
	}
	switch msgid {
	case 27: // cr_begin_fishing_req
		return fail(30)
	case 29: // cr_start_fishing_req
		return fail(32)
	case 28: // cr_end_fishing_req
		return fail(31)
	case 30: // cr_finish_fishing_req
		return fail(33)
	case 26: // cr_complete_collection_req: the book reward refusal
		return s.reply(frame, 29, pbVar(nil, 1, 0))
	case 59: // cr_inven_item_list_req: an empty rod list, without the boss icon
		return s.reply(frame, 78, nil)
	case 89: // cr_buy_item_req: "This item is currently unavailable."
		return s.reply(frame, 117, pbVar(nil, 1, 655376))
	case 90: // cr_equip_item_req
		return s.reply(frame, 118, pbVar(nil, 1, 655376))
	case 91: // cr_get_equip_item_req: nothing listed
		return s.reply(frame, 119, nil)
	case 45: // cr_start_fishing_boss_try_req: not started, no tries shown
		return s.reply(frame, 57, squareBossTry(false, economy.ActiveFishBoss(), 0))
	case 57: // cr_complete_fish_boss_collection_req: not claimed
		return s.reply(frame, 76, pbVar(nil, 1, 0))
	case 53: // cr_gacha_purchase_req: the Boss Coin office's failed buy
		return s.reply(frame, 69, pbVar(nil, 1, squareExchangeFail))
	case 62: // cr_exchange_shop_purchase_req: the Ride Point refusal
		return s.reply(frame, 81, pbVar(nil, 1, squareRideShopRefused))
	case 66: // cr_update_minigame_room_req: "You can't make the Room."
		return s.reply(frame, 84, pbVar(nil, 1, squareRacingRefused))
	case 68: // cr_get_minigame_tutorial_info_req: no tutorials
		return s.reply(frame, 86, nil)
	}
	return nil
}

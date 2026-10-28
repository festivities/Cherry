package lpn

import (
	"log"
	"math"
	"math/rand/v2"
	"strconv"
	"time"

	"cherry/internal/economy"
	"cherry/internal/store"
)

// Garden butlers (Garden LPN agent 10; spec butler-spec.md): the Shop (51 list, 52 hire), the Book (53 list,
// 50 gather, 56 chief) and Skin Select (62). Every other butler request gets a refusal with no data and no
// spend, so the client's spinner clears. Field numbers and wire types are the spec's contract (section 3).
// Reply msgids differ from request msgids (section 2). The content is Cherry-made: economy/butler.go and
// data/butlers.json. The chief spawns through hc_room_relaystart_res fields 8 and 9 (garden.go relay start).

// butlerRefused is the generic refusal result of a butler reply: nothing changed, no data.
const butlerRefused = 1

// butlerNoDate is "no date" for the v64 sale, sell and free end times (int64 -1; 0 would read as 1970).
const butlerNoDate = math.MaxUint64

// butlerChiefTile is the Garden tile the chief stands on [I]. (33,39) is about 120 px right of the owner's spawn
// (30,39) on the 60x60 packs, so the chief does not stand hidden behind the owner's avatar.
var butlerChiefTile = [2]int{33, 39}

// butlerReplies maps every butler request to its reply msgid (spec section 2).
var butlerReplies = map[uint16]uint16{
	48: 57, 49: 58, 50: 59, 51: 60, 52: 61, 53: 63, 54: 64, 55: 65, 56: 66, 57: 67, 58: 69, 59: 70,
	60: 71, 61: 72, 62: 73, 63: 74, 64: 75, 65: 76, 66: 77, 67: 78, 68: 79, 69: 80, 70: 81,
}

func gardenButlerMsg(msgid uint16) bool {
	_, ok := butlerReplies[msgid]
	return ok
}

// butlerSkinID is the skin number of a roster butler: its place in the Cherry block [I].
func butlerSkinID(b economy.ButlerDef) uint64 { return uint64(b.ID - economy.CherryButlerFirst + 1) }

// butlerImageURL is the portrait of a roster butler: a RELATIVE path (the client prefixes its static base), served by
// economy.HandleButlerImage at /cherry/butler/<id>.png.
func butlerImageURL(b economy.ButlerDef) string {
	return "cherry/butler/" + strconv.FormatInt(b.ID, 10) + ".png"
}

// butlerSkin is ButlerSkinInfo (spec 12.1): skinId, skinCd, the portrait (skinImgUrl, f3), avatarSkinColor (f4), the
// outfit (f5, one ItemInfo per item: the numeric id and dyeType 0) and skinName. The client does not read skinCd.
func butlerSkin(b economy.ButlerDef) []byte {
	skin := pbVar(nil, 1, butlerSkinID(b))
	skin = pbLen(skin, 2, []byte(b.Skin))
	skin = pbLen(skin, 3, []byte(butlerImageURL(b)))
	skin = pbVar(skin, 4, uint64(b.SkinColor))
	for _, id := range economy.ButlerOutfitIDs(b) {
		skin = pbLen(skin, 5, pbVar(pbVar(nil, 1, uint64(id)), 3, 0))
	}
	return pbLen(skin, 6, []byte(b.Name))
}

// butlerSeconds is a stored epoch-millisecond time as the wire carries it: epoch SECONDS (the client reads the
// contract times as seconds; a millisecond value showed as year 58793 on the device, 2026-10-28).
func butlerSeconds(ms int64) uint64 { return uint64(ms / 1000) }

// butlerEmployment is ButlerEmploymentInfo: employed, extendable under 10 days left (string 8619), the contract
// times in epoch seconds and the time left in seconds (remainTime is int32) [I].
func butlerEmployment(h store.ButlerHire, now time.Time) []byte {
	left := h.End - now.UnixMilli()
	body := pbVar(nil, 1, 1)
	body = pbVar(body, 2, squareBool(left < 10*86400000))
	body = pbVar(body, 3, butlerSeconds(h.Begin))
	body = pbVar(body, 4, butlerSeconds(h.Begin))
	body = pbVar(body, 5, butlerSeconds(h.End))
	return pbVar(body, 6, uint64(max(left/1000, 0)))
}

// butlerInfo is ButlerInfo (spec 3.3): id, name, gender (the client's sex enum), isLeader, skinInfo, and for a
// contract the employmentInfo. The chief also gets positionInfo (MovePoint, the varint px/py the Garden NPCs use).
func butlerInfo(b economy.ButlerDef, hire *store.ButlerHire, chief bool, now time.Time) []byte {
	body := pbVar(nil, 1, uint64(b.ID))
	body = pbLen(body, 2, []byte(b.Name))
	body = pbVar(body, 3, squareSex(b.Gender))
	body = pbVar(body, 4, squareBool(chief))
	body = pbLen(body, 5, butlerSkin(b))
	if hire != nil {
		body = pbLen(body, 6, butlerEmployment(*hire, now))
	}
	if chief {
		px, py := gardenPx(butlerChiefTile[0], butlerChiefTile[1])
		body = pbLen(body, 7, pbVar(pbVar(nil, 1, px), 2, py))
	}
	return body
}

// butlerDetail is ButlerDetailInfo for one contract (spec 3.3): the info, the gathering state (idle [I]) and one
// ability: the gathering type, today's gathers against the cap. vipGrade (f2) is left unset on purpose: a grade would
// trigger the client's VIP gate (spec 12.3) and its "Become VIP" route, which is real money. Without a grade the
// line "Hiring available for  and above." reads blank. That is client-inherent, not a bug.
func butlerDetail(b economy.ButlerDef, hire store.ButlerHire, chief bool, gathers int64, now time.Time) []byte {
	body := pbLen(nil, 1, butlerInfo(b, &hire, chief, now))
	body = pbVar(body, 4, 0) // gatheringStatus [I]
	body = pbVar(body, 5, 0) // gatheringRemainTime
	ability := pbVar(nil, 1, uint64(economy.ButlerGatherType()))
	ability = pbVar(ability, 2, 0) // gatheringTime
	ability = pbVar(ability, 3, uint64(gathers))
	ability = pbVar(ability, 4, uint64(economy.ActiveButlers().Gathering.GathersPerDay))
	return pbLen(body, 6, ability)
}

// butlerCoin is CoinInfo: the coin type and its value.
func butlerCoin(coinType, value int64) []byte {
	return pbVar(pbVar(nil, 1, uint64(coinType)), 2, uint64(value))
}

// butlerBuyInfo is one Shop row (ButlerBuyInfo, spec 3.3). The free first hire is shown while open; the top-level
// costValue is 0 (the server prices every hire itself) and the hire periods carry the earned-Cash prices (salePrice
// is the charged amount, spec 4). The sale and sell windows never end.
func butlerBuyInfo(b economy.ButlerDef, v economy.ButlerView, now time.Time) []byte {
	days := uint64(economy.ActiveButlers().FreeDays)
	body := pbLen(nil, 1, pbLen(nil, 1, butlerInfo(b, nil, false, now)))
	body = pbVar(body, 2, uint64(economy.ButlerCoinCash))
	body = pbVar(body, 3, 0)
	body = pbVar(body, 4, days)
	body = pbVar(body, 5, butlerNoDate)
	body = pbVar(body, 9, butlerNoDate)
	body = pbVar(body, 10, squareBool(v.FreeOpen()))
	body = pbVar(body, 11, days)
	body = pbVar(body, 12, butlerNoDate)
	for _, p := range economy.ActiveButlers().Hire {
		hire := pbVar(nil, 1, uint64(economy.ButlerCoinCash))
		hire = pbVar(hire, 2, uint64(p.Cash))
		hire = pbVar(hire, 3, uint64(p.Days))
		hire = pbVar(hire, 4, uint64(p.Cash))
		body = pbLen(body, 13, hire)
	}
	return body
}

// butlerShopBody is the Shop list (hc 60): result 0, one row per roster butler, the Cash balance (coinInfos).
func butlerShopBody(v economy.ButlerView, now time.Time) []byte {
	body := pbVar(nil, 1, 0)
	for _, b := range economy.ActiveButlers().Butlers {
		body = pbLen(body, 2, butlerBuyInfo(b, v, now))
	}
	return pbLen(body, 3, butlerCoin(economy.ButlerCoinCash, v.Cash))
}

// butlerBuyBody is the hire reply (hc 61): the new contract, the Cash charged and the free-hire flags. The
// contractTime and isFirstButler meanings are [I].
func butlerBuyBody(got economy.ButlerHired, now time.Time) []byte {
	body := pbVar(nil, 1, 0)
	body = pbLen(body, 2, pbLen(nil, 1, butlerInfo(got.Butler, &got.Hire, false, now)))
	body = pbLen(body, 3, butlerCoin(economy.ButlerCoinCash, got.Price))
	body = pbVar(body, 4, butlerSeconds(got.Hire.Begin))
	body = pbVar(body, 5, butlerSeconds(got.Hire.End))
	body = pbVar(body, 6, butlerSeconds(got.Hire.End-got.Hire.Begin)) // contractTime: a duration in seconds [I]
	body = pbVar(body, 7, squareBool(got.Free))
	body = pbVar(body, 8, squareBool(got.Free))
	return pbVar(body, 9, uint64(economy.ActiveButlers().FreeDays))
}

// butlerBookBody is the Book list (hc 63): result 0 (explicit, the expiry notice reads it), one ButlerDetailInfo
// per contract, and the butlers that just ran out (name of the first, ids of all) for the expiry notice.
func butlerBookBody(v economy.ButlerView, expired []economy.ButlerDef, now time.Time) []byte {
	body := pbVar(nil, 1, 0)
	_, gathers := v.Today()
	chief := v.Chief()
	for _, b := range economy.ActiveButlers().Butlers {
		if h, ok := v.Hire(b.ID); ok {
			body = pbLen(body, 2, butlerDetail(b, h, b.ID == chief, gathers, now))
		}
	}
	if len(expired) > 0 {
		body = pbLen(body, 3, []byte(expired[0].Name))
		for _, b := range expired {
			body = pbVar(body, 4, uint64(b.ID))
		}
	}
	return body
}

// butlerThumb is one Skin Select row (ButlerThumbnailInfo): the id, name, whether it is under contract, its skin and
// its portrait (skinImgUrl, f5; an empty URL leaves the cell spinning, spec 12.2).
func butlerThumb(b economy.ButlerDef, employed bool) []byte {
	t := pbVar(nil, 1, uint64(b.ID))
	t = pbLen(t, 2, []byte(b.Name))
	t = pbVar(t, 3, squareBool(employed))
	t = pbVar(t, 4, butlerSkinID(b))
	return pbLen(t, 5, []byte(butlerImageURL(b)))
}

// butlerGatherBody is the gather reply (hc 59): the result, the request's gathering status echoed [I], no time
// left and an empty butler icon.
func butlerGatherBody(result, status uint64) []byte {
	body := pbVar(nil, 1, result)
	body = pbVar(body, 2, status)
	body = pbVar(body, 3, 0)
	return pbLen(body, 6, nil)
}

// butlerChangeLeader is the chief push (hc 68): the new chief's ButlerInfo and its icon.
func butlerChangeLeader(b economy.ButlerDef, h store.ButlerHire, now time.Time) []byte {
	body := pbLen(nil, 1, butlerInfo(b, &h, true, now))
	return pbLen(body, 2, pbVar(nil, 1, 0))
}

// butlerRefusal is the reply to a request the butlers do not serve here: result butlerRefused, or the
// message the Garden's popup shows for it (reward info: "not available"), or an empty reply where the reply
// has no result field (spec 3.2). Nothing is spent.
func butlerRefusal(reply uint16) []byte {
	switch reply {
	case 77, 80:
		return nil
	case 64:
		return pbVar(nil, 1, 217096) // "Reward info not available." (Book popup, spec 5.2)
	}
	return pbVar(nil, 1, butlerRefused)
}

// gardenButlerRelay is what relay start adds for the owner's chief (hc_room_relaystart_res fields 8 ButlerInfo and
// 9 ButlerIconInfo, spec 6): nothing when no chief is under contract. Visitors see the owner's chief.
func gardenButlerRelay(owner uint64, now time.Time) []byte {
	b, h, ok := economy.ButlerChief(strconv.FormatUint(owner, 10), now)
	if !ok {
		return nil
	}
	body := pbLen(nil, 8, butlerInfo(b, &h, true, now))
	return pbLen(body, 9, pbVar(nil, 1, 0))
}

// gardenButlerReply answers one butler request and returns the frames to send, in order. viewer is the account on
// the connection; its own butlers answer. Nothing is spent unless the economy call succeeds.
func gardenButlerReply(msgid uint16, viewer uint64, data []byte, now time.Time, logger *log.Logger) []gardenMsg {
	aid := strconv.FormatUint(viewer, 10)
	switch msgid {
	case 50: // do_gathering {butler_id, gathering_status, gathering_req_type}
		id, _ := protobufUintField(data, 1)
		status, _ := protobufUintField(data, 2)
		got, err := economy.GatherButler(aid, int64(id), now, rand.IntN)
		if err != nil {
			logger.Printf("BUTLER gather aid=%s butler=%d result=217095 err=%v", aid, id, err)
			return []gardenMsg{{59, butlerGatherBody(217095, status)}} // "Collecting currently unavailable."
		}
		logger.Printf("BUTLER gather aid=%s butler=%d materials=%d gems=%d result=0", aid, id, len(got.Drops), got.Gems)
		return []gardenMsg{{59, butlerGatherBody(0, status)}}
	case 51: // shop {}
		return []gardenMsg{{60, butlerShopBody(economy.ButlerViewOf(aid, now), now)}}
	case 52: // buy {butler_id, cost_type, cost_value (ignored: the server prices it), is_free, employ_days}
		id, _ := protobufUintField(data, 1)
		costType, _ := protobufUintField(data, 2)
		free, _ := protobufUintField(data, 4)
		days, _ := protobufUintField(data, 5)
		got, err := economy.HireButler(aid, int64(id), int64(costType), int64(days), free == 1, now)
		if err != nil {
			logger.Printf("BUTLER hire aid=%s butler=%d days=%d result=%d err=%v", aid, id, days, butlerRefused, err)
			return []gardenMsg{{61, pbVar(nil, 1, butlerRefused)}}
		}
		logger.Printf("BUTLER hire aid=%s butler=%d days=%d cash=%d free=%v result=0", aid, id, days, got.Price, got.Free)
		return []gardenMsg{{61, butlerBuyBody(got, now)}}
	case 53: // book {}
		expired, err := economy.ExpireButlers(aid, now)
		if err != nil {
			logger.Printf("BUTLER expire aid=%s err=%v", aid, err)
		}
		return []gardenMsg{{63, butlerBookBody(economy.ButlerViewOf(aid, now), expired, now)}}
	case 56: // leader_setting {butler_id}
		id, _ := protobufUintField(data, 1)
		if err := economy.SetButlerChief(aid, int64(id), now); err != nil {
			logger.Printf("BUTLER chief aid=%s butler=%d result=217103 err=%v", aid, id, err)
			return []gardenMsg{{66, pbVar(nil, 1, 217103)}} // a contract ran out: the Book refreshes (spec 5.2)
		}
		logger.Printf("BUTLER chief aid=%s butler=%d result=0", aid, id)
		msgs := []gardenMsg{{66, pbVar(nil, 1, 0)}}
		if b, h, ok := economy.ButlerChief(aid, now); ok {
			msgs = append(msgs, gardenMsg{68, butlerChangeLeader(b, h, now)})
		}
		return msgs
	case 62: // thumbnail_list {}
		v := economy.ButlerViewOf(aid, now)
		body := pbVar(nil, 1, 0)
		for _, b := range economy.ActiveButlers().Butlers {
			_, employed := v.Hire(b.ID)
			body = pbLen(body, 2, butlerThumb(b, employed))
		}
		return []gardenMsg{{73, body}}
	}
	reply := butlerReplies[msgid]
	logger.Printf("BUTLER refused aid=%s msgid=%d reply=%d", aid, msgid, reply)
	return []gardenMsg{{reply, butlerRefusal(reply)}}
}

// gardenButlerHandle answers one butler request on the relayed Garden tunnel.
func gardenButlerHandle(conn *lockedConn, frame []byte, msgid uint16, viewer uint64, logger *log.Logger) error {
	for _, m := range gardenButlerReply(msgid, viewer, frame[12:], economy.NowFunc(), logger) {
		if _, err := conn.Write(squareFrame(frame[1], frame[2:10], m.id, m.body)); err != nil {
			return err
		}
	}
	return nil
}

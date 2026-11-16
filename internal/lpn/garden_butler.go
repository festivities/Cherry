package lpn

import (
	"log"
	"math"
	"math/rand/v2"
	"path"
	"strconv"
	"time"

	"cherry/internal/economy"
	"cherry/internal/store"
)

// Garden butlers (Garden LPN agent 10; spec butler-spec.md): the Shop (51 list, 52 hire), the Book (53 list,
// 50 gather, 56 chief), Skin Select (62), the Skin Shop (60 list, 61 buy, 59 wear; butler-robo-spec section 2), the
// Voice Book (63; section 1), the Memories (69 list, 70 open; section 3) and the Chat (48 dialog, 49 talk; section 10).
// Every other butler request gets a refusal with no data and no spend, so the client's spinner clears. Field numbers
// and wire types are the spec's contract (section 3).
// Reply msgids differ from request msgids (section 2). The roster is original data with Cherry-made ids and prices:
// economy/butler.go and data/butlers.json. The chief spawns through hc_room_relaystart_res fields 8 and 9 (garden.go
// relay start).

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

// butlerImageURL is the default skin's picture of a roster butler: a RELATIVE path (the client prefixes its static base),
// served by economy.HandleButlerImage at /cherry/butler/<id>.png with the original picture (the form the client already loads).
func butlerImageURL(b economy.ButlerDef) string {
	return "cherry/butler/" + strconv.FormatInt(b.ID, 10) + ".png"
}

// butlerSkinURL is the picture of skin s of butler b (skinImgUrl, ThumbnailInfo f5): the default's /cherry/butler/<id>.png,
// a further skin's /cherry/butler/skin/<skin id>.png.
func butlerSkinURL(b economy.ButlerDef, s economy.ButlerSkin) string {
	if s.ID == b.Skins[0].ID {
		return butlerImageURL(b)
	}
	return "cherry/butler/skin/" + strconv.FormatInt(s.ID, 10) + ".png"
}

// butlerRewardURL is the gather-reward picture (ButlerRewardInfo butlerRewardImg): /cherry/butler/<id>-reward.png when the
// data names an original reward picture (reward_image, butler-robo-spec 2026-11-16), else the butler's picture.
func butlerRewardURL(b economy.ButlerDef) string {
	if b.RewardImage == "" {
		return butlerImageURL(b)
	}
	return "cherry/butler/" + strconv.FormatInt(b.ID, 10) + "-reward.png"
}

// butlerSkin is ButlerSkinInfo (spec 12.1): skinId, skinCd, the picture (skinImgUrl, f3), avatarSkinColor (f4), the
// outfit (f5, one ItemInfo per item: the numeric id and dyeType 0) and skinName. The client does not read skinCd.
func butlerSkin(b economy.ButlerDef, s economy.ButlerSkin) []byte {
	skin := pbVar(nil, 1, uint64(s.ID))
	skin = pbLen(skin, 2, []byte(s.Code))
	skin = pbLen(skin, 3, []byte(butlerSkinURL(b, s)))
	skin = pbVar(skin, 4, uint64(s.SkinColor))
	for _, id := range economy.ButlerOutfitIDs(s) {
		skin = pbLen(skin, 5, pbVar(pbVar(nil, 1, uint64(id)), 3, 0))
	}
	return pbLen(skin, 6, []byte(s.Name))
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

// butlerInfo is ButlerInfo (spec 3.3): id, name, gender (the client's sex enum), isLeader, skinInfo (the skin the butler
// wears, worn), and for a contract the employmentInfo. The chief also gets positionInfo (MovePoint, the varint px/py the
// Garden NPCs use). A butler with a voice pack gets voiceInfo (f9): the Profile shows its Voice Book button only when
// that record is present (butler-robo-spec 9).
func butlerInfo(b economy.ButlerDef, worn economy.ButlerSkin, hire *store.ButlerHire, chief bool, now time.Time) []byte {
	body := pbVar(nil, 1, uint64(b.ID))
	body = pbLen(body, 2, []byte(b.Name))
	body = pbVar(body, 3, squareSex(b.Gender))
	body = pbVar(body, 4, squareBool(chief))
	body = pbLen(body, 5, butlerSkin(b, worn))
	if hire != nil {
		body = pbLen(body, 6, butlerEmployment(*hire, now))
	}
	if chief {
		px, py := gardenPx(butlerChiefTile[0], butlerChiefTile[1])
		body = pbLen(body, 7, pbVar(pbVar(nil, 1, px), 2, py))
	}
	if b.Voices != nil {
		body = pbLen(body, 9, butlerVoiceInfo(b))
	}
	return body
}

// butlerVoiceInfo is ButlerVoiceInfo: the pack's revision (f1) and one record with the zip's path (f2 f4, spec 1.2-1.3).
func butlerVoiceInfo(b economy.ButlerDef) []byte {
	return pbLen(pbVar(nil, 1, uint64(b.Voices.Revision)), 2, pbLen(nil, 4, []byte(b.Voices.ZipPath())))
}

// butlerGatherState is the gathering status and end time the card shows at now (ButlerDetailInfo f4 and f5, hc 59 f2
// and f3; spec section 2.2): 1 idle (no gathering), 3 in progress until the end, 5 collect once the end has passed. The
// end is epoch seconds, and the client reads it only at 3.
func butlerGatherState(g *store.ButlerGather, now time.Time) (status, end uint64) {
	switch {
	case g == nil:
		return 1, 0
	case economy.ButlerGatherDone(*g, now):
		return 5, butlerSeconds(g.End)
	}
	return 3, butlerSeconds(g.End)
}

// butlerDetail is ButlerDetailInfo (spec 3.3): the info (with the employment when hire is a contract), the gathering
// state (butlerGatherState) and one ability per gathering type of the butler: the type, today's gathers against the cap.
// vipGrade (f2) is left unset on purpose: a grade would trigger the client's VIP gate (spec 12.3) and its "Become VIP"
// route, which is real money. Without a grade the line "Hiring available for  and above." reads blank. That is
// client-inherent, not a bug.
func butlerDetail(b economy.ButlerDef, worn economy.ButlerSkin, hire *store.ButlerHire, chief bool, gathers int64, gather *store.ButlerGather, now time.Time) []byte {
	body := pbLen(nil, 1, butlerInfo(b, worn, hire, chief, now))
	status, end := butlerGatherState(gather, now)
	body = pbVar(body, 4, status) // gatheringStatus
	body = pbVar(body, 5, end)    // gatheringRemainTime: an absolute end time, epoch seconds
	perDay := uint64(economy.ActiveButlers().Gathering.GathersPerDay)
	for _, kind := range economy.ButlerAbilities(b) {
		ability := pbVar(nil, 1, uint64(kind))
		ability = pbVar(ability, 2, 0) // gatheringTime
		ability = pbVar(ability, 3, uint64(gathers))
		ability = pbVar(ability, 4, perDay)
		body = pbLen(body, 6, ability)
	}
	return body
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
	body := pbLen(nil, 1, pbLen(nil, 1, butlerInfo(b, v.Worn(b), nil, false, now)))
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
	body = pbLen(body, 2, pbLen(nil, 1, butlerInfo(got.Butler, got.Worn, &got.Hire, false, now)))
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
//
// The Book pairs the entries into rows: entry 2k is the left card, 2k+1 the right. Only the left card can show the
// gather button (spec section 2.4: the right card's AUTO button stays hidden, device check section 7.1), so every
// contract takes a left card. An empty ButlerDetailInfo (id 0, so its card stays hidden) fills an odd slot before it.
// Roster order is kept otherwise.
func butlerBookBody(v economy.ButlerView, expired []economy.ButlerDef, now time.Time) []byte {
	body := pbVar(nil, 1, 0)
	_, gathers := v.Today()
	chief := v.Chief()
	entries := 0
	for _, b := range economy.ActiveButlers().Butlers {
		if h, ok := v.Hire(b.ID); ok {
			if entries%2 == 1 {
				body = pbLen(body, 2, nil)
				entries++
			}
			body = pbLen(body, 2, butlerDetail(b, v.Worn(b), &h, b.ID == chief, gathers, v.Gathering(b.ID), now))
			entries++
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

// butlerThumb is one Skin Select row (ButlerThumbnailInfo): the id, name, whether it is under contract, the skin it
// wears (f4) and that skin's picture (f5; an empty URL leaves the cell spinning, spec 12.2).
func butlerThumb(b economy.ButlerDef, worn economy.ButlerSkin, employed bool) []byte {
	t := pbVar(nil, 1, uint64(b.ID))
	t = pbLen(t, 2, []byte(b.Name))
	t = pbVar(t, 3, squareBool(employed))
	t = pbVar(t, 4, uint64(worn.ID))
	return pbLen(t, 5, []byte(butlerSkinURL(b, worn)))
}

// butlerThumbOf is butler b's thumbnail as view v shows it: the skin it wears and whether a contract runs.
func butlerThumbOf(b economy.ButlerDef, v economy.ButlerView) []byte {
	_, employed := v.Hire(b.ID)
	return butlerThumb(b, v.Worn(b), employed)
}

// butlerSkinCoinCash is the Skin Shop's Cash coin type: the row's f6 type picks the icon and the balance the client checks
// (1 Cash, anything else Gems; butler-robo-spec 8), unlike the Butler Shop's ButlerCoinCash 2.
const butlerSkinCoinCash = 1

// butlerSkinRow is one Skin Shop row (ButlerSkinBuyInfo, spec 2.6): f1 the butler id, f2 its sex (the older spec's name),
// f3 the skin (ButlerSkinInfo: picture, outfit, name), f4 owned and f5 worn (1 or 0: the field numbers are a probe, see
// butler-robo-spec 2.6), f6 the coin (its type picks the icon and the balance checked) and f9 the price (the client reads
// only f9's value, spec 8); no sale and no free hire (f8, f11). The sell and sale end times are 0 (f7, f10): an end time
// of -1 printed "Sales Period: Dec 31 1969", and 0 hides that line and the second price block (butler-robo-spec 10.6).
func butlerSkinRow(b economy.ButlerDef, s economy.ButlerSkin, v economy.ButlerView) []byte {
	row := pbVar(nil, 1, uint64(b.ID))
	row = pbVar(row, 2, squareSex(b.Gender))
	row = pbLen(row, 3, butlerSkin(b, s))
	row = pbVar(row, 4, squareBool(v.Owns(b, s.ID)))
	row = pbVar(row, 5, squareBool(v.Worn(b).ID == s.ID))
	price := butlerCoin(butlerSkinCoinCash, s.Price)
	row = pbLen(row, 6, price)
	row = pbVar(row, 7, 0)
	row = pbVar(row, 8, 0)
	row = pbLen(row, 9, price)
	row = pbVar(row, 10, 0)
	return pbVar(row, 11, 0)
}

// appendSkinRows adds one butlerSkinRow per skin of b to body as repeated field f.
func appendSkinRows(body []byte, f uint64, b economy.ButlerDef, v economy.ButlerView) []byte {
	for _, s := range b.Skins {
		body = pbLen(body, f, butlerSkinRow(b, s, v))
	}
	return body
}

// butlerSkinBody is the Skin Shop list (hc 71) and the reply to a buy (hc 72): result 0, the butler's thumbnail (f2), one
// row per skin (f3) and the Cash balance (f4, CoinInfo).
func butlerSkinBody(b economy.ButlerDef, v economy.ButlerView) []byte {
	body := pbVar(nil, 1, 0)
	body = pbLen(body, 2, butlerThumbOf(b, v))
	body = appendSkinRows(body, 3, b, v)
	return pbLen(body, 4, butlerCoin(economy.ButlerCoinCash, v.Cash))
}

// butlerEquipBody is the reply to a wear (hc 70): result 0, the skin now worn (f2), the rows (f3) and the thumbnail (f4).
func butlerEquipBody(b economy.ButlerDef, v economy.ButlerView) []byte {
	body := pbVar(nil, 1, 0)
	body = pbVar(body, 2, uint64(v.Worn(b).ID))
	body = appendSkinRows(body, 3, b, v)
	return pbLen(body, 4, butlerThumbOf(b, v))
}

// butlerVoiceBody is the Voice Book (hc 74): result 0, the butler's thumbnail (f2) and, for a butler with a voice pack, its
// revision and zip record (f3: revision f1, one record with the zip's path in f4) and one script row per line: the key N
// (f1), the display order from 1 (f2: the "#n" header and the sort, butler-robo-spec 10.7), 1 open (f3), the label "Voice N",
// the empty text, the file's path under cache/butler/. A butler without voices gets neither f3 nor f4: an empty voiceInfo
// means no pack (spec 1.2), so the download popup does nothing (not a refusal, which would show an error).
func butlerVoiceBody(b economy.ButlerDef, v economy.ButlerView) []byte {
	body := pbVar(nil, 1, 0)
	body = pbLen(body, 2, butlerThumbOf(b, v))
	if b.Voices == nil {
		return body
	}
	body = pbLen(body, 3, butlerVoiceInfo(b))
	for i, n := range b.Voices.Lines {
		row := pbVar(nil, 1, uint64(n))
		row = pbVar(row, 2, uint64(i+1))
		row = pbVar(row, 3, 1)
		row = pbLen(row, 4, []byte("Voice "+strconv.FormatInt(n, 10)))
		row = pbLen(row, 5, nil)
		row = pbLen(row, 6, []byte(b.Voices.FilePath(n)))
		body = pbLen(body, 4, row)
	}
	return body
}

// memoryURL is a memory picture's relative URL (SIMPLE_INFO f6, SCENE_INFO f4): /cherry/butler/memory/<file>.png, served by
// economy.HandleButlerImage for the pictures the data lists.
func memoryURL(p string) string { return "cherry/butler/memory/" + path.Base(p) }

// butlerMemoryRow is one memory card (BUTLER_MEMORY_SIMPLE_INFO, spec 3.2): the butler id (f1), the memory id (f2: the popup
// sends it back in ch 70), the title (f3, SetTitle), the list picture (f6: the list cell's background, DownloadBGImage; the one
// picture a row carries, so the thumbnail has no field), open (f7) and can open (f8). The open window (f4, f5), the need (f9)
// and the open condition text (f10) are left unset: every memory is open.
func butlerMemoryRow(b economy.ButlerDef, m economy.ButlerMemory) []byte {
	row := pbVar(nil, 1, uint64(b.ID))
	row = pbVar(row, 2, uint64(m.ID))
	row = pbLen(row, 3, []byte(m.Title))
	row = pbLen(row, 6, []byte(memoryURL(m.ListPicture)))
	row = pbVar(row, 7, 1)
	return pbVar(row, 8, 1)
}

// butlerMemoryList is the Memories popup's list (hc 80, spec 3.1): the butler id, the top message (f2), one row per memory
// (f3), the butler's picture (f4) and name (f5). A butler without memories gets no rows.
func butlerMemoryList(b economy.ButlerDef) []byte {
	body := pbVar(nil, 1, uint64(b.ID))
	body = pbLen(body, 2, []byte(b.MemoryMessage))
	for _, m := range b.Memories {
		body = pbLen(body, 3, butlerMemoryRow(b, m))
	}
	body = pbLen(body, 4, []byte(butlerImageURL(b)))
	return pbLen(body, 5, []byte(b.Name))
}

// butlerMemoryOpen is the memory's story (hc 81, spec 3.1): result 0, the butler and memory ids, one SCENE_INFO per scene
// (f4: the scene number f3 from 1 and its back picture f4) and the butler's name (f5) and ButlerInfo (f6) with the skin
// it wears. Each scene carries one DETAIL row (f5): step 1, talker 0 (no character sprite), the line (f3) and an empty
// voice (f4). The ENDSCENE (f6) is left unset, so the client reads its defaults.
//
// Field choice (the one place, butler-robo-spec 3.2, decompiled 2026-11-16): SCENE f4 is the back picture (SetScene loads
// scene+8 as the texture). DETAIL f3 is the line (SetStep sets it as the text label) and DETAIL f4 the voice (SetStep plays
// it only when non-empty). No DETAIL string is a picture: the brief's "DETAIL rows (picture strings)" does not match the
// decompile. Not taken: a picture in DETAIL f4 would play as a voice and fail.
func butlerMemoryOpen(b economy.ButlerDef, m economy.ButlerMemory, worn economy.ButlerSkin, now time.Time) []byte {
	body := pbVar(nil, 1, 0)
	body = pbVar(body, 2, uint64(b.ID))
	body = pbVar(body, 3, uint64(m.ID))
	for i, s := range m.Scenes {
		line := pbVar(nil, 1, 1)
		line = pbVar(line, 2, 0)
		line = pbLen(line, 3, []byte(s.Line))
		line = pbLen(line, 4, nil)
		scene := pbVar(nil, 3, uint64(i+1))
		scene = pbLen(scene, 4, []byte(memoryURL(s.Picture)))
		scene = pbLen(scene, 5, line)
		body = pbLen(body, 4, scene)
	}
	body = pbLen(body, 5, []byte(b.Name))
	return pbLen(body, 6, butlerInfo(b, worn, nil, false, now))
}

// butlerGatherBody is the gather reply (hc 59): the result, the NEW gathering status, its absolute end time in epoch
// seconds (0 when idle) and an empty butler icon (ButlerIconInfo, icon status 0).
func butlerGatherBody(result, status, end uint64) []byte {
	body := pbVar(nil, 1, result)
	body = pbVar(body, 2, status)
	body = pbVar(body, 3, end)
	return pbLen(body, 6, nil)
}

// butlerRewardBody is the reward popup (hc 64) of a ready gathering: result 0 and one ButlerRewardInfo of reward type 1
// (the gathering layer). Each rolled item is a ButlerRewardItemInfo with a numeric itemCode, its count 1, the Bag's
// craft picture and grade 1. The Gems are paid at the claim but not listed (they are not an item). The sub id is the
// start in epoch seconds; it comes back in ch 55.
func butlerRewardBody(b economy.ButlerDef, g store.ButlerGather) []byte {
	var list []byte
	for _, it := range g.Items {
		row := pbLen(nil, 1, []byte(strconv.FormatInt(it.Item, 10)))
		row = pbLen(row, 2, []byte(it.Name))
		row = pbVar(row, 3, 1)                                         // itemCount
		row = pbVar(row, 4, uint64(it.Item/100000))                    // itemType: the Bag's filter tab (1 fish, 2 essence, 3 ore)
		row = pbLen(row, 6, []byte(economy.GatherCraftImage(it.Item))) // imgUrl: the Bag's craft picture
		row = pbVar(row, 7, 1)                                         // itemGrade
		list = pbLen(list, 1, row)
	}
	info := pbVar(nil, 1, uint64(b.ID))
	info = pbVar(info, 2, 1)                            // rewardType: gathering
	info = pbLen(info, 3, []byte(butlerRewardURL(b)))   // butlerRewardImg: the reward picture
	info = pbLen(info, 4, list)                         // itemList
	info = pbVar(info, 7, uint64((g.End-g.Begin)/1000)) // gatheringIngTime: the gathering's length in seconds
	info = pbVar(info, 8, uint64(len(g.Items)))         // totalRewardCount
	info = pbVar(info, 9, butlerSeconds(g.Begin))       // rewardSubId, echoed by ch 55
	return pbLen(pbVar(nil, 1, 0), 2, info)
}

// butlerClaimBody is the claim reply (hc 65): result 0 and an empty butlerIconInfo.
func butlerClaimBody() []byte { return pbLen(pbVar(nil, 1, 0), 2, nil) }

// butlerChangeLeader is the chief push (hc 68): the new chief's ButlerInfo and its icon.
func butlerChangeLeader(b economy.ButlerDef, worn economy.ButlerSkin, h store.ButlerHire, now time.Time) []byte {
	body := pbLen(nil, 1, butlerInfo(b, worn, &h, true, now))
	return pbLen(body, 2, pbVar(nil, 1, 0))
}

// butlerChatButton is one ButlerDialogPopupButtonInfo (butler-robo-spec 10.2): the slot (1 talk, 2 gathering, 3 butler list,
// 4 special order, 5 Memories), its label and that slot's state (10.3).
func butlerChatButton(index uint64, text string, state uint64) []byte {
	return pbVar(pbLen(pbVar(nil, 1, index), 2, []byte(text)), 3, state)
}

// butlerChatButtons is the Chat popup's buttonList (butler-robo-spec 10.3), every index with its label. The gathering slot
// (2) takes the state the Book card shows for the butler's gathering: idle (status 1) state 0, collecting (3) state 1, done
// (5) state 2. The labels are the client's strings where the spec names them, Cherry-made otherwise (data/butlers.json note).
func butlerChatButtons(g *store.ButlerGather, now time.Time) [][]byte {
	status, _ := butlerGatherState(g, now)
	gather := butlerChatButton(2, "Please gather!", 0)
	switch status {
	case 3:
		gather = butlerChatButton(2, "Currently collecting.", 1)
	case 5:
		gather = butlerChatButton(2, "Collecting complete!", 2)
	}
	return [][]byte{
		butlerChatButton(1, "I'm bored! Play with me!", 0),
		gather,
		butlerChatButton(3, "Take me to the butler list!", 0),
		butlerChatButton(4, "Make me a special order!", 0),
		butlerChatButton(5, "Memories", 0),
	}
}

// butlerDialogBody is the Chat popup (hc 57, butler-robo-spec 10.2): result 0, the greeting (f2, one line for every butler),
// the buttons (f3), the butler's ButlerInfo with the skin it wears, its contract and its chief flag (f4) and, for a butler
// with voices, the file of its first line (f5, played on open; silent until the pack is downloaded).
func butlerDialogBody(b economy.ButlerDef, v economy.ButlerView, h store.ButlerHire, now time.Time) []byte {
	body := pbVar(nil, 1, 0)
	body = pbLen(body, 2, []byte(economy.ActiveButlers().ChatGreeting))
	for _, btn := range butlerChatButtons(v.Gathering(b.ID), now) {
		body = pbLen(body, 3, btn)
	}
	body = pbLen(body, 4, butlerInfo(b, v.Worn(b), &h, v.Chief() == b.ID, now))
	if b.Voices != nil {
		body = pbLen(body, 5, []byte(b.Voices.FilePath(b.Voices.Lines[0])))
	}
	return body
}

// butlerIntimacyMax is the heart gauge's maximum the Talk popup is sent. Cherry keeps no intimacy (the gauge stays 0), so
// this only scales an empty gauge; Cherry-made.
const butlerIntimacyMax = 100

// butlerTalkBody is the Talk popup (hc 58, butler-robo-spec 10.4): result 0 and ButlerIntimacyPopupInfo (f2): the heart
// gauge (now 0, max, add 0: no gain, so the client plays the character action), the butler's ButlerInfo, the talk line
// (Cherry-made, data/butlers.json talk_line) and, for a butler with voices, one of its lines (auto-played), rotating
// with the time. A reply without f2 opened the popup empty: no name, no butler, no line (device 2026-11-17).
func butlerTalkBody(b economy.ButlerDef, v economy.ButlerView, h store.ButlerHire, now time.Time) []byte {
	info := pbLen(nil, 1, pbVar(pbVar(pbVar(nil, 1, 0), 2, butlerIntimacyMax), 3, 0))
	info = pbLen(info, 2, butlerInfo(b, v.Worn(b), &h, v.Chief() == b.ID, now))
	info = pbLen(info, 3, []byte(economy.ActiveButlers().TalkLine))
	if b.Voices != nil {
		lines := b.Voices.Lines
		info = pbLen(info, 4, []byte(b.Voices.FilePath(lines[now.Unix()%int64(len(lines))])))
	}
	return pbLen(pbVar(nil, 1, 0), 2, info)
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
	b, worn, h, ok := economy.ButlerChief(strconv.FormatUint(owner, 10), now)
	if !ok {
		return nil
	}
	body := pbLen(nil, 8, butlerInfo(b, worn, &h, true, now))
	return pbLen(body, 9, pbVar(nil, 1, 0))
}

// gardenButlerReply answers one butler request and returns the frames to send, in order. viewer is the account on
// the connection; its own butlers answer. Nothing is spent unless the economy call succeeds.
func gardenButlerReply(msgid uint16, viewer uint64, data []byte, now time.Time, logger *log.Logger) []gardenMsg {
	aid := strconv.FormatUint(viewer, 10)
	switch msgid {
	case 48: // dialog_popup {butler_id}: the Chat popup of a butler under contract (hc 57)
		id, _ := protobufUintField(data, 1)
		v := economy.ButlerViewOf(aid, now)
		if b, ok := economy.ButlerByID(int64(id)); ok {
			if h, ok := v.Hire(b.ID); ok {
				logger.Printf("BUTLER dialog aid=%s butler=%d result=0", aid, id)
				return []gardenMsg{{57, butlerDialogBody(b, v, h, now)}}
			}
		}
		// an unknown or not-hired butler is refused below
	case 49: // talk {butler_id}: the Talk popup of a butler under contract, no intimacy gain (hc 58)
		id, _ := protobufUintField(data, 1)
		if b, ok := economy.ButlerByID(int64(id)); ok {
			v := economy.ButlerViewOf(aid, now)
			if h, ok := v.Hire(b.ID); ok {
				logger.Printf("BUTLER talk aid=%s butler=%d result=0", aid, id)
				return []gardenMsg{{58, butlerTalkBody(b, v, h, now)}}
			}
		}
		// an unknown or not-hired butler is refused below
	case 50: // do_gathering {butler_id, gathering_status: 2 start, 4 stop, gathering_req_type}
		id, _ := protobufUintField(data, 1)
		status, _ := protobufUintField(data, 2)
		switch status {
		case 2:
			g, err := economy.StartButlerGather(aid, int64(id), now, rand.IntN)
			if err != nil {
				logger.Printf("BUTLER gather start aid=%s butler=%d result=217095 err=%v", aid, id, err)
				return []gardenMsg{{59, pbVar(nil, 1, 217095)}} // "Collecting currently unavailable."
			}
			logger.Printf("BUTLER gather start aid=%s butler=%d items=%d gems=%d result=0", aid, id, len(g.Items), g.Gems)
			return []gardenMsg{{59, butlerGatherBody(0, 3, butlerSeconds(g.End))}}
		case 4:
			part, err := economy.StopButlerGather(aid, int64(id), now)
			if err != nil {
				logger.Printf("BUTLER gather stop aid=%s butler=%d result=217095 err=%v", aid, id, err)
				return []gardenMsg{{59, pbVar(nil, 1, 217095)}}
			}
			logger.Printf("BUTLER gather stop aid=%s butler=%d items=%d gems=%d result=0", aid, id, len(part.Items), part.Gems)
			// The part that ran is paid silently (no popup exists for a stop); hc 28 refreshes the Gem HUD.
			msgs := []gardenMsg{{59, butlerGatherBody(0, 1, 0)}}
			if part.Gems > 0 {
				msgs = append(msgs, gardenMsg{28, pbVar(nil, 2, 0)})
			}
			return msgs
		}
		logger.Printf("BUTLER gather aid=%s butler=%d status=%d result=217095", aid, id, status)
		return []gardenMsg{{59, pbVar(nil, 1, 217095)}}
	case 54: // reward_info {butler_id, reward_type: 1 gathering}: the popup, only once the gathering is ready
		id, _ := protobufUintField(data, 1)
		rewardType, _ := protobufUintField(data, 2)
		g := economy.ButlerViewOf(aid, now).Gathering(int64(id))
		if b, known := economy.ButlerByID(int64(id)); known && rewardType == 1 && g != nil && economy.ButlerGatherDone(*g, now) {
			logger.Printf("BUTLER reward aid=%s butler=%d items=%d result=0", aid, id, len(g.Items))
			return []gardenMsg{{64, butlerRewardBody(b, *g)}}
		}
		logger.Printf("BUTLER reward refused aid=%s butler=%d type=%d", aid, id, rewardType)
	case 55: // reward {butler_id, reward_type, reward_sub_id}: the claim pays the ready gathering once
		id, _ := protobufUintField(data, 1)
		rewardType, _ := protobufUintField(data, 2)
		sub, _ := protobufUintField(data, 3)
		if rewardType == 1 {
			g, err := economy.ClaimButlerGather(aid, int64(id), int64(sub), now)
			if err == nil {
				logger.Printf("BUTLER claim aid=%s butler=%d items=%d gems=%d result=0", aid, id, len(g.Items), g.Gems)
				// hc 28 (quest call-npc reply) makes the client refetch coin/balance, as after a gather.
				return []gardenMsg{{65, butlerClaimBody()}, {28, pbVar(nil, 2, 0)}}
			}
			logger.Printf("BUTLER claim aid=%s butler=%d sub=%d err=%v", aid, id, sub, err)
		}
		return []gardenMsg{{65, pbVar(nil, 1, butlerRefused)}}
	case 51: // shop {}
		return []gardenMsg{{60, butlerShopBody(economy.ButlerViewOf(aid, now), now)}}
	case 57: // profile_info {butler_id}: the Profile popup shows the butler's info and abilities, and its employment if any
		id, _ := protobufUintField(data, 1)
		b, ok := economy.ButlerByID(int64(id))
		if !ok {
			break // refused below
		}
		v := economy.ButlerViewOf(aid, now)
		var hire *store.ButlerHire
		if h, ok := v.Hire(b.ID); ok {
			hire = &h
		}
		_, gathers := v.Today()
		body := pbVar(nil, 1, 0)
		body = pbLen(body, 2, butlerDetail(b, v.Worn(b), hire, v.Chief() == b.ID, gathers, v.Gathering(b.ID), now))
		return []gardenMsg{{67, body}}
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
		if b, worn, h, ok := economy.ButlerChief(aid, now); ok {
			msgs = append(msgs, gardenMsg{68, butlerChangeLeader(b, worn, h, now)})
		}
		return msgs
	case 62: // thumbnail_list {}
		v := economy.ButlerViewOf(aid, now)
		body := pbVar(nil, 1, 0)
		for _, b := range economy.ActiveButlers().Butlers {
			body = pbLen(body, 2, butlerThumbOf(b, v))
		}
		return []gardenMsg{{73, body}}
	case 59: // skin_equip {butler_id, skin_id}: wear an owned skin (hc 70)
		id, _ := protobufUintField(data, 1)
		skin, _ := protobufUintField(data, 2)
		if err := economy.EquipButlerSkin(aid, int64(id), int64(skin), now); err != nil {
			logger.Printf("BUTLER skin equip aid=%s butler=%d skin=%d result=%d err=%v", aid, id, skin, butlerRefused, err)
			return []gardenMsg{{70, pbVar(nil, 1, butlerRefused)}}
		}
		logger.Printf("BUTLER skin equip aid=%s butler=%d skin=%d result=0", aid, id, skin)
		b, _ := economy.ButlerByID(int64(id))
		return []gardenMsg{{70, butlerEquipBody(b, economy.ButlerViewOf(aid, now))}}
	case 60: // skin_shop {butler_id}: the butler's skins with their owned and worn state (hc 71)
		id, _ := protobufUintField(data, 1)
		if b, ok := economy.ButlerByID(int64(id)); ok {
			return []gardenMsg{{71, butlerSkinBody(b, economy.ButlerViewOf(aid, now))}}
		}
		// an unknown butler is refused below
	case 61: // skin_buy {butler_id, butler_skin_id, coin_info, is_free}: the server's Cash price (the request's coin is not read) (hc 72)
		id, _ := protobufUintField(data, 1)
		skin, _ := protobufUintField(data, 2)
		if err := economy.BuyButlerSkin(aid, int64(id), int64(skin), now); err != nil {
			logger.Printf("BUTLER skin buy aid=%s butler=%d skin=%d result=%d err=%v", aid, id, skin, butlerRefused, err)
			return []gardenMsg{{72, pbVar(nil, 1, butlerRefused)}}
		}
		logger.Printf("BUTLER skin buy aid=%s butler=%d skin=%d result=0", aid, id, skin)
		b, _ := economy.ButlerByID(int64(id))
		return []gardenMsg{{72, butlerSkinBody(b, economy.ButlerViewOf(aid, now))}}
	case 63: // voicebook {butler_id}: the butler's voice pack and its lines (hc 74)
		id, _ := protobufUintField(data, 1)
		if b, ok := economy.ButlerByID(int64(id)); ok {
			return []gardenMsg{{74, butlerVoiceBody(b, economy.ButlerViewOf(aid, now))}}
		}
		// an unknown butler is refused below
	case 69: // memory_list {butler_id}: the Memories popup's list, for any roster butler (hc 80)
		id, _ := protobufUintField(data, 1)
		if b, ok := economy.ButlerByID(int64(id)); ok {
			return []gardenMsg{{80, butlerMemoryList(b)}}
		}
		// an unknown butler is refused below
	case 70: // memory_open {butler_id, memory_id}: one memory's scenes, for any roster butler (hc 81)
		id, _ := protobufUintField(data, 1)
		mem, _ := protobufUintField(data, 2)
		if b, ok := economy.ButlerByID(int64(id)); ok {
			if m, ok := b.Memory(int64(mem)); ok {
				logger.Printf("BUTLER memory open aid=%s butler=%d memory=%d scenes=%d result=0", aid, id, mem, len(m.Scenes))
				return []gardenMsg{{81, butlerMemoryOpen(b, m, economy.ButlerViewOf(aid, now).Worn(b), now)}}
			}
		}
		// an unknown butler or memory is refused below
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

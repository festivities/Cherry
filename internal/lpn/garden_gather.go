package lpn

import (
	"log"
	"math"
	"math/rand/v2"
	"strconv"
	"time"

	"cherry/internal/economy"
)

// Garden gathering (Economy Phase DE): three touch spots in every Garden, the gather flow (begin 12
// -> hc 17, do 11 -> hc 16, end 13 -> hc 18 and the reward push hc 41) and the Bag (25, 26, 27, 28,
// 47 -> hc 29, 30, 31, 32, 56). Field numbers and the spot rules: %TEMP%\opencode\cherry\phaseDE-20261022\
// gather-craft-spec.md sections 1-4. Caps, rewards and the Bag are in economy/gathering.go.

// gardenSpot is one gathering spot: the NPC's touch-key serial, its sprite kind and action, and the
// host object it links to (map_object_id at interior_id, which is the host record's dword@0) on tile
// (x, y).
type gardenSpot struct {
	serial   uint64
	npcIndex uint64 // NpcAppearance.npc_index, the spot kind
	kind     economy.GatherKind
	action   uint64 // ActionInfo.action_type: 40 fish, 41 essence, 42 ore
	host     string // InteriorAppearance.map_object_id, the host item code
	hostIdx  uint64 // InteriorAppearance.interior_id, the host's dword@0 (the link key's high half)
	x, y     int    // host tile, game px through gardenPx
}

// gardenSpots is the one place that sets spot tiles and art. The client places its own copy of
// the host item (f5) at the spot tile (PlacedInterior 0x1eddd74) and links to that copy, never to
// a map object (spec 13), so spots sit on open ground near the spawn (30,39) with big map art;
// each spot needs its OWN host item: the client tells spots apart by it (two RUGO00B0F spots both
// began as ore). Cat tower = essence, moon fountain = ore. hostIdx (f2) has no effect.
var gardenSpots = []gardenSpot{
	{serial: 101, npcIndex: 0x302000B, kind: economy.GatherEssence, action: 41, host: "RUGO00B0F", hostIdx: 10, x: 27, y: 41},
	{serial: 102, npcIndex: 0x302000C, kind: economy.GatherOre, action: 42, host: "RUGO00A3N", hostIdx: 0, x: 38, y: 42},
	// The fish spot is never placed by the client (NaGardenInNpcFishAppearSpot::ParsingServerData
	// 0x1eddab0): it links to the map's own teapot fountain, dword@0 131 at tile (33,57) (spec 16).
	{serial: 103, npcIndex: 0x3020005, kind: economy.GatherFish, action: 40, host: "RUGO00B05", hostIdx: 131, x: 33, y: 57},
}

// gatherActionTimeout is GatheringInfo.action_timeout, the touch budget in seconds [?].
const gatherActionTimeout = 30

// gatherRefused is the result of a refused begin or end. Any non-zero result shows the client's
// SbMsg 10989 popup (spec 3.9); its text per code is not decoded.
const gatherRefused = 1

func gardenSpotBySerial(serial uint64) (gardenSpot, bool) {
	for _, s := range gardenSpots {
		if s.serial == serial {
			return s, true
		}
	}
	return gardenSpot{}, false
}

// gardenSpotNpc is the GardenNpc (MAP::NPCEnum field 3) of one spot. The fish spot also sends
// field 101 (its appear point, the game px); the fish linker and spot ids are unknown and stay 0 [I].
func gardenSpotNpc(s gardenSpot) []byte {
	px, py := gardenPx(s.x, s.y)
	interior := pbLen(nil, 1, pbVar(nil, 1, s.npcIndex)) // InteriorAppearance.apperance
	interior = pbVar(interior, 2, s.hostIdx)             // interior_id, the host index
	interior = pbVar(pbVar(interior, 3, 1), 4, 1)        // size_x, size_y [I]
	interior = pbLen(interior, 5, []byte(s.host))        // map_object_id, the host item code
	npc := pbVar(nil, 1, s.serial)                       // serial: the touch key
	npc = pbLen(npc, 2, interior)                        // appearance
	npc = pbLen(npc, 3, pbVar(pbVar(nil, 1, px), 2, py)) // position_info (MovePoint)
	npc = pbVar(npc, 6, 1)                               // is_visible
	if s.kind == economy.GatherFish {
		npc = pbLen(npc, 101, pbVar(pbVar(pbVar(nil, 2, s.hostIdx), 3, px), 4, py)) // f2: map host's dword@0
	}
	return npc
}

// gardenSpotList is MAP::NPCEnumList with one spot per gardenSpots entry. Room enter (f2) and relay
// start (f3) send the same list.
func gardenSpotList() []byte {
	var list []byte
	for _, s := range gardenSpots {
		list = pbLen(list, 1, pbLen(nil, 3, gardenSpotNpc(s)))
	}
	return list
}

// gardenGatherInfo is GAMEBASE::GatheringInfo for kind (spec 2). The regen countdown is the time to
// the next game day, since the caps reset then; f10 is that reset in epoch milliseconds.
func gardenGatherInfo(kind economy.GatherKind, lim economy.GatherLimit, now time.Time) []byte {
	reset := economy.QuestResetMillis(now)
	regen := uint64(reset/1000 - now.Unix())
	b := pbVar(nil, 1, uint64(kind))
	b = pbVar(b, 2, uint64(lim.Rest))
	b = pbVar(b, 3, uint64(lim.Max))
	// f4 is the cooldown the touch game waits out: CreateGameLayer 0x1f5042c starts only while it is
	// <= 0 and rest > 0, so it stays 0 while gathers are left (spec 14).
	var cooldown uint64
	if lim.Rest == 0 {
		cooldown = regen
	}
	b = pbVar(b, 4, cooldown)
	b = pbVar(b, 5, regen)
	b = pbVar(b, 6, gatherActionTimeout)
	b = pbVar(b, 7, squareBool(lim.Rest > 0)) // can_gather
	b = pbVar(b, 8, uint64(economy.ActiveGathering().FriendCap))
	b = pbVar(b, 9, uint64(economy.ActiveGathering().FriendGardens))
	return pbVar(b, 10, uint64(reset))
}

// gardenGatherRelay is what relay start adds for the viewer: npc_info (f3) and one GatheringInfo
// (f4) per gathering type, all with the viewer's limit in this Garden.
func gardenGatherRelay(viewer, owner uint64, now time.Time) []byte {
	lim := economy.GatherStatus(strconv.FormatUint(viewer, 10), strconv.FormatUint(owner, 10), now)
	b := pbLen(nil, 3, gardenSpotList())
	for _, kind := range []economy.GatherKind{economy.GatherFish, economy.GatherEssence, economy.GatherOre} {
		b = pbLen(b, 4, gardenGatherInfo(kind, lim, now))
	}
	return b
}

// gardenGather is one connection's touch state: the begin accepted last, until its end. touches
// counts the do requests since that begin.
type gardenGather struct {
	armed   bool
	spot    gardenSpot
	owner   uint64
	pick    economy.GatherPick
	action  []byte // the begin's ActionInfo, echoed by the replies
	touches int
}

// gardenMsg is one frame for the Garden tunnel.
type gardenMsg struct {
	id   uint16
	body []byte
}

// gatherEcho adds the begin's ActionInfo (f2) to a reply when the begin had one. The begin's bytes
// are what the client sent, so no field is rebuilt.
func gatherEcho(body, action []byte) []byte {
	if action == nil {
		return body
	}
	return pbLen(body, 2, action)
}

// gatherBegin answers ch 12. The key must be type 3 with a known spot serial whose action type is
// the spot's. The reward is rolled here and paid by a successful end. A refused begin (cap used up,
// unknown spot) arms nothing.
func gatherBegin(viewer, owner uint64, data []byte, g *gardenGather, now time.Time, logger *log.Logger) []gardenMsg {
	key, _, _ := pbBytesField(data, 1)
	typ, _ := protobufUintField(key, 1)
	serial, _ := protobufUintField(key, 2)
	info, _, _ := pbBytesField(data, 2)
	action, _, _ := pbBytesField(info, 3)
	actionType, _ := protobufUintField(action, 1)
	g.armed, g.touches = false, 0
	viewerAid, ownerAid := strconv.FormatUint(viewer, 10), strconv.FormatUint(owner, 10)
	spot, known := gardenSpotBySerial(serial)
	if typ != 3 || !known || spot.action != actionType {
		logger.Printf("GARDEN gather begin aid=%d owner=%d serial=%d result=%d", viewer, owner, serial, gatherRefused)
		return []gardenMsg{{17, gatherEcho(pbVar(nil, 1, gatherRefused), action)}}
	}
	lim := economy.GatherStatus(viewerAid, ownerAid, now)
	pick, ok := economy.PickGather(spot.kind, rand.IntN)
	if lim.Rest == 0 || !ok {
		logger.Printf("GARDEN gather begin aid=%d owner=%d kind=%d rest=%d result=%d", viewer, owner, spot.kind, lim.Rest, gatherRefused)
		body := gatherEcho(pbVar(nil, 1, gatherRefused), action)
		return []gardenMsg{{17, pbLen(body, 4, gardenGatherInfo(spot.kind, lim, now))}}
	}
	g.armed, g.spot, g.owner, g.pick, g.action = true, spot, owner, pick, action
	item := pbVar(nil, 1, uint64(pick.Drop.Item)) // GatheringItem.item_id
	item = pbVar(item, 2, gatherBeginGrade(spot.kind))
	item = pbVar(item, 3, uint64(pick.Length)) // length, cm (fish)
	body := gatherEcho(pbVar(nil, 1, 0), action)
	body = pbLen(body, 3, item)
	body = pbLen(body, 4, gardenGatherInfo(spot.kind, lim, now))
	logger.Printf("GARDEN gather begin aid=%d owner=%d kind=%d item=%d result=0", viewer, owner, spot.kind, pick.Drop.Item)
	return []gardenMsg{{17, body}}
}

// gatherDo answers ch 11, one per touch. The server counts touches only; the client decides
// success and sends end.
func gatherDo(viewer uint64, data []byte, g *gardenGather, logger *log.Logger) []gardenMsg {
	key, _, _ := pbBytesField(data, 1)
	typ, _ := protobufUintField(key, 1)
	serial, _ := protobufUintField(key, 2)
	result := uint64(0)
	if !g.armed || typ != 3 || serial != g.spot.serial {
		result = gatherRefused
	} else {
		g.touches++
	}
	logger.Printf("GARDEN gather do aid=%d serial=%d armed=%v touches=%d result=%d", viewer, serial, g.armed, g.touches, result)
	body := gatherEcho(pbVar(nil, 1, result), g.action)
	body = pbVar(body, 5, serial) // serial
	body = pbVar(body, 6, 1)      // is_visible
	return []gardenMsg{{16, body}}
}

// gatherTouchesToWin is how many do requests a successful grade-1 game sends (spec 15-17): essence
// one per stage (2 stages), ore 3 taps per stage, fish one per HIT (grade 0: one hit). Fewer means a
// miss or a timeout. ponytail: fixed per kind; derive from the grade if begin ever varies it.
var gatherTouchesToWin = map[economy.GatherKind]int{economy.GatherEssence: 2, economy.GatherOre: 6, economy.GatherFish: 1}

// gatherBeginGrade is GatheringItem.grade in the begin reply, which sets the game length. Fish get
// grade 0 = one HIT (GetMaxHitCount 0x1f50ca0; grade 1 would need two): later taps in the 3 s window
// after a HIT end the cast as a miss, so two HITs are hard to land (device 2026-10-22).
func gatherBeginGrade(kind economy.GatherKind) uint64 {
	if kind == economy.GatherFish {
		return 0
	}
	return 1
}

// gatherEnd answers ch 13, which has no key: the armed begin names the spot. The stock client sends
// end on success, miss and timeout with the same body, so the server pays only an end with the full
// winning touch count behind it (gatherTouchesToWin).
// A paid end sends the reward push (hc 41) after the reply, then coin refresh (hc 28): the Gems
// show without a scene change.
func gatherEnd(viewer uint64, g *gardenGather, now time.Time, logger *log.Logger) []gardenMsg {
	armed, touches := g.armed, g.touches
	spot, pick, owner, action := g.spot, g.pick, g.owner, g.action
	g.armed, g.touches = false, 0
	if !armed || touches < gatherTouchesToWin[spot.kind] {
		logger.Printf("GARDEN gather end aid=%d result=%d", viewer, gatherRefused)
		return []gardenMsg{{18, pbVar(nil, 1, gatherRefused)}}
	}
	lim, gems, ok := economy.GatherCredit(strconv.FormatUint(viewer, 10), strconv.FormatUint(owner, 10), spot.kind, pick.Drop.Item, now)
	if !ok {
		logger.Printf("GARDEN gather end aid=%d kind=%d result=%d", viewer, spot.kind, gatherRefused)
		body := pbVar(nil, 1, gatherRefused)
		return []gardenMsg{{18, pbLen(body, 10, gardenGatherInfo(spot.kind, lim, now))}}
	}
	// gather3 (event quest) completes on the third successful gather of the game day; later gathers
	// retry it, and MarkQuest does nothing once it is credited.
	if economy.GatherCount(strconv.FormatUint(viewer, 10), now) >= 3 {
		MarkQuest(viewer, "gather3")
	}
	body := gatherEcho(pbVar(nil, 1, 0), action)
	body = pbLen(body, 10, gardenGatherInfo(spot.kind, lim, now))
	reward := pbVar(nil, 1, uint64(pick.Drop.Item)) // GatheringRewardItem.item_id
	reward = pbVar(reward, 2, 1)                    // grade
	reward = pbVar(reward, 3, uint64(pick.Length))  // length (fish)
	reward = pbLen(reward, 4, []byte(economy.GatherImageURL(pick.Drop.Item)))
	reward = pbLen(reward, 7, []byte(pick.Drop.Name))
	reward = pbVar(reward, 10, 1) // count
	push := pbVar(nil, 1, uint64(spot.kind))
	push = pbLen(push, 2, reward)
	logger.Printf("GARDEN gather end aid=%d owner=%d kind=%d item=%d gems=%d result=0", viewer, owner, spot.kind, pick.Drop.Item, gems)
	return []gardenMsg{{18, body}, {41, push}, {28, pbVar(nil, 2, 0)}}
}

// gardenBagHolds reports whether a bag type lists the materials: 0 (the first request) and 1 (the
// Atelier bag) list the same stacks; any other type is empty.
func gardenBagHolds(bagtype uint64) bool { return bagtype == 0 || bagtype == 1 }

// gardenBagInfo is BagInfo {bagtype, slot_size}.
func gardenBagInfo(bagtype uint64) []byte {
	return pbVar(pbVar(nil, 1, bagtype), 2, uint64(economy.ActiveGathering().BagSlots))
}

// gardenAssetItem is the AssetItem of one material: a name, grade 1, a non-empty code and picture
// and a detail line. Every item id a reply lists needs one (spec 4.1).
func gardenAssetItem(d economy.GatherDrop) []byte {
	a := pbVar(nil, 1, uint64(d.Item))                            // itemid
	a = pbVar(a, 2, uint64(d.Item/100000))                        // itemtype = filter tab: 1 fish (balloon), 2 essence (tree), 3 ore (crystal)
	a = pbLen(a, 3, []byte(d.Name))                               // itemname
	a = pbVar(a, 4, 1)                                            // grade
	a = pbLen(a, 5, []byte("item"+strconv.FormatInt(d.Item, 10))) // item_code
	a = pbLen(a, 6, []byte(economy.GatherCraftImage(d.Item)))     // img_url: local craft path (economy.GatherCraftImage)
	detail := "Gathered in a Garden."
	if _, ok := economy.ActiveAtelier().Good(d.Item); ok {
		detail = "Made in the Atelier."
	}
	a = pbLen(a, 7, []byte(detail))  // detail_msg
	noDate := uint64(math.MaxUint64) // int64 -1: no validity dates (0 would show 1970)
	return pbVar(pbVar(a, 9, noDate), 10, noDate)
}

// gardenBagReply answers the Bag requests: 25 iteminfo, 26 baginfo, 27 bagitem, 28 bagslot extend
// (free) and 47 item delete. The Smile balance rides on 26 and 28.
func gardenBagReply(msgid uint16, viewer uint64, data []byte, logger *log.Logger) []gardenMsg {
	aid := strconv.FormatUint(viewer, 10)
	switch msgid {
	case 25: // iteminfo {itemid...} -> one AssetItem per known material
		var body []byte
		seen := map[uint64]bool{}
		pbScan(data, func(f, v uint64, d []byte) {
			if f != 1 || d != nil || seen[v] {
				return
			}
			seen[v] = true
			if drop, ok := economy.GatherMaterialByID(int64(v)); ok {
				body = pbLen(body, 1, gardenAssetItem(drop))
			}
		})
		return []gardenMsg{{29, body}}
	case 26: // baginfo {bagtype} -> BagInfo, smile_point
		bagtype, _ := protobufUintField(data, 1)
		_, smiles := economy.GardenBag(aid)
		// The Bag lists Materials only if a BagInfo with bagtype 1 exists (2 = Lucky Powder), whatever
		// was requested (NaCraftBag::SetCategory 0x21a8364, spec 18).
		_ = bagtype
		body := pbLen(pbLen(nil, 1, gardenBagInfo(1)), 1, gardenBagInfo(2))
		return []gardenMsg{{30, pbVar(body, 2, uint64(smiles))}}
	case 27: // bagitem {bagtype} -> one InvenItem per stack (serial = item id), one AssetItem per item
		bagtype, _ := protobufUintField(data, 1)
		body := pbVar(nil, 1, 1) // the Materials bag; the client ignores this field
		if gardenBagHolds(bagtype) {
			stacks, _ := economy.GardenBag(aid)
			for _, s := range stacks {
				inven := pbVar(nil, 1, uint64(s.Item))
				inven = pbVar(inven, 2, uint64(s.Item))
				inven = pbVar(inven, 3, uint64(s.Updated))
				inven = pbVar(inven, 4, uint64(s.Count))
				body = pbLen(body, 2, inven)
				if d, ok := economy.GatherMaterialByID(s.Item); ok {
					body = pbLen(body, 3, gardenAssetItem(d))
				}
			}
		}
		logger.Printf("GARDEN bag items aid=%s bagtype=%d bytes=%d", aid, bagtype, len(body))
		return []gardenMsg{{31, body}}
	case 28: // bagslot_extend {bagtype, coin_type} -> ok, the same BagInfo, no charge
		bagtype, _ := protobufUintField(data, 1)
		coin, _ := protobufUintField(data, 2)
		_, smiles := economy.GardenBag(aid)
		body := pbVar(nil, 1, bagtype)
		body = pbVar(body, 2, 0)
		body = pbLen(body, 3, gardenBagInfo(bagtype))
		body = pbVar(body, 4, coin)
		body = pbVar(body, 5, 0)
		return []gardenMsg{{32, pbVar(body, 6, uint64(smiles))}}
	case 47: // item_delete {bagtype, serial, itemid, count} -> result, serial
		serial, _ := protobufUintField(data, 2)
		result := uint64(0)
		if !economy.GardenBagDelete(aid, int64(serial)) {
			result = gatherRefused
		}
		return []gardenMsg{{56, pbVar(pbVar(nil, 1, result), 2, serial)}}
	}
	return nil
}

// gardenGatherMsg reports whether msgid is a touch, Bag or Atelier request answered here.
func gardenGatherMsg(msgid uint16) bool {
	switch msgid {
	case 11, 12, 13, 25, 26, 27, 28, 47:
		return true
	}
	return gardenCraftMsg(msgid)
}

// gardenGatherReply answers one request and returns the frames to send, in order.
func gardenGatherReply(msgid uint16, viewer, owner uint64, data []byte, g *gardenGather, now time.Time, logger *log.Logger) []gardenMsg {
	switch msgid {
	case 11:
		return gatherDo(viewer, data, g, logger)
	case 12:
		return gatherBegin(viewer, owner, data, g, now, logger)
	case 13:
		return gatherEnd(viewer, g, now, logger)
	}
	if gardenCraftMsg(msgid) {
		return gardenCraftReply(msgid, viewer, data, now, logger)
	}
	return gardenBagReply(msgid, viewer, data, logger)
}

// gardenGatherHandle answers one touch or Bag request on the relayed Garden tunnel.
func gardenGatherHandle(conn *lockedConn, frame []byte, msgid uint16, viewer, owner uint64, g *gardenGather, logger *log.Logger) error {
	for _, m := range gardenGatherReply(msgid, viewer, owner, frame[12:], g, economy.NowFunc(), logger) {
		if _, err := conn.Write(squareFrame(frame[1], frame[2:10], m.id, m.body)); err != nil {
			return err
		}
	}
	return nil
}

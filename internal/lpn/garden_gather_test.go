package lpn

import (
	"io"
	"log"
	"math"
	"testing"
	"time"

	"cherry/internal/economy"
	"cherry/internal/store"
	"cherry/internal/testutil"
)

// gatherTouch builds one touch request body: key#1 {type, serial} and, when action is nonzero,
// interaction_info#2 {action_info#3 {action_type#1}}. Begin and do carry the key; end does not.
func gatherTouch(typ, serial, action uint64, withKey bool) []byte {
	var data []byte
	if withKey {
		key := pbVar(pbVar(nil, 1, typ), 2, serial)
		data = pbLen(nil, 1, key)
	}
	if action != 0 {
		info := pbLen(nil, 3, pbVar(nil, 1, action))
		data = pbLen(data, 2, info)
	}
	return data
}

// gatherAccount installs an own-Garden account for the touch tests (no store path, so no disk).
func gatherAccount(t *testing.T, aid string, gather *store.GatherDay) {
	t.Helper()
	testutil.InstallAccounts(t, map[string]*store.Account{"gl-" + aid: {AccessToken: "gl-" + aid, SessionKey: "sk", Aid: aid, Name: "Gather", Gather: gather}})
	testutil.SetLab(t, aid, false)
}

func gatherGems(t *testing.T, aid string) (gems, smiles int64, bagStacks int) {
	t.Helper()
	store.AccountsMu.Lock()
	defer store.AccountsMu.Unlock()
	acc := store.AccountByAidLocked(aid)
	return acc.Gems, acc.Smiles, len(acc.GardenBag)
}

// TestGardenSpotNpcWireBytes checks the spot NPC entry (spec 1.2): NPCEnum field 3 holds a GardenNpc
// with the serial, the appearance (npc_index, interior_id, size, map_object_id), the tile in game px
// and is_visible. Only the fish spot carries field 101.
func TestGardenSpotNpcWireBytes(t *testing.T) {
	for _, s := range gardenSpots {
		entry := pbFieldsOf(t, gardenSpotNpc(s))
		px, py := gardenPx(s.x, s.y)
		if entry.vars[1] != s.serial || entry.vars[6] != 1 {
			t.Fatalf("spot %d serial/is_visible = %v", s.serial, entry.vars)
		}
		app := pbFieldsOf(t, entry.msgs[2][0])
		if app.vars[2] != s.hostIdx || app.vars[3] != 1 || app.vars[4] != 1 || string(app.msgs[5][0]) != s.host {
			t.Fatalf("spot %d appearance = %v host %q", s.serial, app.vars, app.msgs[5])
		}
		if npc := pbFieldsOf(t, app.msgs[1][0]); npc.vars[1] != s.npcIndex {
			t.Fatalf("spot %d npc_index = %d, want %d", s.serial, npc.vars[1], s.npcIndex)
		}
		pos := pbFieldsOf(t, entry.msgs[3][0])
		if pos.vars[1] != px || pos.vars[2] != py {
			t.Fatalf("spot %d position = %v, want %d,%d", s.serial, pos.vars, px, py)
		}
		_, hasFish := entry.msgs[101]
		if hasFish != (s.kind == economy.GatherFish) {
			t.Fatalf("spot %d field 101 present=%v", s.serial, hasFish)
		}
		if hasFish {
			fish := pbFieldsOf(t, entry.msgs[101][0])
			if fish.vars[3] != px || fish.vars[4] != py || fish.vars[2] != s.hostIdx || fish.vars[2] == 0 {
				t.Fatalf("fish appear point = %v", fish.vars)
			}
		}
	}

	// Room enter (f2) and relay start (f3) both carry this list: one NPCEnum per spot.
	list := pbFieldsOf(t, gardenSpotList())
	if len(list.msgs[1]) != len(gardenSpots) {
		t.Fatalf("npc list entries = %d, want %d", len(list.msgs[1]), len(gardenSpots))
	}
	for i, enum := range list.msgs[1] {
		if got := pbFieldsOf(t, enum).msgs[3]; len(got) != 1 || pbFieldsOf(t, got[0]).vars[1] != gardenSpots[i].serial {
			t.Fatalf("entry %d is not spot %d", i, gardenSpots[i].serial)
		}
	}
}

// TestGardenSpotTableShape checks the spot table: unique nonzero serials, actions that match their
// kinds, and distinct tiles on the 60x60 map off the owner (30,39) and visitor (34,36) spawns.
func TestGardenSpotTableShape(t *testing.T) {
	seen := map[uint64]bool{}
	tiles := map[[2]int]bool{{30, 39}: true, {34, 36}: true}
	wantAction := map[economy.GatherKind]uint64{economy.GatherFish: 40, economy.GatherEssence: 41, economy.GatherOre: 42}
	for _, s := range gardenSpots {
		if s.serial == 0 || seen[s.serial] || s.action != wantAction[s.kind] || s.host == "" {
			t.Fatalf("spot %+v breaks the table rules", s)
		}
		seen[s.serial] = true
		tile := [2]int{s.x, s.y}
		if s.x < 0 || s.x > 59 || s.y < 0 || s.y > 59 || tiles[tile] {
			t.Fatalf("spot %+v tile off the map, on a spawn or shared", s)
		}
		tiles[tile] = true
	}
}

// TestGatherBeginDoEndCredits runs one successful gather through the three touch requests and checks
// every reply: begin (hc 17) with the pre-rolled item, do (hc 16), end (hc 18) with the updated
// GatheringInfo, the reward push (hc 41) and the coin refresh (hc 28). The Bag, Smiles and Gems are
// credited once.
func TestGatherBeginDoEndCredits(t *testing.T) {
	gatherAccount(t, "9921", nil)
	now := time.Date(2026, time.October, 11, 12, 0, 0, 0, time.UTC)
	logger := quietLogger()
	g := &gardenGather{}

	msgs := gardenGatherReply(12, 9921, 9921, gatherTouch(3, 101, 41, true), g, now, logger)
	if len(msgs) != 1 || msgs[0].id != 17 {
		t.Fatalf("begin replies = %+v", msgs)
	}
	begin := pbFieldsOf(t, msgs[0].body)
	item := pbFieldsOf(t, begin.msgs[3][0])
	if begin.vars[1] != 0 || item.vars[2] != 1 || item.vars[3] != 0 {
		t.Fatalf("begin = %v item %v", begin.vars, item.vars)
	}
	if _, ok := economy.GatherMaterialByID(int64(item.vars[1])); !ok {
		t.Fatalf("pre-rolled item %d is not a material", item.vars[1])
	}
	info := pbFieldsOf(t, begin.msgs[4][0])
	if info.vars[1] != uint64(economy.GatherEssence) || info.vars[2] != economy.GatherOwnCap || info.vars[7] != 1 {
		t.Fatalf("begin info = %v", info.vars)
	}
	if !g.armed || g.spot.serial != 101 {
		t.Fatalf("begin did not arm the spot: %+v", g)
	}

	msgs = gardenGatherReply(11, 9921, 9921, gatherTouch(3, 101, 41, true), g, now, logger)
	do := pbFieldsOf(t, msgs[0].body)
	if len(msgs) != 1 || msgs[0].id != 16 || do.vars[1] != 0 || do.vars[5] != 101 || do.vars[6] != 1 || g.touches != 1 {
		t.Fatalf("do = %+v vars %v touches %d", msgs, do.vars, g.touches)
	}

	gardenGatherReply(11, 9921, 9921, gatherTouch(3, 101, 41, true), g, now, logger) // essence wins on its 2nd stage
	msgs = gardenGatherReply(13, 9921, 9921, gatherTouch(0, 0, 41, false), g, now, logger)
	if len(msgs) != 3 || msgs[0].id != 18 || msgs[1].id != 41 || msgs[2].id != 28 {
		t.Fatalf("end replies = %+v", msgs)
	}
	end := pbFieldsOf(t, msgs[0].body)
	endInfo := pbFieldsOf(t, end.msgs[10][0])
	if end.vars[1] != 0 || endInfo.vars[2] != economy.GatherOwnCap-1 {
		t.Fatalf("end = %v info %v", end.vars, endInfo.vars)
	}
	push := pbFieldsOf(t, msgs[1].body)
	reward := pbFieldsOf(t, push.msgs[2][0])
	if push.vars[1] != uint64(economy.GatherEssence) || reward.vars[1] != item.vars[1] || reward.vars[2] != 1 || reward.vars[10] != 1 {
		t.Fatalf("reward push = %v reward %v", push.vars, reward.vars)
	}
	if len(reward.msgs[7]) != 1 || len(reward.msgs[4]) != 1 {
		t.Fatalf("reward name/img = %v", reward.msgs)
	}
	if v := pbFieldsOf(t, msgs[2].body).vars[2]; v != 0 {
		t.Fatalf("coin refresh error code = %d", v)
	}

	gems, smiles, stacks := gatherGems(t, "9921")
	if gems != 2 || smiles != economy.GatherSmiles || stacks != 1 {
		t.Fatalf("after credit: gems %d smiles %d stacks %d", gems, smiles, stacks)
	}
	// The pickup is armed no more: a second end pays nothing.
	msgs = gardenGatherReply(13, 9921, 9921, nil, g, now, logger)
	if len(msgs) != 1 || pbFieldsOf(t, msgs[0].body).vars[1] != gatherRefused {
		t.Fatalf("second end = %+v", msgs)
	}
	if gems, smiles, _ := gatherGems(t, "9921"); gems != 2 || smiles != economy.GatherSmiles {
		t.Fatalf("second end paid: gems %d smiles %d", gems, smiles)
	}
}

// TestGatherEndWithoutTouchPaysNothing checks that a begin followed by an end with no do request
// pays nothing, and that a begin with no spot armed refuses its end.
func TestGatherEndWithoutTouchPaysNothing(t *testing.T) {
	gatherAccount(t, "9922", nil)
	now := time.Date(2026, time.October, 11, 12, 0, 0, 0, time.UTC)
	logger := quietLogger()
	g := &gardenGather{}
	gardenGatherReply(12, 9922, 9922, gatherTouch(3, 103, 40, true), g, now, logger)
	msgs := gardenGatherReply(13, 9922, 9922, gatherTouch(0, 0, 40, false), g, now, logger)
	if len(msgs) != 1 || msgs[0].id != 18 || pbFieldsOf(t, msgs[0].body).vars[1] != gatherRefused {
		t.Fatalf("touchless end = %+v", msgs)
	}
	if gems, smiles, stacks := gatherGems(t, "9922"); gems != 0 || smiles != 0 || stacks != 0 {
		t.Fatalf("touchless end paid: gems %d smiles %d stacks %d", gems, smiles, stacks)
	}
}

// TestGatherBeginRefusals checks the refusals: a bad key type, an unknown serial, an action that
// does not match the spot, and a Garden already at its cap. None of them arms a spot, and a refused
// begin's reply carries no item.
func TestGatherBeginRefusals(t *testing.T) {
	now := time.Date(2026, time.October, 11, 12, 0, 0, 0, time.UTC)
	logger := quietLogger()
	cases := []struct {
		name   string
		gather *store.GatherDay
		data   []byte
		info   bool // the refused reply carries the GatheringInfo
	}{
		{"key type 1", nil, gatherTouch(1, 101, 41, true), false},
		{"unknown serial", nil, gatherTouch(3, 999, 41, true), false},
		{"action mismatch", nil, gatherTouch(3, 101, 42, true), false},
		{"own cap used", &store.GatherDay{Day: economy.AttendDayKey(now), Own: economy.GatherOwnCap}, gatherTouch(3, 101, 41, true), true},
		{"three friend gardens used", &store.GatherDay{Day: economy.AttendDayKey(now), Gardens: map[string]int64{"7001": 5, "7002": 5, "7003": 5}}, gatherTouch(3, 102, 42, true), true},
	}
	for _, tc := range cases {
		gatherAccount(t, "9923", tc.gather)
		g := &gardenGather{}
		owner := uint64(9923)
		if tc.name == "three friend gardens used" {
			owner = 7004
		}
		msgs := gardenGatherReply(12, 9923, owner, tc.data, g, now, logger)
		if len(msgs) != 1 || msgs[0].id != 17 || g.armed {
			t.Fatalf("%s: begin = %+v armed %v", tc.name, msgs, g.armed)
		}
		body := pbFieldsOf(t, msgs[0].body)
		if body.vars[1] != gatherRefused || len(body.msgs[3]) != 0 {
			t.Fatalf("%s: refused begin = %v with item %d", tc.name, body.vars, len(body.msgs[3]))
		}
		if tc.info {
			info := pbFieldsOf(t, body.msgs[4][0])
			if info.vars[2] != 0 || info.vars[7] != 0 {
				t.Fatalf("%s: info = %v, want rest 0 and can_gather false", tc.name, info.vars)
			}
		}
		// A do after a refused begin fails, and the end pays nothing.
		if do := pbFieldsOf(t, gardenGatherReply(11, 9923, owner, gatherTouch(3, 101, 41, true), g, now, logger)[0].body); do.vars[1] != gatherRefused {
			t.Fatalf("%s: do after refusal = %v", tc.name, do.vars)
		}
		if gems, smiles, stacks := gatherGems(t, "9923"); gems != 0 || smiles != 0 || stacks != 0 {
			t.Fatalf("%s: refused begin paid gems %d smiles %d stacks %d", tc.name, gems, smiles, stacks)
		}
	}
}

// TestGatherCapStopsAtBeginAndEnd checks that two sessions begun with one gather left cannot both
// pay: the cap is checked again at the end, so the second end pays nothing.
func TestGatherCapStopsAtBeginAndEnd(t *testing.T) {
	now := time.Date(2026, time.October, 11, 12, 0, 0, 0, time.UTC)
	logger := quietLogger()
	gatherAccount(t, "9924", &store.GatherDay{Day: economy.AttendDayKey(now), Own: economy.GatherOwnCap - 1})
	armed := &gardenGather{}
	gardenGatherReply(12, 9924, 9924, gatherTouch(3, 102, 42, true), armed, now, logger) // one left: begins
	other := &gardenGather{}
	gardenGatherReply(12, 9924, 9924, gatherTouch(3, 102, 42, true), other, now, logger)
	for range gatherTouchesToWin[economy.GatherOre] {
		gardenGatherReply(11, 9924, 9924, gatherTouch(3, 102, 42, true), armed, now, logger)
		gardenGatherReply(11, 9924, 9924, gatherTouch(3, 102, 42, true), other, now, logger)
	}
	if msgs := gardenGatherReply(13, 9924, 9924, nil, armed, now, logger); len(msgs) != 3 {
		t.Fatalf("the last gather = %+v, want a paid end", msgs)
	}
	if msgs := gardenGatherReply(13, 9924, 9924, nil, other, now, logger); len(msgs) != 1 || pbFieldsOf(t, msgs[0].body).vars[1] != gatherRefused {
		t.Fatalf("second session past the cap = %+v", msgs)
	}
	if gems, smiles, stacks := gatherGems(t, "9924"); gems != 2 || smiles != economy.GatherSmiles || stacks != 1 {
		t.Fatalf("after two sessions: gems %d smiles %d stacks %d", gems, smiles, stacks)
	}
}

// TestGardenBagReplies checks the Bag replies: bagtype 0 and 1 list the same stacks with one
// InvenItem and one AssetItem per item, other bag types are empty, the Smile balance rides on the
// bag info, the slot extend is free and item delete removes a stack.
func TestGardenBagReplies(t *testing.T) {
	gatherAccount(t, "9925", nil)
	store.AccountsMu.Lock()
	acc := store.AccountByAidLocked("9925")
	acc.Smiles = 9
	acc.Gems = 40
	acc.GardenBag = []store.GardenBagStack{{Item: 210000, Count: 2, Updated: 111}, {Item: 110000, Count: 1, Updated: 222}}
	store.AccountsMu.Unlock()
	const aid = 9925

	// hc 30: BagInfo for bagtype 1 (Materials) and 2 (Lucky Powder) whatever was requested (the
	// stock client asks for 0, and without bagtype 1 the Bag shows Empty), and the Smile balance.
	msgs := gardenBagReply(26, aid, pbVar(nil, 1, 0), log.New(io.Discard, "", 0))
	info := pbFieldsOf(t, msgs[0].body)
	if msgs[0].id != 30 || len(info.msgs[1]) != 2 || info.vars[2] != 9 {
		t.Fatalf("baginfo = %+v", msgs)
	}
	for i, raw := range info.msgs[1] {
		if bag := pbFieldsOf(t, raw); bag.vars[1] != uint64(i+1) || bag.vars[2] != economy.GatherBagSlots {
			t.Fatalf("baginfo %d = %v", i, bag.vars)
		}
	}

	// hc 31: bagitem lists both stacks with one AssetItem each.
	for _, bagtype := range []uint64{0, 1} {
		msgs = gardenBagReply(27, aid, pbVar(nil, 1, bagtype), log.New(io.Discard, "", 0))
		list := pbFieldsOf(t, msgs[0].body)
		if msgs[0].id != 31 || list.vars[1] != 1 || len(list.msgs[2]) != 2 || len(list.msgs[3]) != 2 {
			t.Fatalf("bagitem %d = id %d vars %v inven %d assets %d", bagtype, msgs[0].id, list.vars, len(list.msgs[2]), len(list.msgs[3]))
		}
		for i, raw := range list.msgs[2] {
			inven := pbFieldsOf(t, raw)
			asset := pbFieldsOf(t, list.msgs[3][i])
			if inven.vars[1] != inven.vars[2] || inven.vars[1] != asset.vars[1] || inven.vars[3] == 0 ||
				asset.vars[2] != asset.vars[1]/100000 || asset.vars[9] != math.MaxUint64 || asset.vars[10] != math.MaxUint64 {
				t.Fatalf("row %d inven %v asset %v", i, inven.vars, asset.vars)
			}
			// name (3), item_code (5) and img_url (6) are each present and non-empty.
			for _, f := range []uint64{3, 5, 6} {
				if len(asset.msgs[f]) != 1 || len(asset.msgs[f][0]) == 0 {
					t.Fatalf("row %d asset field %d missing: %v", i, f, asset.msgs[f])
				}
			}
		}
	}
	if list := pbFieldsOf(t, gardenBagReply(27, aid, pbVar(nil, 1, 2), log.New(io.Discard, "", 0))[0].body); len(list.msgs[2]) != 0 || len(list.msgs[3]) != 0 {
		t.Fatalf("bagtype 2 is not empty: %v", list.msgs)
	}

	// hc 29: iteminfo answers one AssetItem per distinct known id.
	req := pbVar(pbVar(pbVar(pbVar(nil, 1, 210000), 1, 210000), 1, 999999), 1, 110000)
	assets := pbFieldsOf(t, gardenBagReply(25, aid, req, log.New(io.Discard, "", 0))[0].body)
	if len(assets.msgs[1]) != 2 {
		t.Fatalf("iteminfo assets = %d, want 2 (dedup, unknown dropped)", len(assets.msgs[1]))
	}

	// hc 32: slot extend is free and keeps the balance.
	msgs = gardenBagReply(28, aid, pbVar(pbVar(nil, 1, 1), 2, 2), log.New(io.Discard, "", 0))
	ext := pbFieldsOf(t, msgs[0].body)
	if msgs[0].id != 32 || ext.vars[2] != 0 || ext.vars[6] != 9 || ext.vars[4] != 2 {
		t.Fatalf("extend = %+v", msgs)
	}
	if gems, _, _ := gatherGems(t, "9925"); gems != 40 {
		t.Fatalf("extend charged: gems %d", gems)
	}

	// hc 56: delete removes the whole stack; a second delete is refused.
	del := pbVar(pbVar(pbVar(pbVar(nil, 1, 1), 2, 210000), 3, 210000), 4, 2)
	msgs = gardenBagReply(47, aid, del, log.New(io.Discard, "", 0))
	if msgs[0].id != 56 || pbFieldsOf(t, msgs[0].body).vars[1] != 0 {
		t.Fatalf("delete = %+v", msgs)
	}
	if msgs = gardenBagReply(47, aid, del, log.New(io.Discard, "", 0)); pbFieldsOf(t, msgs[0].body).vars[1] != gatherRefused {
		t.Fatalf("second delete = %+v", msgs)
	}
	if _, _, stacks := gatherGems(t, "9925"); stacks != 1 {
		t.Fatalf("stacks after delete = %d, want 1", stacks)
	}
}

// TestGardenRelayGatherFields checks what relay start adds for the viewer: the same spot list as
// npc_info (f3) and one GatheringInfo per gathering type (f4), fish, essence, ore, at the viewer's
// remaining limit.
func TestGardenRelayGatherFields(t *testing.T) {
	gatherAccount(t, "9926", &store.GatherDay{Day: economy.AttendDayKey(time.Date(2026, time.October, 11, 12, 0, 0, 0, time.UTC)), Own: 3})
	now := time.Date(2026, time.October, 11, 12, 0, 0, 0, time.UTC)
	fields := pbFieldsOf(t, gardenGatherRelay(9926, 9926, now))
	if len(fields.msgs[3]) != 1 || len(pbFieldsOf(t, fields.msgs[3][0]).msgs[1]) != len(gardenSpots) {
		t.Fatalf("npc_info = %v", fields.msgs)
	}
	if len(fields.msgs[4]) != 3 {
		t.Fatalf("gathering infos = %d, want 3", len(fields.msgs[4]))
	}
	for i, want := range []uint64{1, 2, 3} {
		info := pbFieldsOf(t, fields.msgs[4][i])
		if info.vars[1] != want || info.vars[2] != economy.GatherOwnCap-3 || info.vars[3] != economy.GatherOwnCap || info.vars[7] != 1 || info.vars[10] == 0 {
			t.Fatalf("info %d = %v", want, info.vars)
		}
	}
}

// pbFieldsOf splits one message for the tests (varints in vars, length-delimited in msgs; both may
// repeat, so msgs holds every occurrence in order).
type pbMsg struct {
	vars map[uint64]uint64
	msgs map[uint64][][]byte
}

func pbFieldsOf(t *testing.T, data []byte) pbMsg {
	t.Helper()
	vars, msgs := pbFields(t, data)
	return pbMsg{vars: vars, msgs: msgs}
}

// TestGatherInfoCooldownOnlyWhenCapped: the client starts a touch game only while f4 is 0
// (CreateGameLayer 0x1f5042c), so f4 carries the wait to the reset only once nothing is left.
func TestGatherInfoCooldownOnlyWhenCapped(t *testing.T) {
	now := time.Date(2026, 10, 22, 6, 0, 0, 0, time.UTC)
	open := pbFieldsOf(t, gardenGatherInfo(economy.GatherEssence, economy.GatherLimit{Rest: 3, Max: 20}, now))
	if open.vars[4] != 0 || open.vars[5] == 0 || open.vars[2] != 3 {
		t.Fatalf("open spot info = %v, want f4 0, f5 > 0, f2 3", open.vars)
	}
	capped := pbFieldsOf(t, gardenGatherInfo(economy.GatherEssence, economy.GatherLimit{Max: 20}, now))
	if capped.vars[4] == 0 || capped.vars[7] != 0 {
		t.Fatalf("capped spot info = %v, want f4 > 0 and can_gather 0", capped.vars)
	}
}

// TestGatherEndNeedsTheWinningTouches: the client sends the same end on a miss or a timeout, so an
// end one touch short of the win pays nothing.
func TestGatherEndNeedsTheWinningTouches(t *testing.T) {
	gatherAccount(t, "9925", nil)
	now := time.Date(2026, time.October, 11, 12, 0, 0, 0, time.UTC)
	logger := quietLogger()
	g := &gardenGather{}
	gardenGatherReply(12, 9925, 9925, gatherTouch(3, 102, 42, true), g, now, logger)
	for range gatherTouchesToWin[economy.GatherOre] - 1 {
		gardenGatherReply(11, 9925, 9925, gatherTouch(3, 102, 42, true), g, now, logger)
	}
	if msgs := gardenGatherReply(13, 9925, 9925, nil, g, now, logger); len(msgs) != 1 || pbFieldsOf(t, msgs[0].body).vars[1] != gatherRefused {
		t.Fatalf("short end = %+v, want a refusal", msgs)
	}
	if gems, smiles, stacks := gatherGems(t, "9925"); gems != 0 || smiles != 0 || stacks != 0 {
		t.Fatalf("short end paid gems %d smiles %d stacks %d", gems, smiles, stacks)
	}
}

// TestGatherFishIsOneHit: a fish begins at grade 0 (one HIT) and pays on that single touch.
func TestGatherFishIsOneHit(t *testing.T) {
	gatherAccount(t, "9926", nil)
	now := time.Date(2026, time.October, 11, 12, 0, 0, 0, time.UTC)
	logger := quietLogger()
	g := &gardenGather{}
	begin := pbFieldsOf(t, gardenGatherReply(12, 9926, 9926, gatherTouch(3, 103, 40, true), g, now, logger)[0].body)
	if item := pbFieldsOf(t, begin.msgs[3][0]); begin.vars[1] != 0 || item.vars[2] != 0 {
		t.Fatalf("fish begin = %v item %v, want result 0 and grade 0", begin.vars, item.vars)
	}
	gardenGatherReply(11, 9926, 9926, gatherTouch(3, 103, 40, true), g, now, logger)
	if msgs := gardenGatherReply(13, 9926, 9926, nil, g, now, logger); len(msgs) != 3 {
		t.Fatalf("one-hit fish end = %+v, want a paid end", msgs)
	}
}

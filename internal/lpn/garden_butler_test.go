package lpn

import (
	"encoding/binary"
	"io"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"cherry/internal/economy"
	"cherry/internal/itemcode"
	"cherry/internal/store"
	"cherry/internal/testutil"
)

// butlerWire is one field of a butler message in the spec (butler-spec.md section 3): its wire type, and for a
// length-delimited nested message the table name of that message ("" for a string or a scalar).
type butlerWire struct {
	wire uint64
	sub  string
}

// butlerSpec lists the fields of every message Cherry sends for the butlers, by spec section 3 (names and wire types).
var butlerSpec = map[string]map[uint64]butlerWire{
	"result":  {1: {0, ""}},
	"gather":  {1: {0, ""}, 2: {0, ""}, 3: {0, ""}, 6: {2, "icon"}},
	"icon":    {1: {0, ""}},
	"shop":    {1: {0, ""}, 2: {2, "buy"}, 3: {2, "coin"}},
	"buy":     {1: {2, "detail"}, 2: {0, ""}, 3: {0, ""}, 4: {0, ""}, 5: {0, ""}, 6: {0, ""}, 7: {0, ""}, 8: {0, ""}, 9: {0, ""}, 10: {0, ""}, 11: {0, ""}, 12: {0, ""}, 13: {2, "hire"}},
	"detail":  {1: {2, "info"}, 2: {2, ""}, 3: {2, ""}, 4: {0, ""}, 5: {0, ""}, 6: {2, "ability"}, 7: {0, ""}, 8: {2, ""}, 9: {0, ""}, 10: {2, ""}, 11: {2, ""}, 12: {2, ""}},
	"info":    {1: {0, ""}, 2: {2, ""}, 3: {0, ""}, 4: {0, ""}, 5: {2, "skin"}, 6: {2, "employ"}, 7: {2, "point"}, 8: {2, ""}, 9: {2, ""}},
	"skin":    {1: {0, ""}, 2: {2, ""}, 3: {2, ""}, 4: {0, ""}, 5: {2, "item"}, 6: {2, ""}, 7: {0, ""}, 8: {2, ""}},
	"item":    {1: {0, ""}, 3: {0, ""}},
	"employ":  {1: {0, ""}, 2: {0, ""}, 3: {0, ""}, 4: {0, ""}, 5: {0, ""}, 6: {0, ""}, 7: {2, ""}, 8: {0, ""}, 9: {0, ""}},
	"point":   {1: {0, ""}, 2: {0, ""}},
	"ability": {1: {0, ""}, 2: {0, ""}, 3: {0, ""}, 4: {0, ""}},
	"hire":    {1: {0, ""}, 2: {0, ""}, 3: {0, ""}, 4: {0, ""}},
	"coin":    {1: {0, ""}, 2: {0, ""}},
	"bought":  {1: {0, ""}, 2: {2, "detail"}, 3: {2, "coin"}, 4: {0, ""}, 5: {0, ""}, 6: {0, ""}, 7: {0, ""}, 8: {0, ""}, 9: {0, ""}, 10: {2, ""}},
	"book":    {1: {0, ""}, 2: {2, "detail"}, 3: {2, ""}, 4: {0, ""}},
	"leader":  {1: {0, ""}, 2: {2, ""}},
	"push":    {1: {2, "info"}, 2: {2, "icon"}},
	"thumbs":  {1: {0, ""}, 2: {2, "thumb"}},
	"thumb":   {1: {0, ""}, 2: {2, ""}, 3: {0, ""}, 4: {0, ""}, 5: {2, ""}, 6: {0, ""}},
	"relay":   {8: {2, "info"}, 9: {2, "icon"}},
	// The gathering reward popup (hc 64, spec sections 3.3 and 4) and the claim (hc 65, section 3.4).
	"reward":     {1: {0, ""}, 2: {2, "rewardinfo"}},
	"rewardinfo": {1: {0, ""}, 2: {0, ""}, 3: {2, ""}, 4: {2, "itemlist"}, 5: {2, ""}, 6: {0, ""}, 7: {0, ""}, 8: {0, ""}, 9: {0, ""}, 10: {2, ""}},
	"itemlist":   {1: {2, "rewarditem"}},
	"rewarditem": {1: {2, ""}, 2: {2, ""}, 3: {0, ""}, 4: {0, ""}, 5: {0, ""}, 6: {2, ""}, 7: {0, ""}},
	"claim":      {1: {0, ""}, 2: {2, "icon"}},
}

// checkButlerWire walks body as the spec table name and fails on a field the table does not list, a field with
// another wire type, or a nested message that does not parse. Nested messages are walked too.
func checkButlerWire(t *testing.T, name string, body []byte) {
	t.Helper()
	spec, ok := butlerSpec[name]
	if !ok {
		t.Fatalf("no spec table %q", name)
	}
	for len(body) > 0 {
		key, n := binary.Uvarint(body)
		if n <= 0 {
			t.Fatalf("%s: bad key", name)
		}
		body = body[n:]
		field, wire := key>>3, key&7
		f, ok := spec[field]
		if !ok || f.wire != wire {
			t.Fatalf("%s: field %d wire %d is not in the spec", name, field, wire)
		}
		switch wire {
		case 0:
			_, n := binary.Uvarint(body)
			if n <= 0 {
				t.Fatalf("%s: field %d bad varint", name, field)
			}
			body = body[n:]
		case 2:
			size, n := binary.Uvarint(body)
			if n <= 0 || size > uint64(len(body)-n) {
				t.Fatalf("%s: field %d bad length", name, field)
			}
			d := body[n : n+int(size)]
			body = body[n+int(size):]
			if f.sub != "" {
				checkButlerWire(t, f.sub, d)
			}
		default:
			t.Fatalf("%s: field %d unsupported wire %d", name, field, wire)
		}
	}
}

// TestButlerRepliesMatchSpecWire drives every butler request through the protocol path and checks each reply's
// msgid and its fields (and nested fields) against the spec's numbers and wire types.
func TestButlerRepliesMatchSpecWire(t *testing.T) {
	now := time.Date(2026, 10, 28, 3, 0, 0, 0, time.UTC)
	logger := log.New(io.Discard, "", 0)
	const ophelia, ian = 1900000004, 1900000005
	hired := func(id int64) *store.ButlerBook {
		return &store.ButlerBook{Leader: id, Hired: []store.ButlerHire{{ID: id, Begin: now.UnixMilli(), End: now.UnixMilli() + 30*86400000}}}
	}
	expired := &store.ButlerBook{Hired: []store.ButlerHire{{ID: ian, Begin: now.UnixMilli() - 40*86400000, End: now.UnixMilli() - 3600000}}}
	buy := pbVar(pbVar(pbVar(pbVar(pbVar(nil, 1, ophelia), 2, uint64(economy.ButlerCoinCash)), 3, 0), 4, 0), 5, 30)

	rows := []struct {
		name  string
		req   uint16
		data  []byte
		book  *store.ButlerBook
		reply uint16
		spec  string
		push  bool // a second frame: the chief push (hc 68)
	}{
		{name: "shop", req: 51, reply: 60, spec: "shop"},
		{name: "book", req: 53, reply: 63, spec: "book", book: hired(ophelia)},
		{name: "book with expiry", req: 53, reply: 63, spec: "book", book: expired},
		{name: "profile without a contract", req: 57, reply: 67, spec: "book", data: pbVar(nil, 1, ophelia)},
		{name: "profile under contract", req: 57, reply: 67, spec: "book", data: pbVar(nil, 1, ophelia), book: hired(ophelia)},
		{name: "profile refused for an unknown butler", req: 57, reply: 67, spec: "result", data: pbVar(nil, 1, 1900000050)},
		{name: "skin select", req: 62, reply: 73, spec: "thumbs"},
		{name: "gather", req: 50, reply: 59, spec: "gather", data: pbVar(pbVar(nil, 1, ophelia), 2, 4), book: hired(ophelia)},
		{name: "hire", req: 52, reply: 61, spec: "bought", data: buy},
		{name: "chief", req: 56, reply: 66, spec: "leader", data: pbVar(nil, 1, ophelia), book: hired(ophelia), push: true},
		{name: "reward info refused", req: 54, reply: 64, spec: "result"},
		{name: "hire refused", req: 52, reply: 61, spec: "result"},
		{name: "skin equip refused", req: 59, reply: 70, spec: "result"},
		{name: "skin shop refused", req: 60, reply: 71, spec: "result"},
		{name: "skin buy refused", req: 61, reply: 72, spec: "result"},
		{name: "intimacy shop refused", req: 67, reply: 78, spec: "result"},
		{name: "intimacy buy refused", req: 68, reply: 79, spec: "result"},
		{name: "memory open refused", req: 70, reply: 81, spec: "result"},
		{name: "special order reward refused", req: 65, reply: 76, spec: "result"},
		{name: "special order bag refused", req: 66, reply: 77, spec: ""},
		{name: "memory list refused", req: 69, reply: 80, spec: ""},
	}
	tokens := map[string]*store.Account{}
	for i, r := range rows {
		aid := strconv.Itoa(3300 + i)
		tokens["bt-"+aid] = &store.Account{AccessToken: "bt-" + aid, SessionKey: "sk", Aid: aid, Name: "Butler", Cash: 1000, Butlers: r.book}
	}
	testutil.InstallAccounts(t, tokens)
	for i, r := range rows {
		viewer, _ := strconv.ParseUint(strconv.Itoa(3300+i), 10, 64)
		msgs := gardenButlerReply(r.req, viewer, r.data, now, logger)
		if len(msgs) == 0 || msgs[0].id != r.reply {
			t.Fatalf("%s: req %d got %+v, want first reply %d", r.name, r.req, msgs, r.reply)
		}
		if r.spec == "" {
			if len(msgs[0].body) != 0 {
				t.Fatalf("%s: want an empty reply, got %d bytes", r.name, len(msgs[0].body))
			}
		} else {
			checkButlerWire(t, r.spec, msgs[0].body)
		}
		if r.push {
			if len(msgs) != 2 || msgs[1].id != 68 {
				t.Fatalf("%s: want the chief push (68) after the reply, got %d frames", r.name, len(msgs))
			}
			checkButlerWire(t, "push", msgs[1].body)
		} else if len(msgs) != 1 {
			t.Fatalf("%s: %d frames, want 1", r.name, len(msgs))
		}
	}
}

// TestButlerChiefTileBesideSpawn: the chief stands about 120 px right of the owner's spawn tile (30,39), not behind
// the owner's avatar.
func TestButlerChiefTileBesideSpawn(t *testing.T) {
	cx, _ := gardenPx(butlerChiefTile[0], butlerChiefTile[1])
	ox, _ := gardenPx(30, 39)
	if butlerChiefTile != [2]int{33, 39} || cx-ox != 120 {
		t.Fatalf("chief tile %v is %d px right of the owner's spawn, want (33,39) and 120 px", butlerChiefTile, int64(cx)-int64(ox))
	}
}

// TestButlerTimesAreEpochSeconds: the contract times on the wire are epoch seconds (the stored milliseconds / 1000), and
// remainTime is seconds. The device read milliseconds as year 58793 (2026-10-28).
func TestButlerTimesAreEpochSeconds(t *testing.T) {
	now := time.Date(2026, 10, 28, 3, 0, 0, 0, time.UTC)
	h := store.ButlerHire{ID: 1900000004, Begin: now.UnixMilli(), End: now.UnixMilli() + 7*86400000}
	body := butlerEmployment(h, now)
	if got, _ := protobufUintField(body, 3); got != uint64(now.Unix()) {
		t.Fatalf("firstTime = %d, want epoch seconds %d", got, now.Unix())
	}
	if got, _ := protobufUintField(body, 4); got != uint64(now.Unix()) {
		t.Fatalf("beginTime = %d, want epoch seconds %d", got, now.Unix())
	}
	if got, _ := protobufUintField(body, 5); got != uint64(now.Unix()+7*86400) {
		t.Fatalf("endTime = %d, want epoch seconds %d", got, now.Unix()+7*86400)
	}
	if got, _ := protobufUintField(body, 6); got != 7*86400 {
		t.Fatalf("remainTime = %d, want 604800 seconds", got)
	}
}

// TestButlerRelayFallbackChief: a running contract with no stored chief still spawns (the earliest running contract is
// the chief, economy.ButlerChief), so an account hired before the chief rule gets its butler without a data edit.
func TestButlerRelayFallbackChief(t *testing.T) {
	now := time.Date(2026, 10, 28, 3, 0, 0, 0, time.UTC)
	const ophelia = 1900000004
	testutil.InstallAccounts(t, map[string]*store.Account{"brf-3352": {AccessToken: "brf-3352", SessionKey: "sk", Aid: "3352", Name: "Owner",
		Butlers: &store.ButlerBook{Hired: []store.ButlerHire{{ID: ophelia, Begin: now.UnixMilli(), End: now.UnixMilli() + 86400000}}}}})
	body := gardenButlerRelay(3352, now)
	if len(body) == 0 {
		t.Fatal("a running contract with no stored chief spawns no butler")
	}
	checkButlerWire(t, "relay", body)
}

// TestButlerSkinItemsAreNumericIDs: every outfit code of every roster butler reaches ButlerSkinInfo.items (field 5) as its
// numeric item id, face and animal parts included. Merry's CA codes are encoded too (SquareNumericItem is CU only; the
// butler path uses itemcode.ID). avatarSkinColor (field 4) is the shipped skin_color. Spec 12.1 field numbers are checked.
func TestButlerSkinItemsAreNumericIDs(t *testing.T) {
	for _, d := range economy.ActiveButlers().Butlers {
		skin := butlerSkin(d)
		checkButlerWire(t, "skin", skin)
		var got, want []uint64
		for _, item := range pbAll(t, skin, 5) {
			id, _ := protobufUintField(item, 1)
			got = append(got, id)
		}
		for _, code := range d.Outfit {
			id, ok := itemcode.ID(code)
			if !ok {
				t.Fatalf("%s: outfit code %s is not an item code", d.Name, code)
			}
			want = append(want, uint64(id))
		}
		if !slices.Equal(got, want) {
			t.Fatalf("%s: skin items %v, want %v", d.Name, got, want)
		}
		if c, _ := protobufUintField(skin, 4); c != uint64(d.SkinColor) {
			t.Fatalf("%s: avatarSkinColor %d, want %d", d.Name, c, d.SkinColor)
		}
	}
	merry, ok := economy.ButlerByID(1900000006)
	if !ok {
		t.Fatal("merry is not in the roster")
	}
	items := map[uint64]bool{}
	for _, item := range pbAll(t, butlerSkin(merry), 5) {
		id, _ := protobufUintField(item, 1)
		items[id] = true
	}
	// CAEY0001O = 234100060 (eyes, animal face 41), CAEA0004N = 235700167 (ears, 57): the client's numeric ids.
	if !items[234100060] || !items[235700167] {
		t.Fatalf("merry's animal parts encoded as %v, want 234100060 and 235700167", items)
	}
}

// TestButlerRelayChiefFields: relay start carries the owner's chief in fields 8 and 9 only while its contract runs,
// and nothing otherwise.
func TestButlerRelayChiefFields(t *testing.T) {
	now := time.Date(2026, 10, 28, 3, 0, 0, 0, time.UTC)
	const ophelia = 1900000004
	testutil.InstallAccounts(t, map[string]*store.Account{"brt-3350": {AccessToken: "brt-3350", SessionKey: "sk", Aid: "3350", Name: "Owner",
		Butlers: &store.ButlerBook{Leader: ophelia, Hired: []store.ButlerHire{{ID: ophelia, Begin: now.UnixMilli(), End: now.UnixMilli() + 86400000}}}}})
	body := gardenButlerRelay(3350, now)
	if len(body) == 0 {
		t.Fatal("chief under contract: no relay fields")
	}
	checkButlerWire(t, "relay", body)
	if got := gardenButlerRelay(3350, now.Add(48*time.Hour)); len(got) != 0 {
		t.Fatalf("expired chief still spawns: %d bytes", len(got))
	}
	if got := gardenButlerRelay(3999, now); len(got) != 0 {
		t.Fatal("unknown owner spawns a chief")
	}
}

// butlerCard returns the Book card (ButlerDetailInfo) of butler id in a Book body: its gathering status (f4) and end
// (f5). ok is false when the Book has no card for it.
func butlerCard(t *testing.T, book []byte, id uint64) (status, end uint64, ok bool) {
	t.Helper()
	for _, card := range pbAll(t, book, 2) {
		info := pbAll(t, card, 1)
		if len(info) == 0 {
			continue
		}
		if got, _ := protobufUintField(info[0], 1); got == id {
			status, _ = protobufUintField(card, 4)
			end, _ = protobufUintField(card, 5)
			return status, end, true
		}
	}
	return 0, 0, false
}

// TestButlerGatherSequence walks a gathering the way the client does (spec section 5.2) through the request path: the
// Book shows idle; the start (ch 50 status 2) answers status 3 with the end; the reward is refused before the end; the
// Book shows 5 after it; the reward popup lists the rolled items; a wrong sub id is refused; the claim pays once and the
// card is idle again. A second butler is stopped instead: idle again, nothing paid. Merry is not hired, so her start is
// refused. The Book pairs its cards into rows, so the second butler sits behind an empty card (butlerBookBody).
func TestButlerGatherSequence(t *testing.T) {
	now := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	logger := log.New(io.Discard, "", 0)
	const viewer, ophelia, ian, merry = 3400, 1900000004, 1900000005, 1900000006
	month := int64(30 * 86400000)
	testutil.InstallAccounts(t, map[string]*store.Account{"bgs-3400": {AccessToken: "bgs-3400", SessionKey: "sk", Aid: "3400", Name: "Gather",
		Butlers: &store.ButlerBook{Leader: ophelia, Hired: []store.ButlerHire{
			{ID: ophelia, Begin: now.UnixMilli(), End: now.UnixMilli() + month},
			{ID: ian, Begin: now.UnixMilli(), End: now.UnixMilli() + month},
		}}}})
	dir := t.TempDir()
	store.AccountsMu.Lock()
	store.AccountStorePath = filepath.Join(dir, "accounts.json")
	store.AccountsMu.Unlock()

	// request sends one butler request: the fields are (number, value) pairs.
	request := func(msgid uint16, at time.Time, fields ...uint64) []gardenMsg {
		t.Helper()
		var data []byte
		for i := 0; i+1 < len(fields); i += 2 {
			data = pbVar(data, fields[i], fields[i+1])
		}
		return gardenButlerReply(msgid, viewer, data, at, logger)
	}
	book := func(at time.Time) []byte {
		t.Helper()
		msgs := gardenButlerReply(53, viewer, nil, at, logger)
		checkButlerWire(t, "book", msgs[0].body)
		return msgs[0].body
	}
	// gathering is the ch 50 request {1 butler, 2 status, 3 req_type 2}.
	gathering := func(butler, status uint64) []uint64 { return []uint64{1, butler, 2, status, 3, 2} }
	state := func() (gems, bagCount int64, stacks int) {
		store.AccountsMu.Lock()
		defer store.AccountsMu.Unlock()
		acc := store.AccountByAidLocked("3400")
		for _, s := range acc.GardenBag {
			bagCount += s.Count
		}
		return acc.Gems, bagCount, len(acc.GardenBag)
	}
	mustStatus := func(at time.Time, id, want, wantEnd uint64) {
		t.Helper()
		status, end, ok := butlerCard(t, book(at), id)
		if !ok || status != want || end != wantEnd {
			t.Fatalf("Book at %v: butler %d status %d end %d (found %v), want %d end %d", at.Format(time.TimeOnly), id, status, end, ok, want, wantEnd)
		}
	}
	expectResult := func(msg gardenMsg, msgid uint16, want uint64) {
		t.Helper()
		if msg.id != msgid {
			t.Fatalf("reply msgid %d, want %d", msg.id, msgid)
		}
		if got, _ := protobufUintField(msg.body, 1); got != want {
			t.Fatalf("msgid %d result %d, want %d", msgid, got, want)
		}
	}

	// The Book pairs the two butlers: Ophelia on the left card of row 0, an empty card, Ian on the left card of row 1.
	if entries := pbAll(t, book(now), 2); len(entries) != 3 || len(pbAll(t, entries[1], 1)) != 0 {
		t.Fatalf("Book of two butlers has %d entries, want Ophelia, an empty card, Ian", len(entries))
	}
	mustStatus(now, ophelia, 1, 0)

	// Start: status 3 with the end (60 minutes), and nothing paid.
	end := uint64(now.Add(time.Hour).Unix())
	msgs := request(50, now, gathering(ophelia, 2)...)
	checkButlerWire(t, "gather", msgs[0].body)
	expectResult(msgs[0], 59, 0)
	if got, _ := protobufUintField(msgs[0].body, 2); got != 3 {
		t.Fatalf("start: gatheringStatus %d, want 3", got)
	}
	if got, _ := protobufUintField(msgs[0].body, 3); got != end {
		t.Fatalf("start: gatheringRemainTime %d, want the end %d in epoch seconds", got, end)
	}
	if gems, bag, _ := state(); gems != 0 || bag != 0 {
		t.Fatalf("start paid gems=%d bag=%d", gems, bag)
	}
	mustStatus(now.Add(10*time.Minute), ophelia, 3, end)

	// Before the end the reward popup is refused (217096, nothing listed).
	expectResult(request(54, now.Add(10*time.Minute), 1, ophelia, 2, 1)[0], 64, 217096)

	// At the end: the Book shows collect (5) and the popup lists one item per ability, with the Bag's picture.
	mustStatus(now.Add(time.Hour), ophelia, 5, end)
	msgs = request(54, now.Add(time.Hour), 1, ophelia, 2, 1)
	checkButlerWire(t, "reward", msgs[0].body)
	expectResult(msgs[0], 64, 0)
	info := pbAll(t, msgs[0].body, 2)[0]
	if got, _ := protobufUintField(info, 1); got != ophelia {
		t.Fatalf("reward butler %d, want %d", got, ophelia)
	}
	if got, _ := protobufUintField(info, 2); got != 1 {
		t.Fatalf("reward type %d, want 1 (gathering)", got)
	}
	if got, _ := protobufUintField(info, 9); got != uint64(now.Unix()) {
		t.Fatalf("rewardSubId %d, want the start in epoch seconds %d", got, now.Unix())
	}
	if got, _ := protobufUintField(info, 7); got != 3600 {
		t.Fatalf("gatheringIngTime %d, want 3600 seconds", got)
	}
	items := pbAll(t, pbAll(t, info, 4)[0], 1)
	if len(items) != 3 {
		t.Fatalf("reward lists %d items, want one per ability (3)", len(items))
	}
	for _, it := range items {
		code, _, _ := pbBytesField(it, 1)
		id, err := strconv.ParseUint(string(code), 10, 64)
		if err != nil {
			t.Fatalf("itemCode %q is not numeric", code)
		}
		if _, ok := economy.GatherMaterialByID(int64(id)); !ok {
			t.Fatalf("reward item %d is not a Garden material", id)
		}
		if img, _, _ := pbBytesField(it, 6); string(img) != economy.GatherCraftImage(int64(id)) {
			t.Fatalf("imgUrl %q, want the Bag's craft picture", img)
		}
		if n, _ := protobufUintField(it, 3); n != 1 {
			t.Fatalf("itemCount %d, want 1", n)
		}
	}

	// A claim with another sub id is refused; the right one pays once.
	expectResult(request(55, now.Add(time.Hour), 1, ophelia, 2, 1, 3, uint64(now.Unix())+1)[0], 65, butlerRefused)
	if gems, bag, _ := state(); gems != 0 || bag != 0 {
		t.Fatalf("refused claim paid gems=%d bag=%d", gems, bag)
	}
	msgs = request(55, now.Add(time.Hour), 1, ophelia, 2, 1, 3, uint64(now.Unix()))
	checkButlerWire(t, "claim", msgs[0].body)
	expectResult(msgs[0], 65, 0)
	if gems, bag, stacks := state(); gems != 3 || bag != 3 || stacks != 3 {
		t.Fatalf("claim: gems=%d bag=%d stacks=%d, want 3 gems, 3 materials in 3 stacks", gems, bag, stacks)
	}
	ledger, err := os.ReadFile(filepath.Join(dir, "ledger.jsonl"))
	if err != nil || strings.Count(string(ledger), `"reason":"butler:gather:1900000004:20261101"`) != 1 {
		t.Fatalf("ledger after the claim: %q (err %v), want one butler:gather line for 20261101", ledger, err)
	}
	mustStatus(now.Add(time.Hour+time.Minute), ophelia, 1, 0)

	// A repeat claim is a no-op success: nothing is paid twice.
	expectResult(request(55, now.Add(time.Hour), 1, ophelia, 2, 1, 3, uint64(now.Unix()))[0], 65, 0)
	if gems, bag, _ := state(); gems != 3 || bag != 3 {
		t.Fatalf("repeat claim paid again: gems=%d bag=%d", gems, bag)
	}

	// Ian: start, then stop. Stop goes idle with no reward; a later reward request is refused.
	later := now.Add(2 * time.Hour)
	if msgs = request(50, later, gathering(ian, 2)...); msgs[0].id != 59 {
		t.Fatalf("Ian start: msgid %d", msgs[0].id)
	}
	mustStatus(later, ian, 3, uint64(later.Add(time.Hour).Unix()))
	msgs = request(50, later.Add(time.Minute), gathering(ian, 4)...)
	checkButlerWire(t, "gather", msgs[0].body)
	expectResult(msgs[0], 59, 0)
	if got, _ := protobufUintField(msgs[0].body, 2); got != 1 {
		t.Fatalf("stop: gatheringStatus %d, want 1", got)
	}
	mustStatus(later.Add(time.Minute), ian, 1, 0)
	expectResult(request(54, later.Add(2*time.Hour), 1, ian, 2, 1)[0], 64, 217096)
	if gems, bag, _ := state(); gems != 3 || bag != 3 {
		t.Fatalf("stop paid gems=%d bag=%d", gems, bag)
	}

	// Merry is not under contract: her start is refused with the collecting error and her card is not in the Book.
	expectResult(request(50, later, gathering(merry, 2)...)[0], 59, 217095)
	if _, _, ok := butlerCard(t, book(later), merry); ok {
		t.Fatal("an unhired butler has a Book card")
	}
}

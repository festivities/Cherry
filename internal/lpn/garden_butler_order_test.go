package lpn

import (
	"bytes"
	"encoding/hex"
	"io"
	"log"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"cherry/internal/economy"
	"cherry/internal/store"
	"cherry/internal/testutil"
)

// orderMaterials returns the data's special order materials of star grade grade, in the data's order.
func orderMaterials(grade uint64) []uint64 {
	var out []uint64
	for _, m := range economy.ActiveButlers().SpecialOrder.Materials {
		if uint64(m.Grade) == grade {
			out = append(out, uint64(m.Item))
		}
	}
	return out
}

// orderDeliver is a ch 65 body: the butler (f1), the order id (f2) and one ButlerSpecialOrderBagItem per {item, count} (f3).
func orderDeliver(butler, order uint64, items ...[2]uint64) []byte {
	body := pbVar(pbVar(nil, 1, butler), 2, order)
	for _, it := range items {
		body = pbLen(body, 3, pbVar(pbVar(nil, 2, it[0]), 3, it[1]))
	}
	return body
}

// orderSnap is what a refused special order request must leave alone: the gems, the Bag, the open orders, the day's
// delivered count and the ledger.
type orderSnap struct {
	gems   int64
	bag    []store.GardenBagStack
	open   int
	done   int64
	ledger string
}

// orderState reads the account 3600 of TestSpecialOrderRequestPath.
func orderState(t *testing.T) orderSnap {
	t.Helper()
	ledger, err := testutil.LedgerText(t)
	if err != nil {
		t.Fatal(err)
	}
	store.AccountsMu.Lock()
	defer store.AccountsMu.Unlock()
	acc := store.AccountByAidLocked("3600")
	return orderSnap{gems: acc.Gems, bag: slices.Clone(acc.GardenBag), open: len(acc.Butlers.Orders), done: acc.Butlers.OrderDone, ledger: string(ledger)}
}

// TestSpecialOrderRequestPath walks a special order through the request path (butler-robo-spec 11): the order (ch 64, hc 75)
// is given once and returned again; the day's limit and an unhired butler answer their codes; the picker (ch 66, hc 77) lists
// the Bag's materials of the grade; the refusals (hc 76: 217117 keeps the popup, 217118 ends it, 3039 a malformed body)
// change nothing; the right delivery pays 15 Gems once with its ledger reason and takes the materials; a repeat is refused.
func TestSpecialOrderRequestPath(t *testing.T) {
	now := time.Date(2026, 11, 17, 3, 0, 0, 0, time.UTC)
	logger := log.New(io.Discard, "", 0)
	const viewer, ophelia, ian, merry = 3600, 1900000004, 1900000005, 1900000006
	day := uint64(economy.AttendDayKey(now))
	testutil.InstallAccounts(t, map[string]*store.Account{"sop-3600": {AccessToken: "sop-3600", SessionKey: "sk", Aid: "3600", Name: "Order",
		Butlers: &store.ButlerBook{Leader: ophelia, Hired: []store.ButlerHire{
			{ID: ophelia, Begin: now.UnixMilli(), End: now.UnixMilli() + 30*86400000},
			{ID: ian, Begin: now.UnixMilli(), End: now.UnixMilli() + 30*86400000},
		}}}})
	testutil.UseDatabase(t)
	request := func(msgid uint16, at time.Time, data []byte) gardenMsg {
		t.Helper()
		msgs := gardenButlerReply(msgid, viewer, data, at, logger)
		// A paid delivery is followed by hc 28, which makes the client refetch its balance (the Gem HUD).
		if len(msgs) == 2 && msgid == 65 && msgs[1].id == 28 {
			if r, _ := protobufUintField(msgs[0].body, 1); r == 0 {
				msgs = msgs[:1]
			}
		}
		if len(msgs) != 1 || msgs[0].id != map[uint16]uint16{64: 75, 65: 76, 66: 77}[msgid] {
			t.Fatalf("request %d: got %d frames %+v", msgid, len(msgs), msgs)
		}
		return msgs[0]
	}
	expectResult := func(msg gardenMsg, want uint64) {
		t.Helper()
		if got, _ := protobufUintField(msg.body, 1); got != want {
			t.Fatalf("msgid %d: result %d, want %d", msg.id, got, want)
		}
	}
	orderBody := pbVar(nil, 1, ophelia)

	// The order: result 0, Ophelia, the end at 06:00 JST on the 18th, 3 materials, 15 Gems (type 106).
	first := request(64, now, orderBody)
	checkButlerWire(t, "order", first.body)
	expectResult(first, 0)
	orderID, _ := protobufUintField(first.body, 3)
	wireGrade, _ := protobufUintField(first.body, 5)
	if orderID != day*1000+4 || wireGrade > 2 {
		t.Fatalf("order id %d (day %d) and wire grade %d", orderID, day, wireGrade)
	}
	if end, _ := protobufUintField(first.body, 4); end != uint64(time.Date(2026, 11, 17, 21, 0, 0, 0, time.UTC).Unix()) {
		t.Fatalf("end %d, want 06:00 JST on the 18th", end)
	}
	if n, _ := protobufUintField(first.body, 6); n != 3 {
		t.Fatalf("materials %d, want 3", n)
	}
	reward := pbAll(t, first.body, 7)[0]
	if n, _ := protobufUintField(reward, 3); n != 15 {
		t.Fatalf("reward count %d, want 15", n)
	}
	if typ, _ := protobufUintField(reward, 4); typ != 106 {
		t.Fatalf("reward type %d, want 106 (Gems)", typ)
	}
	if again := request(64, now.Add(time.Hour), orderBody); !bytes.Equal(again.body, first.body) {
		t.Fatal("asking again gave a different order")
	}
	expectResult(request(64, now, pbVar(nil, 1, ian)), 217116)   // the day's one order is in use
	expectResult(request(64, now, pbVar(nil, 1, merry)), 217103) // not under contract

	// The Bag: two materials of the grade, one of another grade and a non-material (the picker lists the first two).
	grade := wireGrade + 1
	mats := orderMaterials(grade)
	other := orderMaterials(grade%3 + 1)
	store.AccountsMu.Lock()
	store.AccountByAidLocked("3600").GardenBag = []store.GardenBagStack{
		{Item: int64(mats[0]), Count: 2}, {Item: int64(mats[1]), Count: 1}, {Item: int64(other[0]), Count: 3}, {Item: 999999, Count: 4},
	}
	store.AccountsMu.Unlock()
	picker := request(66, now, pbVar(nil, 1, wireGrade))
	checkButlerWire(t, "bagitems", picker.body)
	cells := pbAll(t, picker.body, 1)
	if len(cells) != 2 {
		t.Fatalf("picker lists %d cells, want 2", len(cells))
	}
	for i, want := range [][2]uint64{{mats[0], 2}, {mats[1], 1}} {
		serial, _ := protobufUintField(cells[i], 1)
		itemID, _ := protobufUintField(cells[i], 2)
		owned, _ := protobufUintField(cells[i], 3)
		tab, _ := protobufUintField(cells[i], 4)
		cellGrade, _ := protobufUintField(cells[i], 5)
		img, _, _ := pbBytesField(cells[i], 6)
		if serial != want[0] || itemID != want[0] || owned != want[1] || tab != want[0]/100000 || cellGrade != wireGrade || string(img) != economy.GatherCraftImage(int64(want[0])) {
			t.Fatalf("cell %d: serial %d item %d owned %d tab %d grade %d img %q", i, serial, itemID, owned, tab, cellGrade, img)
		}
	}
	if none := request(66, now, pbVar(nil, 1, 3)); len(none.body) != 0 {
		t.Fatalf("a grade with no materials lists %d bytes", len(none.body))
	}

	// Refusals (hc 76): nothing changes. A wrong grade and a short count keep the popup (217117); an unknown or expired order
	// ends it (217118); a malformed item list is the network error (3039).
	before := orderState(t)
	expectResult(request(65, now, orderDeliver(ophelia, orderID, [2]uint64{other[0], 3})), 217117)
	expectResult(request(65, now, orderDeliver(ophelia, orderID, [2]uint64{mats[0], 2})), 217117)
	expectResult(request(65, now, orderDeliver(ophelia, orderID+1, [2]uint64{mats[0], 2}, [2]uint64{mats[1], 1})), 217118)
	expectResult(request(65, now.Add(19*time.Hour), orderDeliver(ophelia, orderID, [2]uint64{mats[0], 2}, [2]uint64{mats[1], 1})), 217118)
	expectResult(request(65, now, append(orderDeliver(ophelia, orderID), 0x1a, 0x05, 0x01)), 3039)
	if after := orderState(t); !reflect.DeepEqual(after, before) {
		t.Fatalf("refused deliveries changed the state: %+v, want %+v", after, before)
	}

	// The delivery (hc 76): the reward popup's ButlerRewardInfo, 15 Gems once, the two stacks gone, one ledger reason.
	paid := request(65, now.Add(time.Hour), orderDeliver(ophelia, orderID, [2]uint64{mats[0], 2}, [2]uint64{mats[1], 1}))
	checkButlerWire(t, "reward", paid.body)
	expectResult(paid, 0)
	info := pbAll(t, paid.body, 2)[0]
	checkButlerWire(t, "rewardinfo", info)
	if id, _ := protobufUintField(info, 1); id != ophelia {
		t.Fatalf("reward butler %d, want %d", id, ophelia)
	}
	if typ, _ := protobufUintField(info, 2); typ != 106 {
		t.Fatalf("reward type %d, want 106", typ)
	}
	row := pbAll(t, pbAll(t, info, 4)[0], 1)[0]
	if n, _ := protobufUintField(row, 3); n != 15 {
		t.Fatalf("reward row count %d, want 15", n)
	}
	after := orderState(t)
	if after.gems != 15 || len(after.bag) != 2 || after.open != 0 || after.done != 1 {
		t.Fatalf("after the delivery: gems %d bag %+v open %d done %d; want 15 gems, the two other stacks, 0 open, 1 done", after.gems, after.bag, after.open, after.done)
	}
	if n := strings.Count(after.ledger, `"reason":"butler:order:1900000004:`+strconv.FormatUint(orderID, 10)+`"`); n != 1 {
		t.Fatalf("ledger has %d rows for the order, want 1: %s", n, after.ledger)
	}

	// Again: the order is ended (217118) and today's limit answers the Chat (217116); nothing changes.
	expectResult(request(65, now.Add(time.Hour), orderDeliver(ophelia, orderID, [2]uint64{mats[0], 2}, [2]uint64{mats[1], 1})), 217118)
	expectResult(request(64, now.Add(time.Hour), orderBody), 217116)
	if again := orderState(t); !reflect.DeepEqual(again, after) {
		t.Fatalf("repeat requests changed the state: %+v, want %+v", again, after)
	}
}

// TestSpecialOrderSampleBytes pins the exact bytes of the order reply (hc 75) for a fixed clock: Ophelia, 17 November 2026
// 03:00 UTC, account 3601.
func TestSpecialOrderSampleBytes(t *testing.T) {
	now := time.Date(2026, 11, 17, 3, 0, 0, 0, time.UTC)
	logger := log.New(io.Discard, "", 0)
	const viewer, ophelia = 3601, 1900000004
	testutil.InstallAccounts(t, map[string]*store.Account{"sob-3601": {AccessToken: "sob-3601", SessionKey: "sk", Aid: "3601", Name: "Sample",
		Butlers: &store.ButlerBook{Leader: ophelia, Hired: []store.ButlerHire{{ID: ophelia, Begin: now.UnixMilli(), End: now.UnixMilli() + 30*86400000}}}}})
	msgs := gardenButlerReply(64, viewer, pbVar(nil, 1, ophelia), now, logger)
	if len(msgs) != 1 || msgs[0].id != 75 {
		t.Fatalf("got %+v, want one hc 75", msgs)
	}
	const want = "0800" + // result 0
		"122d" + "0884e6fe890712074f7068656c69612a1c6368657272792f6275746c65722f313930303030303030342e706e67" + // thumbnail: id, "Ophelia", cherry/butler/1900000004.png
		"18f4f8f309" + // order id 20774004: day 20774 times 1000, plus butler 4
		"20d080f3d706" + // end 1794949200: 06:00 JST on the 18th
		"2802" + // grade 2 on the wire (3 stars)
		"3003" + // 3 materials
		"3a04180f206a" // reward: 15 of type 106 (Gems)
	if got := hex.EncodeToString(msgs[0].body); got != want {
		t.Fatalf("hc 75 body\n got %s\nwant %s", got, want)
	}
}

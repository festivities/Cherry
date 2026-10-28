package lpn

import (
	"encoding/binary"
	"io"
	"log"
	"strconv"
	"testing"
	"time"

	"cherry/internal/economy"
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
	const hazel, wren = 1900000001, 1900000002
	hired := func(id int64) *store.ButlerBook {
		return &store.ButlerBook{Leader: id, Hired: []store.ButlerHire{{ID: id, Begin: now.UnixMilli(), End: now.UnixMilli() + 30*86400000}}}
	}
	expired := &store.ButlerBook{Hired: []store.ButlerHire{{ID: wren, Begin: now.UnixMilli() - 40*86400000, End: now.UnixMilli() - 3600000}}}
	buy := pbVar(pbVar(pbVar(pbVar(pbVar(nil, 1, hazel), 2, uint64(economy.ButlerCoinCash)), 3, 0), 4, 0), 5, 30)

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
		{name: "book", req: 53, reply: 63, spec: "book", book: hired(hazel)},
		{name: "book with expiry", req: 53, reply: 63, spec: "book", book: expired},
		{name: "skin select", req: 62, reply: 73, spec: "thumbs"},
		{name: "gather", req: 50, reply: 59, spec: "gather", data: pbVar(pbVar(nil, 1, hazel), 2, 4), book: hired(hazel)},
		{name: "hire", req: 52, reply: 61, spec: "bought", data: buy},
		{name: "chief", req: 56, reply: 66, spec: "leader", data: pbVar(nil, 1, hazel), book: hired(hazel), push: true},
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
	h := store.ButlerHire{ID: 1900000001, Begin: now.UnixMilli(), End: now.UnixMilli() + 7*86400000}
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
	const hazel = 1900000001
	testutil.InstallAccounts(t, map[string]*store.Account{"brf-3352": {AccessToken: "brf-3352", SessionKey: "sk", Aid: "3352", Name: "Owner",
		Butlers: &store.ButlerBook{Hired: []store.ButlerHire{{ID: hazel, Begin: now.UnixMilli(), End: now.UnixMilli() + 86400000}}}}})
	body := gardenButlerRelay(3352, now)
	if len(body) == 0 {
		t.Fatal("a running contract with no stored chief spawns no butler")
	}
	checkButlerWire(t, "relay", body)
}

// TestButlerRelayChiefFields: relay start carries the owner's chief in fields 8 and 9 only while its contract runs,
// and nothing otherwise.
func TestButlerRelayChiefFields(t *testing.T) {
	now := time.Date(2026, 10, 28, 3, 0, 0, 0, time.UTC)
	const hazel = 1900000001
	testutil.InstallAccounts(t, map[string]*store.Account{"brt-3350": {AccessToken: "brt-3350", SessionKey: "sk", Aid: "3350", Name: "Owner",
		Butlers: &store.ButlerBook{Leader: hazel, Hired: []store.ButlerHire{{ID: hazel, Begin: now.UnixMilli(), End: now.UnixMilli() + 86400000}}}}})
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

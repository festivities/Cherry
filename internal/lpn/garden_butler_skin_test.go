package lpn

import (
	"io"
	"log"
	"testing"
	"time"

	"cherry/internal/economy"
	"cherry/internal/store"
	"cherry/internal/testutil"
)

// skinRequest sends one butler request with (field, value) pairs as its body.
func skinRequest(t *testing.T, msgid uint16, aid uint64, now time.Time, fields ...uint64) []gardenMsg {
	t.Helper()
	var data []byte
	for i := 0; i+1 < len(fields); i += 2 {
		data = pbVar(data, fields[i], fields[i+1])
	}
	return gardenButlerReply(msgid, aid, data, now, log.New(io.Discard, "", 0))
}

// TestButlerSkinShopAndVoiceBookOnTheWire walks the Skin Shop and the Voice Book through the request path for Merry (hired,
// chief): the list shows her fifteen skins (the default owned and worn, the rest for 30 Cash); a buy charges once and the
// refreshed list shows it owned; a second buy is refused with nothing charged; a wear changes the skin the Garden chief
// spawns with (relay start, ButlerInfo f5) and the thumbnail; the Voice Book lists her 47 lines and Ophelia (no pack) gets
// none; an unknown butler is refused.
func TestButlerSkinShopAndVoiceBookOnTheWire(t *testing.T) {
	now := time.Date(2026, 11, 16, 3, 0, 0, 0, time.UTC)
	const aid, merry, ophelia, silver, unknown = 3500, 1900000006, 1900000004, 1900001601, 1900000050
	const defaultSkin = 7
	hire := store.ButlerHire{ID: merry, Begin: now.UnixMilli(), End: now.UnixMilli() + 30*86400000}
	testutil.InstallAccounts(t, map[string]*store.Account{"bsk-3500": {AccessToken: "bsk-3500", SessionKey: "sk", Aid: "3500", Name: "Skin", Cash: 100,
		Butlers: &store.ButlerBook{Leader: merry, Hired: []store.ButlerHire{hire}}}})
	testutil.UseDatabase(t)

	cash := func() uint64 {
		store.AccountsMu.Lock()
		defer store.AccountsMu.Unlock()
		return uint64(store.AccountByAidLocked("3500").Cash)
	}
	// coinValue is the value of one CoinInfo (the balance f4 of a reply, a row's price f6 or f9).
	coinValue := func(coin []byte) uint64 {
		t.Helper()
		v, _ := protobufUintField(coin, 2)
		return v
	}
	// rows returns the Skin Shop rows (f3) of a reply body and checks its wire layout.
	rows := func(body []byte) [][]byte {
		t.Helper()
		checkButlerWire(t, "skinshop", body)
		return pbAll(t, body, 3)
	}
	result := func(body []byte) uint64 {
		v, _ := protobufUintField(body, 1)
		return v
	}

	// The list: fifteen skins, the default owned and worn, the silver beret for 30 Cash, pictures as the URLs of 2.5.
	msgs := skinRequest(t, 60, aid, now, 1, merry)
	if msgs[0].id != 71 || result(msgs[0].body) != 0 {
		t.Fatalf("skin shop: msgid %d result %d, want 71 and 0", msgs[0].id, result(msgs[0].body))
	}
	list := rows(msgs[0].body)
	if len(list) != 15 {
		t.Fatalf("skin shop lists %d skins, want 15", len(list))
	}
	if owned, _ := protobufUintField(list[0], 4); owned != 1 {
		t.Fatal("the default skin is not owned")
	}
	if worn, _ := protobufUintField(list[0], 5); worn != 1 {
		t.Fatal("the default skin is not worn")
	}
	silverSkin, _, _ := pbBytesField(list[1], 3)
	if id, _ := protobufUintField(silverSkin, 1); id != silver {
		t.Fatalf("row 1 skin id %d, want %d", id, silver)
	}
	if pic, _, _ := pbBytesField(silverSkin, 3); string(pic) != "cherry/butler/skin/1900001601.png" {
		t.Fatalf("silver beret picture %q", pic)
	}
	if owned, _ := protobufUintField(list[1], 4); owned != 0 {
		t.Fatal("the silver beret is listed as owned before the buy")
	}
	if price, _, _ := pbBytesField(list[1], 6); coinValue(price) != 30 {
		t.Fatalf("silver beret price %d, want 30 Cash", coinValue(price))
	}

	// The buy: 30 Cash once; the refreshed list shows the beret owned, not worn, and the balance 70.
	msgs = skinRequest(t, 61, aid, now, 1, merry, 2, silver)
	if msgs[0].id != 72 || result(msgs[0].body) != 0 {
		t.Fatalf("buy: msgid %d result %d, want 72 and 0", msgs[0].id, result(msgs[0].body))
	}
	list = rows(msgs[0].body)
	if owned, _ := protobufUintField(list[1], 4); owned != 1 {
		t.Fatal("the bought beret is not listed as owned")
	}
	if worn, _ := protobufUintField(list[1], 5); worn != 0 {
		t.Fatal("the bought beret is worn without an equip")
	}
	if balance, _, _ := pbBytesField(msgs[0].body, 4); coinValue(balance) != 70 || cash() != 70 {
		t.Fatalf("balance after the buy: reply %d, account %d, want 70", coinValue(balance), cash())
	}

	// A second buy is refused: the result alone, nothing charged.
	msgs = skinRequest(t, 61, aid, now, 1, merry, 2, silver)
	if msgs[0].id != 72 || result(msgs[0].body) != 1 || cash() != 70 {
		t.Fatalf("second buy: msgid %d result %d, cash %d; want a refusal and 70 Cash", msgs[0].id, result(msgs[0].body), cash())
	}

	// Wear the beret: hc 70 names it as worn, with the rows and the thumbnail (its id and picture).
	msgs = skinRequest(t, 59, aid, now, 1, merry, 2, silver)
	checkButlerWire(t, "skinequip", msgs[0].body)
	if msgs[0].id != 70 || result(msgs[0].body) != 0 {
		t.Fatalf("wear: msgid %d result %d, want 70 and 0", msgs[0].id, result(msgs[0].body))
	}
	if worn, _ := protobufUintField(msgs[0].body, 2); worn != silver {
		t.Fatalf("equipSkinId %d, want %d", worn, silver)
	}
	thumb, _, _ := pbBytesField(msgs[0].body, 4)
	if id, _ := protobufUintField(thumb, 4); id != silver {
		t.Fatal("the thumbnail does not show the worn skin")
	}
	if list = pbAll(t, msgs[0].body, 3); len(list) != 15 {
		t.Fatalf("wear lists %d rows", len(list))
	}

	// The Garden chief spawns with the worn skin: ButlerInfo f5 (skinInfo) is the beret, its picture and its outfit.
	relay := gardenButlerRelay(aid, now)
	info, _, _ := pbBytesField(relay, 8)
	chiefSkin, _, _ := pbBytesField(info, 5)
	if id, _ := protobufUintField(chiefSkin, 1); id != silver {
		t.Fatalf("chief skin id %d, want the worn beret %d", id, silver)
	}
	if pic, _, _ := pbBytesField(chiefSkin, 3); string(pic) != "cherry/butler/skin/1900001601.png" {
		t.Fatalf("chief picture %q", pic)
	}
	d, _ := economy.ButlerByID(merry)
	if items := pbAll(t, chiefSkin, 5); len(items) != len(d.Skins[1].Outfit) {
		t.Fatalf("chief outfit has %d items, want the beret's %d", len(items), len(d.Skins[1].Outfit))
	}

	// Wear the default again: the chief shows the default skin and its picture route.
	skinRequest(t, 59, aid, now, 1, merry, 2, defaultSkin)
	relay = gardenButlerRelay(aid, now)
	info, _, _ = pbBytesField(relay, 8)
	chiefSkin, _, _ = pbBytesField(info, 5)
	if id, _ := protobufUintField(chiefSkin, 1); id != defaultSkin {
		t.Fatalf("after the default is worn, chief skin id %d, want %d", id, defaultSkin)
	}
	if pic, _, _ := pbBytesField(chiefSkin, 3); string(pic) != "cherry/butler/1900000006.png" {
		t.Fatalf("default chief picture %q", pic)
	}

	// An unowned skin is refused with the result alone.
	msgs = skinRequest(t, 59, aid, now, 1, merry, 2, silver+1)
	if msgs[0].id != 70 || result(msgs[0].body) != 1 {
		t.Fatalf("unowned wear: msgid %d result %d, want a refusal", msgs[0].id, result(msgs[0].body))
	}

	// The Voice Book: Merry's pack (revision 1, one zip record) and her 47 rows, each with its label and file path.
	msgs = skinRequest(t, 63, aid, now, 1, merry)
	checkButlerWire(t, "voice", msgs[0].body)
	if msgs[0].id != 74 || result(msgs[0].body) != 0 {
		t.Fatalf("voice book: msgid %d result %d, want 74 and 0", msgs[0].id, result(msgs[0].body))
	}
	info, _, _ = pbBytesField(msgs[0].body, 3)
	if rev, _ := protobufUintField(info, 1); rev != 1 {
		t.Fatalf("voice revision %d, want 1", rev)
	}
	record, _, _ := pbBytesField(info, 2)
	if url, _, _ := pbBytesField(record, 4); string(url) != "butler_voice_master/3/cherry_voice_v1.zip" {
		t.Fatalf("voice zip path %q", url)
	}
	scripts := pbAll(t, msgs[0].body, 4)
	if len(scripts) != 47 {
		t.Fatalf("voice book has %d rows, want 47", len(scripts))
	}
	first := scripts[0]
	if n, _ := protobufUintField(first, 1); n != 1 {
		t.Fatalf("row 0 key %d, want 1", n)
	}
	if label, _, _ := pbBytesField(first, 4); string(label) != "Voice 1" {
		t.Fatalf("row 0 label %q", label)
	}
	if text, _, _ := pbBytesField(first, 5); len(text) != 0 {
		t.Fatalf("row 0 text %q, want empty", text)
	}
	if path, _, _ := pbBytesField(first, 6); string(path) != "butler_voice_master/3/voice_001.ogg" {
		t.Fatalf("row 0 file %q", path)
	}
	last, _, _ := pbBytesField(scripts[46], 6)
	if string(last) != "butler_voice_master/3/voice_069.ogg" {
		t.Fatalf("last row file %q", last)
	}

	// Ophelia has no pack: result 0, the thumbnail and no voice record or rows.
	msgs = skinRequest(t, 63, aid, now, 1, ophelia)
	checkButlerWire(t, "voice", msgs[0].body)
	if result(msgs[0].body) != 0 || len(pbAll(t, msgs[0].body, 3)) != 0 || len(pbAll(t, msgs[0].body, 4)) != 0 {
		t.Fatal("a butler without voices has a voice record or rows")
	}
	if _, found, _ := pbBytesField(msgs[0].body, 2); !found {
		t.Fatal("a butler without voices has no thumbnail")
	}

	// An unknown butler is refused in every skin and voice request.
	for _, c := range []struct {
		msgid uint16
		reply uint16
		field []uint64
	}{{60, 71, []uint64{1, unknown}}, {61, 72, []uint64{1, unknown, 2, silver}}, {59, 70, []uint64{1, unknown, 2, silver}}, {63, 74, []uint64{1, unknown}}} {
		msgs = skinRequest(t, c.msgid, aid, now, c.field...)
		if msgs[0].id != c.reply || result(msgs[0].body) != 1 {
			t.Fatalf("req %d for an unknown butler: msgid %d result %d, want a refusal of %d", c.msgid, msgs[0].id, result(msgs[0].body), c.reply)
		}
	}
}

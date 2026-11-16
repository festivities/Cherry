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

// TestButlerMemoriesOnTheWire walks the Memories through the request path. The list (hc 80) serves any roster butler, hired or
// not: Merry's top message, picture, name and her two cards (id, title, open, list picture URL). Opening a memory (hc 81) gives
// its scenes (number, picture URL, the line, no voice), the butler's name and ButlerInfo wearing the default skin, or the worn
// skin when one is equipped. An unknown memory or butler is refused, and a butler without memories lists none.
func TestButlerMemoriesOnTheWire(t *testing.T) {
	now := time.Date(2026, 11, 16, 3, 0, 0, 0, time.UTC)
	logger := log.New(io.Discard, "", 0)
	const merry, ophelia, silver, unknown = 1900000006, 1900000004, 1900001601, 1900000050
	testutil.InstallAccounts(t, map[string]*store.Account{
		"bmem-3600": {AccessToken: "bmem-3600", SessionKey: "sk", Aid: "3600", Name: "Memo"},
		"bmem-3601": {AccessToken: "bmem-3601", SessionKey: "sk", Aid: "3601", Name: "Memo",
			Butlers: &store.ButlerBook{Skins: []store.ButlerSkinOwned{{Butler: merry, Skin: silver, Equipped: true}}}},
	})
	call := func(msgid uint16, aid uint64, fields ...uint64) []gardenMsg {
		var data []byte
		for i := 0; i+1 < len(fields); i += 2 {
			data = pbVar(data, fields[i], fields[i+1])
		}
		return gardenButlerReply(msgid, aid, data, now, logger)
	}
	num := func(body []byte, field uint64) uint64 {
		v, _ := protobufUintField(body, field)
		return v
	}
	str := func(body []byte, field uint64) string {
		v, _, _ := pbBytesField(body, field)
		return string(v)
	}
	result := func(msgs []gardenMsg) uint64 { return num(msgs[0].body, 1) }
	d, _ := economy.ButlerByID(merry)

	// The list for Merry, not hired: her message, picture (b3's default), name and the two cards.
	list := call(69, 3600, 1, merry)
	checkButlerWire(t, "memorylist", list[0].body)
	if list[0].id != 80 || num(list[0].body, 1) != merry {
		t.Fatalf("list: msgid %d butler %d, want 80 and %d", list[0].id, num(list[0].body, 1), merry)
	}
	if str(list[0].body, 2) != d.MemoryMessage || d.MemoryMessage == "" {
		t.Fatalf("list top message %q, want %q", str(list[0].body, 2), d.MemoryMessage)
	}
	if str(list[0].body, 4) != "cherry/butler/1900000006.png" || str(list[0].body, 5) != "Merry" {
		t.Fatalf("list picture %q and name %q", str(list[0].body, 4), str(list[0].body, 5))
	}
	rows := pbAll(t, list[0].body, 3)
	if len(rows) != 2 {
		t.Fatalf("list has %d cards, want 2", len(rows))
	}
	pictures := []string{"cherry/butler/memory/Merry_Thumbnail_1630072965.png", "cherry/butler/memory/admin_img_list_thum_1630075205.png"}
	for i, row := range rows {
		checkButlerWire(t, "memoryrow", row)
		if num(row, 1) != merry || num(row, 2) != uint64(i+1) || num(row, 7) != 1 || num(row, 8) != 1 {
			t.Fatalf("card %d: butler %d memory %d open %d can open %d", i, num(row, 1), num(row, 2), num(row, 7), num(row, 8))
		}
		if str(row, 3) != d.Memories[i].Title || str(row, 6) != pictures[i] {
			t.Fatalf("card %d: title %q picture %q", i, str(row, 3), str(row, 6))
		}
	}

	// Memory 1 as the default skin: result 0, the ids, one scene (number 1, the bedroom picture) with one line and no voice,
	// the name and ButlerInfo with the default skin (id 7, the portrait).
	open := call(70, 3600, 1, merry, 2, 1)
	checkButlerWire(t, "memoryopen", open[0].body)
	if open[0].id != 81 || result(open) != 0 || num(open[0].body, 2) != merry || num(open[0].body, 3) != 1 {
		t.Fatalf("open: msgid %d result %d butler %d memory %d", open[0].id, result(open), num(open[0].body, 2), num(open[0].body, 3))
	}
	scenes := pbAll(t, open[0].body, 4)
	if len(scenes) != 1 {
		t.Fatalf("memory 1 has %d scenes, want 1", len(scenes))
	}
	checkButlerWire(t, "memoryscene", scenes[0])
	if num(scenes[0], 3) != 1 || str(scenes[0], 4) != "cherry/butler/memory/Merry_1630072965.png" {
		t.Fatalf("scene number %d picture %q", num(scenes[0], 3), str(scenes[0], 4))
	}
	details := pbAll(t, scenes[0], 5)
	if len(details) != 1 {
		t.Fatalf("scene has %d lines, want 1", len(details))
	}
	checkButlerWire(t, "memoryline", details[0])
	if num(details[0], 1) != 1 || str(details[0], 3) != "Welcome to my room!" || str(details[0], 4) != "" {
		t.Fatalf("line: step %d text %q voice %q", num(details[0], 1), str(details[0], 3), str(details[0], 4))
	}
	if str(open[0].body, 5) != "Merry" {
		t.Fatalf("open name %q", str(open[0].body, 5))
	}
	info, _, _ := pbBytesField(open[0].body, 6)
	checkButlerWire(t, "info", info)
	if num(info, 1) != merry {
		t.Fatalf("ButlerInfo id %d, want %d", num(info, 1), merry)
	}
	if skin, _, _ := pbBytesField(info, 5); num(skin, 1) != 7 || str(skin, 3) != "cherry/butler/1900000006.png" {
		t.Fatalf("default skin %d picture %q", num(skin, 1), str(skin, 3))
	}

	// Memory 2 (the greenhouse) is the second card's memory: its picture and line.
	open = call(70, 3600, 1, merry, 2, 2)
	scenes = pbAll(t, open[0].body, 4)
	if result(open) != 0 || len(scenes) != 1 || str(scenes[0], 4) != "cherry/butler/memory/butler_memory_make_bg_1630075205.png" ||
		str(pbAll(t, scenes[0], 5)[0], 3) != "The roses are blooming today." {
		t.Fatalf("memory 2: result %d, scenes %d", result(open), len(scenes))
	}

	// With the silver skin equipped, ButlerInfo wears it: its id and its picture route.
	open = call(70, 3601, 1, merry, 2, 1)
	info, _, _ = pbBytesField(open[0].body, 6)
	if skin, _, _ := pbBytesField(info, 5); num(skin, 1) != silver || str(skin, 3) != "cherry/butler/skin/1900001601.png" {
		t.Fatalf("worn skin %d picture %q, want the silver beret", num(skin, 1), str(skin, 3))
	}

	// Refusals: an unknown memory and an unknown butler are hc 81 with result 1; an unknown butler's list is empty.
	for _, c := range []struct {
		name  string
		msgid uint16
		data  []uint64
	}{
		{"unknown memory", 70, []uint64{1, merry, 2, 9}},
		{"unknown butler", 70, []uint64{1, unknown, 2, 1}},
	} {
		msgs := call(c.msgid, 3600, c.data...)
		if msgs[0].id != 81 || result(msgs) != 1 {
			t.Fatalf("%s: msgid %d result %d, want a refusal of 81", c.name, msgs[0].id, result(msgs))
		}
	}
	if msgs := call(69, 3600, 1, unknown); msgs[0].id != 80 || len(msgs[0].body) != 0 {
		t.Fatalf("unknown butler's list: msgid %d, %d bytes, want an empty 80", msgs[0].id, len(msgs[0].body))
	}

	// Ophelia has no memories: her list has no cards, and opening one is refused.
	list = call(69, 3600, 1, ophelia)
	if list[0].id != 80 || num(list[0].body, 1) != ophelia || len(pbAll(t, list[0].body, 3)) != 0 {
		t.Fatal("a butler without memories lists cards")
	}
	if msgs := call(70, 3600, 1, ophelia, 2, 1); msgs[0].id != 81 || result(msgs) != 1 {
		t.Fatalf("opening a memory of a butler without any: result %d", result(msgs))
	}
}

package store

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// TestChatRoomStoreRoundTrip: the changes of a room (its creation, a message, a message that trims the oldest one, an ack)
// come back from the database after a restart as the book the changes made; each save counts once.
func TestChatRoomStoreRoundTrip(t *testing.T) {
	path := swapStore(t)
	book := ChatRoomBook{NextID: 1960000001}
	mem := func() ChatRoomBook { return book }
	before := ChatRoomWrites()
	save := func(ch ChatChange, next ChatRoomBook) {
		t.Helper()
		book = next
		if err := SaveChatRoomLocked(ch, mem); err != nil {
			t.Fatalf("save: %v", err)
		}
	}
	room := ChatRoom{ID: 1960000001, Aids: [2]string{"9981", "9982"}, Acks: map[string]int64{"9981": 0, "9982": 0}}
	save(ChatChange{Room: room, NextID: 1960000002}, ChatRoomBook{NextID: 1960000002, Rooms: []ChatRoom{room}})
	m1 := ChatMsg{Sn: 1, Sec: 1700000000, Sender: "9981", Text: "hi"}
	room.Ceil = 1
	save(ChatChange{Room: room, Msg: &m1, NextID: 1960000002}, ChatRoomBook{NextID: 1960000002, Rooms: []ChatRoom{{ID: room.ID, Aids: room.Aids, Ceil: 1, Acks: room.Acks, Msgs: []ChatMsg{m1}}}})
	m2 := ChatMsg{Sn: 2, Sec: 1700000001, Sender: "9982", Text: "yo"}
	room.Ceil, room.Acks = 2, map[string]int64{"9981": 0, "9982": 1}
	want := ChatRoomBook{NextID: 1960000002, Rooms: []ChatRoom{{ID: room.ID, Aids: room.Aids, Ceil: 2, Acks: room.Acks, Msgs: []ChatMsg{m2}}}}
	save(ChatChange{Room: room, Msg: &m2, TrimBelow: 2, NextID: 1960000002}, want)
	if n := ChatRoomWrites() - before; n != 3 {
		t.Fatalf("writes = %d, want one per save (3)", n)
	}

	if err := CloseStore(); err != nil {
		t.Fatal(err)
	}
	if err := OpenStore(path); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { CloseStore() })
	got, err := LoadChatRooms()
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("reloaded = %+v (err %v), want %+v", got, err, want)
	}
}

// TestChatRoomSaveRollsBack: a save that the database refuses returns the error and counts nothing; the caller restores
// its registry.
func TestChatRoomSaveRollsBack(t *testing.T) {
	swapStore(t)
	before := ChatRoomWrites()
	if err := AttachedDB().Close(); err != nil {
		t.Fatal(err)
	}
	room := ChatRoom{ID: 1960000001, Aids: [2]string{"9981", "9982"}}
	if err := SaveChatRoomLocked(ChatChange{Room: room, NextID: 1960000002}, func() ChatRoomBook { return ChatRoomBook{} }); err == nil {
		t.Fatal("a save with a closed database succeeded")
	}
	if ChatRoomWrites() != before {
		t.Fatal("a refused save counted as a write")
	}
}

// TestChatRoomStoreMemoryOnly: with no database a save writes nothing and the write count does not move.
func TestChatRoomStoreMemoryOnly(t *testing.T) {
	if err := CloseStore(); err != nil {
		t.Fatal(err)
	}
	before := ChatRoomWrites()
	if err := SaveChatRoomLocked(ChatChange{Room: ChatRoom{ID: 1960000001}, NextID: 1}, func() ChatRoomBook { return ChatRoomBook{} }); err != nil {
		t.Fatalf("save without a database: %v", err)
	}
	if ChatRoomWrites() != before {
		t.Fatal("a save without a database counted as a write")
	}
}

// TestChatFileParser: the importer's reader of chatrooms.json: a missing file is an empty registry, and a file that is
// corrupt or has another version is refused.
func TestChatFileParser(t *testing.T) {
	path := filepath.Join(t.TempDir(), "chatrooms.json")
	if book, err := parseChatFile(path); err != nil || len(book.Rooms) != 0 {
		t.Fatalf("absent file: book %+v err %v", book, err)
	}
	if err := os.WriteFile(path, []byte(`{"version":2,"nextId":0,"rooms":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := parseChatFile(path); err == nil {
		t.Fatal("version 2 accepted")
	}
	if err := os.WriteFile(path, []byte(`{"version":1,"nextId":`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := parseChatFile(path); err == nil {
		t.Fatal("corrupt file accepted")
	}
}

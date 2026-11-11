package store

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// TestChatRoomStoreRoundTrip: a missing chatrooms.json is an empty registry; a saved registry loads back unchanged; a file
// that is corrupt or has another version is refused.
func TestChatRoomStoreRoundTrip(t *testing.T) {
	t.Cleanup(ResetChatRooms)
	path := filepath.Join(t.TempDir(), "chatrooms.json")
	book, err := LoadChatRoomsFrom(path)
	if err != nil || len(book.Rooms) != 0 || book.NextID != 0 {
		t.Fatalf("absent file: book %+v err %v", book, err)
	}
	if ChatRoomsStorePath != path {
		t.Fatalf("save target = %q, want %q", ChatRoomsStorePath, path)
	}

	want := ChatRoomBook{NextID: 1960000002, Rooms: []ChatRoom{{
		ID: 1960000001, Aids: [2]string{"9981", "9982"}, Ceil: 200, Acks: map[string]int64{"9981": 0, "9982": 7},
		Msgs: []ChatMsg{{Sn: 199, Sec: 1700000000, Sender: "9981", Text: "hi"}, {Sn: 200, Sec: 1700000001, Sender: "9982", Text: "yo"}},
	}}}
	if err := SaveChatRooms(want); err != nil {
		t.Fatalf("save: %v", err)
	}
	got, err := LoadChatRoomsFrom(path)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("reloaded = %+v (err %v), want %+v", got, err, want)
	}

	if err := os.WriteFile(path, []byte(`{"version":2,"nextId":0,"rooms":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadChatRoomsFrom(path); err == nil {
		t.Fatal("version 2 accepted")
	}
	if err := os.WriteFile(path, []byte(`{"version":1,"nextId":`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadChatRoomsFrom(path); err == nil {
		t.Fatal("corrupt file accepted")
	}
}

// TestChatRoomStoreMemoryOnly: with no save target a save writes nothing and the write count does not move.
func TestChatRoomStoreMemoryOnly(t *testing.T) {
	ResetChatRooms()
	before := ChatRoomWrites()
	if err := SaveChatRooms(ChatRoomBook{NextID: 1}); err != nil {
		t.Fatalf("save without a target: %v", err)
	}
	if ChatRoomWrites() != before {
		t.Fatal("a save without a target counted as a write")
	}
}

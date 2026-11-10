package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// Chat rooms (1:1 friend chat, lpn/avatarchat.go): the room registry in %LOCALAPPDATA%\Cherry\chatrooms.json beside
// circles.json. It holds only what must never repeat after a restart: each room's id and members, its serial ceiling and
// each member's last ack, and the next room id. Message text is not stored. Absence means no rooms. The rules live in
// lpn; this file only holds the registry and saves it.

// ChatRoom is one room. Aids are its two members in ascending order, as decimal strings. Ceil is the serial ceiling:
// serials up to it are reserved. Acks maps each member's aid to its last acked serial.
type ChatRoom struct {
	ID   int64            `json:"id"`
	Aids [2]string        `json:"aids"`
	Ceil int64            `json:"ceil"`
	Acks map[string]int64 `json:"acks,omitempty"`
}

// ChatRoomBook is the whole registry: the next room id to hand out and the rooms.
type ChatRoomBook struct {
	NextID int64      `json:"nextId"`
	Rooms  []ChatRoom `json:"rooms"`
}

type savedChatRooms struct {
	Version int `json:"version"`
	ChatRoomBook
}

var (
	// ChatRoomsMu guards ChatRoomsStorePath and the write count. It is a leaf lock: nothing else is taken while it is held.
	ChatRoomsMu sync.Mutex
	// ChatRoomsStorePath is chatrooms.json ("" in tests and memory-only runs: nothing is written).
	ChatRoomsStorePath string
	chatRoomWrites     int
)

// LoadChatRooms reads %LOCALAPPDATA%\Cherry\chatrooms.json (none is not an error) and returns the registry.
func LoadChatRooms() (ChatRoomBook, error) {
	dir, err := os.UserCacheDir()
	if err != nil {
		return ChatRoomBook{}, err
	}
	return LoadChatRoomsFrom(filepath.Join(dir, "Cherry", "chatrooms.json"))
}

// LoadChatRoomsFrom reads the registry at path and makes path the save target. A missing file is an empty registry; a
// file that is not valid JSON or has another version is an error and leaves the save target unchanged.
func LoadChatRoomsFrom(path string) (ChatRoomBook, error) {
	data, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return ChatRoomBook{}, err
	}
	var book ChatRoomBook
	if err == nil {
		var saved savedChatRooms
		if err := json.Unmarshal(data, &saved); err != nil {
			return ChatRoomBook{}, fmt.Errorf("invalid chat room store: %w", err)
		}
		if saved.Version != 1 {
			return ChatRoomBook{}, errors.New("invalid chat room store version")
		}
		book = saved.ChatRoomBook
	}
	ChatRoomsMu.Lock()
	ChatRoomsStorePath = path
	ChatRoomsMu.Unlock()
	return book, nil
}

// ResetChatRooms forgets the save target (tests and memory-only runs).
func ResetChatRooms() {
	ChatRoomsMu.Lock()
	ChatRoomsStorePath = ""
	ChatRoomsMu.Unlock()
}

// ChatRoomWrites is how many times the registry file has been written since start (tests).
func ChatRoomWrites() int {
	ChatRoomsMu.Lock()
	defer ChatRoomsMu.Unlock()
	return chatRoomWrites
}

// SaveChatRooms writes book to chatrooms.json (temp file, fsync, then rename). Nothing is written when the save target is
// "". The caller holds its own lock (lpn's avatarChatMu); this takes only ChatRoomsMu.
func SaveChatRooms(book ChatRoomBook) error {
	ChatRoomsMu.Lock()
	defer ChatRoomsMu.Unlock()
	if ChatRoomsStorePath == "" {
		return nil
	}
	data, err := json.Marshal(savedChatRooms{Version: 1, ChatRoomBook: book})
	if err != nil {
		return err
	}
	dir := filepath.Dir(ChatRoomsStorePath)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".chatrooms-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err = tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err = tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	if err = os.Rename(tmp.Name(), ChatRoomsStorePath); err != nil {
		return err
	}
	chatRoomWrites++
	return nil
}

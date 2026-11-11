package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"sync"
	"testing"
	"time"

	"cherry/internal/metrics"
)

// Chat rooms (1:1 friend chat, lpn/avatarchat.go): the room registry in the cherry.db tables chat_rooms, chat_messages and
// meta (chat.nextId). It holds each room's id and members, its newest serial, each member's last ack and delete mark, the
// room's newest messages, and the next room id. The rules and the in-memory registry live in lpn; this file holds the row
// types, saves one change per call, and reads the registry at start. chatrooms.json is read only by cmd/cherry-import.

// ChatRoom is one room. Aids are its two members in ascending order, as decimal strings. Ceil is the room's newest serial
// as of the save, so a restart resumes there (files from the chunked registry keep a serial ceiling in this field, which
// a restart resumes at too). Acks maps each member's aid to its last acked serial. Hid maps a member who deleted the room
// from their talk list to the newest serial at that time (the room returns to that list at a newer serial). Msgs are the
// newest messages, oldest first; a file without them loads with none.
type ChatRoom struct {
	ID   int64            `json:"id"`
	Aids [2]string        `json:"aids"`
	Ceil int64            `json:"ceil"`
	Acks map[string]int64 `json:"acks,omitempty"`
	Hid  map[string]int64 `json:"hid,omitempty"`
	Msgs []ChatMsg        `json:"msgs,omitempty"`
}

// ChatMsg is one stored message: its serial, unix seconds, sender aid (decimal string) and text. The text is private: it is
// never logged.
type ChatMsg struct {
	Sn     int64  `json:"sn"`
	Sec    int64  `json:"sec"`
	Sender string `json:"sender"`
	Text   string `json:"text"`
}

// ChatRoomBook is the whole registry: the next room id to hand out and the rooms.
type ChatRoomBook struct {
	NextID int64      `json:"nextId"`
	Rooms  []ChatRoom `json:"rooms"`
}

// ChatChange names what one chat save changed: the room's row (its members, newest serial, acks and delete marks; the
// Msgs of Room are not written), at most one message added at the end of the room, the room's messages with a serial below
// TrimBelow removed (0: none), and the next room id.
type ChatChange struct {
	Room      ChatRoom
	Msg       *ChatMsg
	TrimBelow int64
	NextID    int64
}

// savedChatRooms is the chatrooms.json file as the server used to write it (cmd/cherry-import reads it).
type savedChatRooms struct {
	Version int `json:"version"`
	ChatRoomBook
}

var (
	// ChatRoomsMu guards chatRoomWrites. It is a leaf lock: nothing else is taken while it is held.
	ChatRoomsMu    sync.Mutex
	chatRoomWrites int
)

// LoadChatRooms reads the registry of the attached database (none attached: an empty registry). lpn calls it at start.
func LoadChatRooms() (ChatRoomBook, error) {
	d := attachedDB()
	if d == nil {
		return ChatRoomBook{}, nil
	}
	var b ChatRoomBook
	err := d.View(func(q Querier) error {
		var err error
		b, err = readChatRooms(q)
		return err
	})
	return b, err
}

// parseChatFile reads a chatrooms.json the way the server used to load it, for cmd/cherry-import and the startup guard. A
// missing file is an empty registry; a file that is not valid JSON or has another version is an error.
func parseChatFile(path string) (ChatRoomBook, error) {
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
	return book, nil
}

// ChatRoomWrites is how many chat saves have committed to the attached database since start (tests).
func ChatRoomWrites() int {
	ChatRoomsMu.Lock()
	defer ChatRoomsMu.Unlock()
	return chatRoomWrites
}

// SaveChatRoomLocked writes one chat change in ONE transaction. Without an attached database it writes nothing. The caller
// holds its own lock (lpn's avatarChatMu) and has changed its registry; on error the caller restores it, and the serial
// stays taken, so no serial is ever handed out twice. memory returns the registry as lpn holds it after the change; test
// builds compare the database with it (dbnet.go), production never calls it.
func SaveChatRoomLocked(ch ChatChange, memory func() ChatRoomBook) error {
	defer metrics.SaveSince(time.Now())
	d := attachedDB()
	if d == nil {
		return nil
	}
	site := callerSite()
	if err := d.Update(func(tx *sql.Tx) error { return saveChatRowsTx(tx, ch) }); err != nil {
		return err
	}
	ChatRoomsMu.Lock()
	chatRoomWrites++
	ChatRoomsMu.Unlock()
	if testing.Testing() {
		netCheckChat(d, memory(), site)
	}
	return nil
}

// saveChatRowsTx writes one chat change inside the caller's transaction.
func saveChatRowsTx(tx *sql.Tx, ch ChatChange) error {
	room := ch.Room
	room.Msgs = nil // the messages are rows of their own
	data, err := rowJSON(room)
	if err != nil {
		return err
	}
	if err := upsertRow(tx, "chat_rooms", slotBack, []string{"room_id", "aid_a", "aid_b", "data"},
		[]any{room.ID, room.Aids[0], room.Aids[1], data}, "room_id = ?", room.ID); err != nil {
		return err
	}
	if m := ch.Msg; m != nil {
		if _, err := tx.Exec(`INSERT INTO chat_messages (room_id, sn, sec, sender, text) VALUES (?, ?, ?, ?, ?)`,
			room.ID, m.Sn, m.Sec, m.Sender, m.Text); err != nil {
			return err
		}
	}
	if ch.TrimBelow > 0 {
		if _, err := tx.Exec(`DELETE FROM chat_messages WHERE room_id = ? AND sn < ?`, room.ID, ch.TrimBelow); err != nil {
			return err
		}
	}
	return setMetaTx(tx, "chat.nextId", strconv.FormatInt(ch.NextID, 10))
}

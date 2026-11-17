package main

import (
	"bytes"
	"database/sql"
	"log"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cherry/internal/config"
	"cherry/internal/store"
)

// TestSweepPrunesOldChatMessages: the hourly sweep deletes the chat messages older than retention.chat-days and keeps the
// newer ones, and says how many it deleted.
func TestSweepPrunesOldChatMessages(t *testing.T) {
	base := t.TempDir()
	t.Setenv("LOCALAPPDATA", base)
	t.Setenv("XDG_CACHE_HOME", base)
	path := filepath.Join(base, store.DBFileName)
	if err := store.OpenStore(path); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.CloseStore() })

	// A second handle on the same file writes the old row (the game path only writes the present).
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	old := time.Now().UTC().AddDate(0, 0, -45).UnixMilli()
	if _, err := raw.Exec(`INSERT INTO chat_log (time, kind, channel, sender, sender_name, text) VALUES (?, 'private', '1960000000', '9001', 'Old', 'long ago')`, old); err != nil {
		t.Fatal(err)
	}

	var logs bytes.Buffer
	logger := log.New(&logs, "", 0)
	store.StartChatLog()
	t.Cleanup(store.StopChatLog)
	store.RecordChat(store.ChatEntry{Kind: store.ChatSquare, Channel: "7", Sender: "9002", SenderName: "New", Text: "just now"})
	store.FlushChatLog()

	sweep(logger, config.Config{ChatDays: 30, IdleTokenDays: 30, DeletedDays: 30})
	rows, err := store.ChatLogSearch(store.ChatFilter{}, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Text != "just now" {
		t.Fatalf("rows after the sweep = %d (first %q), want the one new message", len(rows), first(rows))
	}
	if !strings.Contains(logs.String(), "chat log deleted 1 messages older than 30 days") {
		t.Fatalf("the sweep did not report its deletion: %q", logs.String())
	}
}

// TestSweepUsesThePanelRetention: a retention set in the staff panel replaces retention.chat-days. A 10-day-old message that
// the configured 30 days keeps goes once the panel sets 7, and the sweep says so with the panel's number.
func TestSweepUsesThePanelRetention(t *testing.T) {
	base := t.TempDir()
	t.Setenv("LOCALAPPDATA", base)
	t.Setenv("XDG_CACHE_HOME", base)
	path := filepath.Join(base, store.DBFileName)
	if err := store.OpenStore(path); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.CloseStore() })
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	ten := time.Now().UTC().AddDate(0, 0, -10).UnixMilli()
	if _, err := raw.Exec(`INSERT INTO chat_log (time, kind, channel, sender, sender_name, text) VALUES (?, 'private', '1960000000', '9001', 'Ten', 'ten days old')`, ten); err != nil {
		t.Fatal(err)
	}

	var logs bytes.Buffer
	logger := log.New(&logs, "", 0)
	cfg := config.Config{ChatDays: 30, IdleTokenDays: 30, DeletedDays: 30}
	sweep(logger, cfg)
	rows, err := store.ChatLogSearch(store.ChatFilter{}, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("the configured 30 days kept %d messages, want the 10-day-old one", len(rows))
	}

	if err := store.SetChatRetention(7, "dev-owner"); err != nil {
		t.Fatal(err)
	}
	logs.Reset()
	sweep(logger, cfg)
	rows, err = store.ChatLogSearch(store.ChatFilter{}, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Fatalf("the panel's 7 days kept %d messages, want none", len(rows))
	}
	if !strings.Contains(logs.String(), "chat log deleted 1 messages older than 7 days") {
		t.Fatalf("the sweep did not report the panel's number: %q", logs.String())
	}
}

// first is the text of the first row, for a failure message.
func first(rows []store.ChatLogRow) string {
	if len(rows) == 0 {
		return ""
	}
	return rows[0].Text
}

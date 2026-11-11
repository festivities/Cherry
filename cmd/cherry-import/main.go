// Command cherry-import copies a Cherry data directory's JSON stores (accounts.json, ledger.jsonl, social.json,
// circles.json, treasure.json, chatrooms.json) into a new SQLite database (cherry.db), then reads the database back and
// verifies it. It never modifies the data directory. Lab tool (slice 8.3b part A); the server does not use the database yet.
//
//	cherry-import -data-dir <dir> [-db <file>] [-force]
//
// It prints counts and "verified", or the first difference by store and key (never account, token or message content).
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"cherry/internal/store"
)

func main() {
	dataDir := flag.String("data-dir", "", "data directory holding the JSON stores (required)")
	dbFile := flag.String("db", "", "database file to create (default: "+store.DBFileName+" in -data-dir)")
	force := flag.Bool("force", false, "replace an existing non-empty database")
	flag.Parse()
	if *dataDir == "" {
		fmt.Fprintln(os.Stderr, "cherry-import: -data-dir is required")
		flag.Usage()
		os.Exit(2)
	}
	if fi, err := os.Stat(*dataDir); err != nil || !fi.IsDir() {
		fail("data directory is not a directory")
	}
	path := *dbFile
	if path == "" {
		path = filepath.Join(*dataDir, store.DBFileName)
	}
	if err := store.PrepareDBPath(path, *force); err != nil {
		fail(err.Error())
	}
	db, err := store.OpenDB(path)
	if err != nil {
		fail("open database: " + err.Error())
	}
	rep, err := store.ImportDir(db, *dataDir)
	if cerr := db.Close(); err == nil && cerr != nil {
		err = cerr // closed here so the WAL is checkpointed: fail() exits without deferred calls
	}
	fmt.Printf("accounts %d (tokens %d)\n", rep.Accounts, rep.Tokens)
	fmt.Printf("ledger %d lines\n", rep.LedgerLines)
	fmt.Printf("social: diary posts %d, comments %d, likes %d; guestbook entries %d, replies %d, read markers %d; friendships %d\n",
		rep.DiaryPosts, rep.DiaryComments, rep.DiaryLikes, rep.GuestbookEntries, rep.GuestbookReplies, rep.GuestbookRead, rep.Friendships)
	fmt.Printf("circles %d (leaves %d)\n", rep.Circles, rep.CircleLeaves)
	fmt.Printf("treasure %d records\n", rep.TreasureRecords)
	fmt.Printf("chat: rooms %d, messages %d\n", rep.ChatRooms, rep.ChatMessages)
	if err != nil {
		fail("NOT verified: " + err.Error())
	}
	fmt.Println("verified")
}

func fail(msg string) {
	fmt.Fprintln(os.Stderr, "cherry-import: "+msg)
	os.Exit(1)
}

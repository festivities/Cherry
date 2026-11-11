// Command cherry-import copies a Cherry data directory's JSON stores (accounts.json, ledger.jsonl, social.json,
// circles.json, treasure.json, chatrooms.json) into SQLite (cherry.db), then reads the database back and verifies it. It
// never modifies the data directory. Lab tool (PLAN Roadmap 8, slice 8.3b).
//
//	cherry-import -data-dir <dir> [-db <file>] [-force]
//	cherry-import -data-dir <dir> [-db <file>] -stores social,circles,treasure,chat
//
// The first form creates the database from the whole directory. A database that holds accounts is never replaced by it,
// not even with -force. The second re-imports only the named JSON-backed stores into that existing database: their rows
// are replaced, and the accounts, tokens, ledger and account meta keys are left as they are.
//
// It prints counts and "verified", or the first difference by store and key (never account, token or message content).
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"cherry/internal/store"
)

func main() {
	dataDir := flag.String("data-dir", "", "data directory holding the JSON stores (required)")
	dbFile := flag.String("db", "", "database file (default: "+store.DBFileName+" in -data-dir)")
	force := flag.Bool("force", false, "replace an existing database that holds no accounts (never one that does)")
	stores := flag.String("stores", "", "re-import only these stores, comma-separated: social, circles, treasure, chat (the database must hold accounts)")
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
	if *stores != "" {
		importStores(path, *dataDir, strings.Split(*stores, ","))
		return
	}
	refuseLiveDatabase(path)
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
	printCounts(rep, nil)
	if err != nil {
		fail("NOT verified: " + err.Error())
	}
	fmt.Println("verified")
}

// importStores re-imports the named stores into the existing database at path.
func importStores(path, dir string, names []string) {
	if fi, err := os.Stat(path); err != nil || fi.IsDir() {
		fail("no database at " + path + ": create it with a full import first")
	}
	for i := range names {
		names[i] = strings.TrimSpace(names[i])
	}
	db, err := store.OpenDB(path)
	if err != nil {
		fail("open database: " + err.Error())
	}
	rep, err := store.ImportStores(db, dir, names)
	if cerr := db.Close(); err == nil && cerr != nil {
		err = cerr
	}
	only := map[string]bool{}
	for _, n := range names {
		only[n] = true
	}
	printCounts(rep, only)
	if err != nil {
		fail("NOT verified: " + err.Error())
	}
	fmt.Println("verified")
}

// refuseLiveDatabase stops a full import over a database that holds accounts: those are re-imported with -stores only.
func refuseLiveDatabase(path string) {
	if _, err := os.Stat(path); err != nil {
		return
	}
	db, err := store.OpenDB(path)
	if err != nil {
		fail("open database: " + err.Error())
	}
	n, err := store.CountAccounts(db)
	if cerr := db.Close(); err == nil && cerr != nil {
		err = cerr
	}
	if err != nil {
		fail("count accounts: " + err.Error())
	}
	if n > 0 {
		fail("the database holds accounts: a full import does not replace them; re-import the JSON stores with -stores")
	}
}

// printCounts prints the counts of the stores in only (all of them when nil).
func printCounts(rep store.ImportReport, only map[string]bool) {
	want := func(name string) bool { return only == nil || only[name] }
	if want("accounts") {
		fmt.Printf("accounts %d (tokens %d)\n", rep.Accounts, rep.Tokens)
		fmt.Printf("ledger %d lines\n", rep.LedgerLines)
	}
	if want("social") {
		fmt.Printf("social: diary posts %d, comments %d, likes %d; guestbook entries %d, replies %d, read markers %d; friendships %d\n",
			rep.DiaryPosts, rep.DiaryComments, rep.DiaryLikes, rep.GuestbookEntries, rep.GuestbookReplies, rep.GuestbookRead, rep.Friendships)
	}
	if want("circles") {
		fmt.Printf("circles %d (leaves %d)\n", rep.Circles, rep.CircleLeaves)
	}
	if want("treasure") {
		fmt.Printf("treasure %d records\n", rep.TreasureRecords)
	}
	if want("chat") {
		fmt.Printf("chat: rooms %d, messages %d\n", rep.ChatRooms, rep.ChatMessages)
	}
}

func fail(msg string) {
	fmt.Fprintln(os.Stderr, "cherry-import: "+msg)
	os.Exit(1)
}

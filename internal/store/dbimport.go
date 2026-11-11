package store

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strings"
)

// The importer behind cmd/cherry-import: it parses a data directory's JSON stores with the parsers the server used to load
// them with, writes them to a database (one transaction per store), reads the database back and compares. It never writes
// the data directory and never touches the process-wide stores.

// StoreNames are the stores that still live in JSON files and can be re-imported into a live database (cherry-import
// -stores). The accounts, tokens and ledger are not among them: they are written by the server.
var StoreNames = []string{"social", "circles", "treasure", "chat"}

// storeFiles are the JSON file of each store in StoreNames.
var storeFiles = map[string]string{"social": "social.json", "circles": "circles.json", "treasure": "treasure.json", "chat": "chatrooms.json"}

// storeTables are the tables each store's rows live in (CheckImported asks whether they are empty).
var storeTables = map[string][]string{
	"social":   {"diary_posts", "diary_comments", "diary_likes", "guestbook_entries", "friendships", "guestbook_read"},
	"circles":  {"circles"},
	"treasure": {"treasure_records"},
	"chat":     {"chat_rooms"},
}

// ImportReport counts what ImportDir loaded and wrote.
type ImportReport struct {
	Accounts, Tokens, LedgerLines                                   int
	DiaryPosts, DiaryComments, DiaryLikes, GuestbookEntries         int
	GuestbookReplies, GuestbookRead, Friendships                    int
	Circles, CircleLeaves, TreasureRecords, ChatRooms, ChatMessages int
}

// loadedDir is a data directory's stores as the server holds them after loading.
type loadedDir struct {
	accounts     map[string]*Account // by clear access token (own and aliases)
	latest       *Account
	nextAvatarID uint64
	ledger       []LedgerLine
	social       savedSocial
	circles      CircleBook
	treasure     map[string]TreasureRecord
	chat         ChatRoomBook
}

// ImportDir writes the stores of dir to d and verifies the result: the database is read back and compared with what the
// loaders produced. The error names the first difference by store and by JSON key or index, never by content.
func ImportDir(d *DB, dir string) (ImportReport, error) {
	in, err := loadDataDir(dir)
	if err != nil {
		return ImportReport{}, err
	}
	rep := in.report()
	steps := []struct {
		name string
		fn   func(tx *sql.Tx) error
	}{
		{"accounts", func(tx *sql.Tx) error { return writeAccountsTx(tx, in) }},
		{"ledger", func(tx *sql.Tx) error { return AppendLedgerTx(tx, in.ledger) }},
		{"social", func(tx *sql.Tx) error { return writeSocialTx(tx, in.social) }},
		{"circles", func(tx *sql.Tx) error { return writeCirclesTx(tx, in.circles) }},
		{"treasure", func(tx *sql.Tx) error { return writeTreasureTx(tx, in.treasure) }},
		{"chat rooms", func(tx *sql.Tx) error { return writeChatRoomsTx(tx, in.chat) }},
	}
	for _, s := range steps {
		if err := d.Update(s.fn); err != nil {
			return rep, fmt.Errorf("writing %s: %w", s.name, err)
		}
	}
	if err := verifyDB(d, in); err != nil {
		return rep, err
	}
	return rep, nil
}

// PrepareDBPath makes path ready for a new database. A missing file is fine, an empty one is replaced. An existing
// non-empty database is refused unless force, which removes it with its -wal and -shm files.
func PrepareDBPath(path string, force bool) error {
	fi, err := os.Stat(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err != nil { // missing: stale WAL files beside it would be replayed into the new database
		for _, suffix := range []string{"-wal", "-shm"} {
			if _, serr := os.Stat(path + suffix); serr == nil {
				return errors.New("a WAL or shared-memory file is left without its database; remove it first")
			}
		}
		return nil
	}
	if fi.Size() > 0 && !force {
		return errors.New("the database exists and is not empty; use -force to replace it")
	}
	for _, p := range []string{path, path + "-wal", path + "-shm"} {
		if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}

// loadDataDir parses the files of dir (a missing file is an empty store) and returns what the server would hold.
func loadDataDir(dir string) (loadedDir, error) {
	var in loadedDir
	f, err := readAccountFile(filepath.Join(dir, "accounts.json"))
	if err != nil {
		return in, loadError("accounts.json", err)
	}
	in.accounts, in.latest, in.nextAvatarID = f.byToken, f.latest, f.nextID
	if err := in.loadStores(dir, StoreNames); err != nil {
		return in, err
	}
	in.ledger, err = readLedgerFile(filepath.Join(dir, "ledger.jsonl"))
	return in, err
}

// loadStores parses the JSON files of the named stores of dir into in.
func (in *loadedDir) loadStores(dir string, names []string) error {
	var err error
	for _, name := range names {
		file := storeFiles[name]
		switch name {
		case "social":
			in.social, err = parseSocialFile(filepath.Join(dir, file))
		case "circles":
			in.circles, err = parseCirclesFile(filepath.Join(dir, file))
			in.circles.Circles = nonNilList(in.circles.Circles)
		case "treasure":
			in.treasure, err = parseTreasureFile(filepath.Join(dir, file))
		case "chat":
			in.chat, err = parseChatFile(filepath.Join(dir, file))
			in.chat.Rooms = nonNilList(in.chat.Rooms)
		}
		if err != nil {
			return loadError(file, err)
		}
	}
	return nil
}

// CountAccounts is the number of account rows of d (cmd/cherry-import refuses a full import over a database that has any).
func CountAccounts(d *DB) (int, error) {
	var n int
	err := d.View(func(q Querier) error {
		return q.QueryRow(`SELECT COUNT(*) FROM accounts`).Scan(&n)
	})
	return n, err
}

// ImportStores replaces the rows of the named stores (StoreNames) of the existing database d with the JSON files in dir, one
// transaction per store, then reads those stores back and compares them with what was parsed. The database must hold
// accounts (it is a live server database); the accounts, tokens, ledger and the account meta keys are not touched.
func ImportStores(d *DB, dir string, names []string) (ImportReport, error) {
	var rep ImportReport
	want := map[string]bool{}
	for _, n := range names {
		if _, ok := storeFiles[n]; !ok {
			return rep, fmt.Errorf("unknown store %q", n)
		}
		want[n] = true
	}
	accounts, err := CountAccounts(d)
	if err != nil {
		return rep, err
	}
	if accounts == 0 {
		return rep, errors.New("the database holds no accounts; a database with no accounts is imported without -stores")
	}
	var in loadedDir
	var selected []string
	for _, n := range StoreNames {
		if want[n] {
			selected = append(selected, n)
		}
	}
	if err := in.loadStores(dir, selected); err != nil {
		return rep, err
	}
	full := in.report()
	for _, n := range selected {
		write := storeWriter(in, n)
		if err := d.Update(write); err != nil {
			return rep, fmt.Errorf("writing %s: %w", n, err)
		}
		rep.set(n, full)
	}
	if err := verifyStores(d, in, want); err != nil {
		return rep, err
	}
	return rep, nil
}

// storeWriter is the replace writer of one store of a parsed directory.
func storeWriter(in loadedDir, name string) func(tx *sql.Tx) error {
	switch name {
	case "social":
		return func(tx *sql.Tx) error { return writeSocialTx(tx, in.social) }
	case "circles":
		return func(tx *sql.Tx) error { return writeCirclesTx(tx, in.circles) }
	case "treasure":
		return func(tx *sql.Tx) error { return writeTreasureTx(tx, in.treasure) }
	}
	return func(tx *sql.Tx) error { return writeChatRoomsTx(tx, in.chat) }
}

// set copies the counts of one store from the full report into r.
func (r *ImportReport) set(name string, full ImportReport) {
	switch name {
	case "social":
		r.DiaryPosts, r.DiaryComments, r.DiaryLikes = full.DiaryPosts, full.DiaryComments, full.DiaryLikes
		r.GuestbookEntries, r.GuestbookReplies, r.GuestbookRead, r.Friendships = full.GuestbookEntries, full.GuestbookReplies, full.GuestbookRead, full.Friendships
	case "circles":
		r.Circles, r.CircleLeaves = full.Circles, full.CircleLeaves
	case "treasure":
		r.TreasureRecords = full.TreasureRecords
	case "chat":
		r.ChatRooms, r.ChatMessages = full.ChatRooms, full.ChatMessages
	}
}

// verifyStores reads the wanted stores back in one snapshot and compares them with the parsed input.
func verifyStores(d *DB, in loadedDir, want map[string]bool) error {
	return d.View(func(q Querier) error {
		if want["social"] {
			social, err := readSocial(q)
			if err != nil {
				return err
			}
			if err := checkJSON("social", in.social, social); err != nil {
				return err
			}
		}
		if want["circles"] {
			circles, err := readCircles(q)
			if err != nil {
				return err
			}
			if err := checkJSON("circles", in.circles, circles); err != nil {
				return err
			}
		}
		if want["treasure"] {
			treasure, err := readTreasure(q)
			if err != nil {
				return err
			}
			if err := checkJSON("treasure", in.treasure, treasure); err != nil {
				return err
			}
		}
		if want["chat"] {
			chat, err := readChatRooms(q)
			if err != nil {
				return err
			}
			return checkJSON("chat rooms", in.chat, chat)
		}
		return nil
	})
}

// CheckImported is the startup guard of the stores that still have JSON files: when the attached database holds no rows of
// a store but that store's file in dir holds rows, the server refuses to start rather than run with the store empty. The
// error names the cherry-import command that loads it. A directory without those files starts empty. Call it after OpenStore.
func CheckImported(dir string) error {
	d := attachedDB()
	if d == nil {
		return nil
	}
	var names []string
	for _, name := range StoreNames {
		empty := true
		if err := d.View(func(q Querier) error {
			for _, table := range storeTables[name] {
				var n int
				if err := q.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&n); err != nil {
					return err
				}
				if n > 0 {
					empty = false
				}
			}
			return nil
		}); err != nil {
			return err
		}
		if !empty {
			continue
		}
		var in loadedDir
		if err := in.loadStores(dir, []string{name}); err != nil {
			return err
		}
		if in.storeRows(name) > 0 {
			names = append(names, name)
		}
	}
	if len(names) == 0 {
		return nil
	}
	return fmt.Errorf("the database has no rows for %s but %s holds them: run cherry-import -data-dir %s -stores %s",
		strings.Join(names, ", "), dir, dir, strings.Join(names, ","))
}

// storeRows is how many rows a parsed store holds.
func (in loadedDir) storeRows(name string) int {
	switch name {
	case "social":
		return in.social.rows()
	case "circles":
		return len(in.circles.Circles) + len(in.circles.Leaves)
	case "treasure":
		return len(in.treasure)
	}
	return len(in.chat.Rooms)
}

// loadError names the file that failed to load. JSON syntax and type errors quote the offending byte, so they give only
// the offset: the output of the importer must never show file content.
func loadError(file string, err error) error {
	var se *json.SyntaxError
	if errors.As(err, &se) {
		return fmt.Errorf("%s: invalid JSON at byte %d", file, se.Offset)
	}
	var te *json.UnmarshalTypeError
	if errors.As(err, &te) {
		return fmt.Errorf("%s: a value has the wrong JSON type at byte %d", file, te.Offset)
	}
	return fmt.Errorf("%s: %w", file, err)
}

func nonNilList[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}

// readLedgerFile parses ledger.jsonl: one ledger line per non-empty line. A missing file has no lines.
func readLedgerFile(path string) ([]LedgerLine, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var lines []LedgerLine
	for i, raw := range bytes.Split(data, []byte("\n")) {
		if len(bytes.TrimSpace(raw)) == 0 {
			continue
		}
		var l LedgerLine
		if err := json.Unmarshal(raw, &l); err != nil {
			return nil, fmt.Errorf("ledger.jsonl line %d is not a ledger line", i+1)
		}
		lines = append(lines, l)
	}
	return lines, nil
}

// ownAccounts lists each account once, through its own access token, sorted by aid then token.
func (in loadedDir) ownAccounts() []*Account {
	var own []*Account
	for token, acc := range in.accounts {
		if token == acc.AccessToken {
			own = append(own, acc)
		}
	}
	slices.SortFunc(own, func(a, b *Account) int {
		if c := strings.Compare(a.Aid, b.Aid); c != 0 {
			return c
		}
		return strings.Compare(a.AccessToken, b.AccessToken)
	})
	return own
}

func (in loadedDir) report() ImportReport {
	r := ImportReport{
		Accounts: len(in.ownAccounts()), Tokens: len(in.accounts), LedgerLines: len(in.ledger),
		DiaryPosts: len(in.social.Posts), DiaryComments: len(in.social.Comments), DiaryLikes: len(in.social.Likes),
		GuestbookEntries: len(in.social.Guestbook), GuestbookRead: len(in.social.GuestbookRead),
		Friendships: len(in.social.Friendships), Circles: len(in.circles.Circles), CircleLeaves: len(in.circles.Leaves),
		TreasureRecords: len(in.treasure), ChatRooms: len(in.chat.Rooms),
	}
	for _, e := range in.social.Guestbook {
		r.GuestbookReplies += len(e.Reply)
	}
	for _, room := range in.chat.Rooms {
		r.ChatMessages += len(room.Msgs)
	}
	return r
}

// writeAccountsTx writes the rows of the accounts with the same row writers the server saves with: one row per account,
// then each of its tokens (the own one flagged as own), then the scalars.
func writeAccountsTx(tx *sql.Tx, in loadedDir) error {
	tokens := make(map[*Account][]string, len(in.accounts))
	for token, acc := range in.accounts {
		tokens[acc] = append(tokens[acc], token)
	}
	ids := make(map[*Account]int64, len(in.accounts))
	for _, acc := range in.ownAccounts() {
		id, _, err := insertAccountRowTx(tx, acc)
		if err != nil {
			return err
		}
		ids[acc] = id
		for _, token := range tokens[acc] {
			p := pendingToken{acc: acc, hash: CodeHash(token), own: token == acc.AccessToken}
			if err := addTokenRowTx(tx, id, p); err != nil {
				return err
			}
		}
	}
	var latestID int64
	if in.latest != nil {
		latestID = ids[in.latest]
	}
	return SetAccountMetaTx(tx, in.nextAvatarID, latestID)
}

// verifyDB reads the database back in one snapshot and compares it with the loaded stores.
func verifyDB(d *DB, in loadedDir) error {
	return d.View(func(q Querier) error {
		st, err := ReadAccounts(q)
		if err != nil {
			return err
		}
		own := in.ownAccounts()
		if len(st.Accounts) != len(own) {
			return fmt.Errorf("accounts: %d in the database, %d in the input", len(st.Accounts), len(own))
		}
		if len(st.ByToken) != len(in.accounts) {
			return fmt.Errorf("tokens: %d in the database, %d in the input", len(st.ByToken), len(in.accounts))
		}
		for _, acc := range own {
			got := st.ByToken[CodeHash(acc.AccessToken)]
			if got == nil {
				return fmt.Errorf("account aid %s is not in the database", acc.Aid)
			}
			wantGuest, wantData, _, err := accountRow(acc)
			if err != nil {
				return err
			}
			gotGuest, gotData, _, err := accountRow(got)
			if err != nil {
				return err
			}
			if wantGuest != gotGuest {
				return fmt.Errorf("account aid %s: guest code hash differs", acc.Aid)
			}
			if err := checkJSON("account aid "+acc.Aid, json.RawMessage(wantData), json.RawMessage(gotData)); err != nil {
				return err
			}
		}
		for token, acc := range in.accounts {
			ownDB := st.ByToken[CodeHash(acc.AccessToken)]
			if st.ByToken[CodeHash(token)] != ownDB {
				return fmt.Errorf("tokens: a token of account aid %s is not on its account", acc.Aid)
			}
		}
		if (in.latest == nil) != (st.Latest == nil) || (in.latest != nil && st.ByToken[CodeHash(in.latest.AccessToken)] != st.Latest) {
			return errors.New("accounts: the latest account differs")
		}
		if st.NextAvatarID != in.nextAvatarID {
			return errors.New("accounts: the next avatar id differs")
		}
		lines, err := ReadLedger(q)
		if err != nil {
			return err
		}
		if len(lines) != len(in.ledger) {
			return fmt.Errorf("ledger: %d lines in the database, %d in the input", len(lines), len(in.ledger))
		}
		for i := range lines {
			if lines[i] != in.ledger[i] {
				return fmt.Errorf("ledger: line %d differs", i+1)
			}
		}
		social, err := readSocial(q)
		if err != nil {
			return err
		}
		if err := checkJSON("social", in.social, social); err != nil {
			return err
		}
		circles, err := readCircles(q)
		if err != nil {
			return err
		}
		if err := checkJSON("circles", in.circles, circles); err != nil {
			return err
		}
		treasure, err := readTreasure(q)
		if err != nil {
			return err
		}
		if err := checkJSON("treasure", in.treasure, treasure); err != nil {
			return err
		}
		chat, err := readChatRooms(q)
		if err != nil {
			return err
		}
		return checkJSON("chat rooms", in.chat, chat)
	})
}

// checkJSON compares two values by their JSON; on a difference the error gives the first differing key or index.
func checkJSON(name string, want, got any) error {
	a, err := json.Marshal(want)
	if err != nil {
		return err
	}
	b, err := json.Marshal(got)
	if err != nil {
		return err
	}
	if bytes.Equal(a, b) {
		return nil
	}
	var av, bv any
	if err := json.Unmarshal(a, &av); err != nil {
		return err
	}
	if err := json.Unmarshal(b, &bv); err != nil {
		return err
	}
	path := jsonPath(name, av, bv)
	if path == "" {
		path = name + " (encoding)"
	}
	return fmt.Errorf("%s differs at %s", name, path)
}

// jsonPath is the path (object keys and array indexes) of the first difference between two decoded JSON values.
func jsonPath(path string, a, b any) string {
	switch av := a.(type) {
	case map[string]any:
		bv, ok := b.(map[string]any)
		if !ok {
			return path + " (type)"
		}
		keys := make([]string, 0, len(av)+len(bv))
		for k := range av {
			keys = append(keys, k)
		}
		for k := range bv {
			if _, ok := av[k]; !ok {
				keys = append(keys, k)
			}
		}
		sort.Strings(keys)
		for _, k := range keys {
			x, inA := av[k]
			y, inB := bv[k]
			if inA != inB {
				return path + "." + k + " (missing)"
			}
			if p := jsonPath(path+"."+k, x, y); p != "" {
				return p
			}
		}
		return ""
	case []any:
		bv, ok := b.([]any)
		if !ok {
			return path + " (type)"
		}
		for i := 0; i < len(av) && i < len(bv); i++ {
			if p := jsonPath(fmt.Sprintf("%s[%d]", path, i), av[i], bv[i]); p != "" {
				return p
			}
		}
		if len(av) != len(bv) {
			return path + " (length)"
		}
		return ""
	default:
		if !reflect.DeepEqual(a, b) {
			return path
		}
		return ""
	}
}

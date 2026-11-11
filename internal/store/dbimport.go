package store

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strings"
)

// The importer behind cmd/cherry-import: it loads a data directory's JSON stores with the server's own loaders, writes
// them to a database (one transaction per store), reads the database back and compares. It never writes the data
// directory. The loaders set the process-wide stores, so resetStores puts them back when the import ends.

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
	defer resetStores()
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

// loadDataDir runs the server's loaders on the files of dir (a missing file is an empty store) and copies the result out.
func loadDataDir(dir string) (loadedDir, error) {
	var in loadedDir
	if err := LoadAccountsFrom(filepath.Join(dir, "accounts.json")); err != nil {
		return in, loadError("accounts.json", err)
	}
	in.accounts, in.latest, in.nextAvatarID = accountsSnapshot()
	if err := LoadSocialFrom(filepath.Join(dir, "social.json")); err != nil {
		return in, loadError("social.json", err)
	}
	in.social = socialSnapshot()
	if err := LoadCirclesFrom(filepath.Join(dir, "circles.json")); err != nil {
		return in, loadError("circles.json", err)
	}
	in.circles = circlesSnapshot()
	if err := LoadTreasureFrom(filepath.Join(dir, "treasure.json")); err != nil {
		return in, loadError("treasure.json", err)
	}
	in.treasure = treasureSnapshot()
	chat, err := LoadChatRoomsFrom(filepath.Join(dir, "chatrooms.json"))
	if err != nil {
		return in, loadError("chatrooms.json", err)
	}
	chat.Rooms = nonNilList(chat.Rooms)
	in.chat = chat
	in.ledger, err = readLedgerFile(filepath.Join(dir, "ledger.jsonl"))
	return in, err
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

// resetStores puts back the process-wide stores that the loaders set.
func resetStores() {
	AccountsMu.Lock()
	Accounts = make(map[string]*Account)
	LatestAcc, NextAvatarID, AccountStorePath = nil, 0, ""
	AccountsMu.Unlock()
	ResetSocial()
	ResetCircles()
	ResetTreasure()
	ResetChatRooms()
}

func accountsSnapshot() (map[string]*Account, *Account, uint64) {
	AccountsMu.Lock()
	defer AccountsMu.Unlock()
	m := make(map[string]*Account, len(Accounts))
	maps.Copy(m, Accounts)
	return m, LatestAcc, NextAvatarID
}

// socialSnapshot is social.json's content as SaveSocialLocked would write it.
func socialSnapshot() savedSocial {
	SocialMu.Lock()
	defer SocialMu.Unlock()
	return savedSocial{
		Version: 1, FriendRemoved: FriendRemoved, FriendBookmarked: FriendBookmarked,
		NextDiaryNo: NextDiaryNo, Posts: nonNilList(DiaryPosts), NextGuestHistNo: NextGuestHistNo,
		Guestbook: nonNilList(GuestbookEntries), Friendships: Friendships,
		Comments: nonNilList(DiaryComments), NextCommentNo: NextCommentNo,
		Likes: nonNilList(DiaryLikes), NextLikeNo: NextLikeNo, NextReplyHistNo: NextReplyHistNo,
		GuestbookRead: nonNilMap(GuestbookRead),
	}
}

func circlesSnapshot() CircleBook {
	CirclesMu.Lock()
	defer CirclesMu.Unlock()
	b := CircleState
	b.Circles = nonNilList(b.Circles)
	return b
}

func treasureSnapshot() map[string]TreasureRecord {
	TreasureMu.Lock()
	defer TreasureMu.Unlock()
	m := make(map[string]TreasureRecord, len(TreasureAccounts))
	maps.Copy(m, TreasureAccounts)
	return m
}

func nonNilList[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}

func nonNilMap[K comparable, V any](m map[K]V) map[K]V {
	if m == nil {
		return map[K]V{}
	}
	return m
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

func writeAccountsTx(tx *sql.Tx, in loadedDir) error {
	own := in.ownAccounts()
	for _, acc := range own {
		if err := InsertAccountTx(tx, acc, acc.AccessToken); err != nil {
			return err
		}
	}
	for token, acc := range in.accounts {
		if token != acc.AccessToken {
			if err := AddTokenTx(tx, acc, token); err != nil {
				return err
			}
		}
	}
	return SetAccountMetaTx(tx, in.nextAvatarID, in.latest)
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

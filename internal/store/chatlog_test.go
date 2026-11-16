package store

import (
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cherry/internal/metrics"
)

// insertChatRow writes one chat_log row at Unix milliseconds at, straight into the attached database (the tests' way to
// place rows at chosen times; the game path goes through RecordChat). It returns the row id.
func insertChatRow(t *testing.T, at int64, kind, channel, sender, name, text string) int64 {
	t.Helper()
	d, err := docDB()
	if err != nil {
		t.Fatal(err)
	}
	var id int64
	if err := d.Update(func(tx *sql.Tx) error {
		res, err := tx.Exec(`INSERT INTO chat_log (time, kind, channel, sender, sender_name, text) VALUES (?, ?, ?, ?, ?, ?)`,
			at, kind, channel, sender, name, text)
		if err != nil {
			return err
		}
		id, err = res.LastInsertId()
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return id
}

// chatRowCount is the number of rows in chat_log.
func chatRowCount(t *testing.T) int {
	t.Helper()
	d, err := docDB()
	if err != nil {
		t.Fatal(err)
	}
	var n int
	if err := d.View(func(q Querier) error {
		return q.QueryRow(`SELECT COUNT(*) FROM chat_log`).Scan(&n)
	}); err != nil {
		t.Fatal(err)
	}
	return n
}

// TestMigrationV4ToV5KeepsRows: a database at schema v4 with rows migrates to v5 with every row in place; the chat log table
// and its three indexes exist, and its kind column takes only the channel kinds.
func TestMigrationV4ToV5KeepsRows(t *testing.T) {
	path := filepath.Join(t.TempDir(), DBFileName)
	raw, err := sql.Open("sqlite", path+"?"+dbPragmas)
	if err != nil {
		t.Fatal(err)
	}
	if err := migrateTo(raw, 4); err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`INSERT INTO media_files (kind, owner, file, size, created) VALUES ('diary', '9031', 'kept.img', 4, '2026-11-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	raw.Close()

	d, err := OpenDB(path)
	if err != nil {
		t.Fatalf("migrate v4 to v5: %v", err)
	}
	defer d.Close()
	var version int
	if err := d.write.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != len(migrations) || version != 6 {
		t.Fatalf("user_version = %d (err %v), want 6", version, err)
	}
	var kept string
	if err := d.write.QueryRow("SELECT file FROM media_files").Scan(&kept); err != nil || kept != "kept.img" {
		t.Fatalf("v4 row after migration = %q (err %v)", kept, err)
	}
	rows, err := d.write.Query(`SELECT name FROM sqlite_master WHERE type = 'index' AND tbl_name = 'chat_log' ORDER BY name`)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for rows.Next() {
		var n string
		rows.Scan(&n)
		names = append(names, n)
	}
	rows.Close()
	if strings.Join(names, ",") != "chat_log_channel,chat_log_sender,chat_log_time" {
		t.Fatalf("chat_log indexes = %v", names)
	}
	if _, err := d.write.Exec(`INSERT INTO chat_log (time, kind, channel, sender, sender_name, text) VALUES (1, 'bogus', '1', '1', 'x', 'x')`); err == nil {
		t.Fatal("a channel kind outside the list was stored")
	}
}

// TestChatLogWriterFlushesBatches: messages recorded through RecordChat reach the table once FlushChatLog returns, in batches
// past the batch size as well.
func TestChatLogWriterFlushesBatches(t *testing.T) {
	docStore(t)
	StartChatLog()
	t.Cleanup(StopChatLog)
	for i := 0; i < 3; i++ {
		RecordChat(ChatEntry{Kind: ChatSquare, Channel: "7", Sender: "9981", SenderName: "Ann", Text: "hi"})
	}
	FlushChatLog()
	if n := chatRowCount(t); n != 3 {
		t.Fatalf("%d rows after a flush, want 3", n)
	}
	for i := 0; i < 2*chatLogBatch+5; i++ {
		RecordChat(ChatEntry{Kind: ChatSquare, Channel: "7", Sender: "9981", SenderName: "Ann", Text: "batch"})
	}
	FlushChatLog()
	if n := chatRowCount(t); n != 3+2*chatLogBatch+5 {
		t.Fatalf("%d rows after the batches, want %d", n, 3+2*chatLogBatch+5)
	}
}

// TestChatLogShutdownWritesTheQueue: messages still queued when the writer stops are written before StopChatLog returns.
func TestChatLogShutdownWritesTheQueue(t *testing.T) {
	docStore(t)
	StartChatLog()
	for i := 0; i < 5; i++ {
		RecordChat(ChatEntry{Kind: ChatParty, Channel: "9981", Sender: "9982", SenderName: "Ben", Text: "last words"})
	}
	StopChatLog()
	if n := chatRowCount(t); n != 5 {
		t.Fatalf("%d rows after the shutdown, want 5", n)
	}
	RecordChat(ChatEntry{Kind: ChatParty, Channel: "9981", Sender: "9982", SenderName: "Ben", Text: "after"})
	StopChatLog() // a second stop is a no-op
	if n := chatRowCount(t); n != 5 {
		t.Fatalf("a message recorded after the stop was kept: %d rows", n)
	}
}

// TestChatLogQueueDropsAndCounts: with the writer not draining, a full queue drops the newest messages and counts each one in
// metrics chat_log_dropped; nothing blocks.
func TestChatLogQueueDropsAndCounts(t *testing.T) {
	l := &chatLogger{in: make(chan chatRecord, 2), flush: make(chan chan struct{}), quit: make(chan struct{}), done: make(chan struct{})}
	chatLog.Store(l)
	t.Cleanup(func() { chatLog.Store(nil) })
	before := metrics.ChatLogDropped.Load()
	for i := 0; i < 5; i++ {
		RecordChat(ChatEntry{Kind: ChatLounge, Channel: "1930000001", Sender: "9981", SenderName: "Ann", Text: "x"})
	}
	if len(l.in) != 2 {
		t.Fatalf("queue holds %d messages, want 2", len(l.in))
	}
	if got := metrics.ChatLogDropped.Load() - before; got != 3 {
		t.Fatalf("dropped = %d, want 3", got)
	}
}

// TestChatLogWriteFailureCountsTheBatch: a batch the database refuses (none attached) is dropped and counted, and the writer
// goes on.
func TestChatLogWriteFailureCountsTheBatch(t *testing.T) {
	docStore(t)
	StartChatLog()
	t.Cleanup(StopChatLog)
	if err := CloseStore(); err != nil {
		t.Fatal(err)
	}
	before := metrics.ChatLogDropped.Load()
	RecordChat(ChatEntry{Kind: ChatCircle, Channel: "1940000001", Sender: "9981", SenderName: "Ann", Text: "lost"})
	RecordChat(ChatEntry{Kind: ChatCircle, Channel: "1940000001", Sender: "9981", SenderName: "Ann", Text: "lost"})
	FlushChatLog()
	if got := metrics.ChatLogDropped.Load() - before; got != 2 {
		t.Fatalf("dropped after a failed write = %d, want 2", got)
	}
}

// TestChatLogWithoutWriterKeepsNothing: with no writer (the tests that do not ask for one, memory-only runs), RecordChat
// does nothing at all and counts nothing.
func TestChatLogWithoutWriterKeepsNothing(t *testing.T) {
	before := metrics.ChatLogDropped.Load()
	RecordChat(ChatEntry{Kind: ChatPrivate, Channel: "1960000000", Sender: "9981", SenderName: "Ann", Text: "nobody hears"})
	FlushChatLog()
	if got := metrics.ChatLogDropped.Load() - before; got != 0 {
		t.Fatalf("dropped without a writer = %d, want 0", got)
	}
}

// TestRecordChatCutsTheText: a message longer than chatLogTextMax characters keeps its first characters.
func TestRecordChatCutsTheText(t *testing.T) {
	docStore(t)
	StartChatLog()
	t.Cleanup(StopChatLog)
	RecordChat(ChatEntry{Kind: ChatSquare, Channel: "7", Sender: "9981", SenderName: "Ann", Text: strings.Repeat("あ", chatLogTextMax+200)})
	FlushChatLog()
	rows, err := ChatLogSearch(ChatFilter{}, 0, 10)
	if err != nil || len(rows) != 1 {
		t.Fatalf("rows = %d (err %v), want 1", len(rows), err)
	}
	if n := len([]rune(rows[0].Text)); n != chatLogTextMax {
		t.Fatalf("kept %d characters, want %d", n, chatLogTextMax)
	}
}

// TestPruneChatLogKeepsTheWindow: rows older than the retention days go; newer rows stay; zero days keeps everything.
func TestPruneChatLogKeepsTheWindow(t *testing.T) {
	docStore(t)
	now := time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)
	insertChatRow(t, now.AddDate(0, 0, -40).UnixMilli(), ChatPrivate, "1960000000", "9981", "Ann", "old")
	insertChatRow(t, now.AddDate(0, 0, -29).UnixMilli(), ChatPrivate, "1960000000", "9981", "Ann", "recent")
	insertChatRow(t, now.UnixMilli(), ChatPrivate, "1960000000", "9981", "Ann", "now")

	if n, err := PruneChatLog(now, 0); err != nil || n != 0 {
		t.Fatalf("zero days deleted %d (err %v), want nothing", n, err)
	}
	n, err := PruneChatLog(now, 30)
	if err != nil || n != 1 {
		t.Fatalf("30 days deleted %d (err %v), want 1", n, err)
	}
	rows, err := ChatLogSearch(ChatFilter{}, 0, 10)
	if err != nil || len(rows) != 2 || rows[0].Text != "now" || rows[1].Text != "recent" {
		t.Fatalf("rows after the sweep = %+v (err %v)", rows, err)
	}
}

// TestChatLogSearchFilters: each filter keeps its rows; the context window keeps the channel around a time; pages go by id;
// the values a filter cannot take are refused.
func TestChatLogSearchFilters(t *testing.T) {
	docStore(t)
	d, err := docDB()
	if err != nil {
		t.Fatal(err)
	}
	if err := d.Update(func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO accounts (aid, name, created, updated, data_hash, data) VALUES ('9981', 'Ann', '', '', '', '{}')`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	var annRow int64
	if err := d.View(func(q Querier) error {
		return q.QueryRow(`SELECT id FROM accounts WHERE aid = '9981'`).Scan(&annRow)
	}); err != nil {
		t.Fatal(err)
	}
	t0 := time.Date(2026, 11, 16, 20, 0, 0, 0, time.UTC)
	ms := func(d time.Duration) int64 { return t0.Add(d).UnixMilli() }
	r1 := insertChatRow(t, ms(0), ChatPrivate, "1960000000", "9981", "Ann", "hello <script>alert(1)</script>")
	r2 := insertChatRow(t, ms(time.Minute), ChatLounge, "1930000001", "9982", "Ben", "Hi Ann")
	r3 := insertChatRow(t, ms(2*time.Minute), ChatPrivate, "1960000000", "9982", "Ben", "Hello again")
	r4 := insertChatRow(t, ms(72*time.Hour), ChatCircle, "1940000001", "9981", "Ann", "club")
	ids := func(rows []ChatLogRow) []int64 {
		var out []int64
		for _, r := range rows {
			out = append(out, r.ID)
		}
		return out
	}
	same := func(what string, got []ChatLogRow, want ...int64) {
		t.Helper()
		if g := ids(got); len(g) != len(want) || (len(want) > 0 && !equalIDs(g, want)) {
			t.Fatalf("%s: rows %v, want %v", what, g, want)
		}
	}
	search := func(f ChatFilter) []ChatLogRow {
		t.Helper()
		rows, err := ChatLogSearch(f, 0, 50)
		if err != nil {
			t.Fatalf("search %+v: %v", f, err)
		}
		return rows
	}

	same("all, newest first", search(ChatFilter{}), r4, r3, r2, r1)
	same("kind private", search(ChatFilter{Kind: ChatPrivate}), r3, r1)
	same("channel", search(ChatFilter{Channel: "1930000001"}), r2)
	same("player by avatar id", search(ChatFilter{Player: "9982"}), r3, r2)
	same("player by name, any case", search(ChatFilter{Player: "ANN"}), r4, r1)
	same("text, any case", search(ChatFilter{Text: "HELLO"}), r3, r1)
	same("one day", search(ChatFilter{From: "2026-11-16", To: "2026-11-16"}), r3, r2, r1)
	same("the context of r3", search(ChatFilter{Kind: ChatPrivate, Channel: "1960000000", Around: ms(2 * time.Minute)}), r3, r1)
	if rows := search(ChatFilter{Player: "9981"}); len(rows) != 2 || rows[0].PlayerID != annRow {
		t.Fatalf("the sender's account row = %d, want %d", rows[0].PlayerID, annRow)
	}
	if rows := search(ChatFilter{Player: "9982"}); rows[0].PlayerID != 0 {
		t.Fatalf("a sender with no account row has player id %d", rows[0].PlayerID)
	}

	page, err := ChatLogSearch(ChatFilter{}, 0, 2)
	if err != nil || !equalIDs(ids(page), []int64{r4, r3}) {
		t.Fatalf("first page = %v (err %v)", ids(page), err)
	}
	next, err := ChatLogSearch(ChatFilter{}, page[1].ID, 2)
	if err != nil || !equalIDs(ids(next), []int64{r2, r1}) {
		t.Fatalf("second page = %v (err %v)", ids(next), err)
	}

	for _, bad := range []ChatFilter{{Kind: "dm"}, {From: "16/11/2026"}, {To: "2026-13-01"}} {
		if _, err := ChatLogSearch(bad, 0, 10); !errors.Is(err, ErrBadInput) {
			t.Errorf("filter %+v: err %v, want ErrBadInput", bad, err)
		}
	}
	for _, n := range []int{0, 201} {
		if _, err := ChatLogSearch(ChatFilter{}, 0, n); !errors.Is(err, ErrBadInput) {
			t.Errorf("limit %d: err %v, want ErrBadInput", n, err)
		}
	}
}

// equalIDs reports whether two id lists are the same.
func equalIDs(a, b []int64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

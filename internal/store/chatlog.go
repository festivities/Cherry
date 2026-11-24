package store

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"slices"
	"strings"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"cherry/internal/metrics"
)

// The player chat log (PLAN Roadmap 8, staff "Moderation"; schema v5 chat_log). Every message a player typed in a chat
// channel is one row. The game handlers only enqueue it (RecordChat never blocks); one writer goroutine writes the queue in
// batches, so no game handler waits for the database. A full queue drops the message and a failed write drops its batch;
// both are counted in metrics chat_log_dropped and never retried. The rows are kept for retention.chat-days and swept by
// the maintenance loop (PruneChatLog). The text is never logged.

// Channel kinds (chat_log.kind). Garden has none: the stock client sends no chat there. Stickers are not text and are not
// logged.
const (
	ChatPrivate = "private" // 1:1 friend chat (lpn/avatarchat.go): the channel is the room id
	ChatLounge  = "lounge"  // Lounge Chat (lpn/themechat.go): the room id
	ChatCircle  = "circle"  // Circle chat (lpn/circle.go): the circle id
	ChatSquare  = "square"  // Square chat (lpn/square.go): the Square's theme id
	ChatWhisper = "whisper" // Square 1:1 whisper (lpn/square_whisper.go): the Square's theme id
	ChatParty   = "party"   // Room Party chat (lpn/roomparty.go): the host's avatar id
)

// ChatKinds are the kinds the table holds, in the order the staff page lists them.
var ChatKinds = []string{ChatPrivate, ChatLounge, ChatCircle, ChatSquare, ChatWhisper, ChatParty}

// ponytail: 1024 queued messages is a few seconds of a busy server's chat; a longer burst drops the newest and is counted.
const chatLogQueue = 1024

const (
	chatLogBatch     = 200                    // messages per transaction
	chatLogFlushEach = 250 * time.Millisecond // the longest a queued message waits for its batch
	chatLogTextMax   = 1000                   // characters kept of one message (the game's own limits are 500 to 512)
	// ChatContextWindow is the span either side of a message that a context link (the staff page) shows.
	ChatContextWindow = 10 * time.Minute
)

// ChatEntry is one message to record: the channel's kind and id, the sender's avatar id and name (the name it had when it
// was sent), optional recipient avatar id and name (1:1 messages), and the text.
type ChatEntry struct {
	Kind, Channel, Sender, SenderName, Target, TargetName, Text string
}

// chatRecord is a queued message with its time (Unix milliseconds, UTC).
type chatRecord struct {
	at int64
	e  ChatEntry
}

// chatLogger is one running writer. in carries the messages, flush asks for a write of what is queued now (tests and the
// shutdown use it), quit ends the writer after it writes the queue, done closes when it has.
type chatLogger struct {
	in    chan chatRecord
	flush chan chan struct{}
	quit  chan struct{}
	done  chan struct{}
}

// chatLog is the running writer, nil when none runs: RecordChat then keeps nothing (memory-only runs and tests).
var chatLog atomic.Pointer[chatLogger]

// StartChatLog starts the writer. cmd/cherry calls it once the database is open.
func StartChatLog() { startChatLog(chatLogQueue) }

func startChatLog(queue int) {
	l := &chatLogger{
		in:    make(chan chatRecord, queue),
		flush: make(chan chan struct{}),
		quit:  make(chan struct{}),
		done:  make(chan struct{}),
	}
	if chatLog.CompareAndSwap(nil, l) {
		go l.run()
	}
}

// run writes the queue in batches of chatLogBatch, or every chatLogFlushEach, whichever comes first. A flush request writes
// everything queued before it; quit writes the rest and returns.
func (l *chatLogger) run() {
	defer close(l.done)
	var batch []chatRecord
	tick := time.NewTicker(chatLogFlushEach)
	defer tick.Stop()
	for {
		select {
		case r := <-l.in:
			batch = append(batch, r)
			if len(batch) < chatLogBatch {
				continue
			}
		case <-tick.C:
		case ack := <-l.flush:
			batch = l.drain(batch)
			writeChatBatch(batch)
			close(ack)
			batch = batch[:0]
			continue
		case <-l.quit:
			batch = l.drain(batch)
			writeChatBatch(batch)
			return
		}
		writeChatBatch(batch)
		batch = batch[:0]
	}
}

// drain takes every message queued now into batch.
func (l *chatLogger) drain(batch []chatRecord) []chatRecord {
	for {
		select {
		case r := <-l.in:
			batch = append(batch, r)
		default:
			return batch
		}
	}
}

// writeChatBatch writes the batch in one transaction. A failure drops the batch and counts it; the error names no text.
func writeChatBatch(batch []chatRecord) {
	if len(batch) == 0 {
		return
	}
	err := ErrNoDatabase
	if d := attachedDB(); d != nil {
		err = d.Update(func(tx *sql.Tx) error {
			for _, r := range batch {
				if _, err := tx.Exec(`INSERT INTO chat_log (time, kind, channel, sender, sender_name, target, target_name, text) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
					r.at, r.e.Kind, r.e.Channel, r.e.Sender, r.e.SenderName, r.e.Target, r.e.TargetName, r.e.Text); err != nil {
					return err
				}
			}
			return nil
		})
	}
	if err != nil {
		metrics.ChatLogDropped.Add(int64(len(batch)))
		log.Printf("cherry: chat log: %d messages not kept: %v", len(batch), err)
	}
}

// RecordChat queues one message for the chat log. It never blocks: a full queue, or no writer, drops the message (a full
// queue is counted).
func RecordChat(e ChatEntry) {
	l := chatLog.Load()
	if l == nil {
		return
	}
	e.Text = cutRunes(e.Text, chatLogTextMax)
	select {
	case l.in <- chatRecord{at: time.Now().UnixMilli(), e: e}:
	default:
		metrics.ChatLogDropped.Add(1)
	}
}

// FlushChatLog returns once every message queued before the call is written. Without a writer it returns at once.
func FlushChatLog() {
	l := chatLog.Load()
	if l == nil {
		return
	}
	ack := make(chan struct{})
	select {
	case l.flush <- ack:
		<-ack
	case <-l.done:
	}
}

// StopChatLog ends the writer after it writes the queue, and waits for it. Shutdown calls it before the database closes. A
// message recorded during the call may be lost; none is written after it returns.
func StopChatLog() {
	l := chatLog.Swap(nil)
	if l == nil {
		return
	}
	close(l.quit)
	<-l.done
}

// PruneChatLog deletes the messages older than days and returns how many it deleted. Zero days keeps every message.
// ponytail: one DELETE per sweep; a very large backlog is one long transaction, so cap the rows per sweep if that ever shows.
func PruneChatLog(now time.Time, days int) (int64, error) {
	if days <= 0 {
		return 0, nil
	}
	d := attachedDB()
	if d == nil {
		return 0, ErrNoDatabase
	}
	cutoff := now.Add(-time.Duration(days) * 24 * time.Hour).UnixMilli()
	var n int64
	err := d.Update(func(tx *sql.Tx) error {
		res, err := tx.Exec(`DELETE FROM chat_log WHERE time < ?`, cutoff)
		if err != nil {
			return err
		}
		n, err = res.RowsAffected()
		return err
	})
	return n, err
}

// ChatLogRow is one message of the chat log as the staff page shows it. PlayerID and TargetPlayerID are the account row
// ids of the sender and recipient (0 when absent or when no account row exists now).
type ChatLogRow struct {
	ID                                                          int64
	Time                                                        int64 // Unix milliseconds, UTC
	Kind, Channel, Sender, SenderName, Target, TargetName, Text string
	PlayerID, TargetPlayerID                                    int64
}

// ChatFilter narrows the chat log. Kind and Channel match exactly. Player matches the sender's or recipient's avatar id
// exactly or either name as a substring (any case). Text matches a substring (any case). From and To are UTC days
// (YYYY-MM-DD, both inclusive). Around (Unix milliseconds, 0 = none) keeps the messages within ChatContextWindow of it.
type ChatFilter struct {
	Kind, Channel, Player, Text, From, To string
	Around                                int64
}

// chatLogInnerSelect selects matching chat_log rows before joining accounts so the join only touches the <= limit returned
// rows. The outer join uses a.aid <> '0' (matching the partial unique index accounts_aid) so SQLite performs covering index
// lookups instead of full-scanning the accounts table.
const (
	chatLogInnerSelect = `SELECT c.id, c.time, c.kind, c.channel, c.sender, c.sender_name, c.target, c.target_name, c.text FROM chat_log c`
	chatLogOuterJoin   = `SELECT m.id, m.time, m.kind, m.channel, m.sender, m.sender_name, m.target, m.target_name, m.text,
	COALESCE(a.id, 0), COALESCE(ta.id, 0)
	FROM m
	LEFT JOIN accounts a ON a.aid = m.sender AND a.aid <> '0'
	LEFT JOIN accounts ta ON ta.aid = m.target AND ta.aid <> '0' AND m.target <> ''`
)

// chatFilterClauses validates f and builds the fixed SQL predicates and bound arguments for it. When forcePK is true,
// secondary-index columns are wrapped with unary '+' so SQLite walks chat_log by its INTEGER PRIMARY KEY from the cursor
// (ChatLogAfter: only the rows written since the last poll). Otherwise a kind, or a kind and channel, reads newest first
// from chat_log_kind or chat_log_kind_channel (schema v12), which are in id order within one value.
func chatFilterClauses(f ChatFilter, forcePK bool) (where []string, args []any, err error) {
	add := func(clause string, v ...any) {
		where = append(where, clause)
		args = append(args, v...)
	}
	if f.Kind != "" {
		if !slices.Contains(ChatKinds, f.Kind) {
			return nil, nil, fmt.Errorf("%w: unknown channel kind", ErrBadInput)
		}
		if forcePK {
			add("+c.kind = ?", f.Kind)
		} else {
			add("c.kind = ?", f.Kind)
		}
	}
	if f.Channel != "" {
		if forcePK {
			add("+c.channel = ?", f.Channel)
		} else {
			add("c.channel = ?", f.Channel)
		}
	}
	if f.Player != "" {
		add("(c.sender = ? OR c.target = ? OR instr(lower(c.sender_name), lower(?)) > 0 OR instr(lower(c.target_name), lower(?)) > 0)",
			f.Player, f.Player, f.Player, f.Player)
	}
	if f.Text != "" {
		add("instr(lower(c.text), lower(?)) > 0", f.Text)
	}
	timeCol := "c.time"
	if forcePK {
		timeCol = "+c.time"
	}
	if f.From != "" {
		t, err := time.Parse(time.DateOnly, f.From)
		if err != nil {
			return nil, nil, fmt.Errorf("%w: the from date is a day", ErrBadInput)
		}
		add(timeCol+" >= ?", t.UnixMilli())
	}
	if f.To != "" {
		t, err := time.Parse(time.DateOnly, f.To)
		if err != nil {
			return nil, nil, fmt.Errorf("%w: the to date is a day", ErrBadInput)
		}
		add(timeCol+" < ?", t.AddDate(0, 0, 1).UnixMilli())
	}
	if f.Around > 0 {
		w := ChatContextWindow.Milliseconds()
		add(timeCol+" BETWEEN ? AND ?", f.Around-w, f.Around+w)
	}
	return where, args, nil
}

// chatLogSearchQuery builds the CTE SQL and bound arguments for ChatLogSearch.
func chatLogSearchQuery(f ChatFilter, before int64, limit int) (string, []any, error) {
	filterWhere, filterArgs, err := chatFilterClauses(f, false)
	if err != nil {
		return "", nil, err
	}
	var where []string
	var args []any
	if before > 0 {
		where = append(where, "c.id < ?")
		args = append(args, before)
	}
	where = append(where, filterWhere...)
	args = append(args, filterArgs...)
	args = append(args, limit)
	whereSQL := ""
	if len(where) > 0 {
		whereSQL = " WHERE " + strings.Join(where, " AND ")
	}
	query := `WITH m AS (` + chatLogInnerSelect + whereSQL + ` ORDER BY c.id DESC LIMIT ?) ` + chatLogOuterJoin + ` ORDER BY m.id DESC`
	return query, args, nil
}

// chatLogAfterQuery builds the CTE SQL and bound arguments for ChatLogAfter.
func chatLogAfterQuery(f ChatFilter, after int64, limit int) (string, []any, error) {
	filterWhere, filterArgs, err := chatFilterClauses(f, true)
	if err != nil {
		return "", nil, err
	}
	where := append([]string{"c.id > ?"}, filterWhere...)
	args := append([]any{after}, filterArgs...)
	args = append(args, limit+1)
	query := `WITH m AS (` + chatLogInnerSelect + ` WHERE ` + strings.Join(where, " AND ") + ` ORDER BY c.id ASC LIMIT ?) ` + chatLogOuterJoin + ` ORDER BY m.id ASC`
	return query, args, nil
}

// queryChatRowsContext runs a chat log query bounded by ctx and scans its rows.
func queryChatRowsContext(ctx context.Context, tx *sql.Tx, query string, args ...any) ([]ChatLogRow, error) {
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ChatLogRow{}
	for rows.Next() {
		var r ChatLogRow
		if err := rows.Scan(&r.ID, &r.Time, &r.Kind, &r.Channel, &r.Sender, &r.SenderName,
			&r.Target, &r.TargetName, &r.Text, &r.PlayerID, &r.TargetPlayerID); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ChatLogSearch returns up to limit messages the filter keeps, newest first, with ids below before (0 = from the newest).
// ErrBadInput for a kind the table does not hold, a date that is not a day or a limit outside 1 to 200.
// ponytail: the text and name filters scan the table; index them when it grows past a few hundred thousand rows.
func ChatLogSearch(f ChatFilter, before int64, limit int) ([]ChatLogRow, error) {
	d := attachedDB()
	if d == nil {
		return nil, ErrNoDatabase
	}
	if limit < 1 || limit > 200 {
		return nil, fmt.Errorf("%w: the chat page is 1 to 200 rows", ErrBadInput)
	}
	query, args, err := chatLogSearchQuery(f, before, limit)
	if err != nil {
		return nil, err
	}
	var out []ChatLogRow
	err = d.ViewBounded(func(ctx context.Context, tx *sql.Tx) error {
		var err error
		out, err = queryChatRowsContext(ctx, tx, query, args...)
		return err
	})
	return out, err
}

// ChatLogMaxID returns the highest id currently stored in chat_log, or 0 when the table is empty.
func ChatLogMaxID() (int64, error) {
	d := attachedDB()
	if d == nil {
		return 0, ErrNoDatabase
	}
	var maxID int64
	err := d.View(func(q Querier) error {
		return q.QueryRow(`SELECT COALESCE(MAX(id), 0) FROM chat_log`).Scan(&maxID)
	})
	return maxID, err
}

// ChatLogAfter returns up to limit messages matching f with id > after, in ascending id order. hasMore reports whether more
// matching rows remain past limit; latestID is the last returned row's id when capped, else max(after, maxID) in the same
// snapshot so non-matching rows do not stall polling.
func ChatLogAfter(f ChatFilter, after int64, limit int) (rows []ChatLogRow, latestID int64, hasMore bool, err error) {
	d := attachedDB()
	if d == nil {
		return nil, 0, false, ErrNoDatabase
	}
	if limit < 1 || limit > 200 {
		return nil, 0, false, fmt.Errorf("%w: the chat page is 1 to 200 rows", ErrBadInput)
	}
	if after < 0 {
		return nil, 0, false, fmt.Errorf("%w: invalid chat cursor", ErrBadInput)
	}
	query, args, err := chatLogAfterQuery(f, after, limit)
	if err != nil {
		return nil, 0, false, err
	}
	rows = []ChatLogRow{}
	err = d.ViewBounded(func(ctx context.Context, tx *sql.Tx) error {
		var maxID int64
		if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(id), 0) FROM chat_log`).Scan(&maxID); err != nil {
			return err
		}
		got, err := queryChatRowsContext(ctx, tx, query, args...)
		if err != nil {
			return err
		}
		if len(got) > limit {
			hasMore = true
			got = got[:limit]
		}
		rows = got
		if hasMore {
			latestID = rows[len(rows)-1].ID
		} else {
			latestID = max(after, maxID)
		}
		return nil
	})
	if err != nil {
		return nil, 0, false, err
	}
	return rows, latestID, hasMore, nil
}

// cutRunes is the first n characters of s.
func cutRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n])
}

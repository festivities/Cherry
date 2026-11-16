package store

import (
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
	ChatParty   = "party"   // Room Party chat (lpn/roomparty.go): the host's avatar id
)

// ChatKinds are the kinds the table holds, in the order the staff page lists them.
var ChatKinds = []string{ChatPrivate, ChatLounge, ChatCircle, ChatSquare, ChatParty}

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
// was sent) and the text.
type ChatEntry struct {
	Kind, Channel, Sender, SenderName, Text string
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
				if _, err := tx.Exec(`INSERT INTO chat_log (time, kind, channel, sender, sender_name, text) VALUES (?, ?, ?, ?, ?, ?)`,
					r.at, r.e.Kind, r.e.Channel, r.e.Sender, r.e.SenderName, r.e.Text); err != nil {
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

// ChatLogRow is one message of the chat log as the staff page shows it. PlayerID is the row id of the sender's account (0
// when the sender has no account row now).
type ChatLogRow struct {
	ID                                      int64
	Time                                    int64 // Unix milliseconds, UTC
	Kind, Channel, Sender, SenderName, Text string
	PlayerID                                int64
}

// ChatFilter narrows the chat log. Kind and Channel match exactly. Player matches the sender's avatar id exactly or the
// sender's name as a substring (any case). Text matches a substring (any case). From and To are UTC days (YYYY-MM-DD, both
// inclusive). Around (Unix milliseconds, 0 = none) keeps the messages within ChatContextWindow of it.
type ChatFilter struct {
	Kind, Channel, Player, Text, From, To string
	Around                                int64
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
	// Only fixed clauses enter the SQL text; every value is a bound argument.
	where := []string{"(? = 0 OR c.id < ?)"}
	args := []any{before, before}
	add := func(clause string, v ...any) {
		where = append(where, clause)
		args = append(args, v...)
	}
	if f.Kind != "" {
		if !slices.Contains(ChatKinds, f.Kind) {
			return nil, fmt.Errorf("%w: unknown channel kind", ErrBadInput)
		}
		add("c.kind = ?", f.Kind)
	}
	if f.Channel != "" {
		add("c.channel = ?", f.Channel)
	}
	if f.Player != "" {
		add("(c.sender = ? OR instr(lower(c.sender_name), lower(?)) > 0)", f.Player, f.Player)
	}
	if f.Text != "" {
		add("instr(lower(c.text), lower(?)) > 0", f.Text)
	}
	if f.From != "" {
		t, err := time.Parse(time.DateOnly, f.From)
		if err != nil {
			return nil, fmt.Errorf("%w: the from date is a day", ErrBadInput)
		}
		add("c.time >= ?", t.UnixMilli())
	}
	if f.To != "" {
		t, err := time.Parse(time.DateOnly, f.To)
		if err != nil {
			return nil, fmt.Errorf("%w: the to date is a day", ErrBadInput)
		}
		add("c.time < ?", t.AddDate(0, 0, 1).UnixMilli())
	}
	if f.Around > 0 {
		w := ChatContextWindow.Milliseconds()
		add("c.time BETWEEN ? AND ?", f.Around-w, f.Around+w)
	}
	args = append(args, limit)
	query := `SELECT c.id, c.time, c.kind, c.channel, c.sender, c.sender_name, c.text, COALESCE(a.id, 0)
		FROM chat_log c LEFT JOIN accounts a ON a.aid = c.sender AND c.sender <> '0'
		WHERE ` + strings.Join(where, " AND ") + ` ORDER BY c.id DESC LIMIT ?`
	out := []ChatLogRow{}
	err := d.View(func(q Querier) error {
		rows, err := q.Query(query, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var r ChatLogRow
			if err := rows.Scan(&r.ID, &r.Time, &r.Kind, &r.Channel, &r.Sender, &r.SenderName, &r.Text, &r.PlayerID); err != nil {
				return err
			}
			out = append(out, r)
		}
		return rows.Err()
	})
	return out, err
}

// cutRunes is the first n characters of s.
func cutRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n])
}

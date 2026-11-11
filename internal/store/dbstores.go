package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
)

// Rows of the social, circle, Treasure World and chat stores (social.json, circles.json, treasure.json, chatrooms.json).
// The server writes each change as the rows it names (SaveSocialLocked, SaveCirclesLocked, UpdateTreasure and
// SaveChatRoomLocked, in their store files); the replace writers below write a store's whole value (AttachDB and the
// importer). A row's data column is the exact JSON of its Go value, and a list's order is the row id: a row added at the
// front of a list takes the lowest id, a row added at the end takes SQLite's next rowid (see slotOf).

// rowJSON is the data column of one row: the marshal of its value.
func rowJSON(v any) (string, error) {
	b, err := json.Marshal(v)
	return string(b), err
}

// listJSON reads the data column of every row of query (which orders them) into a list that is never nil.
func listJSON[T any](q Querier, query string) ([]T, error) {
	rows, err := q.Query(query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []T{}
	for rows.Next() {
		var data string
		if err := rows.Scan(&data); err != nil {
			return nil, err
		}
		var v T
		if err := json.Unmarshal([]byte(data), &v); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

func setMetaUintTx(tx *sql.Tx, key string, v uint64) error {
	return setMetaTx(tx, key, strconv.FormatUint(v, 10))
}

// Where a new row of an ordered list goes. The list in memory is changed at one of its ends by every save that adds a row
// (a diary post and a guestbook entry at the front, the others at the back); a row added anywhere else is refused rather
// than saved out of order.
const (
	slotFront = iota // the lowest id: before every row
	slotBack         // SQLite's next rowid: after every row
	slotNone         // not at an end: refused
)

// slotOf is the slot of the row at memory index i of n, for a row that is new.
func slotOf(i, n int) int {
	switch i {
	case 0:
		return slotFront
	case n - 1:
		return slotBack
	}
	return slotNone
}

// frontID is an id below every row of table.
func frontID(tx *sql.Tx, table string) (int64, error) {
	var id int64
	err := tx.QueryRow(`SELECT COALESCE(MIN(id), 1) - 1 FROM ` + table).Scan(&id)
	return id, err
}

// upsertRow writes the row of table that where selects, with cols set to vals (cols include the key columns). An existing
// row is updated in place, so its id and its place in the list stay. A new row goes to slot, which must be slotFront or
// slotBack (a table without an id column takes slotBack).
func upsertRow(tx *sql.Tx, table string, slot int, cols []string, vals []any, where string, whereArgs ...any) error {
	set := make([]string, len(cols))
	for i, c := range cols {
		set[i] = c + " = ?"
	}
	res, err := tx.Exec(`UPDATE `+table+` SET `+strings.Join(set, ", ")+` WHERE `+where, append(slices.Clone(vals), whereArgs...)...)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil || n > 0 {
		return err
	}
	if slot == slotNone {
		return fmt.Errorf("%s: a new row is not at the end of its list", table)
	}
	names, args := cols, vals
	if slot == slotFront {
		id, err := frontID(tx, table)
		if err != nil {
			return err
		}
		names = append([]string{"id"}, cols...)
		args = append([]any{id}, vals...)
	}
	marks := strings.TrimSuffix(strings.Repeat("?, ", len(args)), ", ")
	_, err = tx.Exec(`INSERT INTO `+table+` (`+strings.Join(names, ", ")+`) VALUES (`+marks+`)`, args...)
	return err
}

// deleteRow removes the rows of table that where selects; a row that is not there is not an error.
func deleteRow(tx *sql.Tx, table, where string, whereArgs ...any) error {
	_, err := tx.Exec(`DELETE FROM `+table+` WHERE `+where, whereArgs...)
	return err
}

// writeSocialTx replaces the social rows and scalars with s (the attach and the importer; a save names its rows instead).
func writeSocialTx(tx *sql.Tx, s savedSocial) error {
	for _, stmt := range []string{
		`DELETE FROM guestbook_replies`, `DELETE FROM guestbook_entries`, `DELETE FROM guestbook_read`,
		`DELETE FROM diary_posts`, `DELETE FROM diary_comments`, `DELETE FROM diary_likes`, `DELETE FROM friendships`,
	} {
		if _, err := tx.Exec(stmt); err != nil {
			return err
		}
	}
	for _, p := range s.Posts {
		data, err := rowJSON(p)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(`INSERT INTO diary_posts (diary_no, avatar_id, data) VALUES (?, ?, ?)`, p.DiaryNo, p.AvatarID, data); err != nil {
			return err
		}
	}
	for _, c := range s.Comments {
		data, err := rowJSON(c)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(`INSERT INTO diary_comments (comment_no, diary_no, data) VALUES (?, ?, ?)`, c.CommentNo, c.DiaryNo, data); err != nil {
			return err
		}
	}
	for _, l := range s.Likes {
		data, err := rowJSON(l)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(`INSERT INTO diary_likes (like_no, diary_no, like_avatar_id, data) VALUES (?, ?, ?, ?)`, l.LikeNo, l.DiaryNo, l.LikeAvatarID, data); err != nil {
			return err
		}
	}
	for _, e := range s.Guestbook {
		replies := e.Reply
		e.Reply = nil // the replies are rows of their own
		data, err := rowJSON(e)
		if err != nil {
			return err
		}
		res, err := tx.Exec(`INSERT INTO guestbook_entries (host_avt_no, guest_hist_no, data) VALUES (?, ?, ?)`, e.HostAvtNo, e.GuestHistNo, data)
		if err != nil {
			return err
		}
		entryID, err := res.LastInsertId()
		if err != nil {
			return err
		}
		for _, r := range replies {
			rdata, err := rowJSON(r)
			if err != nil {
				return err
			}
			if _, err := tx.Exec(`INSERT INTO guestbook_replies (entry_id, reply_hist_no, data) VALUES (?, ?, ?)`, entryID, r.ReplyHistNo, rdata); err != nil {
				return err
			}
		}
	}
	for host, hist := range s.GuestbookRead {
		if _, err := tx.Exec(`INSERT INTO guestbook_read (host_aid, hist_no) VALUES (?, ?)`, host, int64(hist)); err != nil {
			return err
		}
	}
	for _, f := range s.Friendships {
		if _, err := tx.Exec(`INSERT INTO friendships (a, b, state) VALUES (?, ?, ?)`, f.A, f.B, f.State); err != nil {
			return err
		}
	}
	if err := setSocialScalarsTx(tx, s.FriendRemoved, s.FriendBookmarked, s.NextDiaryNo, s.NextGuestHistNo, s.NextCommentNo, s.NextLikeNo, s.NextReplyHistNo); err != nil {
		return err
	}
	return nil
}

// setSocialScalarsTx writes the friend flags and the number counters of the social store.
func setSocialScalarsTx(tx *sql.Tx, friendRemoved, friendBookmarked bool, diary, guestHist, comment, like, reply uint64) error {
	if err := setMetaBoolTx(tx, "social.friendRemoved", friendRemoved); err != nil {
		return err
	}
	if err := setMetaBoolTx(tx, "social.friendBookmarked", friendBookmarked); err != nil {
		return err
	}
	for key, v := range map[string]uint64{
		"social.nextDiaryNo": diary, "social.nextGuestHistNo": guestHist,
		"social.nextCommentNo": comment, "social.nextLikeNo": like, "social.nextReplyHistNo": reply,
	} {
		if err := setMetaUintTx(tx, key, v); err != nil {
			return err
		}
	}
	return nil
}

// readSocial reads the social store back in the shape social.json has after loading (lists never nil).
func readSocial(q Querier) (savedSocial, error) {
	s := savedSocial{Version: 1, Guestbook: []SavedGuestbookEntry{}, GuestbookRead: map[string]uint64{}, Friendships: nil}
	var err error
	if s.Posts, err = listJSON[DiaryPost](q, `SELECT data FROM diary_posts ORDER BY id`); err != nil {
		return s, err
	}
	if s.Comments, err = listJSON[DiaryComment](q, `SELECT data FROM diary_comments ORDER BY id`); err != nil {
		return s, err
	}
	if s.Likes, err = listJSON[DiaryLike](q, `SELECT data FROM diary_likes ORDER BY id`); err != nil {
		return s, err
	}
	if err := readGuestbook(q, &s); err != nil {
		return s, err
	}
	if err := readFriendships(q, &s); err != nil {
		return s, err
	}
	rows, err := q.Query(`SELECT host_aid, hist_no FROM guestbook_read`)
	if err != nil {
		return s, err
	}
	defer rows.Close()
	for rows.Next() {
		var host string
		var hist int64
		if err := rows.Scan(&host, &hist); err != nil {
			return s, err
		}
		s.GuestbookRead[host] = uint64(hist)
	}
	if err := rows.Err(); err != nil {
		return s, err
	}
	if s.FriendRemoved, err = metaBool(q, "social.friendRemoved"); err != nil {
		return s, err
	}
	if s.FriendBookmarked, err = metaBool(q, "social.friendBookmarked"); err != nil {
		return s, err
	}
	for key, dst := range map[string]*uint64{
		"social.nextDiaryNo": &s.NextDiaryNo, "social.nextGuestHistNo": &s.NextGuestHistNo,
		"social.nextCommentNo": &s.NextCommentNo, "social.nextLikeNo": &s.NextLikeNo, "social.nextReplyHistNo": &s.NextReplyHistNo,
	} {
		if *dst, err = metaUint(q, key); err != nil {
			return s, err
		}
	}
	return s, nil
}

// readGuestbook reads the entries with their replies (oldest first, as the store keeps them).
func readGuestbook(q Querier, s *savedSocial) error {
	rows, err := q.Query(`SELECT id, data FROM guestbook_entries ORDER BY id`)
	if err != nil {
		return err
	}
	index := map[int64]int{}
	for rows.Next() {
		var id int64
		var data string
		if err := rows.Scan(&id, &data); err != nil {
			rows.Close()
			return err
		}
		var e SavedGuestbookEntry
		if err := json.Unmarshal([]byte(data), &e); err != nil {
			rows.Close()
			return err
		}
		e.Reply = []GuestbookReply{}
		index[id] = len(s.Guestbook)
		s.Guestbook = append(s.Guestbook, e)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	replies, err := q.Query(`SELECT entry_id, data FROM guestbook_replies ORDER BY id`)
	if err != nil {
		return err
	}
	defer replies.Close()
	for replies.Next() {
		var entryID int64
		var data string
		if err := replies.Scan(&entryID, &data); err != nil {
			return err
		}
		i, ok := index[entryID]
		if !ok {
			return errors.New("guestbook reply without its entry")
		}
		var r GuestbookReply
		if err := json.Unmarshal([]byte(data), &r); err != nil {
			return err
		}
		s.Guestbook[i].Reply = append(s.Guestbook[i].Reply, r)
	}
	return replies.Err()
}

func readFriendships(q Querier, s *savedSocial) error {
	rows, err := q.Query(`SELECT a, b, state FROM friendships ORDER BY id`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var f Friendship
		if err := rows.Scan(&f.A, &f.B, &f.State); err != nil {
			return err
		}
		s.Friendships = append(s.Friendships, f)
	}
	return rows.Err()
}

// writeCirclesTx replaces the circle rows (one per circle, in list order), the leaves and the next id with b.
func writeCirclesTx(tx *sql.Tx, b CircleBook) error {
	if _, err := tx.Exec(`DELETE FROM circles`); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM circle_leaves`); err != nil {
		return err
	}
	for _, c := range b.Circles {
		data, err := rowJSON(c)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(`INSERT INTO circles (circle_id, data) VALUES (?, ?)`, c.ID, data); err != nil {
			return err
		}
	}
	for aid, v := range b.Leaves {
		if _, err := tx.Exec(`INSERT INTO circle_leaves (aid, value) VALUES (?, ?)`, aid, v); err != nil {
			return err
		}
	}
	return setMetaTx(tx, "circles.nextId", strconv.FormatInt(b.NextID, 10))
}

func readCircles(q Querier) (CircleBook, error) {
	var b CircleBook
	var err error
	if b.Circles, err = listJSON[Circle](q, `SELECT data FROM circles ORDER BY id`); err != nil {
		return b, err
	}
	rows, err := q.Query(`SELECT aid, value FROM circle_leaves`)
	if err != nil {
		return b, err
	}
	defer rows.Close()
	for rows.Next() {
		var aid string
		var v int64
		if err := rows.Scan(&aid, &v); err != nil {
			return b, err
		}
		if b.Leaves == nil {
			b.Leaves = map[string]int64{}
		}
		b.Leaves[aid] = v
	}
	if err := rows.Err(); err != nil {
		return b, err
	}
	b.NextID, err = metaInt(q, "circles.nextId")
	return b, err
}

// writeTreasureTx replaces the Treasure World rows (one per account aid) with m.
func writeTreasureTx(tx *sql.Tx, m map[string]TreasureRecord) error {
	if _, err := tx.Exec(`DELETE FROM treasure_records`); err != nil {
		return err
	}
	for aid, rec := range m {
		data, err := rowJSON(rec)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(`INSERT INTO treasure_records (aid, data) VALUES (?, ?)`, aid, data); err != nil {
			return err
		}
	}
	return nil
}

func readTreasure(q Querier) (map[string]TreasureRecord, error) {
	rows, err := q.Query(`SELECT aid, data FROM treasure_records`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	m := map[string]TreasureRecord{}
	for rows.Next() {
		var aid, data string
		if err := rows.Scan(&aid, &data); err != nil {
			return nil, err
		}
		var rec TreasureRecord
		if err := json.Unmarshal([]byte(data), &rec); err != nil {
			return nil, err
		}
		m[aid] = rec
	}
	return m, rows.Err()
}

// writeChatRoomsTx replaces the chat rows: one row per room (its messages are rows of their own) and the next room id.
func writeChatRoomsTx(tx *sql.Tx, b ChatRoomBook) error {
	if _, err := tx.Exec(`DELETE FROM chat_messages`); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM chat_rooms`); err != nil {
		return err
	}
	for _, r := range b.Rooms {
		msgs := r.Msgs
		r.Msgs = nil
		data, err := rowJSON(r)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(`INSERT INTO chat_rooms (room_id, aid_a, aid_b, data) VALUES (?, ?, ?, ?)`, r.ID, r.Aids[0], r.Aids[1], data); err != nil {
			return err
		}
		for _, m := range msgs {
			if _, err := tx.Exec(`INSERT INTO chat_messages (room_id, sn, sec, sender, text) VALUES (?, ?, ?, ?, ?)`, r.ID, m.Sn, m.Sec, m.Sender, m.Text); err != nil {
				return err
			}
		}
	}
	return setMetaTx(tx, "chat.nextId", strconv.FormatInt(b.NextID, 10))
}

func readChatRooms(q Querier) (ChatRoomBook, error) {
	var b ChatRoomBook
	var err error
	b.Rooms = []ChatRoom{}
	rows, err := q.Query(`SELECT room_id, data FROM chat_rooms ORDER BY id`)
	if err != nil {
		return b, err
	}
	index := map[int64]int{}
	for rows.Next() {
		var roomID int64
		var data string
		if err := rows.Scan(&roomID, &data); err != nil {
			rows.Close()
			return b, err
		}
		var r ChatRoom
		if err := json.Unmarshal([]byte(data), &r); err != nil {
			rows.Close()
			return b, err
		}
		index[roomID] = len(b.Rooms)
		b.Rooms = append(b.Rooms, r)
	}
	if err := rows.Err(); err != nil {
		return b, err
	}
	msgs, err := q.Query(`SELECT room_id, sn, sec, sender, text FROM chat_messages ORDER BY id`)
	if err != nil {
		return b, err
	}
	defer msgs.Close()
	for msgs.Next() {
		var roomID int64
		var m ChatMsg
		if err := msgs.Scan(&roomID, &m.Sn, &m.Sec, &m.Sender, &m.Text); err != nil {
			return b, err
		}
		i, ok := index[roomID]
		if !ok {
			return b, errors.New("chat message without its room")
		}
		b.Rooms[i].Msgs = append(b.Rooms[i].Msgs, m)
	}
	if err := msgs.Err(); err != nil {
		return b, err
	}
	b.NextID, err = metaInt(q, "chat.nextId")
	return b, err
}

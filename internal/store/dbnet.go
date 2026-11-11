package store

import (
	"cmp"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strconv"
)

// The safety net of the stores written by rows (test builds only: testing.Testing). After each save with a database
// attached, the store is read back from the database and compared with memory, row by row, in list order. A difference
// panics naming the store, the kind of row, its key and the save site, never its content. It proves that a save named
// every row it changed (the chat and social saves name rows; circles find theirs by comparing the books, so for circles the
// net checks the rows written).

// netRow is one row as the net compares it: its kind, its key and the JSON of its value.
type netRow struct{ kind, key, data string }

func netRowOf(kind, key string, v any) netRow {
	b, _ := json.Marshal(v) // plain values: cannot fail
	return netRow{kind: kind, key: key, data: string(b)}
}

// checkRows panics at the first row where memory and the database differ.
func checkRows(store, site string, mem, db []netRow) {
	for i := 0; i < max(len(mem), len(db)); i++ {
		switch {
		case i >= len(mem):
			panic(fmt.Sprintf("store: %s %s %s is in the database but not in memory (save at %s)", store, db[i].kind, db[i].key, site))
		case i >= len(db):
			panic(fmt.Sprintf("store: %s %s %s is in memory but not in the database (save at %s)", store, mem[i].kind, mem[i].key, site))
		case mem[i] != db[i]:
			panic(fmt.Sprintf("store: %s %s %s differs between memory and the database (save at %s)", store, mem[i].kind, mem[i].key, site))
		}
	}
}

// socialNetRows lists the social store's rows in the order the database reads them back.
func socialNetRows(s savedSocial) []netRow {
	var out []netRow
	for _, p := range s.Posts {
		out = append(out, netRowOf("diary post", p.DiaryNo, p))
	}
	for _, c := range s.Comments {
		out = append(out, netRowOf("diary comment", c.CommentNo, c))
	}
	for _, l := range s.Likes {
		out = append(out, netRowOf("diary like", l.LikeNo, l))
	}
	for _, e := range s.Guestbook {
		replies := e.Reply
		e.Reply = nil // the replies are rows of their own
		out = append(out, netRowOf("guestbook entry", e.GuestHistNo, e))
		for _, r := range replies {
			out = append(out, netRowOf("guestbook reply", r.ReplyHistNo, struct {
				Entry string
				Reply GuestbookReply
			}{e.GuestHistNo, r}))
		}
	}
	for _, f := range s.Friendships {
		out = append(out, netRowOf("friendship", min(f.A, f.B)+"|"+max(f.A, f.B), f))
	}
	for _, host := range slices.Sorted(maps.Keys(s.GuestbookRead)) {
		out = append(out, netRowOf("guestbook read marker", host, s.GuestbookRead[host]))
	}
	out = append(out, netRowOf("social counters", "", struct {
		FriendRemoved, FriendBookmarked        bool
		Diary, GuestHist, Comment, Like, Reply uint64
	}{s.FriendRemoved, s.FriendBookmarked, s.NextDiaryNo, s.NextGuestHistNo, s.NextCommentNo, s.NextLikeNo, s.NextReplyHistNo}))
	return out
}

// netCheckSocial reads the social rows back and compares them with memory. Caller holds SocialMu.
func netCheckSocial(d *DB, site string) {
	var db savedSocial
	if err := d.View(func(q Querier) error {
		var err error
		db, err = readSocial(q)
		return err
	}); err != nil {
		panic(fmt.Sprintf("store: safety net: social could not be read back (save at %s)", site))
	}
	checkRows("social", site, socialNetRows(socialSnapshotLocked()), socialNetRows(db))
}

// circlesNetRows lists the circle book's rows: the circles in list order, the leaves by aid and the next id.
func circlesNetRows(b CircleBook) []netRow {
	var out []netRow
	for _, c := range b.Circles {
		out = append(out, netRowOf("circle", strconv.FormatInt(c.ID, 10), c))
	}
	for _, aid := range slices.Sorted(maps.Keys(b.Leaves)) {
		out = append(out, netRowOf("circle leave", aid, b.Leaves[aid]))
	}
	return append(out, netRowOf("circle counter", "nextId", b.NextID))
}

// netCheckCircles reads the circle rows back and compares them with next, the book the save installs (caller holds
// CirclesMu).
func netCheckCircles(d *DB, next CircleBook, site string) {
	var db CircleBook
	if err := d.View(func(q Querier) error {
		var err error
		db, err = readCircles(q)
		return err
	}); err != nil {
		panic(fmt.Sprintf("store: safety net: circles could not be read back (save at %s)", site))
	}
	checkRows("circles", site, circlesNetRows(next), circlesNetRows(db))
}

// treasureNetRows lists the board rows by aid.
func treasureNetRows(m map[string]TreasureRecord) []netRow {
	var out []netRow
	for _, aid := range slices.Sorted(maps.Keys(m)) {
		out = append(out, netRowOf("treasure record", aid, m[aid]))
	}
	return out
}

// netCheckTreasure reads the board rows back and compares them with memory (caller holds TreasureMu).
func netCheckTreasure(d *DB, site string) {
	var db map[string]TreasureRecord
	if err := d.View(func(q Querier) error {
		var err error
		db, err = readTreasure(q)
		return err
	}); err != nil {
		panic(fmt.Sprintf("store: safety net: treasure could not be read back (save at %s)", site))
	}
	checkRows("treasure", site, treasureNetRows(TreasureAccounts), treasureNetRows(db))
}

// chatNetRows lists the registry's rows: each room by id, its messages in order, and the next id.
func chatNetRows(b ChatRoomBook) []netRow {
	rooms := slices.Clone(b.Rooms)
	slices.SortFunc(rooms, func(x, y ChatRoom) int { return cmp.Compare(x.ID, y.ID) })
	var out []netRow
	for _, r := range rooms {
		msgs := r.Msgs
		r.Msgs = nil
		key := strconv.FormatInt(r.ID, 10)
		out = append(out, netRowOf("chat room", key, r))
		for _, m := range msgs {
			out = append(out, netRowOf("chat message", key+"/"+strconv.FormatInt(m.Sn, 10), m))
		}
	}
	return append(out, netRowOf("chat counter", "nextId", b.NextID))
}

// netCheckChat reads the registry rows back and compares them with mem, the registry lpn holds after the change.
func netCheckChat(d *DB, mem ChatRoomBook, site string) {
	var db ChatRoomBook
	if err := d.View(func(q Querier) error {
		var err error
		db, err = readChatRooms(q)
		return err
	}); err != nil {
		panic(fmt.Sprintf("store: safety net: chat rooms could not be read back (save at %s)", site))
	}
	checkRows("chat", site, chatNetRows(mem), chatNetRows(db))
}

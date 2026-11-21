package admin

import (
	"net/http"
	"strconv"
	"time"

	"cherry/internal/economy"
	"cherry/internal/store"
)

// The deleted avatars (2026-11-21, user: avatar ids are never reused, so every avatar Delete Avatar removed stays viewable): the
// list, newest first, and one avatar's record in sections. Both are read-only; the owner's restore is in avatarmove.go. The record is the one the
// delete kept (store.AvatarRecord); the ledger is read from the live table by avatar id, as the player page reads it.

const (
	deletedListPage = 50  // rows of the list
	recordRows      = 20  // posts, guestbook entries and comments of each kind on the record's page
	deletedChatMax  = 100 // chat lines and room messages shown, newest first (rooms: the newest of each)
	deletedLedger   = 20  // ledger lines on the record's page, newest first
	pastAvatarsPage = 50  // deleted avatars of one identity on a player page
)

// deletedRow is a row of the list, of a player page's past avatars and of the search.
type deletedRow struct {
	ID                              int64
	Href, Aid, Name, Identity, Date string
	Restored                        string // the restore time (rfcDate) when the avatar was restored; "" = not restored
}

// deletedRowOf is the row of one deleted avatar.
func deletedRowOf(d store.DeletedAvatar) deletedRow {
	row := deletedRow{ID: d.ID, Href: deletedPath(d.ID), Aid: d.Aid, Name: d.Name, Identity: d.Identity, Date: rfcDate(d.DeletedAt)}
	if d.RestoredAt != "" {
		row.Restored = rfcDate(d.RestoredAt)
	}
	return row
}

// deletedPath is the page of one deleted avatar.
func deletedPath(id int64) string { return "/players/deleted/" + strconv.FormatInt(id, 10) }

// deletedPost is a post of the record: its number, date, text cut, and the stored pictures it shows (row ids, for /photos/{id}/file).
type deletedPost struct {
	No, Date, Text string
	Pictures       []int64
}

// deletedEntry is a guestbook entry of the record: the guestbook it is in, who wrote it, its date and text.
type deletedEntry struct {
	No, Host, By, Date, Text string
}

// deletedFriend is one friendship of the record: the other avatar and its state at the delete.
type deletedFriend struct{ Aid, State string }

// deletedMail is one mail of the record's mailbox (the mail that was not yet claimed).
type deletedMail struct {
	Created, Kind, Sender, Message string
	Amount                         int64
	Item                           string
}

// deletedLine is one chat_log line of the record: time, channel kind and id, sender (avatar id and name) and text.
type deletedLine struct {
	Date, Kind, Channel, Sender, Name, Text string
}

// deletedRoom is one 1:1 room of the record: the other avatar and its newest messages (oldest first).
type deletedRoom struct {
	With string
	Msgs []deletedRoomMsg
}

// deletedRoomMsg is one stored message of a 1:1 room.
type deletedRoomMsg struct {
	Date, Sender, Text string
}

// deletedAvatarPage is the record of one deleted avatar as the detail page shows it.
type deletedAvatarPage struct {
	ID                                    int64
	Aid, Name, Identity, Deleted, Created string
	RestoredAt, RestoredHref              string // the restore time and the player page it went to (restored records only)
	CanRestore                            bool   // the owner may restore this record now
	Ban                                   *store.Ban
	Gems, Cash                            int64
	Book                                  economy.PlayerBook
	Outfit                                []string
	Mail                                  []deletedMail
	Sections                              []link
	Friends                               []deletedFriend
	Circle                                *store.CircleMembership
	Posts                                 []deletedPost
	Entries                               []deletedEntry
	Comments                              []playerComment
	Likes                                 int
	Pictures                              []playerMedia
	Lines                                 []deletedLine
	Rooms                                 []deletedRoom
	Treasure                              *store.TreasureRecord
	Ledger                                []store.LedgerLine
}

// deletedAvatarSections are the anchors of the record's page, in page order.
var deletedAvatarSections = []link{
	{Href: "#overview", Label: "Overview"},
	{Href: "#inventory", Label: "Inventory"},
	{Href: "#garden-bag", Label: "Garden bag"},
	{Href: "#butlers", Label: "Butlers"},
	{Href: "#outfit", Label: "Outfit, room and mail"},
	{Href: "#social", Label: "Social"},
	{Href: "#writing", Label: "Writing"},
	{Href: "#pictures", Label: "Pictures"},
	{Href: "#chat", Label: "Chat"},
	{Href: "#treasure", Label: "Treasure World"},
	{Href: "#ledger", Label: "Ledger"},
}

// handleDeleted: GET /players/deleted, with ?before= for the older rows.
func (s *Server) handleDeleted(w http.ResponseWriter, r *http.Request, sess session) {
	var before int64
	if raw := r.URL.Query().Get("before"); raw != "" {
		n, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || n < 1 {
			s.fail(w, http.StatusBadRequest, "The page marker is not valid.")
			return
		}
		before = n
	}
	rows, more, err := store.DeletedAvatarList(before, deletedListPage)
	if err != nil {
		s.storeError(w, err)
		return
	}
	v := s.base(r, sess, "Deleted avatars")
	v.Crumbs = []link{{Href: "/players", Label: "Players"}, {Label: "Deleted avatars"}}
	v.DeletedRows = []deletedRow{}
	for _, d := range rows {
		v.DeletedRows = append(v.DeletedRows, deletedRowOf(d))
	}
	if more {
		v.OlderHref = "/players/deleted?before=" + strconv.FormatInt(rows[len(rows)-1].ID, 10)
	}
	s.render(w, http.StatusOK, "deleted", v)
}

// handleDeletedAvatar: GET /players/deleted/{id}, the record of one deleted avatar.
func (s *Server) handleDeletedAvatar(w http.ResponseWriter, r *http.Request, sess session) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id < 1 {
		s.fail(w, http.StatusBadRequest, "The deleted avatar's number is not valid.")
		return
	}
	det, found, err := store.DeletedAvatarByID(id)
	if err != nil {
		s.storeError(w, err)
		return
	}
	if !found {
		s.fail(w, http.StatusNotFound, "No such deleted avatar.")
		return
	}
	page, err := deletedAvatarPageOf(det, s.now())
	if err != nil {
		s.storeError(w, err)
		return
	}
	v := s.base(r, sess, "Deleted avatar #"+det.Aid)
	v.Crumbs = []link{{Href: "/players", Label: "Players"}, {Href: "/players/deleted", Label: "Deleted avatars"}, {Label: "#" + strconv.FormatInt(id, 10)}}
	page.CanRestore = s.isOwner(sess) && det.RestoredAt == ""
	v.DeletedAvatar = page
	s.render(w, http.StatusOK, "deletedavatar", v)
}

// deletedAvatarPageOf builds the page of one record at now: the record's parts, the Garden and economy figures of its account
// (economy.BookOfAccount), and the ledger lines its avatar id has in the live table.
func deletedAvatarPageOf(det store.DeletedAvatarDetail, now time.Time) (deletedAvatarPage, error) {
	rec := det.Record
	acc := store.AccountOfRecord(rec.Account)
	lines, err := store.LedgerOfAid(det.Aid, deletedLedger)
	if err != nil {
		return deletedAvatarPage{}, err
	}
	page := deletedAvatarPage{
		ID: det.ID, Aid: det.Aid, Name: det.Name, Identity: det.Identity, Deleted: rfcDate(det.DeletedAt),
		Ban: rec.Account.Ban, Gems: rec.Account.Gems, Cash: rec.Account.Cash,
		Book: economy.BookOfAccount(acc, now), Outfit: rec.Account.ItemCodes, Sections: deletedAvatarSections,
		Circle: rec.Circle, Likes: len(rec.Likes), Treasure: rec.Treasure, Ledger: lines,
	}
	if rec.Account.AvatarCreated != 0 {
		page.Created = unixMsDate(rec.Account.AvatarCreated)
	}
	for _, m := range rec.Account.Mail {
		page.Mail = append(page.Mail, deletedMail{Created: unixMsDate(m.Created), Kind: m.Type, Amount: m.Amount, Item: m.Item,
			Message: m.Message, Sender: m.Sender})
	}
	// The pictures: a post names one by its URL, which maps to the picture row of the record by file name.
	rowOf := map[string]int64{}
	for _, m := range rec.Media {
		rowOf[m.File] = m.ID
	}
	for _, p := range rec.Posts[:min(len(rec.Posts), recordRows)] {
		post := deletedPost{No: p.DiaryNo, Date: msDate(p.RegDate), Text: cutText(p.Content, writingText)}
		for _, img := range p.Images {
			if f, ok := store.MediaRefFile(img.ImageURL); ok && rowOf[f] != 0 {
				post.Pictures = append(post.Pictures, rowOf[f])
			}
		}
		page.Posts = append(page.Posts, post)
	}
	for _, e := range rec.Guestbook[:min(len(rec.Guestbook), recordRows)] {
		page.Entries = append(page.Entries, deletedEntry{No: e.GuestHistNo, Host: e.HostAvtNo, By: e.GuestAvtNo, Date: msDate(e.RegYmdt), Text: cutText(e.Content, writingText)})
	}
	for _, c := range rec.Comments[:min(len(rec.Comments), recordRows)] {
		page.Comments = append(page.Comments, playerComment{No: c.CommentNo, Host: c.OwnerAvatarID, Post: c.DiaryNo, Date: msDate(c.Date), Text: cutText(c.Content, writingText)})
	}
	for _, f := range rec.Friendships {
		other := f.B
		if f.B == det.Aid {
			other = f.A
		}
		page.Friends = append(page.Friends, deletedFriend{Aid: other, State: f.State})
	}
	page.Pictures = mediaRows(rec.Media)
	if det.RestoredAt != "" {
		page.RestoredAt, page.RestoredHref = rfcDate(det.RestoredAt), playerPath(det.RestoredAccount)
	}
	for i := len(rec.ChatLog) - 1; i >= 0 && len(page.Lines) < deletedChatMax; i-- {
		l := rec.ChatLog[i]
		page.Lines = append(page.Lines, deletedLine{Date: unixMsDate(l.Time), Kind: l.Kind, Channel: l.Channel, Sender: l.Sender, Name: l.SenderName, Text: l.Text})
	}
	for _, room := range rec.ChatRooms {
		dr := deletedRoom{With: room.Aids[0]}
		if room.Aids[0] == det.Aid {
			dr.With = room.Aids[1]
		}
		msgs := room.Msgs[max(0, len(room.Msgs)-deletedChatMax):]
		for _, m := range msgs {
			dr.Msgs = append(dr.Msgs, deletedRoomMsg{Date: time.Unix(m.Sec, 0).UTC().Format("2006-01-02 15:04:05"), Sender: m.Sender, Text: m.Text})
		}
		page.Rooms = append(page.Rooms, dr)
	}
	return page, nil
}

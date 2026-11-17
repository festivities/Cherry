package admin

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"cherry/internal/store"
)

// The Moderation pages (PLAN Roadmap 8, staff UI): the chat log (/chat, the player messages kept in chat_log) and the photo
// log (/photos, the stored pictures). A picture is served to staff only by GET /photos/{id}/file, which looks the file up by
// the row's id and sniffs its type; the removal is the player's picture removal (store.RemovePlayerMedia) with its audit row.

const (
	chatPage       = 50       // messages on a chat page
	photoPage      = 24       // pictures on a photo page (a grid)
	chatTextMax    = 100      // characters of the chat's text filter
	photoMaxBytes  = 20 << 20 // the largest picture shown: the upload cap of a diary picture (web/media.go maxDiaryImageSize)
	photoSniffSize = 512      // bytes read to tell the picture type (http.DetectContentType reads this many)
)

// photoTypes are the picture types the photo route serves; any other sniffed type is refused.
var photoTypes = map[string]bool{"image/png": true, "image/jpeg": true, "image/gif": true, "image/webp": true}

// photoKinds are the kinds of picture the store keeps (store.MediaDiary, MediaRoom, MediaProfile).
var photoKinds = []string{store.MediaDiary, store.MediaRoom, store.MediaProfile}

// chatRow is one message of the chat page: its time, channel, sender and text, with the links the page shows.
type chatRow struct {
	Time, Kind, Channel, Name, Aid, Text string
	PlayerHref                           string // "" when the sender has no account row now
	ContextHref                          string // the channel around the message's time
}

// pictureRow is one picture of the photo page.
type pictureRow struct {
	ID                      int64
	Kind, Owner, Name, Date string
	KiB                     int64
	PlayerHref              string
}

// unixMsDate shows a Unix time in milliseconds in UTC, to the second.
func unixMsDate(ms int64) string {
	return time.UnixMilli(ms).UTC().Format("2006-01-02 15:04:05")
}

// allDigits reports whether s is one or more ASCII digits.
func allDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// dateOK reports whether each non-empty value is a UTC day (YYYY-MM-DD).
func dateOK(days ...string) bool {
	for _, day := range days {
		if _, err := time.Parse(time.DateOnly, day); day != "" && err != nil {
			return false
		}
	}
	return true
}

// chatFilterOf reads the chat log's filter from a query. The problem it returns is shown to the staff member (400).
func chatFilterOf(q url.Values) (store.ChatFilter, string) {
	f := store.ChatFilter{
		Kind:    strings.TrimSpace(q.Get("kind")),
		Channel: strings.TrimSpace(q.Get("channel")),
		Player:  strings.TrimSpace(q.Get("player")),
		Text:    strings.TrimSpace(q.Get("text")),
		From:    strings.TrimSpace(q.Get("from")),
		To:      strings.TrimSpace(q.Get("to")),
	}
	if f.Kind != "" && !slices.Contains(store.ChatKinds, f.Kind) {
		return f, "The channel kind is one the chat log lists."
	}
	if f.Channel != "" && (len(f.Channel) > 20 || !allDigits(f.Channel)) {
		return f, "The channel id is a number of at most 20 digits."
	}
	if utf8.RuneCountInString(f.Player) > playerQueryMax || utf8.RuneCountInString(f.Text) > chatTextMax {
		return f, "The player and text filters are at most 100 characters."
	}
	if !dateOK(f.From, f.To) {
		return f, "The dates are days in the form YYYY-MM-DD."
	}
	if raw := q.Get("around"); raw != "" {
		n, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || n < 1 {
			return f, "The context time is a Unix time in milliseconds."
		}
		f.Around = n
	}
	return f, ""
}

// chatHref is the chat page with the filter and, when before is set, the page marker.
func chatHref(f store.ChatFilter, before int64) string {
	q := url.Values{}
	for k, v := range map[string]string{"kind": f.Kind, "channel": f.Channel, "player": f.Player, "text": f.Text, "from": f.From, "to": f.To} {
		if v != "" {
			q.Set(k, v)
		}
	}
	if f.Around > 0 {
		q.Set("around", strconv.FormatInt(f.Around, 10))
	}
	if before > 0 {
		q.Set("before", strconv.FormatInt(before, 10))
	}
	if len(q) == 0 {
		return "/chat"
	}
	return "/chat?" + q.Encode()
}

// chatRows turns the messages into the page's rows.
func chatRows(msgs []store.ChatLogRow) []chatRow {
	rows := []chatRow{}
	for _, m := range msgs {
		row := chatRow{
			Time: unixMsDate(m.Time), Kind: m.Kind, Channel: m.Channel, Name: m.SenderName, Aid: m.Sender, Text: m.Text,
			ContextHref: chatHref(store.ChatFilter{Kind: m.Kind, Channel: m.Channel, Around: m.Time}, 0),
		}
		if m.PlayerID > 0 {
			row.PlayerHref = playerPath(m.PlayerID)
		}
		rows = append(rows, row)
	}
	return rows
}

// handleChatLog: GET /chat. The filter and the page marker come from the query; a context link (kind, channel, around)
// shows one channel in the window around a message.
func (s *Server) handleChatLog(w http.ResponseWriter, r *http.Request, sess session) {
	q := r.URL.Query()
	var before int64
	if raw := q["before"]; len(raw) > 0 {
		n, err := strconv.ParseInt(raw[0], 10, 64)
		if len(raw) != 1 || err != nil || n < 1 {
			s.fail(w, http.StatusBadRequest, "The page marker is not valid.")
			return
		}
		before = n
	}
	f, problem := chatFilterOf(q)
	if problem != "" {
		s.fail(w, http.StatusBadRequest, problem)
		return
	}
	msgs, err := store.ChatLogSearch(f, before, chatPage)
	if err != nil {
		s.storeError(w, err)
		return
	}
	v := s.base(r, sess, "Chat log")
	v.ChatRows, v.ChatFilter, v.ChatKinds = chatRows(msgs), f, store.ChatKinds
	v.Keep = store.ChatRetention(s.cfg.ChatDays)
	v.NewestHref = chatHref(f, 0)
	if len(msgs) == chatPage {
		v.OlderHref = chatHref(f, msgs[len(msgs)-1].ID)
	}
	if f.Around > 0 {
		v.ChatContext = fmt.Sprintf("Messages in %s %s within %d minutes of %s (UTC).", f.Kind, f.Channel, int(store.ChatContextWindow.Minutes()), unixMsDate(f.Around))
	}
	s.render(w, http.StatusOK, "chat", v)
}

// photoHref is the photo page with the filter and, when before is set, the page marker.
func photoHref(f store.PictureFilter, before int64) string {
	q := url.Values{}
	if f.Kind != "" {
		q.Set("kind", f.Kind)
	}
	if f.Player != "" {
		q.Set("player", f.Player)
	}
	if before > 0 {
		q.Set("before", strconv.FormatInt(before, 10))
	}
	if len(q) == 0 {
		return "/photos"
	}
	return "/photos?" + q.Encode()
}

// handlePhotos: GET /photos. The newest pictures first, a page of photoPage.
func (s *Server) handlePhotos(w http.ResponseWriter, r *http.Request, sess session) {
	q := r.URL.Query()
	var before int64
	if raw := q["before"]; len(raw) > 0 {
		n, err := strconv.ParseInt(raw[0], 10, 64)
		if len(raw) != 1 || err != nil || n < 1 {
			s.fail(w, http.StatusBadRequest, "The page marker is not valid.")
			return
		}
		before = n
	}
	f := store.PictureFilter{Kind: strings.TrimSpace(q.Get("kind")), Player: strings.TrimSpace(q.Get("player"))}
	if f.Kind != "" && !slices.Contains(photoKinds, f.Kind) {
		s.fail(w, http.StatusBadRequest, "The picture kind is diary, room or profile.")
		return
	}
	if utf8.RuneCountInString(f.Player) > playerQueryMax {
		s.fail(w, http.StatusBadRequest, "The player filter is at most 100 characters.")
		return
	}
	pics, err := store.MediaSearch(f, before, photoPage)
	if err != nil {
		s.storeError(w, err)
		return
	}
	v := s.base(r, sess, "Photos")
	v.Pictures, v.PicFilter, v.PicKinds = pictureRows(pics), f, photoKinds
	v.NewestHref = photoHref(f, 0)
	if len(pics) == photoPage {
		v.OlderHref = photoHref(f, pics[len(pics)-1].ID)
	}
	s.render(w, http.StatusOK, "photos", v)
}

// pictureRows turns the pictures into the page's cards.
func pictureRows(pics []store.PictureRow) []pictureRow {
	rows := []pictureRow{}
	for _, p := range pics {
		row := pictureRow{ID: p.ID, Kind: p.Kind, Owner: p.Owner, Name: p.OwnerName, Date: rfcDate(p.Created), KiB: (p.Size + 1023) / 1024}
		if p.PlayerID > 0 {
			row.PlayerHref = playerPath(p.PlayerID)
		}
		rows = append(rows, row)
	}
	return rows
}

// handlePhotoFile: GET /photos/{id}/file, the picture of row id, for staff only (the page guard). The file is found by the
// row's name in its kind's folder; the bytes are sniffed and served only when they are a PNG, JPEG, GIF or WebP picture.
func (s *Server) handlePhotoFile(w http.ResponseWriter, r *http.Request, sess session) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id < 1 {
		s.fail(w, http.StatusBadRequest, "The picture number is not valid.")
		return
	}
	row, found, err := store.MediaRowByID(id)
	if err != nil {
		s.storeError(w, err)
		return
	}
	if !found {
		s.fail(w, http.StatusNotFound, "No such picture.")
		return
	}
	path, err := store.MediaPath(row)
	if err != nil {
		s.logger.Printf("cherry: staff picture %d: %v", id, err)
		s.fail(w, http.StatusNotFound, "No such picture.")
		return
	}
	file, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		s.fail(w, http.StatusNotFound, "No such picture.")
		return
	}
	if err != nil {
		s.logger.Printf("cherry: staff picture %d: open: %v", id, err)
		s.fail(w, http.StatusInternalServerError, "Something went wrong. The server log has the detail.")
		return
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 {
		s.fail(w, http.StatusNotFound, "No such picture.")
		return
	}
	if info.Size() > photoMaxBytes {
		s.fail(w, http.StatusRequestEntityTooLarge, "The picture is too large to show.")
		return
	}
	head := make([]byte, photoSniffSize)
	n, err := io.ReadFull(file, head)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
		s.fail(w, http.StatusInternalServerError, "Something went wrong. The server log has the detail.")
		return
	}
	ctype := http.DetectContentType(head[:n])
	if !photoTypes[ctype] {
		s.fail(w, http.StatusUnsupportedMediaType, "This file is not a picture the staff pages show.")
		return
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		s.fail(w, http.StatusInternalServerError, "Something went wrong. The server log has the detail.")
		return
	}
	h := w.Header()
	h.Set("Content-Type", ctype)
	h.Set("Content-Disposition", "inline")
	h.Set("X-Content-Type-Options", "nosniff")
	http.ServeContent(w, r, "", info.ModTime(), file)
}

// handlePhotoRemove: POST /photos/{id}/remove (privileged). The form: reason. The picture's uploader is the player the
// removal names; the store clears the picture everywhere it is shown and writes the audit row.
func (s *Server) handlePhotoRemove(w http.ResponseWriter, r *http.Request, sess session) {
	mid, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || mid < 1 {
		s.fail(w, http.StatusBadRequest, "The picture number is not valid.")
		return
	}
	row, found, err := store.MediaRowByID(mid)
	if err != nil {
		s.storeError(w, err)
		return
	}
	if !found {
		s.fail(w, http.StatusNotFound, "No such picture.")
		return
	}
	pid, ok := store.PlayerRowOfAid(row.Owner)
	if !ok {
		s.fail(w, http.StatusNotFound, "The uploader has no account, so this picture cannot be removed here.")
		return
	}
	if err := store.RemovePlayerMedia(pid, mid, r.PostFormValue("reason"), sess.identity); err != nil {
		if errors.Is(err, store.ErrNoItem) {
			s.fail(w, http.StatusNotFound, "No such picture.")
			return
		}
		s.storeError(w, err)
		return
	}
	redirect(w, http.StatusSeeOther, "/photos?notice=picture-removed")
}

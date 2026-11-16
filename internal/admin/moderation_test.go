package admin

import (
	"bytes"
	"fmt"
	"html"
	"image"
	"image/png"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"cherry/internal/store"
)

// chatWriter starts the chat log's writer for the test; it stops before the fixture's database closes (cleanups run last in,
// first out).
func chatWriter(t *testing.T) {
	t.Helper()
	store.StartChatLog()
	t.Cleanup(store.StopChatLog)
}

// say records one chat message the way the game does and waits until the log holds it.
func say(kind, channel, sender, name, text string) {
	store.RecordChat(store.ChatEntry{Kind: kind, Channel: channel, Sender: sender, SenderName: name, Text: text})
}

// pngBytes is a small valid PNG.
func pngBytes(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// seedPicture writes a stored picture of kind for owner (the file holds data, named as an upload names it) and returns its row.
func seedPicture(t *testing.T, kind, owner, ctime string, data []byte) store.MediaFile {
	t.Helper()
	dir, err := store.MediaDir(kind)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	file := store.MediaFileName(owner, ctime, owner+"_"+ctime)
	if err := os.WriteFile(filepath.Join(dir, file), data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := store.AddMediaFile(store.MediaFile{Kind: kind, Owner: owner, File: file, Size: int64(len(data))}); err != nil {
		t.Fatal(err)
	}
	row, found, err := store.MediaRowByFile(file)
	if err != nil || !found {
		t.Fatalf("row of the picture: found=%t err=%v", found, err)
	}
	return row
}

// wrapCell marks the text cell of a chat row: one per message on the page.
var wrapCell = regexp.MustCompile(`<td class="wrap">`)

// chatRowsIn is the number of messages on a chat page.
func chatRowsIn(body string) int { return len(wrapCell.FindAllString(body, -1)) }

// hrefsWith returns the hrefs of body that contain part, unescaped.
func hrefsWith(body, part string) []string {
	var out []string
	for _, m := range regexp.MustCompile(`href="([^"]*)"`).FindAllStringSubmatch(body, -1) {
		if strings.Contains(m[1], part) {
			out = append(out, html.UnescapeString(m[1]))
		}
	}
	return out
}

// TestModerationPagesNeedASession: the chat log, the photo log and a picture are the sign-in page without a session.
func TestModerationPagesNeedASession(t *testing.T) {
	fx := newFixture(t, devConfig())
	c := fx.client()
	for _, target := range []string{"/chat", "/chat?kind=private", "/photos", "/photos/1/file"} {
		expect(t, c.get(target), http.StatusUnauthorized)
	}
}

// TestChatPageEscapesAndLinks: a message's text is shown as text (a script tag is escaped), a sender with an account links to
// its player page, and the player page links to the chat by that player.
func TestChatPageEscapesAndLinks(t *testing.T) {
	store.ResetSocial()
	fx := newFixture(t, devConfig())
	chatWriter(t)
	c := fx.client()
	c.signInDev("owner")
	pid := seedPlayer(t, "dev-chatter", "9201", "Chatty", 0)
	say(store.ChatPrivate, "1960000000", "9201", "Chatty", "<script>alert(1)</script> & hi")
	say(store.ChatLounge, "1930000001", "9202", "Ghost", "a line from nobody's account")
	store.FlushChatLog()

	r := c.get("/chat")
	expect(t, r, http.StatusOK)
	checkPage(t, "/chat", r)
	if !strings.Contains(r.body, "&lt;script&gt;alert(1)&lt;/script&gt; &amp; hi") {
		t.Fatal("the message text is not shown escaped")
	}
	if strings.Contains(r.body, "<script>alert(1)") {
		t.Fatal("the message text reached the page as markup")
	}
	if !strings.Contains(r.body, fmt.Sprintf(`href="/players/%d"`, pid)) {
		t.Fatal("the sender with an account does not link to its player page")
	}
	if strings.Contains(r.body, `href="/players/0"`) {
		t.Fatal("a sender with no account links to a player")
	}
	if !strings.Contains(r.body, "a line from nobody&#39;s account") {
		t.Fatal("the lounge line is missing")
	}
	if !strings.Contains(c.get(playerURL(pid)).body, `href="/chat?player=9201"`) {
		t.Fatal("the player page has no link to the chat by this player")
	}
	if !strings.Contains(c.get(playerURL(pid)).body, `href="/photos?player=9201"`) {
		t.Fatal("the player page has no link to the photos by this player")
	}
}

// TestChatFiltersAndPagination: each filter keeps its rows; the page holds fifty rows and the older link reads the rest; a
// filter that is not a value the log holds is refused with 400.
func TestChatFiltersAndPagination(t *testing.T) {
	store.ResetSocial()
	fx := newFixture(t, devConfig())
	chatWriter(t)
	c := fx.client()
	c.signInDev("owner")
	seedPlayer(t, "dev-pager", "9203", "Pager", 0)
	for i := 1; i <= 55; i++ {
		say(store.ChatPrivate, "1960000000", "9203", "Pager", fmt.Sprintf("msg %d", i))
	}
	for i := 1; i <= 5; i++ {
		say(store.ChatLounge, "1930000001", "9204", "Other", fmt.Sprintf("lounge %d", i))
	}
	store.FlushChatLog()

	if n := chatRowsIn(c.get("/chat").body); n != chatPage {
		t.Fatalf("first page has %d rows, want %d", n, chatPage)
	}
	older := hrefsWith(c.get("/chat").body, "before=")
	if len(older) == 0 {
		t.Fatal("the first page has no link to older messages")
	}
	if n := chatRowsIn(c.get(older[0]).body); n != 10 {
		t.Fatalf("older page has %d rows, want the other 10", n)
	}
	if n := chatRowsIn(c.get("/chat?kind=lounge").body); n != 5 {
		t.Fatalf("kind lounge: %d rows, want 5", n)
	}
	if n := chatRowsIn(c.get("/chat?channel=1960000000").body); n != chatPage {
		t.Fatalf("channel: %d rows on the first page, want %d", n, chatPage)
	}
	if n := chatRowsIn(c.get("/chat?player=pager").body); n != chatPage {
		t.Fatalf("player by name: %d rows on the first page, want %d", n, chatPage)
	}
	if n := chatRowsIn(c.get("/chat?player=9204").body); n != 5 {
		t.Fatalf("player by avatar id: %d rows, want 5", n)
	}
	if n := chatRowsIn(c.get("/chat?text=MSG%2054").body); n != 1 {
		t.Fatalf("text (any case): %d rows, want 1", n)
	}
	today := time.Now().UTC().Format(time.DateOnly)
	if n := chatRowsIn(c.get("/chat?from=" + today + "&to=" + today).body); n != chatPage {
		t.Fatalf("today: %d rows on the first page, want %d", n, chatPage)
	}
	for _, bad := range []string{
		"/chat?kind=dm", "/chat?channel=12a", "/chat?from=2026-02-30", "/chat?around=soon",
		"/chat?before=0", "/chat?before=1&before=2", "/chat?player=" + strings.Repeat("x", 101),
	} {
		expect(t, c.get(bad), http.StatusBadRequest)
	}
}

// TestChatContextShowsTheChannelAroundAMessage: a row's channel link shows that channel in the window around the message,
// and only that channel.
func TestChatContextShowsTheChannelAroundAMessage(t *testing.T) {
	store.ResetSocial()
	fx := newFixture(t, devConfig())
	chatWriter(t)
	c := fx.client()
	c.signInDev("owner")
	say(store.ChatLounge, "1930000001", "9205", "Lee", "lounge context")
	say(store.ChatPrivate, "1960000000", "9205", "Lee", "private context")
	store.FlushChatLog()

	links := hrefsWith(c.get("/chat").body, "around=")
	var lounge string
	for _, l := range links {
		if strings.Contains(l, "kind=lounge") {
			lounge = l
		}
	}
	if lounge == "" {
		t.Fatalf("no context link for the lounge row among %v", links)
	}
	r := c.get(lounge)
	expect(t, r, http.StatusOK)
	if !strings.Contains(r.body, "within 10 minutes of") {
		t.Fatal("the context page does not say which window it shows")
	}
	if !strings.Contains(r.body, "lounge context") || strings.Contains(r.body, "private context") {
		t.Fatal("the context page does not show only the channel")
	}
}

// TestPhotosGridFiltersAndPictures: the photo page lists each picture as a lazily loaded image of the file route, filters by
// kind and player, and the file route serves a picture with its type and refuses what is not a picture.
func TestPhotosGridFiltersAndPictures(t *testing.T) {
	store.ResetSocial()
	fx := newFixture(t, devConfig())
	c := fx.client()
	c.signInDev("owner")
	seedPlayer(t, "dev-snap", "9301", "Snap", 0)
	pic := pngBytes(t)
	diary := seedPicture(t, store.MediaDiary, "9301", "1760000101", pic)
	seedPicture(t, store.MediaRoom, "9301", "1760000102", pic)
	seedPicture(t, store.MediaProfile, "9302", "1760000103", pic)

	grid := c.get("/photos")
	expect(t, grid, http.StatusOK)
	checkPage(t, "/photos", grid)
	if !strings.Contains(grid.body, fmt.Sprintf(`<img src="/photos/%d/file"`, diary.ID)) || !strings.Contains(grid.body, `loading="lazy"`) {
		t.Fatal("the grid has no lazily loaded image for the diary picture")
	}
	if !strings.Contains(grid.body, fmt.Sprintf(`action="/photos/%d/remove"`, diary.ID)) {
		t.Fatal("the grid has no removal form for the diary picture")
	}
	if n := strings.Count(grid.body, `class="photo-card"`); n != 3 {
		t.Fatalf("%d cards, want 3", n)
	}
	if n := strings.Count(c.get("/photos?kind=room").body, `class="photo-card"`); n != 1 {
		t.Fatalf("kind room: %d cards, want 1", n)
	}
	if n := strings.Count(c.get("/photos?player=9301").body, `class="photo-card"`); n != 2 {
		t.Fatalf("player 9301: %d cards, want 2", n)
	}
	if n := strings.Count(c.get("/photos?player=snap").body, `class="photo-card"`); n != 2 {
		t.Fatalf("player by name: %d cards, want 2", n)
	}
	for _, bad := range []string{"/photos?kind=avatar", "/photos?player=" + strings.Repeat("y", 101), "/photos?before=x"} {
		expect(t, c.get(bad), http.StatusBadRequest)
	}

	file := c.get(fmt.Sprintf("/photos/%d/file", diary.ID))
	expect(t, file, http.StatusOK)
	if ct := file.Header.Get("Content-Type"); ct != "image/png" {
		t.Fatalf("content type %q, want image/png", ct)
	}
	if cd := file.Header.Get("Content-Disposition"); cd != "inline" {
		t.Fatalf("content disposition %q, want inline", cd)
	}
	if nosniff := file.Header.Get("X-Content-Type-Options"); nosniff != "nosniff" {
		t.Fatalf("X-Content-Type-Options %q", nosniff)
	}
	if file.body != string(pic) {
		t.Fatal("the served bytes are not the stored picture")
	}
	expect(t, c.get("/photos/999999/file"), http.StatusNotFound)
	expect(t, c.get("/photos/abc/file"), http.StatusBadRequest)

	// Bytes that are not a picture, and a page dressed up as one, are refused whatever the row says.
	for name, data := range map[string][]byte{"text": []byte("just text, no picture"), "html": []byte("<html><script>alert(1)</script></html>")} {
		row := seedPicture(t, store.MediaDiary, "9301", "1760000199"+name, data)
		r := c.get(fmt.Sprintf("/photos/%d/file", row.ID))
		expect(t, r, http.StatusUnsupportedMediaType)
		if strings.Contains(r.body, "<script>alert") {
			t.Errorf("%s: the refusal shows the file", name)
		}
	}

	// A file over the size cap is refused before it is read.
	big := seedPicture(t, store.MediaDiary, "9301", "1760000300", pic)
	bigPath, err := store.MediaPath(big)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(bigPath, photoMaxBytes+1); err != nil {
		t.Fatal(err)
	}
	expect(t, c.get(fmt.Sprintf("/photos/%d/file", big.ID)), http.StatusRequestEntityTooLarge)

	// A signed-out browser gets the sign-in page, not the picture.
	stranger := fx.client()
	expect(t, stranger.get(fmt.Sprintf("/photos/%d/file", diary.ID)), http.StatusUnauthorized)
}

// TestPhotoRemovalFromThePhotoPage: a removal is a privileged POST with the CSRF token and a reason; it deletes the picture,
// its file and its references, writes the audit row without the picture's address, and goes back to the photo page.
func TestPhotoRemovalFromThePhotoPage(t *testing.T) {
	store.ResetSocial()
	fx := newFixture(t, devConfig())
	c := fx.client()
	c.signInDev("owner")
	seedPlayer(t, "dev-gone", "9311", "Gone", 0)
	row := seedPicture(t, store.MediaDiary, "9311", "1760000400", pngBytes(t))
	path, err := store.MediaPath(row)
	if err != nil {
		t.Fatal(err)
	}
	remove := fmt.Sprintf("/photos/%d/remove", row.ID)
	if page := c.get("/photos").body; !strings.Contains(page, remove) {
		t.Fatalf("the photo page does not offer %s", remove)
	}
	expect(t, c.post(remove, url.Values{"reason": {"x"}}), http.StatusForbidden)
	if r := c.get(remove); r.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("GET of the removal: status %d, want 405", r.StatusCode)
	}
	expect(t, c.post(remove, url.Values{"csrf": {c.csrf()}, "reason": {" "}}), http.StatusBadRequest)
	expect(t, c.post("/photos/999999/remove", url.Values{"csrf": {c.csrf()}, "reason": {"x"}}), http.StatusNotFound)
	if _, found, _ := store.MediaRowByFile(row.File); !found {
		t.Fatal("a refused removal deleted the row")
	}

	// The fresh sign-in window is five minutes: an older sign-in gets the sign-in page and removes nothing.
	fx.clk.advance(6 * time.Minute)
	expect(t, c.post(remove, url.Values{"csrf": {c.csrf()}, "reason": {"stale"}}), http.StatusForbidden)
	if _, found, _ := store.MediaRowByFile(row.File); !found {
		t.Fatal("a removal without a fresh sign-in deleted the row")
	}
	c.signInDev("owner")

	r := c.post(remove, url.Values{"csrf": {c.csrf()}, "reason": {"unwanted picture"}})
	expect(t, r, http.StatusSeeOther)
	if loc := r.Header.Get("Location"); loc != "/photos?notice=picture-removed" {
		t.Fatalf("redirect to %q", loc)
	}
	if _, found, _ := store.MediaRowByFile(row.File); found {
		t.Fatal("the removed picture's row remains")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("the removed picture's file remains: %v", err)
	}
	rows := auditRows(t)
	if !hasAudit(rows, "dev-owner", "remove-media", "unwanted picture") {
		t.Fatal("no remove-media audit row with the reason")
	}
	for _, a := range rows {
		if a.Action == "remove-media" && (strings.Contains(a.Detail, row.File) || strings.Contains(a.Detail, "/photos")) {
			t.Fatal("the audit row names the picture's file or address")
		}
	}
	if !strings.Contains(c.get("/photos?notice=picture-removed").body, notices["picture-removed"]) {
		t.Fatal("the photo page does not show the removal's notice")
	}
}

// TestModerationIsInTheSidebar: the chat log and the photos are under a Moderation heading, and the entry of the page
// you are on is marked.
func TestModerationIsInTheSidebar(t *testing.T) {
	fx := newFixture(t, devConfig())
	c := fx.client()
	c.signInDev("owner")
	r := c.get("/chat")
	if !strings.Contains(r.body, `<h2 class="nav-group">Moderation</h2>`) {
		t.Fatal("the sidebar has no Moderation heading")
	}
	if !strings.Contains(r.body, `href="/chat" aria-current="page"`) || !strings.Contains(r.body, `href="/photos"`) {
		t.Fatal("the Moderation links are not in the sidebar, or the chat log is not marked")
	}
	if !strings.Contains(c.get("/photos").body, `href="/photos" aria-current="page"`) {
		t.Fatal("the photos entry is not marked on the photo page")
	}
}

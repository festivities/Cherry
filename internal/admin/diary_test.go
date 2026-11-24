package admin

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"cherry/internal/store"
)

func diaryCardCount(body string) int {
	return strings.Count(body, `class="diary-card `)
}

func TestDiaryRequiresSessionAndNavAndEscapesXSS(t *testing.T) {
	store.ResetSocial()
	t.Cleanup(store.ResetSocial)
	fx := newFixture(t, devConfig())
	c := fx.client()

	expect(t, c.get("/diary"), http.StatusUnauthorized)
	expect(t, c.get("/diary?filter=1&posts=1"), http.StatusUnauthorized)

	c.signInDev("owner")
	pidAlice := seedPlayer(t, "dev-da1", "9701", "<b>Alice</b>", 0)
	pidBob := seedPlayer(t, "dev-db1", "9702", "Bob<script>x</script>", 0)
	pic := seedPicture(t, store.MediaDiary, "9701", "1760000101", pngBytes(t))
	picURL := store.DiaryDownloadPath + "?userid=9701&ctime=1760000101&oid=9701_1760000101"
	missingURL := store.DiaryDownloadPath + "?userid=9701&ctime=1760000999&oid=9701_1760000999"

	store.SocialMu.Lock()
	store.DiaryPosts = []store.DiaryPost{
		{
			DiaryNo: "11", AvatarID: "9701", Nickname: "<b>Alice</b>", RegDate: "1700000002000",
			Title: `<script>alert("t")</script>`, Content: `<img src=x onerror=alert(1)> body`, OpenState: "public",
			Images: []store.DiaryImage{{ImageURL: picURL}},
		},
		{
			DiaryNo: "10", AvatarID: "9702", Nickname: "Bob<script>x</script>", RegDate: "1700000001000",
			Title: "Unresolved media", Content: "no stored row", OpenState: "friend",
			Images: []store.DiaryImage{{ImageURL: missingURL}},
		},
	}
	store.DiaryComments = []store.DiaryComment{
		{
			CommentNo: "5", DiaryNo: "11", OwnerAvatarID: "9701", CommentAvatarID: "9702",
			Nickname: "Bob", Content: `<script>alert("c")</script> nice`, Date: "1700000003000",
		},
	}
	store.SocialMu.Unlock()

	r := c.get("/diary")
	expect(t, r, http.StatusOK)
	checkPage(t, "/diary", r)

	for _, want := range []string{
		`href="/diary" aria-current="page"`,
		`&lt;b&gt;Alice&lt;/b&gt;`,
		`Bob&lt;script&gt;x&lt;/script&gt;`,
		`&lt;script&gt;alert(&#34;t&#34;)&lt;/script&gt;`,
		`&lt;img src=x onerror=alert(1)&gt; body`,
		`&lt;script&gt;alert(&#34;c&#34;)&lt;/script&gt; nice`,
		fmt.Sprintf(`<a href="/photos/%d/file"><img src="/photos/%d/file" alt="Diary photo #%d" loading="lazy" class="diary-thumb"></a>`, pic.ID, pic.ID, pic.ID),
		`Photo attached (no stored media row).`,
		fmt.Sprintf(`href="/players/%d"`, pidAlice),
		fmt.Sprintf(`href="/players/%d"`, pidBob),
		`<script src="/static/diary.js?v=`,
		`data-live="1"`,
	} {
		if !strings.Contains(r.body, want) {
			t.Errorf("/diary missing %q", want)
		}
	}
	for _, bad := range []string{`<script>alert`, `<img src=x onerror`} {
		if strings.Contains(r.body, bad) {
			t.Errorf("/diary leaked unescaped markup %q", bad)
		}
	}
}

func TestDiaryFiltersPaginationAndBadParams(t *testing.T) {
	store.ResetSocial()
	t.Cleanup(store.ResetSocial)
	fx := newFixture(t, devConfig())
	c := fx.client()
	c.signInDev("owner")

	seedPlayer(t, "dev-df1", "9711", "PhotoGal", 0)
	seedPlayer(t, "dev-df2", "9712", "TextGuy", 0)
	pic := seedPicture(t, store.MediaDiary, "9711", "1760000201", pngBytes(t))
	picURL := store.DiaryDownloadPath + "?userid=9711&ctime=1760000201&oid=9711_1760000201"

	store.SocialMu.Lock()
	for i := 1; i <= 52; i++ {
		imgs := []store.DiaryImage(nil)
		aid, nick, title := "9712", "TextGuy", fmt.Sprintf("Text title %d", i)
		if i%2 == 0 {
			aid, nick, title = "9711", "PhotoGal", fmt.Sprintf("Photo title %d", i)
			imgs = []store.DiaryImage{{ImageURL: picURL}}
		}
		store.DiaryPosts = append([]store.DiaryPost{{
			DiaryNo: strconv.Itoa(i), AvatarID: aid, Nickname: nick,
			RegDate: fmt.Sprintf("%d", int64(1700000000000+i*1000)),
			Title:   title, Content: fmt.Sprintf("body %d", i), Images: imgs,
		}}, store.DiaryPosts...)
	}
	for i := 1; i <= 3; i++ {
		store.DiaryComments = append(store.DiaryComments, store.DiaryComment{
			CommentNo: strconv.Itoa(i), DiaryNo: "52", OwnerAvatarID: "9711", CommentAvatarID: "9712",
			Content: fmt.Sprintf("comment %d", i), Date: fmt.Sprintf("%d", int64(1700000100000+i*1000)),
		})
	}
	store.SocialMu.Unlock()
	_ = pic

	// Default: 52 posts + 3 comments = 55 items -> page 1 has 50, page 2 has 5.
	p1 := c.get("/diary")
	expect(t, p1, http.StatusOK)
	if n := diaryCardCount(p1.body); n != diaryPageSize {
		t.Fatalf("page 1 cards = %d, want %d", n, diaryPageSize)
	}
	if !strings.Contains(p1.body, "Showing 1–50 of 55 matching items.") {
		t.Fatal("page 1 missing result range summary")
	}
	p2 := c.get("/diary?page=2")
	expect(t, p2, http.StatusOK)
	if n := diaryCardCount(p2.body); n != 5 {
		t.Fatalf("page 2 cards = %d, want 5", n)
	}
	if !strings.Contains(p2.body, "Showing 51–55 of 55 matching items.") || !strings.Contains(p2.body, `data-live="0"`) {
		t.Fatal("page 2 missing summary or data-live=0")
	}

	// Checkbox combinations.
	for _, tc := range []struct {
		query string
		want  int
	}{
		{"/diary?filter=1", 0}, // all unchecked
		{"/diary?filter=1&posts=1&photos=1", 26},
		{"/diary?filter=1&posts=1&textonly=1", 26},
		{"/diary?filter=1&comments=1", 3},
		{"/diary?filter=1&posts=1&photos=1&textonly=1&player=9711", 26},
		{"/diary?filter=1&posts=1&comments=1&photos=1&textonly=1&player=9711", 29}, // 26 posts + 3 comments on 9711's post
		{"/diary?filter=1&posts=1&photos=1&text=Photo+title+52", 1},
		{"/diary?filter=1&comments=1&text=comment+2", 1},
	} {
		r := c.get(tc.query)
		expect(t, r, http.StatusOK)
		if got := diaryCardCount(r.body); got != tc.want {
			t.Errorf("%s: got %d cards, want %d", tc.query, got, tc.want)
		}
	}

	// Filtered pagination preserves filter=1 and active checkboxes.
	f1 := c.get("/diary?filter=1&posts=1&photos=1")
	expect(t, f1, http.StatusOK)
	if !strings.Contains(f1.body, `data-query="filter=1&amp;photos=1&amp;posts=1"`) {
		t.Errorf("filtered page did not preserve query attribute: %s", f1.body)
	}

	// Bad query params -> 400.
	for _, bad := range []string{
		"/diary?page=0",
		"/diary?page=-1",
		"/diary?page=abc",
		"/diary?page=1&page=2",
		"/diary?player=" + strings.Repeat("a", 101),
		"/diary?text=" + strings.Repeat("b", 101),
	} {
		expect(t, c.get(bad), http.StatusBadRequest)
	}
}

func TestDiaryRemovePostAndComment(t *testing.T) {
	store.ResetSocial()
	t.Cleanup(store.ResetSocial)
	fx := newFixture(t, devConfig())
	c := fx.client()
	c.signInDev("owner")

	pidAuthor := seedPlayer(t, "dev-dr1", "9721", "Author", 0)
	pidCommenter := seedPlayer(t, "dev-dr2", "9722", "Commenter", 0)

	store.SocialMu.Lock()
	store.DiaryPosts = []store.DiaryPost{
		{DiaryNo: "21", AvatarID: "9721", Nickname: "Author", RegDate: "1700000001000", Title: "Post A", Content: "remove me", Images: []store.DiaryImage{}},
		{DiaryNo: "22", AvatarID: "9721", Nickname: "Author", RegDate: "1700000002000", Title: "Post B", Content: "keep me", Images: []store.DiaryImage{}},
	}
	store.DiaryComments = []store.DiaryComment{
		{CommentNo: "31", DiaryNo: "22", OwnerAvatarID: "9721", CommentAvatarID: "9722", Nickname: "Commenter", Content: "bad comment", Date: "1700000003000"},
	}
	if err := store.SaveSocialLocked(store.SocialChange{Posts: []string{"21", "22"}, Comments: []string{"31"}, Scalars: true}); err != nil {
		store.SocialMu.Unlock()
		t.Fatal(err)
	}
	store.SocialMu.Unlock()

	// CSRF and validation checks.
	expect(t, c.post("/diary/remove-post", url.Values{"player_id": {strconv.FormatInt(pidAuthor, 10)}, "no": {"21"}, "reason": {"spam"}}), http.StatusForbidden)
	expect(t, c.post("/diary/remove-post", url.Values{"csrf": {c.csrf()}, "player_id": {"0"}, "no": {"21"}, "reason": {"spam"}}), http.StatusBadRequest)
	expect(t, c.post("/diary/remove-post", url.Values{"csrf": {c.csrf()}, "player_id": {strconv.FormatInt(pidAuthor, 10)}, "no": {"abc"}, "reason": {"spam"}}), http.StatusBadRequest)
	expect(t, c.post("/diary/remove-post", url.Values{"csrf": {c.csrf()}, "player_id": {strconv.FormatInt(pidAuthor, 10)}, "no": {"21"}, "reason": {"  "}}), http.StatusBadRequest)

	// Fresh login check: stale login returns 403 and redirects back to /diary after re-login.
	fx.clk.advance(6 * time.Minute)
	stale := c.post("/diary/remove-post", url.Values{"csrf": {c.csrf()}, "player_id": {strconv.FormatInt(pidAuthor, 10)}, "no": {"21"}, "reason": {"stale"}})
	expect(t, stale, http.StatusForbidden)
	if !strings.Contains(stale.body, `/auth/login?next=%2Fdiary`) {
		t.Fatalf("stale fresh-login redirect did not point back to /diary: %s", stale.body)
	}
	c.signInDev("owner")

	// Remove post 21 -> redirects to /diary?notice=post-removed.
	rp := c.post("/diary/remove-post", url.Values{"csrf": {c.csrf()}, "player_id": {strconv.FormatInt(pidAuthor, 10)}, "no": {"21"}, "reason": {"spam post"}})
	expect(t, rp, http.StatusSeeOther)
	if loc := rp.Header.Get("Location"); loc != "/diary?notice=post-removed" {
		t.Fatalf("post remove redirect = %q, want /diary?notice=post-removed", loc)
	}
	if !strings.Contains(c.get(rp.Header.Get("Location")).body, notices["post-removed"]) {
		t.Fatal("missing post-removed flash notice")
	}

	// Remove comment 31 -> redirects to /diary?notice=comment-removed.
	rc := c.post("/diary/remove-comment", url.Values{"csrf": {c.csrf()}, "player_id": {strconv.FormatInt(pidCommenter, 10)}, "no": {"31"}, "reason": {"rude comment"}})
	expect(t, rc, http.StatusSeeOther)
	if loc := rc.Header.Get("Location"); loc != "/diary?notice=comment-removed" {
		t.Fatalf("comment remove redirect = %q, want /diary?notice=comment-removed", loc)
	}
	if !strings.Contains(c.get(rc.Header.Get("Location")).body, notices["comment-removed"]) {
		t.Fatal("missing comment-removed flash notice")
	}

	// Verify state and audit rows.
	store.SocialMu.Lock()
	remainingPosts := len(store.DiaryPosts)
	remainingComments := len(store.DiaryComments)
	store.SocialMu.Unlock()
	if remainingPosts != 1 || remainingComments != 0 {
		t.Fatalf("remaining posts=%d comments=%d, want 1 and 0", remainingPosts, remainingComments)
	}
	rows := auditRows(t)
	if !hasAudit(rows, "dev-owner", "remove-post", "spam post") {
		t.Fatal("missing remove-post audit row")
	}
	if !hasAudit(rows, "dev-owner", "remove-comment", "rude comment") {
		t.Fatal("missing remove-comment audit row")
	}
}

func TestDiaryJSONEndpoint(t *testing.T) {
	store.ResetSocial()
	t.Cleanup(store.ResetSocial)
	fx := newFixture(t, devConfig())
	c := fx.client()

	// 401 JSON without session.
	unauth := c.get("/diary.json?after_post=0&after_comment=0")
	expect(t, unauth, http.StatusUnauthorized)
	if ct := unauth.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Fatalf("401 content type = %q", ct)
	}

	c.signInDev("owner")
	seedPlayer(t, "dev-dj1", "9731", "Jone", 0)
	seedPlayer(t, "dev-dj2", "9732", "Jtwo", 0)

	store.SocialMu.Lock()
	for i := 1; i <= 55; i++ {
		store.DiaryPosts = append([]store.DiaryPost{{
			DiaryNo: strconv.Itoa(i), AvatarID: "9731", Nickname: "Jone",
			RegDate: fmt.Sprintf("%d", int64(1700000000000+i*1000)),
			Title:   fmt.Sprintf("Title %d", i), Content: fmt.Sprintf("Body %d", i),
		}}, store.DiaryPosts...)
	}
	store.DiaryComments = []store.DiaryComment{
		{CommentNo: "1", DiaryNo: "55", OwnerAvatarID: "9731", CommentAvatarID: "9732", Nickname: "Jtwo", Content: "c1", Date: "1700000056000"},
	}
	store.SocialMu.Unlock()

	// ?before_time=&before_no=&before_kind= returns descending rows with has_more.
	bRes := c.get("/diary.json?before_time=1700000056000&before_no=1&before_kind=comment")
	expect(t, bRes, http.StatusOK)
	if ct := bRes.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Fatalf("200 content type = %q", ct)
	}
	var bData struct {
		Rows    []diaryRow `json:"rows"`
		HasMore bool       `json:"has_more"`
	}
	if err := json.Unmarshal([]byte(bRes.body), &bData); err != nil {
		t.Fatal(err)
	}
	if len(bData.Rows) != diaryPageSize || !bData.HasMore || bData.Rows[0].No != "55" || bData.Rows[diaryPageSize-1].No != "6" {
		t.Fatalf("before got len=%d hasMore=%t first=%+v last=%+v", len(bData.Rows), bData.HasMore, bData.Rows[0], bData.Rows[len(bData.Rows)-1])
	}

	// ?after_post=&after_comment= returns ascending rows and advances cursors past non-matching items.
	store.SocialMu.Lock()
	store.DiaryPosts = append([]store.DiaryPost{
		{DiaryNo: "56", AvatarID: "9732", Nickname: "Jtwo", RegDate: "1700000057000", Title: "Other", Content: "skip"},
		{DiaryNo: "57", AvatarID: "9731", Nickname: "Jone", RegDate: "1700000058000", Title: "Match", Content: "hit"},
	}, store.DiaryPosts...)
	store.DiaryComments = append(store.DiaryComments,
		store.DiaryComment{CommentNo: "2", DiaryNo: "57", OwnerAvatarID: "9731", CommentAvatarID: "9732", Nickname: "Jtwo", Content: "reply", Date: "1700000059000"},
	)
	store.SocialMu.Unlock()

	aRes := c.get("/diary.json?after_post=55&after_comment=1&filter=1&posts=1&photos=1&textonly=1&player=9731")
	expect(t, aRes, http.StatusOK)
	var aData struct {
		Rows          []diaryRow `json:"rows"`
		LatestPost    int64      `json:"latest_post"`
		LatestComment int64      `json:"latest_comment"`
		HasMore       bool       `json:"has_more"`
	}
	if err := json.Unmarshal([]byte(aRes.body), &aData); err != nil {
		t.Fatal(err)
	}
	if len(aData.Rows) != 1 || aData.Rows[0].No != "57" || aData.LatestPost != 57 || aData.LatestComment != 2 || aData.HasMore {
		t.Fatalf("after filtered got %+v", aData)
	}

	// Bad/mixed/missing params -> 400 JSON.
	for _, bad := range []string{
		"/diary.json",
		"/diary.json?after_post=0",
		"/diary.json?after_comment=0",
		"/diary.json?after_post=-1&after_comment=0",
		"/diary.json?after_post=0&after_comment=0&before_time=0&before_no=1&before_kind=post",
		"/diary.json?before_time=0&before_no=1",
		"/diary.json?before_time=0&before_no=1&before_kind=bogus",
		"/diary.json?before_time=-1&before_no=1&before_kind=post",
		"/diary.json?after_post=0&after_comment=0&player=" + strings.Repeat("x", 101),
	} {
		r := c.get(bad)
		expect(t, r, http.StatusBadRequest)
		if ct := r.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
			t.Errorf("%s: content type = %q, want application/json", bad, ct)
		}
	}

	// Polling /diary.json does NOT extend the idle timeout.
	fx.clk.advance(idleTimeout - 5*time.Minute)
	expect(t, c.get("/diary.json?after_post=0&after_comment=0"), http.StatusOK)
	fx.clk.advance(6 * time.Minute)
	expect(t, c.get("/diary.json?after_post=0&after_comment=0"), http.StatusUnauthorized)
}

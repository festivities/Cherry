package store

import (
	"errors"
	"fmt"
	"slices"
	"strconv"
	"testing"
)

func diaryRowKeys(rows []DiaryModRow) []string {
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = r.Kind + ":" + r.No
	}
	return out
}

func TestDiaryModSearchFiltersOrderingAndPhotos(t *testing.T) {
	socialDB(t)
	mediaDataDir(t)

	_, idAlice := staffAccount(t, "dev-da", "9601", "Alice", 0)
	_, idBob := staffAccount(t, "dev-db", "9602", "Bobby", 0)

	m1 := storeMediaFile(t, MediaDiary, "9601", "9601", "1760000101", "9601_1760000101")
	m2 := storeMediaFile(t, MediaDiary, "9601", "9601", "1760000102", "9601_1760000102")
	url1 := DiaryDownloadPath + "?userid=9601&ctime=1760000101&oid=9601_1760000101"
	url2 := DiaryDownloadPath + "?userid=9601&ctime=1760000102&oid=9601_1760000102"
	urlMissing := DiaryDownloadPath + "?userid=9601&ctime=1760000999&oid=9601_1760000999"

	SocialMu.Lock()
	DiaryPosts = []DiaryPost{
		{
			DiaryNo: "10", AvatarID: "9601", Nickname: "Alice", RegDate: "1700000003000",
			Title: "Beach Day", Content: "Sunny waves", OpenState: "public",
			Images: []DiaryImage{{ImageURL: url2}, {ImageURL: urlMissing}, {ImageURL: url1}},
		},
		{
			DiaryNo: "2", AvatarID: "9602", Nickname: "Bobby", RegDate: "1700000001000",
			Title: "Plain Note", Content: "Only text here", OpenState: "friend",
		},
		{
			DiaryNo: "1", AvatarID: "9699", Nickname: "Ghost", RegDate: "bad-date",
			Title: "Legacy", Content: "No account", OpenState: "private",
		},
	}
	DiaryComments = []DiaryComment{
		{
			CommentNo: "5", DiaryNo: "2", OwnerAvatarID: "9602", CommentAvatarID: "9601",
			Nickname: "", Content: "Nice note!", Date: "1700000002000",
		},
		{
			CommentNo: "10", DiaryNo: "10", OwnerAvatarID: "9601", CommentAvatarID: "9699",
			Nickname: "Ghost", Content: "Same time tie", Date: "1700000003000",
		},
	}
	SocialMu.Unlock()

	allFilter := DiaryModFilter{Posts: true, Comments: true, WithPhotos: true, TextOnly: true}

	// Default all: newest-first across posts and comments; tie at time=1700000003000 no=10 puts post before comment.
	rows, total, hasMore, err := DiaryModSearch(allFilter, 0, 50)
	if err != nil {
		t.Fatalf("DiaryModSearch all: %v", err)
	}
	if total != 5 || hasMore {
		t.Fatalf("total=%d hasMore=%t, want 5, false", total, hasMore)
	}
	wantOrder := []string{"post:10", "comment:10", "comment:5", "post:2", "post:1"}
	if got := diaryRowKeys(rows); !slices.Equal(got, wantOrder) {
		t.Fatalf("order = %v, want %v", got, wantOrder)
	}

	// PhotoIDs resolved in original image order, omitting unresolved urlMissing; account IDs and names resolved.
	if !rows[0].HasPhotos || !equalIDs(rows[0].PhotoIDs, []int64{m2.ID, m1.ID}) {
		t.Fatalf("post:10 photo IDs = %v (hasPhotos=%t), want [%d %d]", rows[0].PhotoIDs, rows[0].HasPhotos, m2.ID, m1.ID)
	}
	if rows[0].AuthorPlayerID != idAlice || rows[0].AuthorName != "Alice" || rows[0].OpenState != "public" {
		t.Fatalf("post:10 author = %+v", rows[0])
	}
	if rows[2].AuthorPlayerID != idAlice || rows[2].AuthorName != "Alice" || rows[2].OwnerPlayerID != idBob || rows[2].OwnerName != "Bobby" {
		t.Fatalf("comment:5 resolved = %+v", rows[2])
	}
	if rows[4].Time != 0 || rows[4].RawDate != "bad-date" || rows[4].AuthorPlayerID != 0 || rows[4].AuthorName != "Ghost" {
		t.Fatalf("post:1 fallback = %+v", rows[4])
	}

	// Entries only (both photo and text).
	rows, total, _, err = DiaryModSearch(DiaryModFilter{Posts: true, WithPhotos: true, TextOnly: true}, 0, 50)
	if err != nil || total != 3 || !slices.Equal(diaryRowKeys(rows), []string{"post:10", "post:2", "post:1"}) {
		t.Fatalf("entries only = %v total=%d err=%v", diaryRowKeys(rows), total, err)
	}

	// Comments only (independent of WithPhotos/TextOnly).
	rows, total, _, err = DiaryModSearch(DiaryModFilter{Comments: true}, 0, 50)
	if err != nil || total != 2 || !slices.Equal(diaryRowKeys(rows), []string{"comment:10", "comment:5"}) {
		t.Fatalf("comments only = %v total=%d err=%v", diaryRowKeys(rows), total, err)
	}

	// Entries with photos only.
	rows, total, _, err = DiaryModSearch(DiaryModFilter{Posts: true, WithPhotos: true}, 0, 50)
	if err != nil || total != 1 || !slices.Equal(diaryRowKeys(rows), []string{"post:10"}) {
		t.Fatalf("with photos = %v total=%d err=%v", diaryRowKeys(rows), total, err)
	}

	// Text-only entries.
	rows, total, _, err = DiaryModSearch(DiaryModFilter{Posts: true, TextOnly: true}, 0, 50)
	if err != nil || total != 2 || !slices.Equal(diaryRowKeys(rows), []string{"post:2", "post:1"}) {
		t.Fatalf("text only = %v total=%d err=%v", diaryRowKeys(rows), total, err)
	}

	// Player filter: author aid exact ("9699") matches post:1 and comment:10.
	rows, _, _, err = DiaryModSearch(DiaryModFilter{Posts: true, Comments: true, WithPhotos: true, TextOnly: true, Player: "9699"}, 0, 50)
	if err != nil || !slices.Equal(diaryRowKeys(rows), []string{"comment:10", "post:1"}) {
		t.Fatalf("player 9699 = %v err=%v", diaryRowKeys(rows), err)
	}

	// Player filter: comment owner aid exact ("9602") matches post:2 (author) and comment:5 (owner).
	rows, _, _, err = DiaryModSearch(DiaryModFilter{Posts: true, Comments: true, WithPhotos: true, TextOnly: true, Player: "9602"}, 0, 50)
	if err != nil || !slices.Equal(diaryRowKeys(rows), []string{"comment:5", "post:2"}) {
		t.Fatalf("player 9602 = %v err=%v", diaryRowKeys(rows), err)
	}

	// Player filter: case-insensitive name substring ("bob") matches post:2 (author Bobby) and comment:5 (owner Bobby).
	rows, _, _, err = DiaryModSearch(DiaryModFilter{Posts: true, Comments: true, WithPhotos: true, TextOnly: true, Player: "BOB"}, 0, 50)
	if err != nil || !slices.Equal(diaryRowKeys(rows), []string{"comment:5", "post:2"}) {
		t.Fatalf("player BOB = %v err=%v", diaryRowKeys(rows), err)
	}

	// Text filter: title ("BEACH") and content ("note").
	rows, _, _, err = DiaryModSearch(DiaryModFilter{Posts: true, Comments: true, WithPhotos: true, TextOnly: true, Text: "BEACH"}, 0, 50)
	if err != nil || !slices.Equal(diaryRowKeys(rows), []string{"post:10"}) {
		t.Fatalf("text BEACH = %v err=%v", diaryRowKeys(rows), err)
	}
	rows, _, _, err = DiaryModSearch(DiaryModFilter{Posts: true, Comments: true, WithPhotos: true, TextOnly: true, Text: "NOTE"}, 0, 50)
	if err != nil || !slices.Equal(diaryRowKeys(rows), []string{"comment:5", "post:2"}) {
		t.Fatalf("text NOTE = %v err=%v", diaryRowKeys(rows), err)
	}

	// Pagination: offset, limit, total, hasMore, and beyond end.
	p1, total, more1, err := DiaryModSearch(allFilter, 0, 2)
	if err != nil || total != 5 || !more1 || !slices.Equal(diaryRowKeys(p1), []string{"post:10", "comment:10"}) {
		t.Fatalf("page 1 = %v total=%d more=%t err=%v", diaryRowKeys(p1), total, more1, err)
	}
	p2, total, more2, err := DiaryModSearch(allFilter, 2, 2)
	if err != nil || total != 5 || !more2 || !slices.Equal(diaryRowKeys(p2), []string{"comment:5", "post:2"}) {
		t.Fatalf("page 2 = %v total=%d more=%t err=%v", diaryRowKeys(p2), total, more2, err)
	}
	p3, total, more3, err := DiaryModSearch(allFilter, 4, 2)
	if err != nil || total != 5 || more3 || !slices.Equal(diaryRowKeys(p3), []string{"post:1"}) {
		t.Fatalf("page 3 = %v total=%d more=%t err=%v", diaryRowKeys(p3), total, more3, err)
	}
	pEmpty, total, moreEmpty, err := DiaryModSearch(allFilter, 5, 2)
	if err != nil || total != 5 || moreEmpty || len(pEmpty) != 0 {
		t.Fatalf("page empty = %v total=%d more=%t err=%v", pEmpty, total, moreEmpty, err)
	}

	// Validation errors.
	for _, tc := range []struct {
		offset, limit int
	}{
		{-1, 10},
		{0, 0},
		{0, 201},
	} {
		if _, _, _, err := DiaryModSearch(allFilter, tc.offset, tc.limit); !errors.Is(err, ErrBadInput) {
			t.Errorf("offset=%d limit=%d: err=%v, want ErrBadInput", tc.offset, tc.limit, err)
		}
	}
}

func TestDiaryModBeforeAndAfter(t *testing.T) {
	socialDB(t)
	staffAccount(t, "dev-ba1", "9611", "One", 0)
	staffAccount(t, "dev-ba2", "9612", "Two", 0)

	SocialMu.Lock()
	DiaryPosts = []DiaryPost{
		{DiaryNo: "1", AvatarID: "9611", Nickname: "One", RegDate: "1700000001000", Content: "p1"},
		{DiaryNo: "2", AvatarID: "9612", Nickname: "Two", RegDate: "1700000002000", Content: "p2"},
		{DiaryNo: "3", AvatarID: "9611", Nickname: "One", RegDate: "1700000004000", Content: "p3"},
	}
	DiaryComments = []DiaryComment{
		{CommentNo: "1", DiaryNo: "1", OwnerAvatarID: "9611", CommentAvatarID: "9612", Content: "c1", Date: "1700000002000"},
		{CommentNo: "2", DiaryNo: "2", OwnerAvatarID: "9612", CommentAvatarID: "9611", Content: "c2", Date: "1700000003000"},
	}
	SocialMu.Unlock()

	maxP, maxC := DiaryModMaxIDs()
	if maxP != 3 || maxC != 2 {
		t.Fatalf("DiaryModMaxIDs = (%d, %d), want (3, 2)", maxP, maxC)
	}

	allFilter := DiaryModFilter{Posts: true, Comments: true, WithPhotos: true, TextOnly: true}

	// Keyset pagination with DiaryModBefore.
	b1, more1, err := DiaryModBefore(allFilter, 1700000004000, 3, "post", 2)
	if err != nil || !more1 || !slices.Equal(diaryRowKeys(b1), []string{"comment:2", "post:2"}) {
		t.Fatalf("before post:3 = %v more=%t err=%v", diaryRowKeys(b1), more1, err)
	}
	b2, more2, err := DiaryModBefore(allFilter, 1700000002000, 2, "post", 2)
	if err != nil || more2 || !slices.Equal(diaryRowKeys(b2), []string{"comment:1", "post:1"}) {
		t.Fatalf("before post:2 = %v more=%t err=%v", diaryRowKeys(b2), more2, err)
	}

	// Add new items (some non-matching for player=9611 filter on posts, plus a comment on 9611's diary).
	SocialMu.Lock()
	DiaryPosts = append([]DiaryPost{
		{DiaryNo: "4", AvatarID: "9612", Nickname: "Two", RegDate: "1700000005000", Content: "skip post"},
		{DiaryNo: "5", AvatarID: "9611", Nickname: "One", RegDate: "1700000006000", Content: "keep post"},
	}, DiaryPosts...)
	DiaryComments = append(DiaryComments,
		DiaryComment{CommentNo: "3", DiaryNo: "4", OwnerAvatarID: "9612", CommentAvatarID: "9612", Content: "skip comment", Date: "1700000005500"},
		DiaryComment{CommentNo: "4", DiaryNo: "5", OwnerAvatarID: "9611", CommentAvatarID: "9612", Content: "keep comment", Date: "1700000007000"},
	)
	SocialMu.Unlock()

	only9611 := DiaryModFilter{Posts: true, Comments: true, WithPhotos: true, TextOnly: true, Player: "9611"}
	afterRows, lp, lc, hasMore, err := DiaryModAfter(only9611, 3, 2, 100)
	if err != nil {
		t.Fatalf("DiaryModAfter: %v", err)
	}
	if lp != 5 || lc != 4 || hasMore {
		t.Fatalf("cursors = (%d, %d) hasMore=%t, want (5, 4) false", lp, lc, hasMore)
	}
	// Ascending order: post:5 (t=6000) then comment:4 (t=7000).
	if !slices.Equal(diaryRowKeys(afterRows), []string{"post:5", "comment:4"}) {
		t.Fatalf("afterRows = %v, want [post:5 comment:4]", diaryRowKeys(afterRows))
	}

	// Sparse filter with 0 matches still advances cursors past non-matching items.
	noMatch := DiaryModFilter{Posts: true, Comments: true, WithPhotos: true, TextOnly: true, Text: "does-not-exist"}
	noneRows, lp2, lc2, hasMore2, err := DiaryModAfter(noMatch, 0, 0, 100)
	if err != nil || len(noneRows) != 0 || lp2 != 5 || lc2 != 4 || hasMore2 {
		t.Fatalf("sparse after = len=%d cursors=(%d,%d) more=%t err=%v", len(noneRows), lp2, lc2, hasMore2, err)
	}

	// Capping test: add 5 more posts and fetch with limit=2.
	SocialMu.Lock()
	for i := 6; i <= 10; i++ {
		no := strconv.Itoa(i)
		DiaryPosts = append([]DiaryPost{{
			DiaryNo: no, AvatarID: "9611", Nickname: "One",
			RegDate: fmt.Sprintf("170000001%04d", i), Content: "burst " + no,
		}}, DiaryPosts...)
	}
	SocialMu.Unlock()

	burst1, lpB1, lcB1, moreB1, err := DiaryModAfter(allFilter, 5, 4, 2)
	if err != nil || !moreB1 || lpB1 != 7 || lcB1 != 4 || !slices.Equal(diaryRowKeys(burst1), []string{"post:6", "post:7"}) {
		t.Fatalf("burst1 = %v cursors=(%d,%d) more=%t err=%v", diaryRowKeys(burst1), lpB1, lcB1, moreB1, err)
	}
	burst2, lpB2, lcB2, moreB2, err := DiaryModAfter(allFilter, lpB1, lcB1, 2)
	if err != nil || !moreB2 || lpB2 != 9 || lcB2 != 4 || !slices.Equal(diaryRowKeys(burst2), []string{"post:8", "post:9"}) {
		t.Fatalf("burst2 = %v cursors=(%d,%d) more=%t err=%v", diaryRowKeys(burst2), lpB2, lcB2, moreB2, err)
	}
	burst3, lpB3, lcB3, moreB3, err := DiaryModAfter(allFilter, lpB2, lcB2, 2)
	if err != nil || moreB3 || lpB3 != 10 || lcB3 != 4 || !slices.Equal(diaryRowKeys(burst3), []string{"post:10"}) {
		t.Fatalf("burst3 = %v cursors=(%d,%d) more=%t err=%v", diaryRowKeys(burst3), lpB3, lcB3, moreB3, err)
	}

	// Validation errors.
	if _, _, err := DiaryModBefore(allFilter, -1, 1, "post", 10); !errors.Is(err, ErrBadInput) {
		t.Errorf("negative beforeTime: err=%v, want ErrBadInput", err)
	}
	if _, _, err := DiaryModBefore(allFilter, 0, 1, "bogus", 10); !errors.Is(err, ErrBadInput) {
		t.Errorf("bad beforeKind: err=%v, want ErrBadInput", err)
	}
	if _, _, _, _, err := DiaryModAfter(allFilter, -1, 0, 10); !errors.Is(err, ErrBadInput) {
		t.Errorf("negative afterPost: err=%v, want ErrBadInput", err)
	}
	if _, _, _, _, err := DiaryModAfter(allFilter, 0, 0, 201); !errors.Is(err, ErrBadInput) {
		t.Errorf("limit 201: err=%v, want ErrBadInput", err)
	}
}

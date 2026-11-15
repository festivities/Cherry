package web

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"cherry/internal/config"
	"cherry/internal/economy"
	"cherry/internal/httpx"
	"cherry/internal/store"
)

// PLAN 8.6b part 5: the count caps. Protocol caps apply in every mode (the stock client never reaches them); the per-player
// caps apply in identity modes only, so the lab keeps every write (each subtest runs both modes where it matters).

func repeatJSON(n int, one string) string {
	items := make([]string, n)
	for i := range items {
		items[i] = one
	}
	return "[" + strings.Join(items, ",") + "]"
}

func TestSessionStatusAidCapIsTheClientsChunk(t *testing.T) {
	ids := func(n int) string { return repeatJSON(n, `"1001"`) }
	rec := httptest.NewRecorder()
	handleSessionStatus(rec, httptest.NewRequest(http.MethodPost, "/v4/session/status", strings.NewReader(ids(sessionStatusMax))))
	if rec.Code != http.StatusOK {
		t.Fatalf("a chunk of %d = %d %s", sessionStatusMax, rec.Code, rec.Body.String())
	}
	rec = httptest.NewRecorder()
	handleSessionStatus(rec, httptest.NewRequest(http.MethodPost, "/v4/session/status", strings.NewReader(ids(sessionStatusMax+1))))
	if rec.Code != http.StatusBadRequest || rec.Body.String() != httpx.BadRequestBody {
		t.Fatalf("%d aids = %d %s, want the 400 body", sessionStatusMax+1, rec.Code, rec.Body.String())
	}
}

// TestAvatarListCapsAreCheckedBeforeTheAccount: an oversized list is refused as malformed whatever the session; the limit
// itself passes the count check (an unknown session then gets its 404).
func TestAvatarListCapsAreCheckedBeforeTheAccount(t *testing.T) {
	item := func(n int) string { return repeatJSON(n, `{"itemCode":"CUAH0036Z","invenSeq":0}`) }
	put := func(body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPut, "/v4/avatar/save/v2", strings.NewReader(body))
		req.AddCookie(&http.Cookie{Name: "AV_AUTH", Value: "no-such-token"})
		handleAvatarSaveV2(rec, req)
		return rec
	}
	if rec := put(item(avatarSaveMaxItems + 1)); rec.Code != http.StatusBadRequest {
		t.Fatalf("save of %d items = %d, want 400", avatarSaveMaxItems+1, rec.Code)
	}
	if rec := put(item(avatarSaveMaxItems)); rec.Code != http.StatusNotFound {
		t.Fatalf("save of %d items = %d, want the unknown-session 404", avatarSaveMaxItems, rec.Code)
	}

	codes := func(n int) string {
		var out []string
		for i := range n {
			out = append(out, fmt.Sprintf(`"CUAH%04dX"`, i))
		}
		return `{"name":"Cap","avatarType":"FEMALE","nationCode":"JP","skinColor":"1","itemCodes":[` + strings.Join(out, ",") + `]}`
	}
	if rec := createAvatar(t, "", []byte(codes(createAvatarMaxItems+1)), ""); rec.Code != http.StatusBadRequest {
		t.Fatalf("creation of %d codes = %d, want 400", createAvatarMaxItems+1, rec.Code)
	}
	if rec := createAvatar(t, "", []byte(codes(createAvatarMaxItems)), ""); rec.Code != http.StatusNotFound {
		t.Fatalf("creation of %d codes = %d, want the no-session 404", createAvatarMaxItems, rec.Code)
	}
}

func TestPinCapIsIdentityModeOnly(t *testing.T) {
	fill := func(t *testing.T) {
		withAccount(t, "pin-me", func(acc *store.Account) {
			acc.Pins = nil
			for i := range maxPins {
				acc.Pins = append(acc.Pins, strconv.Itoa(2000+i))
			}
		})
	}
	t.Run("identity", func(t *testing.T) {
		pinFixture(t)
		useConfig(t, devFakeConfig())
		fill(t)
		rec := socialTestRequestAs(t, "pin-me", http.MethodGet, "/v4/pin/add/1102", "")
		if rec.Code != http.StatusOK || rec.Body.String() != `{"result":false}` {
			t.Fatalf("pin past the cap = %d %s, want the result-false answer", rec.Code, rec.Body.String())
		}
		withAccount(t, "pin-me", func(acc *store.Account) {
			if len(acc.Pins) != maxPins {
				t.Fatalf("pins = %d after a refused add, want %d", len(acc.Pins), maxPins)
			}
		})
	})
	t.Run("lab", func(t *testing.T) {
		pinFixture(t)
		fill(t)
		rec := socialTestRequestAs(t, "pin-me", http.MethodGet, "/v4/pin/add/1102", "")
		if rec.Code != http.StatusOK || rec.Body.String() != `{"result":true}` {
			t.Fatalf("lab pin past the identity cap = %d %s, want the normal answer", rec.Code, rec.Body.String())
		}
	})
}

// dailyFixture installs the accounts 1001 (tok1) and 1002 (tok2) and the social rows fill adds. No database: these writes
// are memory-only saves, and a diary post or guestbook entry is prepended in memory while the database keeps insertion
// order, which the test-build read-back check would report on every save with rows before it.
func dailyFixture(t *testing.T, fill func()) {
	t.Helper()
	installSocialTestAccounts(t, map[string]*store.Account{
		"tok1": {AccessToken: "tok1", Aid: "1001", Name: "One"},
		"tok2": {AccessToken: "tok2", Aid: "1002", Name: "Two"},
	})
	store.ResetSocial()
	t.Cleanup(store.ResetSocial)
	store.SocialMu.Lock()
	store.DiaryPosts = []store.DiaryPost{{DiaryNo: "1", AvatarID: "1002", OpenState: "public", RegDate: nowMillis(), Images: []store.DiaryImage{}}}
	fill()
	store.SocialMu.Unlock()
}

func nowMillis() string { return strconv.FormatInt(time.Now().UnixMilli(), 10) }

func TestDiaryPostDailyCap(t *testing.T) {
	const body = `{"avatarId":"1001","title":"t","content":"c","openState":"public","imageTypeKind":"","images":[]}`
	for _, mode := range []struct {
		name string
		cfg  config.Config
		want int
	}{{"identity", devFakeConfig(), http.StatusBadRequest}, {"lab", config.Config{}, http.StatusOK}} {
		t.Run(mode.name, func(t *testing.T) {
			dailyFixture(t, func() {
				for i := range maxPostsPerDay {
					store.DiaryPosts = append(store.DiaryPosts, store.DiaryPost{DiaryNo: strconv.Itoa(100 + i), AvatarID: "1001", RegDate: nowMillis(), Images: []store.DiaryImage{}})
				}
			})
			useConfig(t, mode.cfg)
			rec := socialTestRequestAs(t, "tok1", http.MethodPost, "/v4/diary2/ext/write", body)
			if rec.Code != mode.want {
				t.Fatalf("post after %d today = %d %s, want %d", maxPostsPerDay, rec.Code, rec.Body.String(), mode.want)
			}
		})
	}
}

func TestPendingFriendApplicationsCap(t *testing.T) {
	for _, mode := range []struct {
		name string
		cfg  config.Config
		want int
	}{{"identity", devFakeConfig(), http.StatusBadRequest}, {"lab", config.Config{}, http.StatusOK}} {
		t.Run(mode.name, func(t *testing.T) {
			dailyFixture(t, func() {
				for i := range maxPendingApplications {
					store.Friendships = append(store.Friendships, store.Friendship{A: "1001", B: strconv.Itoa(3000 + i), State: "pending"})
				}
			})
			useConfig(t, mode.cfg)
			rec := socialTestRequestAs(t, "tok1", http.MethodPost, "/v4/friend/apply/", `{"applyAvatarId":"1002"}`)
			if rec.Code != mode.want {
				t.Fatalf("application past %d pending = %d %s, want %d", maxPendingApplications, rec.Code, rec.Body.String(), mode.want)
			}
		})
	}
}

// TestDailyLimitsOnGuestbookCommentsAndLikes: the entry, comment and like caps count today's rows only, and a like changed
// in place is not a new like.
func TestDailyLimitsOnGuestbookCommentsAndLikes(t *testing.T) {
	dailyFixture(t, func() {
		for i := range maxEntriesPerDay {
			store.GuestbookEntries = append(store.GuestbookEntries, store.SavedGuestbookEntry{HostAvtNo: "1002", GuestHistNo: strconv.Itoa(500 + i), GuestAvtNo: "1001", RegYmdt: nowMillis()})
		}
		for i := range maxCommentsPerDay {
			store.DiaryComments = append(store.DiaryComments, store.DiaryComment{CommentNo: strconv.Itoa(500 + i), DiaryNo: "1", OwnerAvatarID: "1002", CommentAvatarID: "1001", Date: nowMillis()})
		}
		store.DiaryPosts = append(store.DiaryPosts, store.DiaryPost{DiaryNo: "7", AvatarID: "1002", OpenState: "public", RegDate: nowMillis(), Images: []store.DiaryImage{}})
		store.DiaryLikes = append(store.DiaryLikes, store.DiaryLike{LikeNo: "1", DiaryNo: "1", OwnerAvatarID: "1002", LikeAvatarID: "1001", LikeType: 1, Date: nowMillis()})
		for i := 1; i < maxLikesPerDay; i++ {
			store.DiaryLikes = append(store.DiaryLikes, store.DiaryLike{LikeNo: strconv.Itoa(1000 + i), DiaryNo: "9", OwnerAvatarID: "1002", LikeAvatarID: "1001", LikeType: 1, Date: nowMillis()})
		}
	})
	useConfig(t, devFakeConfig())

	if rec := socialTestRequestAs(t, "tok1", http.MethodPost, "/v4/guestbook/write",
		`{"hostAvtNo":"1002","guestAvtNo":"1001","secret":false,"content":"hi"}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("guestbook entry past %d today = %d, want 400", maxEntriesPerDay, rec.Code)
	}
	if rec := socialTestRequestAs(t, "tok1", http.MethodPost, "/v4/diary2/comment/write/1002",
		`{"diaryNo":1,"avatarID":"1001","content":"hi"}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("comment past %d today = %d, want 400", maxCommentsPerDay, rec.Code)
	}
	if rec := socialTestRequestAs(t, "tok1", http.MethodPost, "/v4/diary2/like/save/1002",
		`{"diaryNo":7,"likeAvatarId":"1001","likeType":2}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("new like past %d today = %d, want 400", maxLikesPerDay, rec.Code)
	}
	if rec := socialTestRequestAs(t, "tok1", http.MethodPost, "/v4/diary2/like/save/1002",
		`{"diaryNo":1,"likeAvatarId":"1001","likeType":3}`); rec.Code != http.StatusOK {
		t.Fatalf("changing today's like = %d %s, want 200", rec.Code, rec.Body.String())
	}
}

// TestGameDayCapsStartAtSixJST: a row written at 05:59 JST belongs to the day before, so it does not count toward the day
// that starts at 06:00.
func TestGameDayCapsStartAtSixJST(t *testing.T) {
	jst := time.FixedZone("JST", 9*3600)
	before := time.Date(2026, 11, 16, 5, 59, 0, 0, jst)
	after := time.Date(2026, 11, 16, 6, 0, 0, 0, jst)
	day := economy.AttendDayKey(after)
	if economy.AttendDayKey(before) == day {
		t.Fatal("05:59 JST and 06:00 JST are one game day; the day must start at 06:00")
	}
	if onGameDay(strconv.FormatInt(before.UnixMilli(), 10), day) || !onGameDay(strconv.FormatInt(after.UnixMilli(), 10), day) {
		t.Fatal("a row at 05:59 counted for the 06:00 day, or a row at 06:00 did not")
	}
	if onGameDay("", day) || onGameDay("not a time", day) {
		t.Fatal("an empty or malformed date counted")
	}
}

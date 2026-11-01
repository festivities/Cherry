package economy

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"cherry/data"
	"cherry/internal/opdata"
	"cherry/internal/store"
	"cherry/internal/testutil"
)

// circFixture installs accounts (aid -> Gems) under tokens "ctok-<aid>", a temp dir for accounts.json (so ledger.jsonl
// and circles.json are written there), and an empty circle book. Everything is restored when the test ends.
func circFixture(t *testing.T, gems map[string]int64) string {
	t.Helper()
	entries := map[string]*store.Account{}
	for aid, g := range gems {
		entries["ctok-"+aid] = &store.Account{AccessToken: "ctok-" + aid, SessionKey: "sk", Aid: aid, Name: "P" + aid, Gems: g}
	}
	testutil.InstallAccounts(t, entries)
	dir := t.TempDir()
	store.AccountsMu.Lock()
	store.AccountStorePath = filepath.Join(dir, "accounts.json")
	store.AccountsMu.Unlock()
	store.ResetCircles()
	store.CirclesMu.Lock()
	store.CirclesStorePath = filepath.Join(dir, "circles.json")
	store.CirclesMu.Unlock()
	t.Cleanup(store.ResetCircles)
	return dir
}

// circValidCreate is a request that passes every check, with circle name and join type set by the caller.
func circValidCreate(name string) CircleCreateRequest {
	return CircleCreateRequest{
		DeviceType: "Android", ProductCode: "cherry_circle_create", Price: 5000,
		CircleName: name, CircleIntroduce: "hi", LogoID: 1940001001, LogoBgID: 1940001101,
		JoinType: "OPEN", InterestCodeList: []int64{101, 301},
	}
}

var circNow = time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)

// isRefusal reports whether err is a refusal with the client error code code.
func isRefusal(err error, code int) bool {
	var ce *CircleError
	return errors.As(err, &ce) && ce.Code == code
}

func gemsOf(t *testing.T, aid string) int64 {
	t.Helper()
	store.AccountsMu.Lock()
	defer store.AccountsMu.Unlock()
	acc := store.AccountByAidLocked(aid)
	if acc == nil {
		t.Fatalf("account %s missing", aid)
	}
	return acc.Gems
}

// circAidCircle is the id of the circle aid is a member of, 0 for none.
func circAidCircle(aid string) int64 {
	store.CirclesMu.Lock()
	defer store.CirclesMu.Unlock()
	if ci := circleOf(&store.CircleState, aid); ci >= 0 {
		return store.CircleState.Circles[ci].ID
	}
	return 0
}

func circCount() int {
	store.CirclesMu.Lock()
	defer store.CirclesMu.Unlock()
	return len(store.CircleState.Circles)
}

// setCircFriends makes each pair accepted friends until the test ends.
func setCircFriends(t *testing.T, pairs ...[2]string) {
	t.Helper()
	store.SocialMu.Lock()
	old := store.Friendships
	for _, p := range pairs {
		store.Friendships = append(store.Friendships, store.Friendship{A: p[0], B: p[1], State: "accepted"})
	}
	store.SocialMu.Unlock()
	t.Cleanup(func() {
		store.SocialMu.Lock()
		store.Friendships = old
		store.SocialMu.Unlock()
	})
}

// TestCircleBookIdsStayInCherryBlock: a circle id outside the Cherry block, or above nextId, is refused at startup.
func TestCircleBookIdsStayInCherryBlock(t *testing.T) {
	if err := checkCircleBook(&store.CircleBook{NextID: CherryCircleFirst + 1, Circles: []store.Circle{{ID: CherryCircleFirst + 1}}}); err != nil {
		t.Fatalf("valid book refused: %v", err)
	}
	for _, c := range []struct {
		name string
		b    store.CircleBook
	}{
		{"id below the block", store.CircleBook{NextID: CherryCircleFirst + 1, Circles: []store.Circle{{ID: 5}}}},
		{"id above nextId", store.CircleBook{NextID: CherryCircleFirst + 1, Circles: []store.Circle{{ID: CherryCircleFirst + 2}}}},
		{"nextId past the block", store.CircleBook{NextID: CherryCircleLast + 1}},
	} {
		if err := checkCircleBook(&c.b); err == nil {
			t.Errorf("%s: accepted", c.name)
		}
	}
}

// TestCircleDataFileValidation: the embedded default is accepted, and each broken variant is refused with the field
// named in the error (origin and note, the product, the member cap, the art ids in their block, the art file names).
func TestCircleDataFileValidation(t *testing.T) {
	if _, err := opdata.Decode(data.CircleJSON, checkCircleData); err != nil {
		t.Fatalf("embedded default refused: %v", err)
	}
	base := string(data.CircleJSON)
	noteValue := regexp.MustCompile(`"note": "(?:[^"\\]|\\.)*"`)
	for _, c := range []struct{ name, old, new, want string }{
		{"origin cherry", `"origin": "mixed"`, `"origin": "cherry"`, "origin"},
		{"blank note", "", "", "note"},
		{"price 0", `"price_gems": 5000`, `"price_gems": 0`, "price_gems"},
		{"member cap 0", `"max_members": 30`, `"max_members": 0`, "max_members"},
		{"blank product name", `"name": "Create a Circle"`, `"name": " "`, "product.name"},
		{"logo outside the block", `"id": 1940001001`, `"id": 1940000001`, "logos id 1940000001"},
		{"background id reused", `"id": 1940001101`, `"id": 1940001001`, "must be unique"},
		{"art with a slash", `"art": "circle_logo_sd_dp.png"`, `"art": "x/circle_logo_sd_dp.png"`, "art must be"},
		{"room map not digits", `"map": "2202250100"`, `"map": "2202-250100"`, "room.map must"},
		{"room map size 0", `"map_size": 160`, `"map_size": 0`, "room.map_size"},
		{"room spawn outside the map", `"x": 87`, `"x": 160`, "room.spawn"},
		{"unknown key", `"max_members": 30`, `"max_members": 30, "bogus": 1`, "bogus"},
	} {
		raw := noteValue.ReplaceAllString(base, `"note": " "`) // the blank-note case
		if c.old != "" {
			raw = strings.Replace(base, c.old, c.new, 1)
			if raw == base {
				t.Fatalf("%s: pattern %q not in the default", c.name, c.old)
			}
		}
		if _, err := opdata.Decode([]byte(raw), checkCircleData); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want one mentioning %q", c.name, err, c.want)
		}
	}
}

// TestCreateCircleChargesGemsOnce: creation debits the price once through the ledger (reason circle:create:<id>), the
// book is saved, and a second circle for the same player is refused without a charge.
func TestCreateCircleChargesGemsOnce(t *testing.T) {
	dir := circFixture(t, map[string]int64{"1101": 6000})
	c, err := CreateCircle("ctok-1101", circValidCreate("Sushi Club"), circNow)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if c.ID != 1940000001 || c.Code != "000001" || leaderAid(&c) != "1101" || c.JoinType != "OPEN" {
		t.Fatalf("circle = %+v", c)
	}
	if !slices.Equal(c.Interests, []int64{101, 301}) {
		t.Fatalf("interests = %v", c.Interests)
	}
	if g := gemsOf(t, "1101"); g != 1000 {
		t.Fatalf("gems = %d, want 1000", g)
	}
	lines := ledgerLines(t, dir)
	if len(lines) != 1 || !strings.Contains(lines[0], `"delta":-5000`) || !strings.Contains(lines[0], `"reason":"circle:create:1940000001"`) {
		t.Fatalf("ledger = %q", lines)
	}
	if b, err := os.ReadFile(filepath.Join(dir, "circles.json")); err != nil || !strings.Contains(string(b), "Sushi Club") {
		t.Fatalf("circles.json: %v %s", err, b)
	}
	if _, err := CreateCircle("ctok-1101", circValidCreate("Second"), circNow); !isRefusal(err, 190202) {
		t.Fatalf("second circle: err = %v, want 190202", err)
	}
	if g := gemsOf(t, "1101"); g != 1000 {
		t.Fatalf("refused create changed gems to %d", g)
	}
}

// TestCreateCircleRefusals: each refused request changes nothing: no circle, no Gems, no ledger line.
func TestCreateCircleRefusals(t *testing.T) {
	cases := []struct {
		name string
		gems int64
		edit func(*CircleCreateRequest)
		code int
	}{
		{"short balance", 4999, nil, 60001},
		{"real-money cpId", 6000, func(r *CircleCreateRequest) { r.CpID = "CP-1" }, 400},
		{"cash coin unit", 6000, func(r *CircleCreateRequest) { r.CoinUnit = 1 }, 400},
		{"other product", 6000, func(r *CircleCreateRequest) { r.ProductCode = "other" }, 400},
		{"price 4000", 6000, func(r *CircleCreateRequest) { r.Price = 4000 }, 400},
		{"empty name", 6000, func(r *CircleCreateRequest) { r.CircleName = "" }, 400},
		{"blank name", 6000, func(r *CircleCreateRequest) { r.CircleName = "   " }, 400},
		{"name of 21 characters", 6000, func(r *CircleCreateRequest) { r.CircleName = strings.Repeat("x", 21) }, 400},
		{"intro of 51 characters", 6000, func(r *CircleCreateRequest) { r.CircleIntroduce = strings.Repeat("y", 51) }, 400},
		{"join type PUBLIC", 6000, func(r *CircleCreateRequest) { r.JoinType = "PUBLIC" }, 400},
		{"logo not listed", 6000, func(r *CircleCreateRequest) { r.LogoID = 1940001999 }, 400},
		{"background sent as logo", 6000, func(r *CircleCreateRequest) { r.LogoID = 1940001101 }, 400},
		{"unknown interest", 6000, func(r *CircleCreateRequest) { r.InterestCodeList = []int64{999} }, 400},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := circFixture(t, map[string]int64{"1101": c.gems})
			req := circValidCreate("Club")
			if c.edit != nil {
				c.edit(&req)
			}
			if _, err := CreateCircle("ctok-1101", req, circNow); !isRefusal(err, c.code) {
				t.Fatalf("err = %v, want refusal %d", err, c.code)
			}
			if g := gemsOf(t, "1101"); g != c.gems {
				t.Fatalf("gems = %d, want %d", g, c.gems)
			}
			if n := circCount(); n != 0 {
				t.Fatalf("%d circles after a refusal", n)
			}
			if lines := ledgerLines(t, dir); len(lines) != 0 {
				t.Fatalf("ledger after a refusal: %q", lines)
			}
		})
	}
}

// TestCreateCircleBoundaries: a 20-character name and a 50-character intro are accepted, and an empty intro is allowed.
func TestCreateCircleBoundaries(t *testing.T) {
	circFixture(t, map[string]int64{"1101": 6000, "1102": 6000})
	req := circValidCreate(strings.Repeat("n", 20))
	req.CircleIntroduce = strings.Repeat("i", 50)
	if _, err := CreateCircle("ctok-1101", req, circNow); err != nil {
		t.Fatalf("20 and 50 characters: %v", err)
	}
	req = circValidCreate("Empty intro")
	req.CircleIntroduce = ""
	if _, err := CreateCircle("ctok-1102", req, circNow); err != nil {
		t.Fatalf("empty intro: %v", err)
	}
}

// TestLeaveStartsCooldownBeforeAnotherJoin: after leaving, a player cannot join or start a circle for 24 hours; a
// leader cannot leave.
func TestLeaveStartsCooldownBeforeAnotherJoin(t *testing.T) {
	circFixture(t, map[string]int64{"1101": 6000, "1102": 6000})
	if _, err := CreateCircle("ctok-1101", circValidCreate("Alpha"), circNow); err != nil {
		t.Fatal(err)
	}
	if err := JoinCircle("ctok-1102", 1940000001, circNow); err != nil {
		t.Fatalf("open join: %v", err)
	}
	if err := LeaveCircle("ctok-1102", 1940000001, circNow); err != nil {
		t.Fatalf("leave: %v", err)
	}
	if circAidCircle("1102") != 0 {
		t.Fatal("still a member after leaving")
	}
	if _, err := CreateCircle("ctok-1102", circValidCreate("Beta"), circNow.Add(23*time.Hour+59*time.Minute)); !isRefusal(err, 190215) {
		t.Fatalf("create inside the cooldown: err = %v, want 190215", err)
	}
	if err := JoinCircle("ctok-1102", 1940000001, circNow.Add(time.Hour)); !isRefusal(err, 190215) {
		t.Fatalf("join inside the cooldown: err = %v, want 190215", err)
	}
	if err := JoinCircle("ctok-1102", 1940000001, circNow.Add(24*time.Hour)); err != nil {
		t.Fatalf("join after the cooldown: %v", err)
	}
	if circAidCircle("1102") != 1940000001 {
		t.Fatal("not a member after the cooldown")
	}
	if err := LeaveCircle("ctok-1101", 1940000001, circNow); !isRefusal(err, 10077) {
		t.Fatalf("leader leave: err = %v, want refusal", err)
	}
	if circAidCircle("1101") != 1940000001 {
		t.Fatal("leader left the circle")
	}
}

// TestInviteOnlyCircleTakesInvitedFriendsOnly: an INVITE circle refuses a join without an invitation; an invitation needs
// an accepted friend; the invitee joins by accepting, and the invitation is used up.
func TestInviteOnlyCircleTakesInvitedFriendsOnly(t *testing.T) {
	circFixture(t, map[string]int64{"1101": 6000, "1102": 0, "1103": 0})
	req := circValidCreate("Secret")
	req.JoinType = "INVITE"
	if _, err := CreateCircle("ctok-1101", req, circNow); err != nil {
		t.Fatal(err)
	}
	if err := JoinCircle("ctok-1102", 1940000001, circNow); !isRefusal(err, 190201) {
		t.Fatalf("join without an invitation: err = %v, want 190201", err)
	}
	if err := InviteCircle("ctok-1101", 1940000001, "1102", circNow); !isRefusal(err, 190002) {
		t.Fatalf("invite a stranger: err = %v, want 190002", err)
	}
	setCircFriends(t, [2]string{"1101", "1102"})
	if err := InviteCircle("ctok-1101", 1940000001, "1102", circNow); err != nil {
		t.Fatalf("invite a friend: %v", err)
	}
	if err := InviteCircle("ctok-1101", 1940000001, "1102", circNow); !isRefusal(err, 190208) {
		t.Fatalf("second invite: err = %v, want 190208", err)
	}
	if err := AcceptInvite("ctok-1102", 1940000001, circNow); err != nil {
		t.Fatalf("accept: %v", err)
	}
	if circAidCircle("1102") != 1940000001 {
		t.Fatal("invitee not a member")
	}
	if err := InviteCircle("ctok-1101", 1940000001, "1102", circNow); !isRefusal(err, 190002) {
		t.Fatalf("invite a member: err = %v, want 190002", err)
	}
	if err := JoinCircle("ctok-1103", 1940000001, circNow); !isRefusal(err, 190201) {
		t.Fatalf("join after the invitation was used: err = %v, want 190201", err)
	}
}

// TestCircleCapIsThirtyMembers: a circle takes 30 members (the leader and 29 more); the 30th joiner is refused.
func TestCircleCapIsThirtyMembers(t *testing.T) {
	gems := map[string]int64{"2000": 6000}
	for i := 1; i <= 30; i++ {
		gems[fmt.Sprintf("20%02d", i)] = 0
	}
	circFixture(t, gems)
	if _, err := CreateCircle("ctok-2000", circValidCreate("Full"), circNow); err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 29; i++ {
		if err := JoinCircle(fmt.Sprintf("ctok-20%02d", i), 1940000001, circNow); err != nil {
			t.Fatalf("joiner %d: %v", i, err)
		}
	}
	if err := JoinCircle("ctok-2030", 1940000001, circNow); !isRefusal(err, 10077) {
		t.Fatalf("31st member: err = %v, want refusal", err)
	}
	store.CirclesMu.Lock()
	n := len(store.CircleState.Circles[0].Members)
	store.CirclesMu.Unlock()
	if n != 30 {
		t.Fatalf("members = %d, want 30", n)
	}
}

// TestCloseIsLeaderOnlyAndFreesMembers: only the leader can issue a close code or close; closing removes the circle and
// its members, and the id is not reused.
func TestCloseIsLeaderOnlyAndFreesMembers(t *testing.T) {
	circFixture(t, map[string]int64{"1101": 12000, "1102": 0})
	if _, err := CreateCircle("ctok-1101", circValidCreate("Doomed"), circNow); err != nil {
		t.Fatal(err)
	}
	if err := JoinCircle("ctok-1102", 1940000001, circNow); err != nil {
		t.Fatal(err)
	}
	if err := CloseCircle("ctok-1102", 1940000001); !isRefusal(err, 190212) {
		t.Fatalf("member close: err = %v, want 190212", err)
	}
	if _, _, err := IssueCloseCode("ctok-1102", 1940000001); !isRefusal(err, 190212) {
		t.Fatalf("member close code: err = %v, want 190212", err)
	}
	key, code, err := IssueCloseCode("ctok-1101", 1940000001)
	if err != nil || key == "" || code < 1000 || code > 9999 {
		t.Fatalf("leader close code: key=%q code=%d err=%v", key, code, err)
	}
	if err := CloseCircle("ctok-1101", 1940000001); err != nil {
		t.Fatalf("leader close: %v", err)
	}
	if circAidCircle("1101") != 0 || circAidCircle("1102") != 0 || circCount() != 0 {
		t.Fatal("members or circle left after close")
	}
	c2, err := CreateCircle("ctok-1101", circValidCreate("Again"), circNow)
	if err != nil || c2.ID != 1940000002 {
		t.Fatalf("create after close: id=%d err=%v, want 1940000002", c2.ID, err)
	}
}

// TestRecruitAndSearchSkipInviteOnly: the recruit list and search show open circles only; an invite-only circle is not
// found by name or code.
func TestRecruitAndSearchSkipInviteOnly(t *testing.T) {
	circFixture(t, map[string]int64{"1101": 6000, "1102": 6000})
	if _, err := CreateCircle("ctok-1101", circValidCreate("Sushi Club"), circNow); err != nil {
		t.Fatal(err)
	}
	inv := circValidCreate("Secret Club")
	inv.JoinType = "INVITE"
	if _, err := CreateCircle("ctok-1102", inv, circNow); err != nil {
		t.Fatal(err)
	}
	store.CirclesMu.Lock()
	b := store.CircleState.Clone()
	store.CirclesMu.Unlock()
	if rows := recruitView(&b); len(rows) != 1 || rows[0].CircleName != "Sushi Club" {
		t.Fatalf("recruit = %+v", rows)
	}
	byName := func(c *store.Circle) bool { return strings.Contains(strings.ToLower(c.Name), "club") }
	if page := searchView(&b, byName, 1, 10); page.TotalCount != "1" || len(page.Items) != 1 || page.Items[0].CircleName != "Sushi Club" {
		t.Fatalf("search by name = %+v", page)
	}
	byCode := func(code string) func(*store.Circle) bool {
		return func(c *store.Circle) bool { return c.Code == code }
	}
	if page := searchView(&b, byCode("000002"), 1, 10); page.TotalCount != "0" || len(page.Items) != 0 {
		t.Fatalf("invite-only found by code: %+v", page)
	}
	if page := searchView(&b, byCode("000001"), 1, 10); page.TotalCount != "1" {
		t.Fatalf("open circle not found by code: %+v", page)
	}
	// A huge page is empty, not a panic (the page is clamped before the offset is multiplied).
	if page := searchView(&b, byName, 1<<40, 10); len(page.Items) != 0 || page.NextCursor != "" {
		t.Fatalf("page past the end = %+v", page)
	}
}

// TestModifyCircleLeaderOnlyAndNameFixed: a leader changes the intro, join type, logo and interests; the name cannot
// change; a member cannot change anything.
func TestModifyCircleLeaderOnlyAndNameFixed(t *testing.T) {
	circFixture(t, map[string]int64{"1101": 6000, "1102": 0})
	if _, err := CreateCircle("ctok-1101", circValidCreate("Stable"), circNow); err != nil {
		t.Fatal(err)
	}
	if err := JoinCircle("ctok-1102", 1940000001, circNow); err != nil {
		t.Fatal(err)
	}
	ok := CircleModifyRequest{CircleID: 1940000001, CircleName: "Stable", CircleIntroduce: "new", LogoID: 1940001001, LogoBgID: 1940001101, JoinType: "INVITE", InterestCodeList: []int64{201}}
	if err := ModifyCircle("ctok-1102", ok, circNow); !isRefusal(err, 190212) {
		t.Fatalf("member modify: err = %v, want 190212", err)
	}
	renamed := ok
	renamed.CircleName = "Other"
	if err := ModifyCircle("ctok-1101", renamed, circNow); !isRefusal(err, 400) {
		t.Fatalf("rename: err = %v, want 400", err)
	}
	if err := ModifyCircle("ctok-1101", ok, circNow); err != nil {
		t.Fatalf("leader modify: %v", err)
	}
	store.CirclesMu.Lock()
	c := store.CircleState.Circles[0]
	store.CirclesMu.Unlock()
	if c.Intro != "new" || c.JoinType != "INVITE" || !slices.Equal(c.Interests, []int64{201}) || c.Name != "Stable" {
		t.Fatalf("after modify: %+v", c)
	}
}

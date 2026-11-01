package economy

import (
	"errors"
	"fmt"
	"io/fs"
	"math/rand/v2"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"cherry/data"
	"cherry/internal/opdata"
	"cherry/internal/store"
)

// Circle (player clubs; spec %TEMP%\opencode\cherry\circle-20261101\circle-spec.md, client 10.1.0.0). This file holds the
// rules: creation, the membership, invitations and closing. circle_http.go serves them. The state is store.CircleState
// (circles.json). The membership rules are here; the points, seasons, point steps and missions are in circle_points.go,
// the member-cap upgrades in circle_upgrade.go, and the room (LPN agent 8) in internal/lpn/circle.go, which the
// membership changes reach through CircleRoomHook.
//
// Refusals carry the client's error code (NaDispatcherCircle::ErrorProcess 0x1ab5ec8 [S]); see circleErr*.

// CherryCircleFirst and CherryCircleLast bound the circle ids Cherry makes (circles are numbered from first+1).
// CherryCircleArtFirst and CherryCircleArtLast bound the logo and background ids in data/circle.json. checkCircleData
// refuses an id outside its block. The blocks sit above the lounge block (1930000000).
const (
	CherryCircleFirst    int64 = 1940000000
	CherryCircleLast     int64 = 1940000999
	CherryCircleArtFirst int64 = 1940001000
	CherryCircleArtLast  int64 = 1940001999
)

// circleLeaveCooldown is the client's rule (arts 5214 and 5312): after leaving a circle, a player waits 24 hours before
// joining or starting another.
const circleLeaveCooldown = 24 * time.Hour

// Client limits (NaCreateCircleLayer::IsValidCircleName 0x24d375c: 20; IsValidIntroduce 0x24d3954: 50), counted in
// characters (code points). The client's unit is not traced; a character count never refuses what a byte or UTF-16
// count allows.
const (
	circleNameMax  = 20
	circleIntroMax = 50
	circleListMax  = 50 // rows in the recruit list
	circlePageMax  = 50 // rows in a search page
)

const (
	circleOpen   = "OPEN"
	circleInvite = "INVITE"
)

// Roles in the book. The client's memberLevel strings are circleWireLevel.
const (
	roleLeader = "leader"
	roleVice   = "vice"
	roleMember = "member"
)

// Refusal codes. Each maps to the client's text in ErrorProcess: 190201 "no longer valid" (5258), 190202 "belongs to
// another Circle" (5253), 190002 "cannot be invited" (5263), 190208 "already invited" (5262), 190212 "rights have
// changed" (5273), 190215 "24-hour wait" (5241). circleErrGeneric (10077) covers the cases with no native text (the
// circle is full, the ids are used up, a leader leaves); the client's approval screen shows its own "member limit" text
// (5331) for a full circle. 400 is a malformed or refused request; 60001 is a short Gem balance (gacha's code).
const (
	circleErrNotFound       = 190201
	circleErrInCircle       = 190202
	circleErrCannotInvite   = 190002
	circleErrAlreadyInvited = 190208
	circleErrRights         = 190212
	circleErrCooldown       = 190215
	circleErrGeneric        = 10077
	circleErrBadRequest     = 400
	circleErrNoGems         = 60001
)

// circleInterestCodes is the client's interest table: four groups and their options. It is hard-coded in the client
// (NaCircleDataManager::InitData 0x24e1228 [S]), so Cherry takes it as it is. Names are arts strings (santi
// arts_strings.ast): groups 100 "Every day" (5422), 200 "N/A" (5426), 300 "All" (5433), 400 "Friends" (5440); options
// 101 "Age" (5425), 102 "Pretty often" (5423), 103 "Occasionally" (5424), 201-206 (5427-5432), 301-306 (5434-5439),
// 401-404 (5441-5444). Code 404's text (5444) is a notice, not an interest; the table lists it, so it is accepted.
var circleInterestCodes = []int64{
	100, 101, 102, 103, 200, 201, 202, 203, 204, 205, 206, 300, 301, 302, 303, 304, 305, 306, 400, 401, 402, 403, 404,
}

// CircleData is data/circle.json: the creation product, the member cap, the circle room, the logo and background art,
// the points and seasons, the missions, the member-cap upgrades and the circle gacha switch.
//
//	origin, note  mixed: the ids and the product are Cherry-made, the pictures are the client's (opdata.Provenance)
//	product       code (the create request must send it), name, price_gems (Gems; paymentType GEM), max_members
//	room          the circle room's map and spawn (a Cherry-made stand-in, see note)
//	logos         symbols a circle may use: id in the logo block, art = a file in UIResource/UIImage_hd (read in place)
//	backgrounds   backgrounds a circle may use: the same rules
//	points        season length, triggers, the daily cap and the point steps (circle_points.go)
//	missions      the mission chain: one mission at a time per member and season (circle_points.go)
//	upgrades      member-cap steps, each bought with Gems by the leader (circle_upgrade.go)
//	gacha         whether the circle reward page offers the Cherry Gacha (its pulls use the main gacha route)
type CircleData struct {
	opdata.Provenance
	Product     CircleProduct   `json:"product"`
	Room        CircleRoom      `json:"room"`
	Logos       []CircleArt     `json:"logos"`
	Backgrounds []CircleArt     `json:"backgrounds"`
	Points      CirclePoints    `json:"points"`
	Missions    []CircleMission `json:"missions"`
	Upgrades    []CircleUpgrade `json:"upgrades"`
	Gacha       CircleGacha     `json:"gacha"`
}

// CirclePoints are the circle points rules. SeasonStart is the first season's start day ("2006-01-02", 06:00 JST);
// seasons are SeasonDays long, and gaining stops at the season's end. Triggers are keyed by the kinds in circleKinds.
// DailyCap bounds one member's points per game day. Steps are the circle's season targets, in ascending order.
type CirclePoints struct {
	SeasonDays  int64                    `json:"season_days"`
	SeasonStart string                   `json:"season_start"`
	SeasonName  string                   `json:"season_name"`
	DailyCap    int64                    `json:"daily_cap"`
	Triggers    map[string]CircleTrigger `json:"triggers"`
	Steps       []CircleStep             `json:"steps"`
}

// CircleTrigger is the points one kind of event gives: Points each time, at most PerDay times a game day (0: no limit).
type CircleTrigger struct {
	Points int64 `json:"points"`
	PerDay int64 `json:"per_day"`
}

// CircleStep is a season target: Points circle points reached, then each member who claims it gets Gems.
type CircleStep struct {
	Points int64 `json:"points"`
	Gems   int64 `json:"gems"`
}

// CircleMission is one link of the mission chain: each goal is a season count of one trigger kind. Gems go to the
// member who claims it, and Points to the circle's season total.
type CircleMission struct {
	Name   string       `json:"name"`
	Goals  []CircleGoal `json:"goals"`
	Gems   int64        `json:"gems"`
	Points int64        `json:"points"`
}

// CircleGoal is one goal of a mission: Count credits of Kind in the season, text is shown on the goal's row.
type CircleGoal struct {
	Kind  string `json:"kind"`
	Count int64  `json:"count"`
	Text  string `json:"text"`
}

// CircleUpgrade is a member-cap step: the cap becomes To, for PriceGems Gems. Code is the product code the client sends
// and Name the title its row shows.
type CircleUpgrade struct {
	Code      string `json:"code"`
	Name      string `json:"name"`
	To        int64  `json:"to"`
	PriceGems int64  `json:"price_gems"`
}

// CircleGacha switches the circle reward page's gacha on. The gacha is the main Cherry Gacha: its pulls use the
// gacha route, which serves the one active gacha and charges Gems only.
type CircleGacha struct {
	Enabled bool `json:"enabled"`
}

// circleKinds are the trigger kinds: Garden visit, Square visit, gathering, a completed daily quest and a circle chat
// message. circleQuestKinds maps the quest progress keys (economy.questTriggers) that earn them.
var circleKinds = []string{"garden", "square", "gather", "quest", "chat"}

// circleQuestKinds maps the quest progress keys that give a trigger kind (the daily quest completion is "quest").
var circleQuestKinds = map[string]string{"garden-visit": "garden", "garden-own": "garden", "square": "square", "gather3": "gather"}

// CircleProduct is the creation product.
type CircleProduct struct {
	Code       string `json:"code"`
	Name       string `json:"name"`
	PriceGems  int64  `json:"price_gems"`
	MaxMembers int    `json:"max_members"`
}

// CircleRoom is the circle room's map: its folder id, its size in tiles per side and the spawn tile (the same
// conversion as a Square's). Cherry uses the LINE Café Square map as a stand-in (user decision 2026-11-01).
type CircleRoom struct {
	Map     string     `json:"map"`
	MapSize uint64     `json:"map_size"`
	Spawn   CircleTile `json:"spawn"`
}

// CircleTile is a map tile.
type CircleTile struct {
	X uint64 `json:"x"`
	Y uint64 `json:"y"`
}

// CircleArt is one logo or background choice.
type CircleArt struct {
	ID  int64  `json:"id"`
	Art string `json:"art"`
}

var circleFile = opdata.New("circle.json", data.CircleJSON, checkCircleData, verifyCircleData)

// circleArtFS is the original UI art tree (lpbackup UIResource/UIImage_hd). The logo pictures are read from it in place.
var circleArtFS fs.FS = os.DirFS(filepath.FromSlash(`D:/Dev/projects/Cherry/.opencode/line-play-artifacts/lpbackup/jp.naver.lineplay.android/files/UIResource/UIImage_hd`))

// ActiveCircleData is data/circle.json in use.
func ActiveCircleData() *CircleData { return circleFile.Get() }

// loadCircleBook is the startup check of circles.json (LoadOperatorData runs it after store.LoadCircles): every circle
// id is inside the Cherry block and no id is above nextId.
func loadCircleBook() error {
	store.CirclesMu.Lock()
	defer store.CirclesMu.Unlock()
	return checkCircleBook(&store.CircleState)
}

func checkCircleBook(b *store.CircleBook) error {
	if b.NextID > CherryCircleLast {
		return fmt.Errorf("circles.json: nextId %d is outside the Cherry block %d-%d", b.NextID, CherryCircleFirst, CherryCircleLast)
	}
	for _, c := range b.Circles {
		if c.ID <= CherryCircleFirst || c.ID > b.NextID {
			return fmt.Errorf("circles.json: circle %d is outside the Cherry block %d-%d or above nextId", c.ID, CherryCircleFirst, CherryCircleLast)
		}
	}
	return nil
}

// checkCircleData validates the structure at startup: the origin and note, the product, the member cap, and the logo
// and background lists (ids in the block, unique, one picture file each).
func checkCircleData(c *CircleData) error {
	if err := c.CheckOrigin(opdata.OriginMixed); err != nil {
		return err
	}
	p := c.Product
	switch {
	case p.Code == "" || strings.ContainsAny(p.Code, " /\\"):
		return errors.New("product.code must be a code with no spaces or slashes")
	case strings.TrimSpace(p.Name) == "":
		return errors.New("product.name must not be empty")
	case p.PriceGems < 1 || p.PriceGems > store.MaxBalance:
		return fmt.Errorf("product.price_gems must be 1 to %d", store.MaxBalance)
	case p.MaxMembers < 1 || p.MaxMembers > 100:
		return errors.New("product.max_members must be 1 to 100")
	case !circleMapID(c.Room.Map):
		return errors.New("room.map must be \"../world/<digits>\" or \"../garden/<digits>\" (or bare digits)")
	case c.Room.MapSize < 1 || c.Room.MapSize > 1000:
		return errors.New("room.map_size must be 1 to 1000")
	case c.Room.Spawn.X >= c.Room.MapSize || c.Room.Spawn.Y >= c.Room.MapSize:
		return errors.New("room.spawn must be inside the map")
	case len(c.Logos) < 1 || len(c.Backgrounds) < 1:
		return errors.New("logos and backgrounds must each list at least one picture")
	}
	seen := map[int64]bool{}
	for _, list := range []struct {
		name string
		arts []CircleArt
	}{{"logos", c.Logos}, {"backgrounds", c.Backgrounds}} {
		for _, a := range list.arts {
			where := fmt.Sprintf("%s id %d", list.name, a.ID)
			switch {
			case a.ID < CherryCircleArtFirst || a.ID > CherryCircleArtLast:
				return fmt.Errorf("%s: id must be inside the Cherry block %d-%d", where, CherryCircleArtFirst, CherryCircleArtLast)
			case seen[a.ID]:
				return fmt.Errorf("%s: id must be unique", where)
			case a.Art == "" || strings.ContainsAny(a.Art, `/\`) || !strings.HasSuffix(a.Art, ".png"):
				return fmt.Errorf("%s: art must be a .png file name", where)
			}
			seen[a.ID] = true
		}
	}
	return checkCircleRules(c)
}

// checkCircleRules checks the points, the missions, the upgrades and the season start at startup.
func checkCircleRules(c *CircleData) error {
	p := c.Points
	if _, err := circleSeasonStartKey(p.SeasonStart); err != nil {
		return err
	}
	switch {
	case p.SeasonDays < 1 || p.SeasonDays > 365:
		return errors.New("points.season_days must be 1 to 365")
	case strings.Count(p.SeasonName, "%d") != 1 || strings.Count(p.SeasonName, "%") != 1:
		return errors.New("points.season_name must hold one %d")
	case p.DailyCap < 1 || p.DailyCap > 100000:
		return errors.New("points.daily_cap must be 1 to 100000")
	}
	for kind, t := range p.Triggers {
		switch {
		case !slices.Contains(circleKinds, kind):
			return fmt.Errorf("points.triggers: %q is not a trigger kind", kind)
		case t.Points < 0 || t.Points > 1000 || t.PerDay < 0 || t.PerDay > 1000:
			return fmt.Errorf("points.triggers %s: points and per_day must be 0 to 1000", kind)
		}
	}
	for i, s := range p.Steps {
		switch {
		case s.Points < 1 || s.Points > 1000000 || s.Gems < 0 || s.Gems > 100000:
			return fmt.Errorf("points.steps[%d]: points must be 1 to 1000000 and gems 0 to 100000", i)
		case i > 0 && s.Points <= p.Steps[i-1].Points:
			return fmt.Errorf("points.steps[%d]: points must rise", i)
		}
	}
	for i, m := range c.Missions {
		if m.Name == "" || len(m.Goals) == 0 || m.Gems < 0 || m.Gems > 100000 || m.Points < 0 || m.Points > 100000 {
			return fmt.Errorf("missions[%d]: needs a name, at least one goal, and gems and points of 0 to 100000", i)
		}
		for _, g := range m.Goals {
			if !slices.Contains(circleKinds, g.Kind) || g.Count < 1 || g.Count > 100000 || g.Text == "" {
				return fmt.Errorf("missions[%d]: each goal needs a trigger kind, a count of 1 to 100000 and a text", i)
			}
		}
	}
	base := int64(c.Product.MaxMembers)
	for i, u := range c.Upgrades {
		prev := base
		if i > 0 {
			prev = c.Upgrades[i-1].To
		}
		switch {
		case u.Code == "" || strings.ContainsAny(u.Code, " /\\"):
			return fmt.Errorf("upgrades[%d]: code must be a code with no spaces or slashes", i)
		case strings.TrimSpace(u.Name) == "":
			return fmt.Errorf("upgrades[%d]: name must not be empty", i)
		case u.To <= prev || u.To > 100:
			return fmt.Errorf("upgrades[%d]: to must rise above %d and stay at most 100", i, prev)
		case u.PriceGems < 1 || u.PriceGems > 100000:
			return fmt.Errorf("upgrades[%d]: price_gems must be 1 to 100000", i)
		}
		for _, o := range c.Upgrades[:i] {
			if o.Code == u.Code {
				return fmt.Errorf("upgrades[%d]: code %q is used twice", i, u.Code)
			}
		}
	}
	return nil
}

// verifyCircleData runs at load (not for the embedded default alone): every picture must be in the archive.
func verifyCircleData(c *CircleData) error {
	for _, a := range append(append([]CircleArt(nil), c.Logos...), c.Backgrounds...) {
		if _, err := fs.Stat(circleArtFS, a.Art); err != nil {
			return fmt.Errorf("art %s is not in the archive", a.Art)
		}
	}
	return nil
}

// ErrCircleSession is a request whose session has no account with an avatar.
var ErrCircleSession = errors.New("circle: no avatar for this session")

// CircleError is a refusal. Code is the errorCode the client maps to its text.
type CircleError struct{ Code int }

func (e *CircleError) Error() string { return "circle: refused " + strconv.Itoa(e.Code) }

func circleRefuse(code int) error { return &CircleError{Code: code} }

// CircleCpID is the content id Cherry sends as cpId on the creation price row and the upgrade shop row. It is not a
// billing id: NaCircleMakeStartPopup::CheckCpid 0x24edd14 shows "An unexpected error has occurred." (10077) when the
// row's cpId is EMPTY (device 2026-11-01), and the stock fashion buy sends cpId "avtplay". Cherry-made, like the voucher
// rows' "cherry" (economy.go). Prices are Cherry's own Gem prices, so no cpId can open a real-money path.
const CircleCpID = "cherry"

// circleCpIDOK accepts the cpId a purchase echoes: Cherry's own or none.
func circleCpIDOK(id string) bool { return id == "" || id == CircleCpID }

// CircleCreateRequest is the body of POST circle/create (NaCreateCircleLayer::ReqCircleMake 0x24ee6fc): coinUnit is an
// int (0 for Gems, from the default item's paymentType), price the default item's finalPrice, joinType "OPEN" or
// "INVITE", interestCodeList an array of ints. cpId echoes the price row's CircleCpID; any other value is refused.
type CircleCreateRequest struct {
	CoinUnit         int64   `json:"coinUnit"`
	DeviceType       string  `json:"deviceType"`
	CpID             string  `json:"cpId"`
	ProductCode      string  `json:"productCode"`
	Price            int64   `json:"price"`
	CircleName       string  `json:"circleName"`
	CircleIntroduce  string  `json:"circleIntroduce"`
	LogoID           int64   `json:"logoId"`
	LogoBgID         int64   `json:"logoBgId"`
	JoinType         string  `json:"joinType"`
	InterestCodeList []int64 `json:"interestCodeList"`
}

// CircleModifyRequest is the body of POST circle/modify/withint (ReqCircleModify 0x24eed20). The name is sent back
// unchanged: a circle's name is fixed at creation (wiki; the client only sends the field).
type CircleModifyRequest struct {
	CircleID         looseInt `json:"circleId"`
	CircleName       string   `json:"circleName"`
	CircleIntroduce  string   `json:"circleIntroduce"`
	LogoID           int64    `json:"logoId"`
	LogoBgID         int64    `json:"logoBgId"`
	JoinType         string   `json:"joinType"`
	InterestCodeList []int64  `json:"interestCodeList"`
}

// circleTx runs op for the account behind token, with the account table and the circle book locked. A read (write
// false) gets the book in use and must not change it. A write gets a copy of the book: when op succeeds, the copy is
// saved to circles.json first and then the accounts (only when op spent something), and the ledger lines are flushed
// after both saves. A failed step restores the account and the book.
func circleTx(token string, write bool, op func(acc *store.Account, b *store.CircleBook) ([]store.LedgerLine, error)) error {
	store.AccountsMu.Lock()
	defer store.AccountsMu.Unlock()
	return circleTxLocked(store.Accounts[token], write, op)
}

// circleTxAid is circleTx for an avatar id (the LPN side has no token).
func circleTxAid(aid string, write bool, op func(acc *store.Account, b *store.CircleBook) ([]store.LedgerLine, error)) error {
	store.AccountsMu.Lock()
	defer store.AccountsMu.Unlock()
	return circleTxLocked(store.AccountByAidLocked(aid), write, op)
}

// circleTxLocked is the body of circleTx; the caller holds AccountsMu.
func circleTxLocked(acc *store.Account, write bool, op func(acc *store.Account, b *store.CircleBook) ([]store.LedgerLine, error)) error {
	if acc == nil || !store.ValidAvatarID(acc.Aid) || acc.Aid == "0" {
		return ErrCircleSession
	}
	// ponytail: one global lock for the whole book; per-circle locks if circle traffic ever contends.
	store.CirclesMu.Lock()
	defer store.CirclesMu.Unlock()
	if !write {
		b := store.CircleState
		_, err := op(acc, &b)
		return err
	}
	next := store.CircleState.Clone()
	prevAcc := *acc
	lines, err := op(acc, &next)
	if err != nil {
		*acc = prevAcc
		return err
	}
	prevBook := store.CircleState
	if err := store.SaveCirclesLocked(next); err != nil {
		*acc = prevAcc
		return err
	}
	store.CircleState = next
	if len(lines) > 0 {
		if err := store.SaveAccountsLocked(); err != nil {
			*acc = prevAcc
			store.CircleState = prevBook
			_ = store.SaveCirclesLocked(prevBook)
			return err
		}
		store.FlushLedgerLocked(lines)
	}
	return nil
}

// circleIndex returns the index of circle id in b, -1 when there is none.
func circleIndex(b *store.CircleBook, id int64) int {
	for i := range b.Circles {
		if b.Circles[i].ID == id {
			return i
		}
	}
	return -1
}

// circleOf returns the index of the circle aid belongs to, -1 for none. One circle per player.
func circleOf(b *store.CircleBook, aid string) int {
	for i := range b.Circles {
		if memberIndex(&b.Circles[i], aid) >= 0 {
			return i
		}
	}
	return -1
}

func memberIndex(c *store.Circle, aid string) int {
	return slices.IndexFunc(c.Members, func(m store.CircleMember) bool { return m.Aid == aid })
}

func inviteIndex(c *store.Circle, aid string) int {
	return slices.IndexFunc(c.Invites, func(x store.CircleInvite) bool { return x.Aid == aid })
}

func applyIndex(c *store.Circle, aid string) int {
	return slices.IndexFunc(c.Applies, func(x store.CircleApply) bool { return x.Aid == aid })
}

// roleOf returns aid's role in c, "" when aid is not a member.
func roleOf(c *store.Circle, aid string) string {
	if i := memberIndex(c, aid); i >= 0 {
		return c.Members[i].Role
	}
	return ""
}

// canInvite: leaders and vice-leaders invite (arts 5211); only leaders approve and kick (arts 5210).
func canInvite(c *store.Circle, aid string) bool {
	r := roleOf(c, aid)
	return r == roleLeader || r == roleVice
}

func leaderAid(c *store.Circle) string {
	for _, m := range c.Members {
		if m.Role == roleLeader {
			return m.Aid
		}
	}
	return ""
}

// cooldownActive reports whether aid left a circle less than circleLeaveCooldown before now.
func cooldownActive(b *store.CircleBook, aid string, now time.Time) bool {
	left, ok := b.Leaves[aid]
	return ok && now.UnixMilli()-left < circleLeaveCooldown.Milliseconds()
}

// circleCodeOf is the short code a player searches by (Cherry's numbering from the first circle).
func circleCodeOf(id int64) string { return fmt.Sprintf("%06d", id-CherryCircleFirst) }

// circleCapOf is the member cap of c: its own cap once it was upgraded, else the product's base cap.
func circleCapOf(c *store.Circle) int {
	if c.MaxMembers > 0 {
		return c.MaxMembers
	}
	return ActiveCircleData().Product.MaxMembers
}

// CircleCapOf is the member cap of circle id (0 when there is no such circle).
func CircleCapOf(id int64) int {
	cap := 0
	_ = circleBookTx(false, func(b *store.CircleBook) error {
		if ci := circleIndex(b, id); ci >= 0 {
			cap = circleCapOf(&b.Circles[ci])
		}
		return nil
	})
	return cap
}

func checkCircleName(name string) error {
	if strings.TrimSpace(name) == "" || utf8.RuneCountInString(name) > circleNameMax {
		return circleRefuse(circleErrBadRequest)
	}
	return nil
}

func checkCircleIntro(intro string) error {
	if utf8.RuneCountInString(intro) > circleIntroMax {
		return circleRefuse(circleErrBadRequest)
	}
	return nil
}

// checkCircleSettings checks the fields a leader may change (intro, join type, logo, background, interests).
func checkCircleSettings(intro, joinType string, logo, bg int64, interests []int64) error {
	if err := checkCircleIntro(intro); err != nil {
		return err
	}
	if joinType != circleOpen && joinType != circleInvite {
		return circleRefuse(circleErrBadRequest)
	}
	d := ActiveCircleData()
	if !slices.ContainsFunc(d.Logos, func(a CircleArt) bool { return a.ID == logo }) ||
		!slices.ContainsFunc(d.Backgrounds, func(a CircleArt) bool { return a.ID == bg }) {
		return circleRefuse(circleErrBadRequest)
	}
	for _, code := range interests {
		if !slices.Contains(circleInterestCodes, code) {
			return circleRefuse(circleErrBadRequest)
		}
	}
	return nil
}

// sortedInterests returns the codes once each, in order.
func sortedInterests(codes []int64) []int64 {
	out := slices.Clone(codes)
	slices.Sort(out)
	return slices.Compact(out)
}

// CreateCircle makes a circle for the account behind token, led by it, and charges the creation price in Gems (reason
// circle:create:<id>). Nothing changes on a refusal, a short balance or a failed save. A foreign cpId (not CircleCpID; or a
// coinUnit other than Gems) and a product other than the default one are refused.
func CreateCircle(token string, req CircleCreateRequest, now time.Time) (store.Circle, error) {
	var made store.Circle
	err := circleTx(token, true, func(acc *store.Account, b *store.CircleBook) ([]store.LedgerLine, error) {
		product := ActiveCircleData().Product
		if !circleCpIDOK(req.CpID) || req.CoinUnit != 0 || req.ProductCode != product.Code || req.Price != product.PriceGems {
			return nil, circleRefuse(circleErrBadRequest)
		}
		if err := checkCircleName(req.CircleName); err != nil {
			return nil, err
		}
		if err := checkCircleSettings(req.CircleIntroduce, req.JoinType, req.LogoID, req.LogoBgID, req.InterestCodeList); err != nil {
			return nil, err
		}
		if circleOf(b, acc.Aid) >= 0 {
			return nil, circleRefuse(circleErrInCircle)
		}
		if cooldownActive(b, acc.Aid, now) {
			return nil, circleRefuse(circleErrCooldown)
		}
		id := max(b.NextID, CherryCircleFirst) + 1
		if id > CherryCircleLast {
			return nil, circleRefuse(circleErrGeneric)
		}
		lines, err := store.SpendLocked(acc, store.LedgerDeltas{Gems: -product.PriceGems}, fmt.Sprintf("circle:create:%d", id))
		if err != nil {
			return nil, circleRefuse(circleErrNoGems)
		}
		ms := now.UnixMilli()
		made = store.Circle{
			ID: id, Code: circleCodeOf(id), Name: req.CircleName, Intro: req.CircleIntroduce, JoinType: req.JoinType,
			LogoID: req.LogoID, LogoBgID: req.LogoBgID, Interests: sortedInterests(req.InterestCodeList),
			Created: ms, Modified: ms,
			Members: []store.CircleMember{{Aid: acc.Aid, Role: roleLeader, Joined: ms}},
		}
		b.NextID = id
		b.Circles = append(b.Circles, made)
		return lines, nil
	})
	return made, err
}

// ModifyCircle changes a circle's intro, join type, logo, background and interests. Leaders only; the name must be the
// stored one.
func ModifyCircle(token string, req CircleModifyRequest, now time.Time) error {
	return circleTx(token, true, func(acc *store.Account, b *store.CircleBook) ([]store.LedgerLine, error) {
		ci := circleIndex(b, int64(req.CircleID))
		if ci < 0 {
			return nil, circleRefuse(circleErrNotFound)
		}
		c := &b.Circles[ci]
		if roleOf(c, acc.Aid) != roleLeader {
			return nil, circleRefuse(circleErrRights)
		}
		if req.CircleName != c.Name {
			return nil, circleRefuse(circleErrBadRequest)
		}
		if err := checkCircleSettings(req.CircleIntroduce, req.JoinType, req.LogoID, req.LogoBgID, req.InterestCodeList); err != nil {
			return nil, err
		}
		c.Intro, c.JoinType, c.LogoID, c.LogoBgID = req.CircleIntroduce, req.JoinType, req.LogoID, req.LogoBgID
		c.Interests = sortedInterests(req.InterestCodeList)
		c.Modified = now.UnixMilli()
		return nil, nil
	})
}

// CircleRoomHook receives the membership changes made over HTTP (join, approval, invitation accepted, leave, close)
// once they are saved and no lock is held. internal/lpn sets it to push each change into the circle room. Nil means
// nobody is listening.
var CircleRoomHook func(ev CircleRoomEvent)

// CircleRoomEvent is one saved membership change of circle Circle: the avatars that joined, the avatars that left or
// were removed, and whether the whole circle closed (then Left holds every former member).
type CircleRoomEvent struct {
	Circle int64
	Joined []string
	Left   []string
	Closed bool
}

func notifyCircleRoom(ev CircleRoomEvent) {
	if CircleRoomHook != nil {
		CircleRoomHook(ev)
	}
}

// JoinCircle is POST circle/apply/join/<id>: an OPEN circle takes the player at once; an INVITE circle takes only an
// invited player (who thereby uses the invitation). Anything else is refused as not found.
func JoinCircle(token string, id int64, now time.Time) error {
	var joined string
	err := circleTx(token, true, func(acc *store.Account, b *store.CircleBook) ([]store.LedgerLine, error) {
		ci := circleIndex(b, id)
		if ci < 0 {
			return nil, circleRefuse(circleErrNotFound)
		}
		c := &b.Circles[ci]
		if c.JoinType != circleOpen && inviteIndex(c, acc.Aid) < 0 {
			return nil, circleRefuse(circleErrNotFound)
		}
		joined = acc.Aid
		return nil, joinCircleAt(b, ci, acc.Aid, now)
	})
	if err == nil {
		notifyCircleRoom(CircleRoomEvent{Circle: id, Joined: []string{joined}})
	}
	return err
}

// joinCircleAt adds aid to circle ci as a plain member. It checks the one-circle rule, the 24-hour cooldown and the
// member cap, and drops aid's invitations and applications everywhere.
func joinCircleAt(b *store.CircleBook, ci int, aid string, now time.Time) error {
	if circleOf(b, aid) >= 0 {
		return circleRefuse(circleErrInCircle)
	}
	if cooldownActive(b, aid, now) {
		return circleRefuse(circleErrCooldown)
	}
	c := &b.Circles[ci]
	if len(c.Members) >= circleCapOf(c) {
		return circleRefuse(circleErrGeneric)
	}
	c.Members = append(c.Members, store.CircleMember{Aid: aid, Role: roleMember, Joined: now.UnixMilli()})
	for i := range b.Circles {
		b.Circles[i].Invites = slices.DeleteFunc(b.Circles[i].Invites, func(x store.CircleInvite) bool { return x.Aid == aid })
		b.Circles[i].Applies = slices.DeleteFunc(b.Circles[i].Applies, func(x store.CircleApply) bool { return x.Aid == aid })
	}
	return nil
}

// CancelApply is POST circle/apply/join/cancel/<id>: the player withdraws their own application.
func CancelApply(token string, id int64) error {
	return circleTx(token, true, func(acc *store.Account, b *store.CircleBook) ([]store.LedgerLine, error) {
		ci := circleIndex(b, id)
		if ci < 0 {
			return nil, circleRefuse(circleErrNotFound)
		}
		c := &b.Circles[ci]
		ai := applyIndex(c, acc.Aid)
		if ai < 0 {
			return nil, circleRefuse(circleErrNotFound)
		}
		c.Applies = slices.Delete(c.Applies, ai, ai+1)
		return nil, nil
	})
}

// ApproveApply is POST circle/apply/join/<id>/approval/<aid>: a leader admits an applicant.
func ApproveApply(token string, id int64, target string, now time.Time) error {
	err := circleTx(token, true, func(acc *store.Account, b *store.CircleBook) ([]store.LedgerLine, error) {
		ci := circleIndex(b, id)
		if ci < 0 {
			return nil, circleRefuse(circleErrNotFound)
		}
		c := &b.Circles[ci]
		if roleOf(c, acc.Aid) != roleLeader {
			return nil, circleRefuse(circleErrRights)
		}
		ai := applyIndex(c, target)
		if ai < 0 {
			return nil, circleRefuse(circleErrNotFound)
		}
		c.Applies = slices.Delete(c.Applies, ai, ai+1)
		return nil, joinCircleAt(b, ci, target, now)
	})
	if err == nil {
		notifyCircleRoom(CircleRoomEvent{Circle: id, Joined: []string{target}})
	}
	return err
}

// RefuseApply is POST circle/apply/join/<id>/refuse/<aid>: a leader turns an applicant down.
func RefuseApply(token string, id int64, target string) error {
	return circleTx(token, true, func(acc *store.Account, b *store.CircleBook) ([]store.LedgerLine, error) {
		ci := circleIndex(b, id)
		if ci < 0 {
			return nil, circleRefuse(circleErrNotFound)
		}
		c := &b.Circles[ci]
		if roleOf(c, acc.Aid) != roleLeader {
			return nil, circleRefuse(circleErrRights)
		}
		ai := applyIndex(c, target)
		if ai < 0 {
			return nil, circleRefuse(circleErrNotFound)
		}
		c.Applies = slices.Delete(c.Applies, ai, ai+1)
		return nil, nil
	})
}

// InviteCircle is POST circle/invite/<id>/<aid>: a leader or vice-leader invites an accepted friend who is in no circle.
func InviteCircle(token string, id int64, target string, now time.Time) error {
	return circleTx(token, true, func(acc *store.Account, b *store.CircleBook) ([]store.LedgerLine, error) {
		ci := circleIndex(b, id)
		if ci < 0 {
			return nil, circleRefuse(circleErrNotFound)
		}
		c := &b.Circles[ci]
		if !canInvite(c, acc.Aid) {
			return nil, circleRefuse(circleErrRights)
		}
		if !store.ValidAvatarID(target) || store.AccountByAidLocked(target) == nil {
			return nil, circleRefuse(circleErrNotFound)
		}
		if !store.AcceptedFriends(acc.Aid, target) || memberIndex(c, target) >= 0 { // the social lock is taken alone inside
			return nil, circleRefuse(circleErrCannotInvite)
		}
		if circleOf(b, target) >= 0 {
			return nil, circleRefuse(circleErrInCircle)
		}
		if inviteIndex(c, target) >= 0 {
			return nil, circleRefuse(circleErrAlreadyInvited)
		}
		if len(c.Members) >= circleCapOf(c) {
			return nil, circleRefuse(circleErrGeneric)
		}
		c.Invites = append(c.Invites, store.CircleInvite{Aid: target, By: acc.Aid, Date: now.UnixMilli()})
		return nil, nil
	})
}

// CancelInvite is POST circle/invite/cancel/<id>/<aid>: a leader or vice-leader withdraws an invitation.
func CancelInvite(token string, id int64, target string) error {
	return circleTx(token, true, func(acc *store.Account, b *store.CircleBook) ([]store.LedgerLine, error) {
		ci := circleIndex(b, id)
		if ci < 0 {
			return nil, circleRefuse(circleErrNotFound)
		}
		c := &b.Circles[ci]
		if !canInvite(c, acc.Aid) {
			return nil, circleRefuse(circleErrRights)
		}
		ii := inviteIndex(c, target)
		if ii < 0 {
			return nil, circleRefuse(circleErrNotFound)
		}
		c.Invites = slices.Delete(c.Invites, ii, ii+1)
		return nil, nil
	})
}

// AcceptInvite is POST circle/invite/approval/<id>: the invitee joins.
func AcceptInvite(token string, id int64, now time.Time) error {
	var joined string
	err := circleTx(token, true, func(acc *store.Account, b *store.CircleBook) ([]store.LedgerLine, error) {
		ci := circleIndex(b, id)
		if ci < 0 {
			return nil, circleRefuse(circleErrNotFound)
		}
		if inviteIndex(&b.Circles[ci], acc.Aid) < 0 {
			return nil, circleRefuse(circleErrNotFound)
		}
		joined = acc.Aid
		return nil, joinCircleAt(b, ci, acc.Aid, now)
	})
	if err == nil {
		notifyCircleRoom(CircleRoomEvent{Circle: id, Joined: []string{joined}})
	}
	return err
}

// RefuseInvite is POST circle/invite/refuse/<id>: the invitee declines.
func RefuseInvite(token string, id int64) error {
	return circleTx(token, true, func(acc *store.Account, b *store.CircleBook) ([]store.LedgerLine, error) {
		ci := circleIndex(b, id)
		if ci < 0 {
			return nil, circleRefuse(circleErrNotFound)
		}
		c := &b.Circles[ci]
		ii := inviteIndex(c, acc.Aid)
		if ii < 0 {
			return nil, circleRefuse(circleErrNotFound)
		}
		c.Invites = slices.Delete(c.Invites, ii, ii+1)
		return nil, nil
	})
}

// LeaveCircle is POST circle/leave/<id>: a member leaves and starts the 24-hour cooldown. A leader cannot leave; a
// leader hands the leadership over first (CircleTransferMaster, cs_master_change_req), and then the old leader is a
// plain member and may leave. A leader who wants out instead closes the circle.
func LeaveCircle(token string, id int64, now time.Time) error {
	var left string
	err := circleTx(token, true, func(acc *store.Account, b *store.CircleBook) ([]store.LedgerLine, error) {
		ci := circleIndex(b, id)
		if ci < 0 {
			return nil, circleRefuse(circleErrNotFound)
		}
		c := &b.Circles[ci]
		mi := memberIndex(c, acc.Aid)
		if mi < 0 {
			return nil, circleRefuse(circleErrNotFound)
		}
		if c.Members[mi].Role == roleLeader {
			return nil, circleRefuse(circleErrGeneric)
		}
		c.Members = slices.Delete(c.Members, mi, mi+1)
		if b.Leaves == nil {
			b.Leaves = map[string]int64{}
		}
		b.Leaves[acc.Aid] = now.UnixMilli()
		left = acc.Aid
		return nil, nil
	})
	if err == nil {
		notifyCircleRoom(CircleRoomEvent{Circle: id, Left: []string{left}})
	}
	return err
}

// IssueCloseCode is POST circle/close/issue/code/<id>: a leader gets the code and key the close screen shows. Nothing
// is stored: the close request does not check them (the leader check is the guard).
func IssueCloseCode(token string, id int64) (key string, code int, err error) {
	err = circleTx(token, false, func(acc *store.Account, b *store.CircleBook) ([]store.LedgerLine, error) {
		ci := circleIndex(b, id)
		if ci < 0 {
			return nil, circleRefuse(circleErrNotFound)
		}
		if roleOf(&b.Circles[ci], acc.Aid) != roleLeader {
			return nil, circleRefuse(circleErrRights)
		}
		key = fmt.Sprintf("%08x", rand.Uint32())
		code = 1000 + rand.IntN(9000)
		return nil, nil
	})
	return key, code, err
}

// CloseCircle is POST circle/close/<id>/...: a leader closes the circle. Its members, invitations and applications go
// with it; the id is never reused.
func CloseCircle(token string, id int64) error {
	var former []string
	err := circleTx(token, true, func(acc *store.Account, b *store.CircleBook) ([]store.LedgerLine, error) {
		ci := circleIndex(b, id)
		if ci < 0 {
			return nil, circleRefuse(circleErrNotFound)
		}
		if roleOf(&b.Circles[ci], acc.Aid) != roleLeader {
			return nil, circleRefuse(circleErrRights)
		}
		for _, m := range b.Circles[ci].Members {
			former = append(former, m.Aid)
		}
		b.Circles = slices.Delete(b.Circles, ci, ci+1)
		return nil, nil
	})
	if err == nil {
		notifyCircleRoom(CircleRoomEvent{Circle: id, Left: former, Closed: true})
	}
	return err
}

// circleMapID reports whether s is a map folder id (digits only, as the Square maps are).
//
// The circle room loader (NaLayerCircleRoomLoading::InitLoader 0x25f9758) opens files/circle/<map>.mworldpack only, and
// the client never downloads a circle pack; a missing pack silently returns the player to the Garden (12693 -> 12694,
// device 2026-11-01). So room.map is a pack name RELATIVE to files/circle/ that names a pack the device already has:
// "../world/<id>" (Square worlds) or "../garden/<id>" (Garden packs); spec room-enter-bounce.md.
func circleMapID(s string) bool {
	for _, dir := range []string{"../world/", "../garden/"} {
		if rest, ok := strings.CutPrefix(s, dir); ok {
			s = rest
			break
		}
	}
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// The LPN side (internal/lpn/circle.go) reaches the book through the functions below. They take the avatar as its
// decimal text, check the rules here (the client's buttons are not the guard), and change only the book: no account
// or ledger line.

// CirclePlayerType is the wire role of a member: PlayerType in CircleMemberInfo and PlayerInfo, and the client's
// stCircleAvatarInfo level. 2 is MASTER, 1 is MANAGER, 0 a plain member. [S] LP_sc_room_enter_res copies the
// CircleMemberInfo field at +88 into the level at +48; ParserData maps MASTER to 2 and MANAGER to 1.
func CirclePlayerType(role string) uint64 {
	switch role {
	case roleLeader:
		return 2
	case roleVice:
		return 1
	}
	return 0
}

// circleBookTx runs op on the book in use (write false: it must not change the copy it gets) or on a copy that is
// saved to circles.json and swapped in when op succeeds.
func circleBookTx(write bool, op func(b *store.CircleBook) error) error {
	store.CirclesMu.Lock()
	defer store.CirclesMu.Unlock()
	if !write {
		b := store.CircleState
		return op(&b)
	}
	next := store.CircleState.Clone()
	if err := op(&next); err != nil {
		return err
	}
	if err := store.SaveCirclesLocked(next); err != nil {
		return err
	}
	store.CircleState = next
	return nil
}

// uniqueAids returns aids without repeats, in order.
func uniqueAids(aids []string) []string {
	out := []string{}
	for _, a := range aids {
		if !slices.Contains(out, a) {
			out = append(out, a)
		}
	}
	return out
}

// CircleOf returns the circle aid is in and aid's role there (ok false for none).
func CircleOf(aid string) (id int64, role string, ok bool) {
	_ = circleBookTx(false, func(b *store.CircleBook) error {
		if ci := circleOf(b, aid); ci >= 0 {
			c := &b.Circles[ci]
			id, role, ok = c.ID, roleOf(c, aid), true
		}
		return nil
	})
	return id, role, ok
}

// CircleMembersOf returns a copy of the members of circle id and the leader's aid (ok false when there is no circle).
func CircleMembersOf(id int64) (members []store.CircleMember, master string, ok bool) {
	_ = circleBookTx(false, func(b *store.CircleBook) error {
		if ci := circleIndex(b, id); ci >= 0 {
			c := &b.Circles[ci]
			members, master, ok = slices.Clone(c.Members), leaderAid(c), true
		}
		return nil
	})
	return members, master, ok
}

// CircleChangeAuth makes targets vice-leaders (vice true) or plain members (false). Only the leader does this (arts
// 5210/5211 [S-str]), and the leader is never changed. It returns the changed members as they now are.
func CircleChangeAuth(actor string, id int64, targets []string, vice bool) ([]store.CircleMember, error) {
	targets = uniqueAids(targets)
	var changed []store.CircleMember
	err := circleBookTx(true, func(b *store.CircleBook) error {
		ci := circleIndex(b, id)
		if ci < 0 {
			return circleRefuse(circleErrNotFound)
		}
		c := &b.Circles[ci]
		if roleOf(c, actor) != roleLeader {
			return circleRefuse(circleErrRights)
		}
		for _, t := range targets {
			mi := memberIndex(c, t)
			if mi < 0 {
				return circleRefuse(circleErrNotFound)
			}
			if c.Members[mi].Role == roleLeader {
				return circleRefuse(circleErrRights)
			}
		}
		want := roleMember
		if vice {
			want = roleVice
		}
		for _, t := range targets {
			mi := memberIndex(c, t)
			c.Members[mi].Role = want
			changed = append(changed, c.Members[mi])
		}
		return nil
	})
	return changed, err
}

// CircleTransferMaster hands the leadership to target, a member other than the leader, and the leader becomes a plain
// member (the client does not say what the old leader becomes; plain member is Cherry's choice [I]). Only the leader
// does this. It returns the new leader and the old one as they now are.
func CircleTransferMaster(actor string, id int64, target string) ([]store.CircleMember, error) {
	var changed []store.CircleMember
	err := circleBookTx(true, func(b *store.CircleBook) error {
		ci := circleIndex(b, id)
		if ci < 0 {
			return circleRefuse(circleErrNotFound)
		}
		c := &b.Circles[ci]
		if roleOf(c, actor) != roleLeader {
			return circleRefuse(circleErrRights)
		}
		if target == actor {
			return circleRefuse(circleErrBadRequest)
		}
		ti, ai := memberIndex(c, target), memberIndex(c, actor)
		if ti < 0 {
			return circleRefuse(circleErrNotFound)
		}
		c.Members[ti].Role = roleLeader
		c.Members[ai].Role = roleMember
		changed = []store.CircleMember{c.Members[ti], c.Members[ai]}
		return nil
	})
	return changed, err
}

// CircleFire removes targets from circle id (cs_member_fire_req). The leader removes any member but the leader itself.
// A vice-leader removes plain members only: the client shows its kick button on every row for a vice (ShowMenu reads
// only the viewer's level), so this is the guard. Removal starts no leave cooldown [I]. It returns the removed aids.
func CircleFire(actor string, id int64, targets []string) ([]string, error) {
	targets = uniqueAids(targets)
	var removed []string
	err := circleBookTx(true, func(b *store.CircleBook) error {
		ci := circleIndex(b, id)
		if ci < 0 {
			return circleRefuse(circleErrNotFound)
		}
		c := &b.Circles[ci]
		actorRole := roleOf(c, actor)
		if actorRole != roleLeader && actorRole != roleVice {
			return circleRefuse(circleErrRights)
		}
		for _, t := range targets {
			mi := memberIndex(c, t)
			if mi < 0 {
				return circleRefuse(circleErrNotFound)
			}
			switch c.Members[mi].Role {
			case roleLeader:
				return circleRefuse(circleErrRights)
			case roleVice:
				if actorRole != roleLeader {
					return circleRefuse(circleErrRights)
				}
			}
		}
		c.Members = slices.DeleteFunc(c.Members, func(m store.CircleMember) bool { return slices.Contains(targets, m.Aid) })
		removed = targets
		return nil
	})
	return removed, err
}

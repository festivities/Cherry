package economy

import (
	"errors"
	"fmt"
	"log"
	"math/rand/v2"
	"strconv"
	"strings"
	"sync"
	"time"

	"cherry/data"
	"cherry/internal/itemcode"
	"cherry/internal/opdata"
	"cherry/internal/store"
	"cherry/internal/thumbs"
)

// Treasure World (Blue Marble board game), one solo board per season: the board, tickets, the exchange of Jewels for
// Ranking Points, quests and missions, rankings and rewards. Specs in %TEMP%\opencode\cherry\treasure-20261102
// (treasure-spec.md section 9, cells.md, transport-money.md, roll-gate.md, arrival.md). This file holds the rules and the
// per-account state (store.TreasureRecord, treasure.json); the wire is internal/lpn/treasure*.go (gateway agent 9).
// Gems move only through the ledger (store.SpendLocked) or the Mailbox; nothing here touches CASH or HEART.

// CherryTreasureFirst and CherryTreasureLast bound the ids Cherry makes for Treasure World (the season id in
// data/treasure.json). checkTreasureData refuses an id outside them.
const (
	CherryTreasureFirst int64 = 1950000000
	CherryTreasureLast  int64 = 1950000999
)

// Cell types (BlueMarble::eBoardCellType, treasure-spec section 3). This build serves 0 to 9: 3, 5, 6, 8 and 9 are the slice 3
// squares (buff, secret quest, island trap, broom travel, mini-game). Types 3, 5, 9 and, until a device probe passes, 6 and 8
// are not in the default ring, only in an operator's board. Types 10 and up are refused in the data.
const (
	TreasureCellStart    = 0
	TreasureCellCoin     = 1
	TreasureCellExchange = 2
	TreasureCellBuff     = 3
	TreasureCellBonus    = 4
	TreasureCellSpecial  = 5
	TreasureCellTrap     = 6
	TreasureCellGoldKey  = 7
	TreasureCellTravel   = 8
	TreasureCellMiniGame = 9
)

// treasureJST is the season clock: a season starts and ends at 06:00 JST.
var treasureJST = time.FixedZone("JST", 9*60*60)

// treasureSides are the four sides of the ring in walking order; a square's side is the direction its avatar faces
// (AvatarDirection, cells.md 6.3 and 6.9).
var treasureSides = [4]string{"FR", "BR", "BL", "FL"}

// TreasureRandIntn returns a uniform value in [0, n) for the die and the coin amounts. Tests replace it.
var TreasureRandIntn = func(n int) int { return rand.IntN(n) }

var (
	// ErrTreasureNoSeason is returned for a board action outside the play window, and for any claim while the season cycle
	// is closed (before the first season, or after the claim window when season.repeat is off).
	ErrTreasureNoSeason = errors.New("treasure: no season is running")
	// ErrTreasureNoTickets is returned for a roll the account has no tickets for.
	ErrTreasureNoTickets = errors.New("treasure: not enough tickets")
	// ErrTreasureNoAccount is returned when an action that pays or delivers needs the account and it is not known.
	ErrTreasureNoAccount = errors.New("treasure: no account for this board")
	// ErrTreasureRefused is the generic refusal: the square, the limit, the claim or the purchase is not allowed now.
	ErrTreasureRefused = errors.New("treasure: refused")
)

// TreasureData is data/treasure.json: the season, its names, the tickets, the ring, the rewards and the rules of the
// exchange, quests, missions, rankings and ticket cap.
//
//	origin, note  mixed: the names are the Help Center's; the ids, numbers, texts, ring and every Gem amount are Cherry-made
//	season        id (Cherry block), name, start day (06:00 JST), length in days and repeat (the next season follows, see at)
//	names         the ticket, coin and ranking point names the client shows
//	tickets       cap, starting tickets, seconds per refill, tickets per roll
//	board         the ring: cell types in walking order, four equal sides
//	rewards       coin squares (min to max Jewels), the bonus square, the lap reward (tickets)
//	gold_key      the Mystery Card deck the gold key squares draw from
//	buff          the buff pool (item codes with their BuffPercent, 1 to 40) and the cap on the worn total (1 to 100)
//	special       the secret quest's reward tickets (1 to 50) and the Gacha pulls its goal needs (1 to 100)
//	minigame      the most Jewels one mini-game takes (1 to 1000) and the cups' payout multiples (at least three, 0 to 10; the first three)
//	npcs          the two portrait names and genders the season sends
//	exchange      Ranking Points per Jewel, the Jewels a square takes a day, the Gem price of a square's reset
//	quests        set size, hours before a free refresh, and the pool of quest tasks (tickets as reward)
//	missions      set size and the pool of mission tasks (Jewels as reward)
//	ranking       rows per page and the grades by rank
//	point_rewards Ranking Point thresholds with Gems and an optional dress item, paid by Mailbox
//	rank_rewards  the claim window after the season and the Gems per rank cutline, paid by Mailbox
//	ticket_cap    the cap raises: each cap and its Gem price
//	trap          the island rules: the die that frees a trapped token and the trapped rolls that free it
//	travel        how long a broom prompt may wait before the next board entry clears it
type TreasureData struct {
	opdata.Provenance
	Season       TreasureSeason        `json:"season"`
	Names        TreasureNames         `json:"names"`
	Tickets      TreasureTickets       `json:"tickets"`
	Board        TreasureBoard         `json:"board"`
	Rewards      TreasureRewards       `json:"rewards"`
	GoldKey      TreasureGoldKey       `json:"gold_key"`
	Buff         TreasureBuff          `json:"buff"`
	Special      TreasureSpecial       `json:"special"`
	MiniGame     TreasureMiniGame      `json:"minigame"`
	NPCs         []TreasureNPC         `json:"npcs"`
	Exchange     TreasureExchangeRules `json:"exchange"`
	Quests       TreasureTasks         `json:"quests"`
	Missions     TreasureTasks         `json:"missions"`
	Ranking      TreasureRanking       `json:"ranking"`
	PointRewards []TreasurePointReward `json:"point_rewards"`
	RankRewards  TreasureRankRewards   `json:"rank_rewards"`
	TicketCap    TreasureTicketCap     `json:"ticket_cap"`
	Trap         TreasureTrapRules     `json:"trap"`
	Travel       TreasureTravelRules   `json:"travel"`
}

// TreasureTrapRules is the island square's rule (Cherry-made, island-spec section 7): a roll of HighDice or more frees a trapped
// token, and the trapped roll that reaches MaxTries frees it whatever the die shows.
type TreasureTrapRules struct {
	HighDice int64 `json:"high_dice"`
	MaxTries int64 `json:"max_tries"`
}

// TreasureTravelRules: StaleMinutes is how long a broom prompt may wait for its answer; a board entry after that clears it and
// moves the token off the broom square (treasureRecover).
type TreasureTravelRules struct {
	StaleMinutes int64 `json:"stale_minutes"`
}

// TreasureTrap is an island's counts as the client shows them: Count the trapped rolls so far (CurrentCount), Max the rolls that
// free the token (ExitTargetCount) and High the die that frees it (HighDiceNumber).
type TreasureTrap struct {
	Count, Max, High int64
}

// treasureTrapOf is the island counts of a token trapped Count rolls.
func treasureTrapOf(count int64, d *TreasureData) TreasureTrap {
	return TreasureTrap{Count: count, Max: d.Trap.MaxTries, High: d.Trap.HighDice}
}

// TreasureSeason is the season's id, its name, its first day ("2006-01-02", 06:00 JST), its length in days, whether the
// next season follows it (Repeat: season n+1 starts when the rank claim days after season n end; see at) and the body text
// of the season-end popup (EndMessage, sc 1 f7 in the reward period).
type TreasureSeason struct {
	ID         int64  `json:"id"`
	Name       string `json:"name"`
	Start      string `json:"start"`
	Days       int64  `json:"days"`
	Repeat     bool   `json:"repeat"`
	EndMessage string `json:"end_message"`
}

// TreasureNames are the client's currency texts: the dice ticket, the coin and the ranking point.
type TreasureNames struct {
	Ticket       string `json:"ticket"`
	Coin         string `json:"coin"`
	RankingPoint string `json:"ranking_point"`
}

// TreasureTickets: Max is the cap of the refill, Start the tickets a new season gives, ChargeSeconds the time for one
// refill, PerRoll the tickets one roll costs.
type TreasureTickets struct {
	Max           int64 `json:"max"`
	Start         int64 `json:"start"`
	ChargeSeconds int64 `json:"charge_seconds"`
	PerRoll       int64 `json:"per_roll"`
}

// TreasureBoard is the ring: one cell type per square, from the start line (index 0) round to the start line again.
type TreasureBoard struct {
	Ring []int `json:"ring"`
}

// TreasureRewards: Coin is the Jewels a coin square pays (drawn from Min to Max), Bonus what a bonus square pays,
// Start the tickets each lap (passing or landing on the start line) gives.
type TreasureRewards struct {
	Coin  TreasureRange `json:"coin"`
	Bonus TreasureBonus `json:"bonus"`
	Start TreasureStart `json:"start"`
}

// TreasureRange is an inclusive range of Jewels.
type TreasureRange struct {
	Min int64 `json:"min"`
	Max int64 `json:"max"`
}

// TreasureBonus is a bonus square's amount and the client currency it is named in. CoinUnit must not be a money unit
// (GEM, CASH, HEART): the client would sync its balances, and the bonus pays Jewels only.
type TreasureBonus struct {
	Coins    int64  `json:"coins"`
	CoinUnit string `json:"coin_unit"`
}

// TreasureStart is the lap reward in tickets.
type TreasureStart struct {
	Tickets int64 `json:"tickets"`
}

// TreasureGoldKey is the Mystery Card deck: the cards a gold key square draws from.
type TreasureGoldKey struct {
	Cards []TreasureCard `json:"cards"`
}

// TreasureCard is one Mystery Card: its type (1 forward, 2 back, 6 and 7 Jewels), its weight in the draw and its inclusive
// range, in steps for types 1 and 2 and in Jewels for types 6 and 7.
type TreasureCard struct {
	Type   int64 `json:"type"`
	Weight int64 `json:"weight"`
	Min    int64 `json:"min"`
	Max    int64 `json:"max"`
}

// draw picks a card by weight and draws its value from the card's range. The deck is checked non-empty at startup.
func (g TreasureGoldKey) draw() (TreasureCard, int64) {
	var total int64
	for _, c := range g.Cards {
		total += c.Weight
	}
	pick := int64(TreasureRandIntn(int(total)))
	card := g.Cards[len(g.Cards)-1]
	for _, c := range g.Cards {
		if pick < c.Weight {
			card = c
			break
		}
		pick -= c.Weight
	}
	return card, card.Min + int64(TreasureRandIntn(int(card.Max-card.Min+1)))
}

// TreasureBuff is the buff gacha square's pool: the items a buff square draws from, and Cap the most the worn items add
// to every Jewel credit (percent).
type TreasureBuff struct {
	Items []TreasureBuffItem `json:"items"`
	Cap   int64              `json:"cap"`
}

// TreasureBuffItem is one buff: its item code and the percent it adds while worn.
type TreasureBuffItem struct {
	Code    string `json:"code"`
	Percent int64  `json:"percent"`
}

// TreasureSpecial is the secret quest square's reward in tickets and the Gacha pulls its goal needs.
type TreasureSpecial struct {
	RewardTickets int64 `json:"reward_tickets"`
	GoalPulls     int64 `json:"goal_pulls"`
}

// TreasureMiniGame is the mini-game square: MaxBet the most Jewels one game takes, Multiples the payout multiples the popup
// lists.
type TreasureMiniGame struct {
	MaxBet    int64   `json:"max_bet"`
	Multiples []int64 `json:"multiples"`
}

// TreasureNPC is one portrait the season shows: its name, its sex and its outfit (item codes, sent as the AvatarInfo items; the
// portrait itself is the client's).
type TreasureNPC struct {
	Name   string   `json:"name"`
	Gender string   `json:"gender"`
	Items  []string `json:"items"`
}

// TreasureExchangeRules: Multiple is the Ranking Points one Jewel gives at 1x, Tiles the rate of each exchange square (a
// square left out converts at 1x), DailyMax the Jewels one exchange square takes each day (06:00 JST), ResetGems the Gem
// price of resetting a full square for the day.
type TreasureExchangeRules struct {
	Multiple  int64          `json:"multiple"`
	Tiles     []TreasureTile `json:"tiles"`
	DailyMax  int64          `json:"daily_max"`
	ResetGems int64          `json:"reset_gems"`
}

// TreasureTile is one exchange square's rate (player guide: 1x, 2x, 3x and a gamble): Times multiplies Multiple, or with
// Gamble set each exchange draws 1x to Gamble x.
type TreasureTile struct {
	Square int   `json:"square"`
	Times  int64 `json:"times"`
	Gamble int64 `json:"gamble"`
}

// tile is the rate of ring square pos: its multiplier and the gamble ceiling (0 = fixed).
func (ex TreasureExchangeRules) tile(pos int) (times, gamble int64) {
	for _, t := range ex.Tiles {
		if t.Square == pos {
			if t.Gamble > 0 {
				return 1, t.Gamble
			}
			return t.Times, 0
		}
	}
	return 1, 0
}

// TreasureTasks is a quest or mission list: sets of SetSize tasks rotated through Pool, and (quests only) the hours
// before a free refresh.
type TreasureTasks struct {
	SetSize      int64          `json:"set_size"`
	RefreshHours int64          `json:"refresh_hours"`
	Pool         []TreasureTask `json:"pool"`
}

// TreasureTask is one quest or mission: the event that counts (roll, jewel, lap or exchange), the goal, the reward (tickets
// for a quest, Jewels for a mission), the picture the client draws for its type (1 to 7 and 9 for a quest, 1 to 5 for a
// mission) and, for a quest, the icon item code and the text.
type TreasureTask struct {
	Kind   string `json:"kind"`
	Goal   int64  `json:"goal"`
	Reward int64  `json:"reward"`
	Type   int64  `json:"type"`
	Item   string `json:"item"`
	Text   string `json:"text"`
}

// TreasureRanking: PageSize rows per page, Grades by rank (the first grade whose MaxRank the rank does not pass; the
// last grade has MaxRank 0 and takes the rest). Type is the badge class the client draws (1 to 4).
type TreasureRanking struct {
	PageSize int64           `json:"page_size"`
	Grades   []TreasureGrade `json:"grades"`
}

// TreasureGrade is one ranking grade.
type TreasureGrade struct {
	MaxRank int64  `json:"max_rank"`
	Name    string `json:"name"`
	Type    int64  `json:"type"`
}

// TreasurePointReward is one point reward: at Points season Ranking Points it pays Gems and the dress item (empty for none),
// once per account and season, by Mailbox.
type TreasurePointReward struct {
	ID     int64  `json:"id"`
	Points int64  `json:"points"`
	Gems   int64  `json:"gems"`
	Item   string `json:"item"`
}

// TreasureRankRewards: ClaimDays the window after the season ends in which a rank reward can be claimed, Tiers the Gems by
// rank cutline (the first tier whose Cutline the rank does not pass).
type TreasureRankRewards struct {
	ClaimDays int64              `json:"claim_days"`
	Tiers     []TreasureRankTier `json:"tiers"`
}

// TreasureRankTier is one rank reward: the last rank it covers and its Gems.
type TreasureRankTier struct {
	Cutline int64 `json:"cutline"`
	Gems    int64 `json:"gems"`
}

// TreasureTicketCap is the list of cap raises in order: the first raise sets the cap to Steps[0].Max for Steps[0].Gems.
type TreasureTicketCap struct {
	Steps []TreasureCapStep `json:"steps"`
}

// TreasureCapStep is one cap raise: the new cap and its Gem price.
type TreasureCapStep struct {
	Max  int64 `json:"max"`
	Gems int64 `json:"gems"`
}

var treasureFile = opdata.New("treasure.json", data.TreasureJSON, checkTreasureData, verifyTreasure)

// ActiveTreasureData is data/treasure.json in use.
func ActiveTreasureData() *TreasureData { return treasureFile.Get() }

// TreasureLoadFrom makes the treasure file at path the one in use ("" the embedded default). The gateway tests use it for a
// board with the probe squares; a refused file leaves the active one unchanged.
func TreasureLoadFrom(path string) error { return treasureFile.LoadFrom(path) }

// verifyTreasure checks the item archive at startup, when there is one (artVerifiable): each buff gacha item is a sold item with
// art (itemAvailable, the test a shop listing uses), and each portrait item has art in the store index.
func verifyTreasure(t *TreasureData) error {
	if !artVerifiable() {
		return nil
	}
	for i, it := range t.Buff.Items {
		if !itemAvailable(it.Code) {
			return fmt.Errorf("buff.items[%d] %q is not a sold item with art", i, it.Code)
		}
	}
	for i, npc := range t.NPCs {
		for j, code := range npc.Items {
			if !treasureNPCItemArt(code) {
				return fmt.Errorf("npcs[%d].items[%d] %q has no art in the item archive", i, j, code)
			}
		}
	}
	return nil
}

// treasureNPCItemCode reports whether code is a canonical clothing or face item code (attribute C; itemcode.ID round-trips it).
func treasureNPCItemCode(code string) bool {
	_, ok := itemcode.ID(code)
	return ok && code[0] == 'C'
}

// treasureNPCItemArt reports whether the store index has art for a portrait item: its folder is dress (clothes and accessories)
// or custom (faces and hair).
func treasureNPCItemArt(code string) bool {
	n, ok := itemcode.ID(code)
	return ok && (thumbs.HasArt("dress", n) || thumbs.HasArt("custom", n))
}

// TreasureRingSize is the client's board: 4 corners and 6 squares per side, square 0 the bottom-left corner.
const TreasureRingSize = 28

// treasureSeasonWindow is the first and last instant of a season: from its start day at 06:00 JST for Days days.
func treasureSeasonWindow(s TreasureSeason) (start, end time.Time, err error) {
	day, err := time.ParseInLocation("2006-01-02", s.Start, treasureJST)
	if err != nil {
		return start, end, fmt.Errorf("season.start must be a date (YYYY-MM-DD): %w", err)
	}
	if s.Days < 1 || s.Days > 365 {
		return start, end, errors.New("season.days must be 1 to 365")
	}
	start = day.Add(6 * time.Hour)
	return start, start.AddDate(0, 0, int(s.Days)), nil
}

// TreasurePhase is where a season cycle stands: the play window (the board is open), the reward period after it (the rank
// and point claims only) or closed (no board and no claims).
type TreasurePhase int

const (
	TreasurePhaseClosed TreasurePhase = iota
	TreasurePhasePlay
	TreasurePhaseReward
)

// TreasureCycle is the season the clock stands in: its id, its play window (Start to End), the end of its reward period
// (ClaimEnd) and the phase at the moment asked for.
type TreasureCycle struct {
	ID       int64
	Start    time.Time
	End      time.Time
	ClaimEnd time.Time
	Phase    TreasurePhase
}

// treasureCycleStop logs once that a repeating season cycle has reached the end of the Cherry block.
var treasureCycleStop sync.Once

// at returns the season cycle at now. A cycle is season.days of play followed by the rank claim days (the reward period),
// and season n starts n cycles after season 0, at its start day (06:00 JST). So the next season starts the moment the reward
// period ends, and season n has the id season.id + n. Without season.repeat only season 0 exists: after its reward period
// the phase is closed. A repeating cycle stops at the last id the Cherry block holds (closed after that, logged once).
// Before season 0 the phase is closed and the window is season 0's.
func (d *TreasureData) at(now time.Time) TreasureCycle {
	s := d.Season
	first, _, _ := treasureSeasonWindow(s) // checked at startup
	span := time.Duration(s.Days+d.RankRewards.ClaimDays) * 24 * time.Hour
	var n int64
	if now.After(first) {
		n = int64(now.Sub(first) / span)
	}
	last := int64(0)
	if s.Repeat {
		last = CherryTreasureLast - s.ID
	}
	if n > last {
		if s.Repeat {
			treasureCycleStop.Do(func() {
				log.Printf("cherry: treasure season cycle stops at id %d: the Cherry block ends at %d", s.ID+last, CherryTreasureLast)
			})
		}
		n = last
	}
	start := first.Add(time.Duration(n) * span)
	c := TreasureCycle{ID: s.ID + n, Start: start, End: start.Add(time.Duration(s.Days) * 24 * time.Hour), ClaimEnd: start.Add(span)}
	switch {
	case now.Before(first):
		c.Phase = TreasurePhaseClosed
	case now.Before(c.End):
		c.Phase = TreasurePhasePlay
	case now.Before(c.ClaimEnd):
		c.Phase = TreasurePhaseReward
	default:
		c.Phase = TreasurePhaseClosed
	}
	return c
}

// TreasureCurrent is the season cycle at now in the data in use.
func TreasureCurrent(now time.Time) TreasureCycle { return ActiveTreasureData().at(now) }

// treasureDay is the game day of t: a day turns over at 06:00 JST (21:00 UTC), when the exchange limits reset.
func treasureDay(t time.Time) int64 { return (t.Unix() + 3*3600) / 86400 }

// checkTreasureData validates the file at startup: the origin and note, the Cherry id, the season, the names, the tickets,
// the ring, the rewards, the portraits and the rules of the exchange, quests, missions, rankings, rewards and cap.
func checkTreasureData(t *TreasureData) error {
	if err := t.CheckOrigin(opdata.OriginMixed); err != nil {
		return err
	}
	if !inCherryBlock(t.Season.ID, CherryTreasureFirst, CherryTreasureLast) {
		return fmt.Errorf("season.id %d must be inside the Cherry block %d-%d", t.Season.ID, CherryTreasureFirst, CherryTreasureLast)
	}
	if strings.TrimSpace(t.Season.Name) == "" {
		return errors.New("season.name must not be empty")
	}
	if strings.TrimSpace(t.Season.EndMessage) == "" {
		return errors.New("season.end_message must not be empty (the season-end popup body)")
	}
	if _, _, err := treasureSeasonWindow(t.Season); err != nil {
		return err
	}
	if strings.TrimSpace(t.Names.Ticket) == "" || strings.TrimSpace(t.Names.Coin) == "" || strings.TrimSpace(t.Names.RankingPoint) == "" {
		return errors.New("names.ticket, names.coin and names.ranking_point must not be empty")
	}
	tk := t.Tickets
	switch {
	case tk.Max < 1 || tk.Max > 99:
		return errors.New("tickets.max must be 1 to 99")
	case tk.Start < 0 || tk.Start > tk.Max:
		return errors.New("tickets.start must be 0 to tickets.max")
	case tk.ChargeSeconds < 1 || tk.ChargeSeconds > 86400:
		return errors.New("tickets.charge_seconds must be 1 to 86400")
	case tk.PerRoll < 1 || tk.PerRoll > tk.Max:
		return errors.New("tickets.per_roll must be 1 to tickets.max")
	}
	ring := t.Board.Ring
	// The client places the token from its own 28-entry square table (unk_2F5A9F0, tags 47..70 and 72..75; roll-gate.md
	// section 2): a ring of any other size skips or overruns squares on screen.
	if len(ring) != TreasureRingSize {
		return fmt.Errorf("board.ring must have exactly %d squares (the client's fixed board)", TreasureRingSize)
	}
	hasTravel, hasExchange, hasCoin := false, false, false
	for i, c := range ring {
		switch c {
		case TreasureCellStart, TreasureCellCoin, TreasureCellExchange, TreasureCellBuff, TreasureCellBonus, TreasureCellSpecial,
			TreasureCellTrap, TreasureCellGoldKey, TreasureCellTravel, TreasureCellMiniGame:
		default:
			return fmt.Errorf("board.ring[%d]: cell type %d is not a Treasure World square (0 to 9)", i, c)
		}
		hasTravel = hasTravel || c == TreasureCellTravel
		hasExchange = hasExchange || c == TreasureCellExchange
		hasCoin = hasCoin || c == TreasureCellCoin
	}
	if ring[0] != TreasureCellStart {
		return errors.New("board.ring[0] must be the start line (0)")
	}
	// A trap or a broom square sends the token to the next coin square when its prompt is cleared (treasureRecover), and a broom
	// offers the exchange squares as its targets.
	if !hasCoin {
		return errors.New("board.ring needs a coin square (a cleared trap or broom prompt moves the token there)")
	}
	if hasTravel && !hasExchange {
		return errors.New("board.ring with a travel square needs an exchange square (the travel targets)")
	}
	r := t.Rewards
	switch unit := strings.ToUpper(r.Bonus.CoinUnit); {
	case r.Coin.Min < 1 || r.Coin.Max < r.Coin.Min || r.Coin.Max > 1000:
		return errors.New("rewards.coin must be 1 <= min <= max <= 1000")
	case r.Bonus.Coins < 1 || r.Bonus.Coins > 1000:
		return errors.New("rewards.bonus.coins must be 1 to 1000")
	case unit == "":
		return errors.New("rewards.bonus.coin_unit must not be empty")
	case unit == "GEM" || unit == "CASH" || unit == "HEART":
		return fmt.Errorf("rewards.bonus.coin_unit %q is a money unit; the board refuses it", r.Bonus.CoinUnit)
	case r.Start.Tickets < 0 || r.Start.Tickets > 100:
		return errors.New("rewards.start.tickets must be 0 to 100")
	}
	if len(t.NPCs) != 2 {
		return errors.New("npcs must list exactly two portraits")
	}
	for i, npc := range t.NPCs {
		if strings.TrimSpace(npc.Name) == "" {
			return fmt.Errorf("npcs[%d].name must not be empty", i)
		}
		if npc.Gender != "MALE" && npc.Gender != "FEMALE" {
			return fmt.Errorf("npcs[%d].gender must be MALE or FEMALE", i)
		}
		for j, code := range npc.Items {
			switch {
			case !treasureNPCItemCode(code):
				return fmt.Errorf("npcs[%d].items[%d] %q is not a clothing or face item code", i, j, code)
			case itemcode.AnimalOnly(code): // a human portrait cannot wear an animal part (the species rule of internal/itemcode)
				return fmt.Errorf("npcs[%d].items[%d] %q is an animal part; a human portrait cannot wear it", i, j, code)
			}
		}
	}
	return checkTreasureRules(t)
}

// checkTreasureRules validates the exchange, the quest and mission pools, the rankings, the rewards and the cap raises.
func checkTreasureRules(t *TreasureData) error {
	ex := t.Exchange
	switch {
	case ex.Multiple < 1 || ex.Multiple > 100:
		return errors.New("exchange.multiple must be 1 to 100")
	case ex.DailyMax < 1 || ex.DailyMax > 1000:
		return errors.New("exchange.daily_max must be 1 to 1000")
	case ex.ResetGems < 1 || ex.ResetGems > 10000:
		return errors.New("exchange.reset_gems must be 1 to 10000")
	}
	seen := map[int]bool{}
	for i, tl := range ex.Tiles {
		switch {
		case tl.Square < 0 || tl.Square >= len(t.Board.Ring) || t.Board.Ring[tl.Square] != TreasureCellExchange:
			return fmt.Errorf("exchange.tiles[%d].square %d is not an exchange square", i, tl.Square)
		case seen[tl.Square]:
			return fmt.Errorf("exchange.tiles[%d].square %d is listed twice", i, tl.Square)
		case (tl.Times > 0) == (tl.Gamble > 0):
			return fmt.Errorf("exchange.tiles[%d] needs exactly one of times or gamble", i)
		case tl.Times > 10 || tl.Gamble == 1 || tl.Gamble > 10 || tl.Times < 0 || tl.Gamble < 0:
			return fmt.Errorf("exchange.tiles[%d]: times must be 1 to 10, gamble 2 to 10", i)
		}
		seen[tl.Square] = true
	}
	switch {
	case len(t.Buff.Items) == 0 || t.Buff.Cap < 1 || t.Buff.Cap > 100:
		return errors.New("buff.items must not be empty and buff.cap must be 1 to 100")
	case t.Special.RewardTickets < 1 || t.Special.RewardTickets > 50:
		return errors.New("special.reward_tickets must be 1 to 50")
	case t.Special.GoalPulls < 1 || t.Special.GoalPulls > 100:
		return errors.New("special.goal_pulls must be 1 to 100")
	case t.MiniGame.MaxBet < 1 || t.MiniGame.MaxBet > 1000 || len(t.MiniGame.Multiples) < treasureCups:
		return errors.New("minigame.max_bet must be 1 to 1000 and minigame.multiples must name at least three cups")
	}
	for i, it := range t.Buff.Items {
		switch {
		case !validTreasureItem(it.Code):
			return fmt.Errorf("buff.items[%d].code %q is not an item code", i, it.Code)
		case it.Percent < 1 || it.Percent > 40:
			return fmt.Errorf("buff.items[%d].percent must be 1 to 40", i)
		}
	}
	for i, m := range t.MiniGame.Multiples {
		if m < 0 || m > 10 {
			return fmt.Errorf("minigame.multiples[%d] must be 0 to 10", i)
		}
	}
	switch {
	case t.Trap.HighDice < 1 || t.Trap.HighDice > 6:
		return errors.New("trap.high_dice must be 1 to 6 (a die)")
	case t.Trap.MaxTries < 1 || t.Trap.MaxTries > 20:
		return errors.New("trap.max_tries must be 1 to 20")
	case t.Travel.StaleMinutes < 1 || t.Travel.StaleMinutes > 1440:
		return errors.New("travel.stale_minutes must be 1 to 1440")
	}
	if err := checkTreasureGoldKey(t.GoldKey); err != nil {
		return err
	}
	if err := checkTreasureTasks("quests", t.Quests, true); err != nil {
		return err
	}
	if err := checkTreasureTasks("missions", t.Missions, false); err != nil {
		return err
	}
	rk := t.Ranking
	if rk.PageSize < 1 || rk.PageSize > 100 || len(rk.Grades) == 0 {
		return errors.New("ranking.page_size must be 1 to 100 and ranking.grades must not be empty")
	}
	for i, g := range rk.Grades {
		last := i == len(rk.Grades)-1
		switch {
		case strings.TrimSpace(g.Name) == "":
			return fmt.Errorf("ranking.grades[%d].name must not be empty", i)
		case g.Type < 1 || g.Type > 4:
			return fmt.Errorf("ranking.grades[%d].type must be 1 to 4", i)
		case last != (g.MaxRank == 0):
			return errors.New("the last ranking grade must have max_rank 0 and no other may")
		case i > 0 && g.MaxRank != 0 && g.MaxRank <= rk.Grades[i-1].MaxRank:
			return errors.New("ranking.grades must rise in max_rank")
		}
	}
	var lastPoints int64
	for i, p := range t.PointRewards {
		switch {
		case p.ID < 1 || (i > 0 && p.ID <= t.PointRewards[i-1].ID):
			return fmt.Errorf("point_rewards[%d].id must be positive and rise", i)
		case p.Points < 1 || p.Points <= lastPoints:
			return fmt.Errorf("point_rewards[%d].points must be positive and rise", i)
		case p.Gems < 0 || p.Gems > 10000:
			return fmt.Errorf("point_rewards[%d].gems must be 0 to 10000", i)
		case p.Gems == 0 && p.Item == "":
			return fmt.Errorf("point_rewards[%d] pays nothing", i)
		case p.Item != "" && !validTreasureItem(p.Item):
			return fmt.Errorf("point_rewards[%d].item %q is not an item code", i, p.Item)
		}
		lastPoints = p.Points
	}
	rr := t.RankRewards
	if rr.ClaimDays < 1 || rr.ClaimDays > 60 || len(rr.Tiers) == 0 {
		return errors.New("rank_rewards.claim_days must be 1 to 60 and rank_rewards.tiers must not be empty")
	}
	for i, tier := range rr.Tiers {
		if tier.Gems < 1 || tier.Gems > 10000 || tier.Cutline < 1 || (i > 0 && tier.Cutline <= rr.Tiers[i-1].Cutline) {
			return fmt.Errorf("rank_rewards.tiers[%d] must have a rising cutline and 1 to 10000 gems", i)
		}
	}
	var lastMax int64 = t.Tickets.Max
	for i, s := range t.TicketCap.Steps {
		if s.Max <= lastMax || s.Max > 99 || s.Gems < 1 || s.Gems > 10000 {
			return fmt.Errorf("ticket_cap.steps[%d] must raise the cap above %d (up to 99) for 1 to 10000 gems", i, lastMax)
		}
		lastMax = s.Max
	}
	return nil
}

// checkTreasureGoldKey validates the Mystery Card deck: at least one card, a type of 1, 2, 6 or 7, a weight of 1 or more and
// a range of 1 to 12 steps (types 1 and 2) or 1 to 100 Jewels (types 6 and 7). The warps (types 3 to 5) are refused: their
// card words are not known (slice3-spec section 3.1 [OPEN]).
func checkTreasureGoldKey(g TreasureGoldKey) error {
	if len(g.Cards) == 0 {
		return errors.New("gold_key.cards must not be empty")
	}
	for i, c := range g.Cards {
		var most int64
		switch c.Type {
		case 1, 2:
			most = 12
		case 6, 7:
			most = 100
		case 3, 4, 5:
			return fmt.Errorf("gold_key.cards[%d].type %d is a warp, not built yet (its card words are not known)", i, c.Type)
		default:
			return fmt.Errorf("gold_key.cards[%d].type %d is not a card type (1, 2, 6 or 7)", i, c.Type)
		}
		switch {
		case c.Weight < 1:
			return fmt.Errorf("gold_key.cards[%d].weight must be 1 or more", i)
		case c.Min < 1 || c.Max < c.Min || c.Max > most:
			return fmt.Errorf("gold_key.cards[%d]: min and max must be 1 <= min <= max <= %d", i, most)
		}
	}
	return nil
}

// checkTreasureTasks validates one list: its set size against its pool and every task's kind, goal, reward and picture.
func checkTreasureTasks(name string, l TreasureTasks, quest bool) error {
	if l.SetSize < 1 || l.SetSize > int64(len(l.Pool)) || l.SetSize > 5 {
		return fmt.Errorf("%s.set_size must be 1 to 5 and no more than the pool", name)
	}
	if quest && (l.RefreshHours < 1 || l.RefreshHours > 720) {
		return errors.New("quests.refresh_hours must be 1 to 720")
	}
	for i, task := range l.Pool {
		switch task.Kind {
		case "roll", "jewel", "lap", "exchange", "goldkey", "secret", "escape":
		default:
			return fmt.Errorf("%s.pool[%d].kind %q is not roll, jewel, lap, exchange, goldkey, secret or escape", name, i, task.Kind)
		}
		if task.Goal < 1 || task.Goal > 1000 {
			return fmt.Errorf("%s.pool[%d].goal must be 1 to 1000", name, i)
		}
		if quest {
			if task.Reward < 1 || task.Reward > 10 || task.Text == "" || !validTreasureItem(task.Item) {
				return fmt.Errorf("quests.pool[%d] needs 1 to 10 tickets, a text and an item code", i)
			}
			if task.Type < 1 || task.Type > 7 && task.Type != 9 {
				return fmt.Errorf("quests.pool[%d].type must be 1 to 7 or 9 (the quest pictures)", i)
			}
		} else {
			if task.Reward < 1 || task.Reward > 100 {
				return fmt.Errorf("missions.pool[%d].reward must be 1 to 100 Jewels", i)
			}
			if task.Type < 1 || task.Type > 5 {
				return fmt.Errorf("missions.pool[%d].type must be 1 to 5 (the mission pictures)", i)
			}
		}
	}
	return nil
}

// validTreasureItem reports whether code is an item code the numeric id conversion knows.
func validTreasureItem(code string) bool {
	_, ok := itemcode.ID(code)
	return ok
}

// TreasureCell is one square of the ring: its position, its type and the side it sits on.
type TreasureCell struct {
	Position  int
	Type      int
	Direction string // FR, BR, BL or FL
}

// Cell returns the square at ring position pos.
func (d *TreasureData) Cell(pos int) TreasureCell {
	n := len(d.Board.Ring)
	return TreasureCell{Position: pos, Type: d.Board.Ring[pos], Direction: treasureSides[pos*4/n]}
}

// Cells lists the ring in walking order.
func (d *TreasureData) Cells() []TreasureCell {
	out := make([]TreasureCell, len(d.Board.Ring))
	for i := range out {
		out[i] = d.Cell(i)
	}
	return out
}

// treasureCap is the ticket cap of r: the data's tickets.max, raised by the steps r bought.
func treasureCap(r store.TreasureRecord, d *TreasureData) int64 {
	if r.CapSteps > 0 && int(r.CapSteps) <= len(d.TicketCap.Steps) {
		return d.TicketCap.Steps[r.CapSteps-1].Max
	}
	return d.Tickets.Max
}

// treasureRefill adds the tickets whose refill came due since the timer started, up to the cap. At the cap the timer
// stays idle; it starts again with the next ticket spent below the cap. nowMs and Charged are Unix milliseconds.
func treasureRefill(r *store.TreasureRecord, nowMs int64, d *TreasureData) {
	limit, period := treasureCap(*r, d), d.Tickets.ChargeSeconds*1000
	if r.Tickets >= limit || r.Charged == 0 || r.Charged > nowMs {
		r.Charged = nowMs
		return
	}
	due := (nowMs - r.Charged) / period
	if due == 0 {
		return
	}
	r.Tickets += min(due, limit-r.Tickets)
	if r.Tickets >= limit {
		r.Charged = nowMs
	} else {
		r.Charged += due * period
	}
}

// treasureRemain is the seconds until the next refill (0 at the cap), rounded up.
func treasureRemain(r store.TreasureRecord, nowMs int64, d *TreasureData) int64 {
	if r.Tickets >= treasureCap(r, d) {
		return 0
	}
	left := d.Tickets.ChargeSeconds*1000 - (nowMs - r.Charged)
	return (left + 999) / 1000
}

// treasureOwn makes aid's record current for season: a record of another season starts over (the first square, the start
// tickets, no jewels; the tutorial flag stays with the account), the refill timer catches up with now and the exchange
// counters start a new day when the 06:00 JST day has turned. season is the cycle at now (at): during a season's reward
// period it is still that season, so its records stay for the claims; the next season's first op starts its records over.
func treasureOwn(r *store.TreasureRecord, now time.Time, season int64, d *TreasureData) {
	nowMs := now.UnixMilli()
	if r.Season != season {
		*r = store.TreasureRecord{Season: season, Tickets: d.Tickets.Start, Charged: nowMs, Tutorial: r.Tutorial}
	}
	if r.Position < 0 || r.Position >= len(d.Board.Ring) {
		r.Position = 0
	}
	if day := treasureDay(now); r.ExDay != day {
		r.ExDay, r.Exchanged = day, nil
	}
	treasureRefill(r, nowMs, d)
}

// TreasureView is one account's board state: the tickets with their refill timer and cap, the jewels, the Ranking Points,
// the square the token stands on, the tutorial flag and the exchange state of that square.
type TreasureView struct {
	Tickets       int64
	Max           int64
	RemainSeconds int64
	ChargeSeconds int64
	Jewels        int64
	Points        int64
	Position      int
	Tutorial      bool
	// ExchangeLeft is the Jewels the square under the token still takes today (its daily limit less today's exchanges).
	ExchangeLeft int64
	// ExchangeMax and ExchangeRate are the daily limit of a square and the Ranking Points per Jewel.
	ExchangeMax  int64
	ExchangeRate int64
	// ExchangeReset is true when the square is full today, so its reset can be bought.
	ExchangeReset bool
	// ExchangeFull lists every exchange square (ring positions) that is full today, for the travel popup.
	ExchangeFull []int
	// MiniGameLeft is the Jewels the mini-game square still allows, BuffPercent the worn buffs' total (capped) and Secret the
	// secret quest of the board (nil: none), a copy.
	MiniGameLeft int64
	BuffPercent  int64
	Secret       *store.TreasureSecret
	// TravelPending is true while the broom prompt of the token's square is unanswered; Trap is the island counts of a trapped
	// token (Count is its trapped rolls so far).
	TravelPending bool
	Trap          TreasureTrap
	// Recovery names what the board entry cleared (treasureRecover); empty when nothing was stuck.
	Recovery string
}

// treasureTimes is the fixed multiplier the Trade popup shows for ring position pos (a gamble square shows 1x).
func treasureTimes(d *TreasureData, pos int) int64 { times, _ := d.Exchange.tile(pos); return times }

// treasureUsed is the Jewels exchanged today on ring position pos.
func treasureUsed(r store.TreasureRecord, pos int) int64 { return r.Exchanged[strconv.Itoa(pos)] }

func treasureView(r store.TreasureRecord, acc *store.Account, nowMs int64, d *TreasureData) TreasureView {
	used := treasureUsed(r, r.Position)
	var full []int
	for pos, cell := range d.Board.Ring {
		if cell == TreasureCellExchange && treasureUsed(r, pos) >= d.Exchange.DailyMax {
			full = append(full, pos)
		}
	}
	var secret *store.TreasureSecret
	if r.Secret != nil {
		s := *r.Secret
		secret = &s
	}
	return TreasureView{
		ExchangeFull:  full,
		Tickets:       r.Tickets,
		Max:           treasureCap(r, d),
		RemainSeconds: treasureRemain(r, nowMs, d),
		ChargeSeconds: d.Tickets.ChargeSeconds,
		Jewels:        r.Jewels,
		Points:        r.Points,
		Position:      r.Position,
		Tutorial:      r.Tutorial,
		ExchangeLeft:  max(0, d.Exchange.DailyMax-used),
		ExchangeMax:   d.Exchange.DailyMax,
		ExchangeRate:  d.Exchange.Multiple * treasureTimes(d, r.Position),
		ExchangeReset: used >= d.Exchange.DailyMax,
		MiniGameLeft:  r.MiniGameLeft,
		BuffPercent:   treasureBuffPercent(acc, d),
		Secret:        secret,
		TravelPending: r.TravelPending,
		Trap:          treasureTrapOf(r.TrapCount, d),
	}
}

// treasureGate is the phases a board change runs in: the play window only (the board's own actions), or the play window and
// the reward period (the claims). The closed phase never runs a change.
type treasureGate int

const (
	treasureGatePlay treasureGate = iota
	treasureGateClaim
)

// treasureTx is the one way to change a board record. It runs op under AccountsMu, so an account's Gems and Mailbox move
// together with its board state; op gets the account (nil when the board has none) and the record after the catch-up
// (refill, day, season). Outside the gate's phases nothing changes and ErrTreasureNoSeason comes back. A non-nil error
// from op keeps the catch-up and drops op's changes to the account. A failed save puts the account back.
func treasureTx(aid string, now time.Time, gate treasureGate, op func(acc *store.Account, r *store.TreasureRecord, d *TreasureData) ([]store.LedgerLine, error)) (TreasureView, error) {
	d := ActiveTreasureData()
	cyc := d.at(now)
	if cyc.Phase == TreasurePhaseClosed || (gate == treasureGatePlay && cyc.Phase != TreasurePhasePlay) {
		return TreasureView{}, ErrTreasureNoSeason
	}
	store.AccountsMu.Lock()
	defer store.AccountsMu.Unlock()
	acc := store.AccountByAidLocked(aid)
	rec, _ := store.TreasureRecordOf(aid)
	rec = rec.Clone()
	treasureOwn(&rec, now, cyc.ID, d)
	rec.Worn = nil // legacy: the worn buffs are the account's equipped codes (treasure_buff.go)
	var prev store.Account
	if acc != nil {
		prev = *acc
	}
	lines, err := op(acc, &rec, d)
	if err != nil {
		if acc != nil {
			*acc = prev
		}
		_, _ = store.UpdateTreasure(aid, func(r *store.TreasureRecord) { *r = rec })
		return treasureView(rec, acc, now.UnixMilli(), d), err
	}
	if acc != nil {
		if err := store.SaveAccountsLocked(); err != nil {
			*acc = prev
			return TreasureView{}, err
		}
	}
	if _, err := store.UpdateTreasure(aid, func(r *store.TreasureRecord) { *r = rec }); err != nil {
		if acc != nil {
			*acc = prev
			_ = store.SaveAccountsLocked()
		}
		return TreasureView{}, err
	}
	store.FlushLedgerLocked(lines)
	return treasureView(rec, acc, now.UnixMilli(), d), nil
}

// TreasureOpen brings aid's board state up to date at now (a new season starts the record over, the refill catches up)
// and returns it. It serves the room enter and the relay start of a board in its play window; outside it, it is refused.
// Before it returns, treasureRecover clears a state the client could not leave; view.Recovery says what it did.
func TreasureOpen(aid string, now time.Time) (TreasureView, error) {
	var why string
	view, err := treasureTx(aid, now, treasureGatePlay, func(_ *store.Account, r *store.TreasureRecord, d *TreasureData) ([]store.LedgerLine, error) {
		why = treasureRecover(r, d, now.UnixMilli())
		return nil, nil
	})
	view.Recovery = why
	return view, err
}

// TreasurePeek is aid's board state at now without a write: the catch-up runs on a copy. The reward period and closed
// replies (status 2) use it for the tickets and the tutorial flag, so they change nothing.
func TreasurePeek(aid string, now time.Time) TreasureView {
	d := ActiveTreasureData()
	rec, _ := store.TreasureRecordOf(aid)
	rec = rec.Clone()
	treasureOwn(&rec, now, d.at(now).ID, d)
	store.AccountsMu.Lock()
	defer store.AccountsMu.Unlock()
	return treasureView(rec, store.AccountByAidLocked(aid), now.UnixMilli(), d)
}

// TreasureMove is one roll: the die, the squares passed, the square landed on and what it paid. Lap is true when the
// token passed or landed on the start line (the lap reward is paid).
type TreasureMove struct {
	Dice  int
	From  int
	To    int
	Lap   bool
	Cell  int
	Coins int64
	Bonus int64
	// Buff is the buff a buff square shows (Cell 3); Coins and Bonus are the Jewels credited after the worn buffs.
	Buff TreasureBuffLanding
	// Trap is the island's counts for a trap square (Cell 6); Escaped is true for the roll that frees a trapped token.
	Trap    TreasureTrap
	Escaped bool
	View    TreasureView
}

// treasureLand lands the token on mv.To and pays the square: a coin square pays a random amount of Jewels, a bonus square its
// bonus (both grown by the worn buffs, treasureBoost), a buff square draws its buff, a secret quest square starts its quest,
// a mini-game square allows its bet and a trap square holds the token (Trapped, no count yet). It sets mv.Cell, mv.Coins,
// mv.Bonus, mv.Buff and mv.Trap and credits the Jewels to r.
func treasureLand(acc *store.Account, r *store.TreasureRecord, d *TreasureData, mv *TreasureMove) {
	mv.Cell = d.Board.Ring[mv.To]
	switch mv.Cell {
	case TreasureCellCoin:
		c := d.Rewards.Coin
		mv.Coins = treasureBoost(c.Min+int64(TreasureRandIntn(int(c.Max-c.Min+1))), treasureBuffPercent(acc, d))
		r.Jewels += mv.Coins
	case TreasureCellBonus:
		mv.Bonus = treasureBoost(d.Rewards.Bonus.Coins, treasureBuffPercent(acc, d))
		r.Jewels += mv.Bonus
	case TreasureCellBuff:
		mv.Buff = treasureBuffLanding(acc, d)
	case TreasureCellSpecial:
		treasureSecretLand(r, d, mv.To)
	case TreasureCellTrap:
		r.Trapped, r.TrapCount = true, 0
		mv.Trap = treasureTrapOf(0, d)
	case TreasureCellMiniGame:
		r.MiniGameLeft = d.MiniGame.MaxBet
	}
}

// treasureArmTravel sets or clears the broom prompt of the token's square; TravelSince is the moment it was set.
func treasureArmTravel(r *store.TreasureRecord, armed bool, now time.Time) {
	r.TravelPending, r.TravelSince = armed, 0
	if armed {
		r.TravelSince = now.UnixMilli()
	}
}

// treasureStep walks the token total steps on from its square (unwrapped: at least the ring size passes the start line), pays a
// lap and lands on the square it reaches. The caller sets r.Position to mv.To.
func treasureStep(acc *store.Account, r *store.TreasureRecord, d *TreasureData, mv *TreasureMove, total int) {
	n := len(d.Board.Ring)
	mv.Lap = total >= n
	mv.To = total % n
	if mv.Lap {
		r.Tickets += d.Rewards.Start.Tickets
	}
	r.MiniGameLeft = 0
	treasureLand(acc, r, d, mv)
}

// treasureTrapped is a roll while the token is held on a trap square (island-spec section 6). A die of High or more, or the
// trapped roll that reaches Max, frees the token: it walks by the die from its square, never onto a trap square (a trap square it
// would land on is skipped), pays where it lands and the escape counts once ("Escape traps"). Any other roll keeps the token on
// the square and counts one more trapped roll.
func treasureTrapped(acc *store.Account, r *store.TreasureRecord, d *TreasureData, mv *TreasureMove) {
	mv.To = r.Position
	if int64(mv.Dice) < d.Trap.HighDice && r.TrapCount+1 < d.Trap.MaxTries {
		r.TrapCount++
		mv.Cell = TreasureCellTrap
		mv.Trap = treasureTrapOf(r.TrapCount, d)
		return
	}
	n := len(d.Board.Ring)
	total := r.Position + mv.Dice
	for d.Board.Ring[total%n] == TreasureCellTrap {
		total++
	}
	r.Trapped, r.TrapCount = false, 0
	mv.Escaped = true
	treasureStep(acc, r, d, mv, total)
	treasureProgress(r, d, "escape", 1)
}

// treasureCoinAfter is the first coin square forward of ring position from: where a cleared trap or broom prompt moves the
// token (no payment, no lap). checkTreasureData keeps a coin square on every ring.
func treasureCoinAfter(d *TreasureData, from int) int {
	n := len(d.Board.Ring)
	for i := 1; i <= n; i++ {
		if p := (from + i) % n; d.Board.Ring[p] == TreasureCellCoin {
			return p
		}
	}
	return from
}

// treasureRecover is the server's escape from a board state the client cannot leave, run on every board entry (enter and relay
// start) before the reply, so the reply never re-arms the prompt that holds the token. A broom prompt that is pending off a broom
// square, or pending past travel.stale_minutes, is cleared; a token left on a broom square with no prompt pending moves to the
// next coin square (the entry would show the prompt again with no targets). A trapped token whose square is not a trap, or whose
// count reached trap.max_tries, is released; a token left on a trap square that is not trapped moves to the next coin square
// (the entry would show the island popup with no release). It returns what it did, empty when nothing was stuck.
func treasureRecover(r *store.TreasureRecord, d *TreasureData, nowMs int64) string {
	var why []string
	cell := d.Board.Ring[r.Position]
	move := false
	switch {
	case r.TravelPending && cell != TreasureCellTravel:
		r.TravelPending, r.TravelSince = false, 0
		why = append(why, "broom prompt off a broom square")
	case r.TravelPending && nowMs-r.TravelSince > d.Travel.StaleMinutes*60*1000:
		r.TravelPending, r.TravelSince = false, 0
		move = true
		why = append(why, "broom prompt unanswered past travel.stale_minutes")
	case !r.TravelPending && cell == TreasureCellTravel:
		move = true
		why = append(why, "broom square without a prompt")
	}
	if r.Trapped && (cell != TreasureCellTrap || r.TrapCount >= d.Trap.MaxTries) {
		r.Trapped, r.TrapCount = false, 0
		why = append(why, "trap released (no trap square, or its count reached trap.max_tries)")
	}
	if !r.Trapped && cell == TreasureCellTrap {
		move = true
		why = append(why, "trap square without a trap")
	}
	if move {
		from := r.Position
		r.Position = treasureCoinAfter(d, from)
		why = append(why, fmt.Sprintf("moved from %d to %d", from, r.Position))
	}
	return strings.Join(why, "; ")
}

// TreasureRollDice rolls for aid at now: a die from 1 to 6, the ticket cost, the move round the ring and the payment of
// the square landed on. Without enough tickets nothing moves and ErrTreasureNoTickets comes back with the state. A roll
// sets KeyPending when it lands on a gold key square and clears it otherwise; TravelPending is set the same way for the
// travel square. TreasureGoldKeyUse clears KeyPending too and sets TravelPending by its landing. A token held on a trap
// square takes the trapped roll instead of the walk (treasureTrapped); a trapped flag with no trap square under the token is
// released first.
func TreasureRollDice(aid string, now time.Time) (TreasureMove, error) {
	var mv TreasureMove
	view, err := treasureTx(aid, now, treasureGatePlay, func(acc *store.Account, r *store.TreasureRecord, d *TreasureData) ([]store.LedgerLine, error) {
		if r.Tickets < d.Tickets.PerRoll {
			return nil, ErrTreasureNoTickets
		}
		r.Tickets -= d.Tickets.PerRoll
		mv.Dice = TreasureRandIntn(6) + 1
		mv.From = r.Position
		if r.Trapped && d.Board.Ring[r.Position] != TreasureCellTrap {
			r.Trapped, r.TrapCount = false, 0
		}
		if r.Trapped {
			treasureTrapped(acc, r, d, &mv)
		} else {
			r.MiniGameLeft = 0
			treasureStep(acc, r, d, &mv, r.Position+mv.Dice)
		}
		r.Position = mv.To
		r.KeyPending = mv.Cell == TreasureCellGoldKey
		treasureArmTravel(r, mv.Cell == TreasureCellTravel, now)
		treasureProgress(r, d, "roll", 1)
		if got := mv.Coins + mv.Bonus; got > 0 {
			treasureProgress(r, d, "jewel", got)
		}
		if mv.Lap {
			treasureProgress(r, d, "lap", 1)
		}
		return nil, nil
	})
	mv.View = view
	return mv, err
}

// TreasureCardUse is one Mystery Card use: the card drawn (its type) and its value, and the move. For types 1 and 2 Move is
// the walk and the landing (Move.Cell, and the coins or bonus paid there). For types 6 and 7 the token stays and Move.Coins
// is the Jewels the card paid.
type TreasureCardUse struct {
	Card  int64
	Value int64
	Move  TreasureMove
}

// TreasureGoldKeyUse plays the Mystery Card of the gold key aid stands on (cs 10): it draws a card from the data's deck and
// plays it. Types 1 and 2 walk the token forward or back by the drawn steps, one more for each gold key square on the way (a
// card never ends on one), and pay the square they land on as a roll does. Types 6 and 7 pay the drawn Jewels where the token
// stands. Without a pending key nothing changes and ErrTreasureRefused comes back.
func TreasureGoldKeyUse(aid string, now time.Time) (TreasureCardUse, error) {
	var use TreasureCardUse
	view, err := treasureTx(aid, now, treasureGatePlay, func(acc *store.Account, r *store.TreasureRecord, d *TreasureData) ([]store.LedgerLine, error) {
		if !r.KeyPending {
			return nil, ErrTreasureRefused
		}
		r.KeyPending = false
		r.MiniGameLeft = 0
		card, value := d.GoldKey.draw()
		n := len(d.Board.Ring)
		mv := TreasureMove{From: r.Position, To: r.Position, Cell: d.Board.Ring[r.Position]}
		switch card.Type {
		case 1, 2:
			step := 1
			if card.Type == 2 {
				step = -1
			}
			at := func(delta int) int { return ((r.Position+delta)%n + n) % n }
			delta := step * int(value)
			for d.Board.Ring[at(delta)] == TreasureCellGoldKey {
				delta += step
			}
			end := r.Position + delta
			mv.To = at(delta)
			mv.Lap = card.Type == 1 && end >= n
			if mv.Lap {
				r.Tickets += d.Rewards.Start.Tickets
			}
			treasureLand(acc, r, d, &mv)
		case 6, 7:
			value = treasureBoost(value, treasureBuffPercent(acc, d))
			mv.Coins = value
			r.Jewels += value
		}
		r.Position = mv.To
		treasureArmTravel(r, mv.Cell == TreasureCellTravel, now)
		use = TreasureCardUse{Card: card.Type, Value: value, Move: mv}
		treasureProgress(r, d, "goldkey", 1)
		if got := mv.Coins + mv.Bonus; got > 0 {
			treasureProgress(r, d, "jewel", got)
		}
		if mv.Lap {
			treasureProgress(r, d, "lap", 1)
		}
		return nil, nil
	})
	use.Move.View = view
	return use, err
}

// TreasureTravel moves aid's token from the broom square it stands on to exchange square to (cs 9). It needs a travel pending
// from the landing, a target that is an exchange square and not the square the token is on. Nothing is paid and no lap is
// passed. Any other request is refused with ErrTreasureRefused (the reason follows it) and clears the pending prompt: the
// caller answers with no move, so the client's prompt closes (broom-spec section 3.2).
func TreasureTravel(aid string, to int, now time.Time) (TreasureMove, error) {
	var mv TreasureMove
	view, err := treasureTx(aid, now, treasureGatePlay, func(_ *store.Account, r *store.TreasureRecord, d *TreasureData) ([]store.LedgerLine, error) {
		reason := ""
		switch {
		case !r.TravelPending:
			reason = "no travel pending"
		case d.Board.Ring[r.Position] != TreasureCellTravel:
			reason = "the token is not on a travel square"
		case to == r.Position:
			reason = "the target is the square the token stands on"
		case to < 0 || to >= len(d.Board.Ring):
			reason = "the target is off the ring"
		case d.Board.Ring[to] != TreasureCellExchange:
			reason = "the target is not an exchange square"
		}
		if reason != "" {
			treasureArmTravel(r, false, now)
			return nil, fmt.Errorf("%w: %s", ErrTreasureRefused, reason)
		}
		mv = TreasureMove{From: r.Position, To: to, Cell: TreasureCellExchange}
		r.Position = to
		treasureArmTravel(r, false, now)
		r.MiniGameLeft = 0
		return nil, nil
	})
	mv.View = view
	return mv, err
}

// TreasureJewels is aid's saved jewel count (0 when the account has no board state).
func TreasureJewels(aid string) int64 {
	rec, _ := store.TreasureRecordOf(aid)
	return rec.Jewels
}

// TreasureTutorialDone records that aid has seen the board tutorial; every later enter reports it as complete.
func TreasureTutorialDone(aid string) error {
	store.AccountsMu.Lock()
	defer store.AccountsMu.Unlock()
	_, err := store.UpdateTreasure(aid, func(r *store.TreasureRecord) { r.Tutorial = true })
	return err
}

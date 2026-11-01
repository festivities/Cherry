package economy

import (
	"errors"
	"fmt"
	"math/rand/v2"
	"strings"
	"time"

	"cherry/data"
	"cherry/internal/opdata"
	"cherry/internal/store"
)

// Treasure World (Blue Marble board game), slice 1: the solo board of one season. Specs in %TEMP%\opencode\cherry\
// treasure-20261102 (treasure-spec.md section 9, transport-money.md, cells.md). This file holds the rules and the saved
// per-account state (store.TreasureRecord, treasure.json); the wire is internal/lpn/treasure.go (gateway agent 9).
// Slice 1 builds the start line, the coin square and the bonus square; the other cell types are refused in the data.

// CherryTreasureFirst and CherryTreasureLast bound the ids Cherry makes for Treasure World (the season id in
// data/treasure.json). checkTreasureData refuses an id outside them.
const (
	CherryTreasureFirst int64 = 1950000000
	CherryTreasureLast  int64 = 1950000999
)

// Cell types of this slice (BlueMarble::eBoardCellType, treasure-spec section 3).
const (
	TreasureCellStart = 0
	TreasureCellCoin  = 1
	TreasureCellBonus = 4
)

// treasureJST is the season clock: a season starts and ends at 06:00 JST.
var treasureJST = time.FixedZone("JST", 9*60*60)

// treasureSides are the four sides of the ring in walking order; a square's side is the direction its avatar faces
// (AvatarDirection, cells.md 6.3 and 6.9).
var treasureSides = [4]string{"FR", "BR", "BL", "FL"}

// TreasureRandIntn returns a uniform value in [0, n) for the die and the coin amounts. Tests replace it.
var TreasureRandIntn = func(n int) int { return rand.IntN(n) }

var (
	// ErrTreasureNoSeason is returned while the season is not running (before its start or after its end).
	ErrTreasureNoSeason = errors.New("treasure: no season is running")
	// ErrTreasureNoTickets is returned for a roll the account has no tickets for.
	ErrTreasureNoTickets = errors.New("treasure: not enough tickets")
)

// TreasureData is data/treasure.json: the season, its names, the tickets, the ring and the rewards.
//
//	origin, note  mixed: the names are the Help Center's, the ids, numbers, dates and the ring are Cherry-made
//	season        id (Cherry block), name, start day (06:00 JST) and length in days
//	names         the ticket, coin and ranking point names the client shows
//	tickets       cap, starting tickets, seconds per refill, tickets per roll
//	board         the ring: cell types in walking order, four equal sides
//	rewards       coin squares (min to max Jewels), the bonus square, the lap reward (tickets)
//	npcs          the two portrait names and genders the season sends
type TreasureData struct {
	opdata.Provenance
	Season  TreasureSeason  `json:"season"`
	Names   TreasureNames   `json:"names"`
	Tickets TreasureTickets `json:"tickets"`
	Board   TreasureBoard   `json:"board"`
	Rewards TreasureRewards `json:"rewards"`
	NPCs    []TreasureNPC   `json:"npcs"`
}

// TreasureSeason is the season's id, its name, its first day ("2006-01-02", 06:00 JST) and its length in days.
type TreasureSeason struct {
	ID    int64  `json:"id"`
	Name  string `json:"name"`
	Start string `json:"start"`
	Days  int64  `json:"days"`
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
// (GEM, CASH, HEART): the client would sync its balances, and this slice pays no earned Gems.
type TreasureBonus struct {
	Coins    int64  `json:"coins"`
	CoinUnit string `json:"coin_unit"`
}

// TreasureStart is the lap reward in tickets.
type TreasureStart struct {
	Tickets int64 `json:"tickets"`
}

// TreasureNPC is one portrait the season shows (its name and gender only; the portrait is the client's).
type TreasureNPC struct {
	Name   string `json:"name"`
	Gender string `json:"gender"`
}

var treasureFile = opdata.New("treasure.json", data.TreasureJSON, checkTreasureData, nil)

// ActiveTreasureData is data/treasure.json in use.
func ActiveTreasureData() *TreasureData { return treasureFile.Get() }

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

// TreasureSeasonWindow is the active season's first and last instant (validated at startup).
func TreasureSeasonWindow() (start, end time.Time) {
	start, end, _ = treasureSeasonWindow(ActiveTreasureData().Season)
	return start, end
}

// checkTreasureData validates the structure at startup: the origin and note, the Cherry id, the season dates, the
// names, the tickets, the ring (cell types this slice builds only), the rewards and the two portraits.
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
	if n := len(ring); n < 8 || n > 120 || n%4 != 0 {
		return errors.New("board.ring must have 8 to 120 squares in four equal sides (a multiple of 4)")
	}
	for i, c := range ring {
		switch c {
		case TreasureCellStart, TreasureCellCoin, TreasureCellBonus:
		default:
			return fmt.Errorf("board.ring[%d]: cell type %d is not built yet (this slice builds 0, 1 and 4)", i, c)
		}
	}
	if ring[0] != TreasureCellStart {
		return errors.New("board.ring[0] must be the start line (0)")
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
		return fmt.Errorf("rewards.bonus.coin_unit %q is a money unit; this slice refuses it", r.Bonus.CoinUnit)
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
	}
	return nil
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

// treasureRefill adds the tickets whose refill came due since the timer started, up to the cap. At the cap the timer
// stays idle; it starts again with the next ticket spent below the cap. nowMs and Charged are Unix milliseconds.
func treasureRefill(r *store.TreasureRecord, nowMs int64, d *TreasureData) {
	limit, period := d.Tickets.Max, d.Tickets.ChargeSeconds*1000
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
	if r.Tickets >= d.Tickets.Max {
		return 0
	}
	left := d.Tickets.ChargeSeconds*1000 - (nowMs - r.Charged)
	return (left + 999) / 1000
}

// treasureOwn makes aid's record current: a record of another season starts over (the first square, the start tickets,
// no jewels; the tutorial flag stays with the account), and the refill timer catches up with now.
func treasureOwn(r *store.TreasureRecord, now time.Time, d *TreasureData) {
	nowMs := now.UnixMilli()
	if r.Season != d.Season.ID {
		*r = store.TreasureRecord{Season: d.Season.ID, Tickets: d.Tickets.Start, Charged: nowMs, Tutorial: r.Tutorial}
	}
	if r.Position < 0 || r.Position >= len(d.Board.Ring) {
		r.Position = 0
	}
	treasureRefill(r, nowMs, d)
}

// TreasureView is one account's board state: the tickets with their refill timer, the jewels, the square the token
// stands on and the tutorial flag.
type TreasureView struct {
	Tickets       int64
	Max           int64
	RemainSeconds int64
	ChargeSeconds int64
	Jewels        int64
	Position      int
	Tutorial      bool
}

func treasureView(r store.TreasureRecord, nowMs int64, d *TreasureData) TreasureView {
	return TreasureView{
		Tickets:       r.Tickets,
		Max:           d.Tickets.Max,
		RemainSeconds: treasureRemain(r, nowMs, d),
		ChargeSeconds: d.Tickets.ChargeSeconds,
		Jewels:        r.Jewels,
		Position:      r.Position,
		Tutorial:      r.Tutorial,
	}
}

// treasureUpdate runs fn on aid's current board state under the store's lock and returns the state after it. An error
// from fn comes back with the state, which keeps the refill that was due. Outside the season nothing is touched.
func treasureUpdate(aid string, now time.Time, fn func(r *store.TreasureRecord, d *TreasureData) error) (TreasureView, error) {
	d := ActiveTreasureData()
	start, end, _ := treasureSeasonWindow(d.Season)
	if now.Before(start) || !now.Before(end) {
		return TreasureView{}, ErrTreasureNoSeason
	}
	var ferr error
	rec, err := store.UpdateTreasure(aid, func(r *store.TreasureRecord) {
		treasureOwn(r, now, d)
		ferr = fn(r, d)
	})
	if err != nil {
		return TreasureView{}, err
	}
	return treasureView(rec, now.UnixMilli(), d), ferr
}

// TreasureOpen brings aid's board state up to date at now (a new season starts the record over, the refill catches up)
// and returns it. It serves the room enter and the relay start.
func TreasureOpen(aid string, now time.Time) (TreasureView, error) {
	return treasureUpdate(aid, now, func(*store.TreasureRecord, *TreasureData) error { return nil })
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
	View  TreasureView
}

// TreasureRollDice rolls for aid at now: a die from 1 to 6, the ticket cost, the move round the ring and the payment of
// the square landed on. Without enough tickets nothing moves and ErrTreasureNoTickets comes back with the state.
func TreasureRollDice(aid string, now time.Time) (TreasureMove, error) {
	var mv TreasureMove
	view, err := treasureUpdate(aid, now, func(r *store.TreasureRecord, d *TreasureData) error {
		if r.Tickets < d.Tickets.PerRoll {
			return ErrTreasureNoTickets
		}
		r.Tickets -= d.Tickets.PerRoll
		n := len(d.Board.Ring)
		mv.Dice = TreasureRandIntn(6) + 1
		mv.From = r.Position
		total := r.Position + mv.Dice
		mv.Lap = total >= n
		mv.To = total % n
		if mv.Lap {
			r.Tickets += d.Rewards.Start.Tickets
		}
		mv.Cell = d.Board.Ring[mv.To]
		switch mv.Cell {
		case TreasureCellCoin:
			c := d.Rewards.Coin
			mv.Coins = c.Min + int64(TreasureRandIntn(int(c.Max-c.Min+1)))
			r.Jewels += mv.Coins
		case TreasureCellBonus:
			mv.Bonus = d.Rewards.Bonus.Coins
			r.Jewels += mv.Bonus
		}
		r.Position = mv.To
		return nil
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
	_, err := store.UpdateTreasure(aid, func(r *store.TreasureRecord) { r.Tutorial = true })
	return err
}

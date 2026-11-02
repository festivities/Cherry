package economy

import (
	"cmp"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"cherry/internal/store"
)

// Treasure World points and rewards (cells.md 1.3, 1.4, 3 and 4.3; Help Center): Jewels exchange on exchange squares for
// Ranking Points, a full square resets for earned Gems, the ticket cap rises for earned Gems, the season's ranking lists
// every board, and the point and rank rewards are Mailbox rows. Every Gem spend is a ledger line (treasure:reset:* and
// treasure:cap:*). Nothing here sells CASH or HEART: a reset whose coin type is not GEM is refused.

// treasureRewardText is the Mailbox note of a board reward (Cherry-made).
const treasureRewardText = "Treasure World reward"

// TreasureExchangeResult is one exchange: the Ranking Points it gave and the board after it.
type TreasureExchangeResult struct {
	Added int64
	View  TreasureView
}

// TreasureExchange turns jewels of aid into Ranking Points on the exchange square under the token. It refuses a square
// that is not an exchange square, more Jewels than the account has or than the square still takes today.
func TreasureExchange(aid string, jewels int64, now time.Time) (TreasureExchangeResult, error) {
	var res TreasureExchangeResult
	view, err := treasureTx(aid, now, true, func(_ *store.Account, r *store.TreasureRecord, d *TreasureData) ([]store.LedgerLine, error) {
		if d.Board.Ring[r.Position] != TreasureCellExchange {
			return nil, ErrTreasureRefused
		}
		left := max(0, d.Exchange.DailyMax-treasureUsed(*r, r.Position))
		if jewels < 1 || jewels > r.Jewels || jewels > left {
			return nil, ErrTreasureRefused
		}
		r.Jewels -= jewels
		if r.Exchanged == nil {
			r.Exchanged = map[string]int64{}
		}
		r.Exchanged[strconv.Itoa(r.Position)] += jewels
		times, gamble := d.Exchange.tile(r.Position)
		if gamble > 0 {
			times = 1 + int64(TreasureRandIntn(int(gamble)))
		}
		res.Added = jewels * d.Exchange.Multiple * times
		r.Points += res.Added
		treasureProgress(r, d, "exchange", jewels)
		return nil, nil
	})
	res.View = view
	return res, err
}

// TreasureResetOffer is the reset of the exchange square under the token: its Gem price, the coin type it is paid in, the
// Gems the account has and whether the square is full (so a reset can be bought).
type TreasureResetOffer struct {
	CoinType  string
	Price     int64
	Gems      int64
	Available bool
}

// TreasureResetInfo is the reset offer for the square under the token.
func TreasureResetInfo(aid string, now time.Time) (TreasureResetOffer, error) {
	var out TreasureResetOffer
	_, err := treasureTx(aid, now, true, func(acc *store.Account, r *store.TreasureRecord, d *TreasureData) ([]store.LedgerLine, error) {
		out = TreasureResetOffer{CoinType: "GEM", Price: d.Exchange.ResetGems, Available: treasureResettable(*r, d)}
		if acc != nil {
			out.Gems = acc.Gems
		}
		return nil, nil
	})
	return out, err
}

// treasureResettable: the square under the token is an exchange square and today's limit on it is used up.
func treasureResettable(r store.TreasureRecord, d *TreasureData) bool {
	return d.Board.Ring[r.Position] == TreasureCellExchange && treasureUsed(r, r.Position) >= d.Exchange.DailyMax
}

// TreasureResetResult is one reset: the Gems paid, the account's Gems after it and the board.
type TreasureResetResult struct {
	Price, Gems int64
	View        TreasureView
}

// TreasureResetBuy clears today's limit on the full exchange square under the token for the Gem price. coinType must be
// GEM: nothing else is spent, and the spend goes through the ledger as treasure:reset:<season>:<day>:<square>.
func TreasureResetBuy(aid, coinType string, now time.Time) (TreasureResetResult, error) {
	var res TreasureResetResult
	view, err := treasureTx(aid, now, true, func(acc *store.Account, r *store.TreasureRecord, d *TreasureData) ([]store.LedgerLine, error) {
		if coinType != "GEM" || acc == nil || !treasureResettable(*r, d) {
			return nil, ErrTreasureRefused
		}
		reason := fmt.Sprintf("treasure:reset:%d:%d:%d", d.Season.ID, treasureDay(now), r.Position)
		lines, err := store.SpendLocked(acc, store.LedgerDeltas{Gems: -d.Exchange.ResetGems}, reason)
		if err != nil {
			return nil, ErrTreasureRefused
		}
		delete(r.Exchanged, strconv.Itoa(r.Position))
		res.Price, res.Gems = d.Exchange.ResetGems, acc.Gems
		return lines, nil
	})
	res.View = view
	return res, err
}

// TreasureCapOffer is the next ticket cap raise: the cap now, the top cap the raises reach, the Gem price of the next raise
// (0 and Available false when none is left), the Gems the account has.
type TreasureCapOffer struct {
	Cap, Top, Price, Gems int64
	Available             bool
}

// treasureCapOffer is the offer for record r; acc may be nil.
func treasureCapOffer(acc *store.Account, r store.TreasureRecord, d *TreasureData) TreasureCapOffer {
	out := TreasureCapOffer{Cap: treasureCap(r, d), Top: d.Tickets.Max}
	if n := len(d.TicketCap.Steps); n > 0 {
		out.Top = d.TicketCap.Steps[n-1].Max
	}
	if int(r.CapSteps) < len(d.TicketCap.Steps) {
		out.Price, out.Available = d.TicketCap.Steps[r.CapSteps].Gems, true
	}
	if acc != nil {
		out.Gems = acc.Gems
	}
	return out
}

// TreasureCapInfo is the next cap raise for aid.
func TreasureCapInfo(aid string, now time.Time) (TreasureCapOffer, error) {
	var out TreasureCapOffer
	_, err := treasureTx(aid, now, true, func(acc *store.Account, r *store.TreasureRecord, d *TreasureData) ([]store.LedgerLine, error) {
		out = treasureCapOffer(acc, *r, d)
		return nil, nil
	})
	return out, err
}

// TreasureCapBuy raises aid's ticket cap one step for Gems (treasure:cap:<season>:<step>). It returns the offer after the
// raise (Price is the Gems paid), and the board.
func TreasureCapBuy(aid string, now time.Time) (TreasureCapOffer, TreasureView, error) {
	var out TreasureCapOffer
	view, err := treasureTx(aid, now, true, func(acc *store.Account, r *store.TreasureRecord, d *TreasureData) ([]store.LedgerLine, error) {
		if acc == nil || int(r.CapSteps) >= len(d.TicketCap.Steps) {
			return nil, ErrTreasureRefused
		}
		step := r.CapSteps + 1
		price := d.TicketCap.Steps[r.CapSteps].Gems
		lines, err := store.SpendLocked(acc, store.LedgerDeltas{Gems: -price}, fmt.Sprintf("treasure:cap:%d:%d", d.Season.ID, step))
		if err != nil {
			return nil, ErrTreasureRefused
		}
		r.CapSteps = step
		out = treasureCapOffer(acc, *r, d)
		out.Price = price
		return lines, nil
	})
	return out, view, err
}

// treasureRanked lists the season's boards with Ranking Points, most points first (equal points by aid).
type treasureRanked struct {
	aid    string
	points int64
}

func treasureRankOrder(season int64) []treasureRanked {
	var out []treasureRanked
	for aid, r := range store.TreasureSnapshot() {
		if r.Season == season && r.Points > 0 {
			out = append(out, treasureRanked{aid, r.Points})
		}
	}
	slices.SortFunc(out, func(a, b treasureRanked) int {
		if c := cmp.Compare(b.points, a.points); c != 0 {
			return c
		}
		return strings.Compare(a.aid, b.aid)
	})
	return out
}

// treasureGrade is the grade of rank (the first grade whose max_rank the rank does not pass).
func treasureGrade(d *TreasureData, rank int64) TreasureGrade {
	for _, g := range d.Ranking.Grades {
		if g.MaxRank == 0 || rank <= g.MaxRank {
			return g
		}
	}
	return TreasureGrade{}
}

// TreasureRankRow is one row of the season's ranking: the board's owner, its rank, points, name and grade.
type TreasureRankRow struct {
	Aid    string
	Rank   int64
	Points int64
	Name   string
	Grade  TreasureGrade
}

// TreasureRankingPage lists ranks start to end (1-based, inclusive, one page at most) of the season with the names of the
// accounts, and the number of pages.
func TreasureRankingPage(start, end int64) (rows []TreasureRankRow, pages int64) {
	d := ActiveTreasureData()
	order := treasureRankOrder(d.Season.ID)
	total := int64(len(order))
	pages = (total + d.Ranking.PageSize - 1) / d.Ranking.PageSize
	start = max(start, 1)
	end = min(end, total, start+d.Ranking.PageSize-1)
	for rank := start; rank <= end; rank++ {
		e := order[rank-1]
		row := TreasureRankRow{Aid: e.aid, Rank: rank, Points: e.points, Grade: treasureGrade(d, rank)}
		if snap, ok := store.AccountByAvatarID(e.aid); ok {
			row.Name = snap.Name
		}
		rows = append(rows, row)
	}
	return rows, pages
}

// TreasureMyRank is aid's own place: rank 0 and no grade without points.
type TreasureMyRank struct {
	Rank, Points int64
	Grade        TreasureGrade
}

// TreasureMyRankOf returns aid's place in the season's ranking.
func TreasureMyRankOf(aid string) TreasureMyRank {
	d := ActiveTreasureData()
	var out TreasureMyRank
	if r, _ := store.TreasureRecordOf(aid); r.Season == d.Season.ID {
		out.Points = r.Points
	}
	for i, e := range treasureRankOrder(d.Season.ID) {
		if e.aid == aid {
			out.Rank = int64(i + 1)
			out.Grade = treasureGrade(d, out.Rank)
		}
	}
	return out
}

// TreasurePointRow is one point reward: its threshold, what it pays and whether aid has claimed it.
type TreasurePointRow struct {
	ID, Points, Gems int64
	Item             string
	Claimed          bool
}

// TreasurePointRows lists the season's point rewards and which of them aid has claimed.
func TreasurePointRows(aid string) []TreasurePointRow {
	d := ActiveTreasureData()
	r, _ := store.TreasureRecordOf(aid)
	rows := make([]TreasurePointRow, 0, len(d.PointRewards))
	for _, p := range d.PointRewards {
		rows = append(rows, TreasurePointRow{ID: p.ID, Points: p.Points, Gems: p.Gems, Item: p.Item, Claimed: slices.Contains(r.PointClaimed, p.ID)})
	}
	return rows
}

// treasureMail delivers rows to acc's Mailbox; nothing is added unless every row fits.
func treasureMail(acc *store.Account, rows []store.MailRow, now time.Time) error {
	if acc == nil {
		return ErrTreasureNoAccount
	}
	if len(liveMail(acc, now))+len(rows) > MailMaxRows() {
		return ErrTreasureRefused
	}
	for _, row := range rows {
		if !addMailLocked(acc, row, ActiveRewards().Mail.DefaultDays, now) {
			return ErrTreasureRefused
		}
	}
	return nil
}

// treasureRewardMails are the Mailbox rows of a Gem and item reward (the item row only when the game can grant it).
func treasureRewardMails(gems int64, item string) ([]store.MailRow, bool) {
	var rows []store.MailRow
	if gems > 0 {
		rows = append(rows, store.MailRow{Type: store.MailTypeGems, Amount: gems, Sender: MailSender(), Message: treasureRewardText})
	}
	if item != "" {
		if !itemAvailable(item) {
			return nil, false
		}
		rows = append(rows, store.MailRow{Type: store.MailTypeItem, Item: item, Sender: MailSender(), Message: treasureRewardText})
	}
	return rows, true
}

// TreasurePointClaim pays point reward id to aid's Mailbox once aid has reached its threshold this season. Nothing changes
// when the reward is unknown, not reached, already claimed, its item is unavailable or the Mailbox has no room for it.
func TreasurePointClaim(aid string, id int64, now time.Time) (TreasurePointRow, TreasureView, error) {
	var out TreasurePointRow
	view, err := treasureTx(aid, now, true, func(acc *store.Account, r *store.TreasureRecord, d *TreasureData) ([]store.LedgerLine, error) {
		i := slices.IndexFunc(d.PointRewards, func(p TreasurePointReward) bool { return p.ID == id })
		if i < 0 || r.Points < d.PointRewards[i].Points || slices.Contains(r.PointClaimed, id) {
			return nil, ErrTreasureRefused
		}
		p := d.PointRewards[i]
		rows, ok := treasureRewardMails(p.Gems, p.Item)
		if !ok {
			return nil, ErrTreasureRefused
		}
		if err := treasureMail(acc, rows, now); err != nil {
			return nil, err
		}
		r.PointClaimed = append(r.PointClaimed, id)
		out = TreasurePointRow{ID: p.ID, Points: p.Points, Gems: p.Gems, Item: p.Item, Claimed: true}
		return nil, nil
	})
	return out, view, err
}

// TreasureRankOffer is the rank reward state of aid: the tiers, its own tier's Gems (0 when none), whether the season
// has ended, whether the claim window is open and whether the reward can be claimed now.
type TreasureRankOffer struct {
	Tiers                  []TreasureRankTier
	Rank, Gems             int64
	Ended, Open, Claimable bool
}

// treasureRankGems is the Gems of rank (0 when the rank is past the last cutline or the rank is 0).
func treasureRankGems(d *TreasureData, rank int64) int64 {
	if rank < 1 {
		return 0
	}
	for _, t := range d.RankRewards.Tiers {
		if rank <= t.Cutline {
			return t.Gems
		}
	}
	return 0
}

// treasureRankWindow is the claim window: from the season's end for claim_days.
func treasureRankWindow(d *TreasureData, now time.Time) (ended, open bool) {
	_, end, _ := treasureSeasonWindow(d.Season)
	until := end.AddDate(0, 0, int(d.RankRewards.ClaimDays))
	return !now.Before(end), !now.Before(end) && now.Before(until)
}

// TreasureRankInfo is aid's rank reward state at now.
func TreasureRankInfo(aid string, now time.Time) TreasureRankOffer {
	d := ActiveTreasureData()
	ended, open := treasureRankWindow(d, now)
	me := TreasureMyRankOf(aid)
	rec, _ := store.TreasureRecordOf(aid)
	gems := treasureRankGems(d, me.Rank)
	return TreasureRankOffer{Tiers: d.RankRewards.Tiers, Rank: me.Rank, Gems: gems, Ended: ended, Open: open,
		Claimable: open && gems > 0 && !rec.RankClaimed}
}

// TreasureRankClaim pays aid's rank reward to its Mailbox once the season has ended, inside the claim window, and only once.
func TreasureRankClaim(aid string, now time.Time) (int64, error) {
	var gems int64
	_, err := treasureTx(aid, now, false, func(acc *store.Account, r *store.TreasureRecord, d *TreasureData) ([]store.LedgerLine, error) {
		ended, open := treasureRankWindow(d, now)
		if !ended || !open || r.RankClaimed {
			return nil, ErrTreasureRefused
		}
		rank := TreasureMyRankOf(aid).Rank
		gems = treasureRankGems(d, rank)
		if gems == 0 {
			return nil, ErrTreasureRefused
		}
		if err := treasureMail(acc, []store.MailRow{{Type: store.MailTypeGems, Amount: gems, Sender: MailSender(), Message: treasureRewardText}}, now); err != nil {
			return nil, err
		}
		r.RankClaimed = true
		return nil, nil
	})
	if err != nil {
		return 0, err
	}
	return gems, nil
}

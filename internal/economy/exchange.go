package economy

import (
	"errors"
	"fmt"
	"log"
	"slices"
	"time"

	"cherry/data"
	"cherry/internal/opdata"
	"cherry/internal/store"
	"cherry/internal/thumbs"
)

// Square Boss Coin exchange office (popup 116; wire notes %TEMP%\opencode\cherry\phaseDE-20261022\
// exchange-spec.md). An account trades its Boss Coins for one clothing or room item per row. The
// client shows one group: the standing rows, plus the rows of every event whose season is in force.
// A buy is a direct purchase of the row the client picked; the item is granted in the same save that
// takes the coins.

// Exchange is data/exchange.json: the Boss Coin exchange office. Every value is Cherry-made.
//
//	title, title_desc  the group's title and description on the popup
//	rows[]             {id, item_code, price}: rows always on sale
//	events[]           {season, rows}: rows on sale only while the calendar season named season is in
//	                   force (calendars.json: its game dates, and a staff season override)
//	id                 the row's id, sent back by the client to buy it: unique in the file, at least 1
//	item_code          a clothing or room item with art (a pet works too); the buy grants it
//	price              Boss Coins, at least 1; a boss catch pays 6 (fishboss.json), a participation 1
//
// Every row costs Boss Coins (there is no event currency yet). An item the account already owns is
// refused, not sold back (the shop refuses owned clothing the same way).
type Exchange struct {
	opdata.Provenance
	Title     string          `json:"title"`
	TitleDesc string          `json:"title_desc"`
	Rows      []ExchangeRow   `json:"rows"`
	Events    []ExchangeEvent `json:"events"`
}

// ExchangeRow is one row of the office: a Boss Coin price for one item.
type ExchangeRow struct {
	ID       int64  `json:"id"`
	ItemCode string `json:"item_code"`
	Price    int64  `json:"price"`
}

// ExchangeEvent is a group of rows on sale while a calendar season is in force.
type ExchangeEvent struct {
	Season string        `json:"season"`
	Rows   []ExchangeRow `json:"rows"`
}

var exchangeFile = opdata.New("exchange.json", data.ExchangeJSON, checkExchange, verifyExchange)

// ActiveExchange is the exchange file in use.
func ActiveExchange() *Exchange { return exchangeFile.Get() }

// checkExchange validates the structure: a title, a non-empty standing group, unique positive ids and
// positive prices, and a season name on every event. The item codes and the season names are checked
// at startup (verifyExchange), once the item archive and the calendars are loaded.
// CherryExchangeRowFirst and CherryExchangeRowLast bound the Boss Coin exchange row ids (exchange.json; the client
// sends the row id back as gacha_id, exchange-spec). The standing rows are 1001-1006, the Halloween rows 2001-2004.
// The rows come from the server, so no archive holds an original row id; the block is a reservation.
const (
	CherryExchangeRowFirst int64 = 1001
	CherryExchangeRowLast  int64 = 2999
)

func checkExchange(e *Exchange) error {
	if err := e.CheckOrigin("mixed"); err != nil {
		return err
	}
	if e.Title == "" {
		return errors.New("title must not be empty")
	}
	if len(e.Rows) == 0 {
		return errors.New("rows must not be empty: the office always sells its standing rows")
	}
	seen := map[int64]bool{}
	check := func(where string, rows []ExchangeRow) error {
		for i, r := range rows {
			switch {
			case !inCherryBlock(r.ID, CherryExchangeRowFirst, CherryExchangeRowLast):
				return fmt.Errorf("%s row %d: id %d is outside the Cherry block %d-%d", where, i+1, r.ID, CherryExchangeRowFirst, CherryExchangeRowLast)
			case seen[r.ID]:
				return fmt.Errorf("%s row %d: id %d is used twice", where, i+1, r.ID)
			case r.Price < 1:
				return fmt.Errorf("%s row %d (id %d): price must be at least 1 Boss Coin", where, i+1, r.ID)
			case r.ItemCode == "":
				return fmt.Errorf("%s row %d (id %d): item_code must not be empty", where, i+1, r.ID)
			}
			seen[r.ID] = true
		}
		return nil
	}
	if err := check("rows", e.Rows); err != nil {
		return err
	}
	for i, ev := range e.Events {
		where := fmt.Sprintf("events[%d]", i+1)
		switch {
		case ev.Season == "":
			return fmt.Errorf("%s: season must name a season in calendars.json", where)
		case len(ev.Rows) == 0:
			return fmt.Errorf("%s (%s): rows must not be empty", where, ev.Season)
		}
		if err := check(where+" ("+ev.Season+")", ev.Rows); err != nil {
			return err
		}
	}
	return nil
}

// verifyExchange checks the item codes against the item archive (a code the server cannot price, or
// one with no art, is never sold) and each event's season against the calendar file.
func verifyExchange(e *Exchange) error {
	check := func(where string, rows []ExchangeRow) error {
		for _, r := range rows {
			if _, _, _, ok := ItemPrice(r.ItemCode); !ok {
				return fmt.Errorf("%s: row %d item %s is not a sellable item code", where, r.ID, r.ItemCode)
			}
			if !itemAvailable(r.ItemCode) {
				return fmt.Errorf("%s: row %d item %s has no art in the item archive (%s)", where, r.ID, r.ItemCode, thumbs.DpItemRoot)
			}
		}
		return nil
	}
	if err := check("rows", e.Rows); err != nil {
		return err
	}
	for i, ev := range e.Events {
		where := fmt.Sprintf("events[%d] (%s)", i+1, ev.Season)
		if !slices.ContainsFunc(seasons(), func(s calSchedule) bool { return s.Name == ev.Season }) {
			return fmt.Errorf("%s: season %q is not a season in calendars.json", where, ev.Season)
		}
		if err := check(where, ev.Rows); err != nil {
			return err
		}
	}
	return nil
}

// ExchangeRowsAt is the group the office sells at now: the standing rows, then the rows of every
// event whose season is in force (the calendar's season at now, scheduleAt).
func ExchangeRowsAt(now time.Time) []ExchangeRow {
	e := ActiveExchange()
	rows := slices.Clone(e.Rows)
	name := scheduleAt(now).Name
	for _, ev := range e.Events {
		if ev.Season == name {
			rows = append(rows, ev.Rows...)
		}
	}
	return rows
}

// ExchangeRowsFor is the office list for aid at now: ExchangeRowsAt without the items aid owns (a buy
// of those is refused, and the client can only show that as "Network Error."). If that leaves nothing,
// every row stays: an empty group shows the client's "Boss Raid has finished." box.
func ExchangeRowsFor(aid string, now time.Time) []ExchangeRow {
	rows := ExchangeRowsAt(now)
	store.AccountsMu.Lock()
	defer store.AccountsMu.Unlock()
	acc := store.AccountByAidLocked(aid)
	if acc == nil {
		return rows
	}
	open := slices.DeleteFunc(slices.Clone(rows), func(r ExchangeRow) bool { return itemOwnedLocked(acc, r.ItemCode) })
	if len(open) == 0 {
		return rows
	}
	return open
}

// ExchangeWindowAt is the period the office shows at now (the client always prints one): from the
// current game day's start to the end of the event season in force, or one year on without an event.
func ExchangeWindowAt(now time.Time) (begin, end time.Time) {
	gd := gameDay(now)
	begin = time.Date(gd.Year(), gd.Month(), gd.Day(), attendDayStartHour, 0, 0, 0, jst)
	s := scheduleAt(now)
	for _, ev := range ActiveExchange().Events {
		if ev.Season == s.Name && s.End != 0 {
			return begin, s.limitedEnd(now)
		}
	}
	return begin, begin.AddDate(1, 0, 0)
}

// ExchangeItemID is the numeric item id of an exchange row's item: the name of its art folder, the
// id the client's item table derives from the code too (0 for an item without art).
func ExchangeItemID(code string) int64 {
	if it := loadCatalog().byCode[code]; it != nil {
		return it.ID
	}
	return 0
}

// ExchangeItemImageURL is the item's dp picture as a path RELATIVE to the client's static base: the
// Square shop cells prefix that base (device 2026-10-26: an absolute URL was fetched as
// "/https://play-static.../dp.png" and 404ed), as with the fish-shop pictures. "" when the item has
// no folder.
func ExchangeItemImageURL(code string) string {
	kind, id, ok := itemThumbRef(code)
	if !ok {
		return ""
	}
	return "arts_item_" + kind + "_" + id + "/dp.png"
}

// BossCoinBalance is aid's Boss Coins (0 for an unknown account).
func BossCoinBalance(aid string) int64 {
	store.AccountsMu.Lock()
	defer store.AccountsMu.Unlock()
	if acc := store.AccountByAidLocked(aid); acc != nil {
		return acc.BossCoins
	}
	return 0
}

// ExchangeBuy sells the row with id to aid for its Boss Coins, if the row is on sale at now, aid has
// the coins and does not own the item. The item is granted and the coins taken in one save; a failed
// save restores the account. ok is false (nothing changed) otherwise; balance is aid's Boss Coins
// after the call.
func ExchangeBuy(aid string, id int64, now time.Time) (row ExchangeRow, balance int64, ok bool) {
	store.AccountsMu.Lock()
	defer store.AccountsMu.Unlock()
	acc := store.AccountByAidLocked(aid)
	if acc == nil {
		return ExchangeRow{}, 0, false
	}
	rows := ExchangeRowsAt(now)
	i := slices.IndexFunc(rows, func(r ExchangeRow) bool { return r.ID == id })
	if i < 0 || acc.BossCoins < rows[i].Price {
		return ExchangeRow{}, acc.BossCoins, false
	}
	row = rows[i]
	prev := *acc
	// itemGrantLocked grants nothing for an owned item and returns its sell-back Gems instead: that
	// is a refusal here, so the rollback below undoes any room seeding it did.
	if sellBack, err := itemGrantLocked(acc, row.ItemCode); err != nil || sellBack > 0 {
		*acc = prev
		return ExchangeRow{}, acc.BossCoins, false
	}
	acc.BossCoins = prev.BossCoins - row.Price
	if err := store.SaveAccountsLocked(); err != nil {
		*acc = prev
		log.Printf("cherry: square exchange save failed aid=%s row=%d err=%v", aid, id, err)
		return ExchangeRow{}, acc.BossCoins, false
	}
	return row, acc.BossCoins, true
}

package economy

import (
	"errors"
	"fmt"
	"log"
	"slices"
	"strconv"
	"time"

	"cherry/data"
	"cherry/internal/opdata"
	"cherry/internal/store"
	"cherry/internal/thumbs"
)

// Square Ride Point exchange shop (popup 117; wire notes %TEMP%\opencode\cherry\phaseDE-20261022\
// racing-exchange-spec.md). An account spends its Ride Points (the racing store points, racing.go) on one
// clothing or room item per row. Ride Points are not Gems, Cash or Boss Coins: a buy takes only the points.

// RideShop is data/racing_exchange.json: the Ride Point exchange shop. Every value is Cherry-made.
//
//	rows[]       {id, item_code, price}: the items on sale
//	begin, end   the sale period (RFC 3339). Absent: from one day before now to 365 days after it. A row
//	             is on sale only inside the period, so a shop outside it lists nothing and sells nothing.
//	id           the row's id, sent back by the client to buy it: unique in the file, at least 1
//	item_code    a clothing (CU, CA) or room (RU) item with art; pets are not sold here
//	price        Ride Points, at least 1 (one finish pays 5 by default, racing.json)
//
// An item the account already owns stays on the list, marked owned (the client disables its button),
// and a buy of it is refused.
type RideShop struct {
	Rows  []RideShopRow `json:"rows"`
	Begin time.Time     `json:"begin"`
	End   time.Time     `json:"end"`
}

// RideShopRow is one item on sale for Ride Points.
type RideShopRow struct {
	ID       int64  `json:"id"`
	ItemCode string `json:"item_code"`
	Price    int64  `json:"price"`
}

// RideShopEntry is a row as the client lists it: Owned is true when the account already has the item.
type RideShopEntry struct {
	RideShopRow
	Owned bool
}

var rideShopFile = opdata.New("racing_exchange.json", data.RacingExchangeJSON, checkRideShop, verifyRideShop)

// ActiveRideShop is the Ride Point exchange file in use.
func ActiveRideShop() *RideShop { return rideShopFile.Get() }

// checkRideShop validates the structure: ids unique and at least 1, prices at least 1, a set item code,
// and a period whose end is after its begin. The item codes are checked at startup (verifyRideShop).
func checkRideShop(s *RideShop) error {
	if !s.Begin.IsZero() && !s.End.IsZero() && !s.End.After(s.Begin) {
		return errors.New("end must be after begin")
	}
	seen := map[int64]bool{}
	for i, r := range s.Rows {
		where := fmt.Sprintf("row %d", i+1)
		switch {
		case r.ID < 1:
			return fmt.Errorf("%s: id must be at least 1", where)
		case seen[r.ID]:
			return fmt.Errorf("%s: id %d is used twice", where, r.ID)
		case r.Price < 1:
			return fmt.Errorf("%s (id %d): price must be at least 1 Ride Point", where, r.ID)
		case r.ItemCode == "":
			return fmt.Errorf("%s (id %d): item_code must not be empty", where, r.ID)
		}
		seen[r.ID] = true
	}
	return nil
}

// verifyRideShop checks each item code against the item archive: a code the server cannot price, a
// pet (not sold here) or an item with no art is refused.
func verifyRideShop(s *RideShop) error {
	for i, r := range s.Rows {
		where := fmt.Sprintf("row %d (id %d)", i+1, r.ID)
		if _, _, _, ok := ItemPrice(r.ItemCode); !ok {
			return fmt.Errorf("%s: item %s is not a sellable item code", where, r.ItemCode)
		}
		if _, kind := classify(r.ItemCode); kind == kindPet {
			return fmt.Errorf("%s: item %s is a pet; the Ride Point shop sells clothing and room items", where, r.ItemCode)
		}
		if !itemAvailable(r.ItemCode) {
			return fmt.Errorf("%s: item %s has no art in the item archive (%s)", where, r.ItemCode, thumbs.DpItemRoot)
		}
	}
	return nil
}

// RideShopItemType is a row's ItemType on the wire: 1 clothing, 2 room.
func RideShopItemType(code string) int64 {
	if _, kind := classify(code); kind == kindInterior {
		return 2
	}
	return 1
}

// RideShopWindowAt is the sale period at now: the file's begin and end, each defaulting to one day
// before now and 365 days after it.
func RideShopWindowAt(now time.Time) (begin, end time.Time) {
	s := ActiveRideShop()
	begin, end = s.Begin, s.End
	if begin.IsZero() {
		begin = now.AddDate(0, 0, -1)
	}
	if end.IsZero() {
		end = now.AddDate(0, 0, 365)
	}
	return begin, end
}

// rideShopRowsAt is the rows on sale at now: none outside the sale period.
func rideShopRowsAt(now time.Time) []RideShopRow {
	begin, end := RideShopWindowAt(now)
	if now.Before(begin) || now.After(end) {
		return nil
	}
	return ActiveRideShop().Rows
}

// ridePointsLocked is acc's Ride Points: the career total in its racing record (0 without one).
func ridePointsLocked(acc *store.Account) int64 {
	if acc.Racing == nil {
		return 0
	}
	return acc.Racing.Points
}

// setRidePointsLocked replaces acc's racing record with a copy whose Points is points; the other fields
// stay as they were. The caller saves, and restores the old pointer on failure.
func setRidePointsLocked(acc *store.Account, points int64) {
	rec := store.RacingRecord{}
	if acc.Racing != nil {
		rec = *acc.Racing
	}
	rec.Points = points
	acc.Racing = &rec
}

// RideShopFor is the list for aid at now: the rows on sale (owned ones marked), and the account's Ride
// Points (0 for an unknown account, whose list is still served).
func RideShopFor(aid string, now time.Time) (rows []RideShopEntry, points int64) {
	store.AccountsMu.Lock()
	defer store.AccountsMu.Unlock()
	acc := store.AccountByAidLocked(aid)
	for _, r := range rideShopRowsAt(now) {
		e := RideShopEntry{RideShopRow: r}
		if acc != nil {
			e.Owned = itemOwnedLocked(acc, r.ItemCode)
		}
		rows = append(rows, e)
	}
	if acc != nil {
		points = ridePointsLocked(acc)
	}
	return rows, points
}

// RideShopBuy sells the row with id to aid for its Ride Points, if the row is on sale at now, aid has
// the points and does not own the item. The item is granted and the points taken in one save, with a
// ledger line of currency ridePoints. row is the row with that id when it is on sale (zero otherwise);
// ok is false (nothing changed) for any refusal. points is aid's Ride Points after the call.
func RideShopBuy(aid string, id int64, now time.Time) (row RideShopRow, points int64, ok bool) {
	store.AccountsMu.Lock()
	defer store.AccountsMu.Unlock()
	acc := store.AccountByAidLocked(aid)
	if acc == nil {
		return RideShopRow{}, 0, false
	}
	points = ridePointsLocked(acc)
	rows := rideShopRowsAt(now)
	i := slices.IndexFunc(rows, func(r RideShopRow) bool { return r.ID == id })
	if i < 0 {
		return RideShopRow{}, points, false
	}
	row = rows[i]
	if points < row.Price || itemOwnedLocked(acc, row.ItemCode) {
		return row, points, false
	}
	prev := *acc
	// itemGrantLocked returns sell-back Gems for an owned item, which was refused above, so this grants.
	if sellBack, err := itemGrantLocked(acc, row.ItemCode); err != nil || sellBack > 0 {
		*acc = prev
		return row, points, false
	}
	setRidePointsLocked(acc, points-row.Price)
	if err := store.SaveAccountsLocked(); err != nil {
		*acc = prev
		log.Printf("cherry: square racing exchange save failed aid=%s row=%d err=%v", aid, id, err)
		return row, points, false
	}
	line := store.LedgerLine{
		Time:     now.UTC().Format(time.RFC3339),
		Aid:      aid,
		Currency: "ridePoints",
		Delta:    -row.Price,
		Balance:  points - row.Price,
		Reason:   "racingexchange:" + strconv.FormatInt(row.ID, 10) + " " + row.ItemCode,
	}
	store.FlushLedgerLocked([]store.LedgerLine{line})
	return row, points - row.Price, true
}

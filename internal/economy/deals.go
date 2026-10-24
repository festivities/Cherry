package economy

import (
	"cmp"
	"crypto/sha256"
	"encoding/binary"
	"maps"
	"slices"
	"strconv"
	"time"

	"cherry/internal/store"
)

// Economy Phase E slice 2: daily deals. Six items a game day are 30% off (prices.json: deal_paid_percent,
// deals_per_kind): three clothing and three furniture (walls and floors count as furniture), drawn from the shop catalogs only, never pets,
// faces or limited items (season.go). The game day is the calendar's (06:00 JST boundary,
// AttendDayKey), so every account sees the same deals, they survive a restart and change each day.

// dealPrice is the deal price of a table price: ceil(price x deal_paid_percent), in the item's own
// currency (prices.json; 70 = 30% off).
func dealPrice(price int64) int64 {
	return (price*ActivePrices().DealPaidPercent + 99) / 100
}

// dealSet maps the deal codes of one game day to their deal price.
type dealSet map[string]int64

// dealRanked is a catalog code with its rank for one game day.
type dealRanked struct {
	code string
	rank uint64
}

// dealsOn is the deal set of the game day holding now. Within each kind the deals_per_kind lowest
// sha256 ranks of (day, code) win, so the pick depends only on the day and the catalog.
// ponytail: recomputed per request (about 6,500 catalog rows); cache per game day if a profile shows it.
func dealsOn(now time.Time) dealSet {
	day := AttendDayKey(now)
	deals := dealSet{}
	c := loadCatalog()
	for _, list := range [][]shopItem{c.fashion, c.interior} {
		var cands []dealRanked
		for _, it := range list {
			if limited, _, _ := limitedSale(it.Code, now); limited {
				continue // limited items never become deals, whatever their window
			}
			h := sha256.Sum256([]byte(strconv.FormatInt(day, 10) + "/" + it.Code)) // fnv clustered codes differing in the last byte
			cands = append(cands, dealRanked{code: it.Code, rank: binary.BigEndian.Uint64(h[:8])})
		}
		slices.SortFunc(cands, func(a, b dealRanked) int { return cmp.Compare(a.rank, b.rank) })
		for _, d := range cands[:min(ActivePrices().DealsPerKind, len(cands))] {
			price, _, _, _ := ItemPrice(d.code)
			deals[d.code] = dealPrice(price)
		}
	}
	return deals
}

// saleKeys sets a catalog row's sale fields. A daily deal shows discount, its deal price and SALE,
// ending at the next game day; a limited item shows LIMIT, ending when its window closes. Epoch
// milliseconds go out as strings (the client's sellingEnd/discountEnd parsers). Other rows keep "".
func saleKeys(row map[string]any, code string, deals dealSet, now time.Time) {
	if d, deal := deals[code]; deal {
		end := strconv.FormatInt(keyTime(AttendDayKey(now)+1).UnixMilli(), 10)
		row["discount"], row["discountedPrice"], row["sellingIconType"] = true, d, "SALE"
		row["sellingEnd"], row["discountEnd"] = end, end
	}
	if limited, _, end := limitedSale(code, now); limited {
		row["sellingIconType"], row["sellingEnd"] = "LIMIT", strconv.FormatInt(end.UnixMilli(), 10)
	}
}

// soldPrice is what an owned item pays back: sell_back_percent of what it cost, the Gems it was
// bought for when that was a deal (paid, 0 = no record) else its table price. Only a paid price
// below the table price counts.
func soldPrice(code string, paid int64) int64 {
	price, _, _, _ := ItemPrice(code)
	if paid > 0 && paid < price {
		price = paid
	}
	return price * ActivePrices().SellBackPercent / 100
}

// RoomItemPrice is the Room Deco row price and grade of an owned furniture item: what the room
// recycle pays for it (soldPrice).
func RoomItemPrice(it store.RoomItem) (int64, string) {
	_, _, grade, _ := ItemPrice(it.Cd)
	return soldPrice(it.Cd, it.Paid), grade
}

// ClosetPrice is the closet row price and grade of an owned clothing code, given the account's paid
// table (store.Account.PaidPrices): what the closet recycle pays for it.
func ClosetPrice(paid map[string]int64, code string) (int64, string) {
	_, _, grade, _ := ItemPrice(code)
	return soldPrice(code, paid[code]), grade
}

// withPaid returns a new paid table: old plus add. Accounts hold their tables copy-on-write, because
// the purchase rollback keeps the old map.
func withPaid(old, add map[string]int64) map[string]int64 {
	if len(add) == 0 {
		return old
	}
	out := make(map[string]int64, len(old)+len(add))
	maps.Copy(out, old)
	maps.Copy(out, add)
	return out
}

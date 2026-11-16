package economy

import (
	"errors"
	"fmt"
	"hash/fnv"
	"slices"
	"strings"
	"time"

	"cherry/internal/store"
)

// Butler special orders (Garden LPN agent 10, ch 64 request, ch 65 deliver, ch 66 bag list; replies hc 75, 76 and 77;
// butler-robo-spec section 11). Chat's "Make me a special order!" asks a hired butler for count materials of one star
// grade from the player's Bag. A delivery pays one reward (Cash or Gems) through the ledger. Every value is Cherry-made:
// data/butlers.json special_order.

// ErrOrderEnded is a delivery to no open order of that id for that butler (unknown, delivered or past its end), or to a
// butler whose contract has ended. Nothing changed.
var ErrOrderEnded = errors.New("butler: special order ended")

// ErrOrderItems is a delivery whose materials do not make the order: a grade, count, total, ownership or repeat is wrong.
// Nothing changed.
var ErrOrderItems = errors.New("butler: special order materials")

// ErrOrderDaily is a new order when the account's special orders of the game day are used up (per_day).
var ErrOrderDaily = errors.New("butler: special order daily limit")

// The client's reward types for a currency (butler-robo-spec 11.6): 105 Cash, 106 Gems.
const (
	specialOrderCash int64 = 105
	specialOrderGems int64 = 106
)

// SpecialOrder is data/butlers.json special_order (Cherry-made, butler-robo-spec 11.9).
//
//	materials  the bag materials an order may ask for, each once. item is a gathering drop (data/gathering.json). grade is
//	           its star grade, 1 to 3: Cherry's inference from the item id (the second digit of 11xxxx, 21xxxx and 31xxxx is
//	           1, of 12xxxx, 22xxxx and 32xxxx 2, of 13xxxx, 23xxxx and 33xxxx 3). The wire carries grade minus 1 (the
//	           client's requireItemGrade is 0-based and shown as G+1 stars)
//	count      materials per order (M), at least 1
//	reward     the one reward: type 105 (Cash) or 106 (Gems), and its amount, at least 1
//	per_day    orders a game day per account, at least 1 (IssueSpecialOrder)
//	thanks     the reward popup's line, not empty
type SpecialOrder struct {
	Materials []SpecialOrderMaterial `json:"materials"`
	Count     int64                  `json:"count"`
	Reward    SpecialOrderReward     `json:"reward"`
	PerDay    int64                  `json:"per_day"`
	Thanks    string                 `json:"thanks"`
}

// SpecialOrderMaterial is one material an order may ask for and its star grade.
type SpecialOrderMaterial struct {
	Item  int64 `json:"item"`
	Grade int64 `json:"grade"`
}

// SpecialOrderReward is the one reward of an order: the client's reward type and the amount of that currency.
type SpecialOrderReward struct {
	Type   int64 `json:"type"`
	Amount int64 `json:"amount"`
}

// checkSpecialOrder checks the special_order block at load: the materials are gathering drops, each once, with a star grade
// of 1 to 3; the count, per_day and amount are at least 1; the reward is Cash or Gems; the reward line is not empty.
func checkSpecialOrder(s SpecialOrder) error {
	switch {
	case len(s.Materials) == 0:
		return errors.New("special_order: materials must list at least one material")
	case s.Count < 1:
		return errors.New("special_order: count must be at least 1")
	case s.PerDay < 1:
		return errors.New("special_order: per_day must be at least 1")
	case s.Reward.Type != specialOrderCash && s.Reward.Type != specialOrderGems:
		return fmt.Errorf("special_order: reward type %d must be 105 (Cash) or 106 (Gems)", s.Reward.Type)
	case s.Reward.Amount < 1 || s.Reward.Amount > store.MaxBalance:
		return fmt.Errorf("special_order: reward amount must be 1 to %d", store.MaxBalance)
	case strings.TrimSpace(s.Thanks) == "":
		return errors.New("special_order: thanks must not be empty: it is the reward popup's line")
	}
	seen := map[int64]bool{}
	for _, m := range s.Materials {
		switch {
		case seen[m.Item]:
			return fmt.Errorf("special_order: material %d is listed twice", m.Item)
		case m.Grade < 1 || m.Grade > 3:
			return fmt.Errorf("special_order: material %d: grade must be 1 to 3", m.Item)
		}
		if _, ok := gatherDropByID(m.Item); !ok {
			return fmt.Errorf("special_order: material %d is not a gathering drop", m.Item)
		}
		seen[m.Item] = true
	}
	return nil
}

// specialOrderGrades returns the distinct star grades of the materials, ascending.
func specialOrderGrades(s SpecialOrder) []int64 {
	var out []int64
	for _, m := range s.Materials {
		if !slices.Contains(out, m.Grade) {
			out = append(out, m.Grade)
		}
	}
	slices.Sort(out)
	return out
}

// specialOrderGrade is aid's star grade on game day day: the grades of the materials in turn, starting from a point that
// depends on the account (an FNV hash of its aid), so the accounts do not all ask for one grade on one day.
func specialOrderGrade(aid string, day int64, grades []int64) int64 {
	h := fnv.New32a()
	_, _ = h.Write([]byte(aid))
	return grades[(uint64(day)+uint64(h.Sum32()))%uint64(len(grades))]
}

// specialOrderID is the order id of butler id on game day day: the day's key times 1000 plus the butler's place in the
// Cherry block. It is stable for the day and unique per butler.
func specialOrderID(day, id int64) int64 { return day*1000 + id - CherryButlerFirst }

// specialOrderDay is the game day key of now and the end of that day in epoch milliseconds (the next 06:00 JST).
func specialOrderDay(now time.Time) (day, end int64) {
	day = AttendDayKey(now)
	return day, ((day+1)*86400 - attendBoundaryShift) * 1000
}

// liveOrders returns a copy of orders without those that ended by now.
func liveOrders(orders []store.ButlerOrder, now time.Time) []store.ButlerOrder {
	return slices.DeleteFunc(slices.Clone(orders), func(o store.ButlerOrder) bool { return o.End <= now.UnixMilli() })
}

// ordersUsed is how many of the account's special orders game day day has used: the delivered ones and the open ones
// given that day. An open order holds its place until it is delivered (then it counts as delivered) or ends.
func ordersUsed(book *store.ButlerBook, day int64) int64 {
	var n int64
	if book.OrderDay == day {
		n = book.OrderDone
	}
	for _, o := range book.Orders {
		if o.Day == day {
			n++
		}
	}
	return n
}

// specialOrderMaterial reports whether item is a material of star grade grade in so.
func specialOrderMaterial(so SpecialOrder, item, grade int64) bool {
	return slices.ContainsFunc(so.Materials, func(m SpecialOrderMaterial) bool { return m.Item == item && m.Grade == grade })
}

// bagCount is how many of item bag holds.
func bagCount(bag []store.GardenBagStack, item int64) int64 {
	if i := slices.IndexFunc(bag, func(s store.GardenBagStack) bool { return s.Item == item }); i >= 0 {
		return bag[i].Count
	}
	return 0
}

// IssueSpecialOrder gives aid the special order of butler id (ch 64). An open order of that butler is returned as it is.
// Otherwise a new one is given: the grade from specialOrderGrade, count materials, running to the end of the game day. The
// butler must be under contract (ErrButlerRefused). A new order needs one of the day's per_day places, and the delivered
// and the open orders of the day both take one (ErrOrderDaily). Nothing is spent. The order is saved before it returns.
func IssueSpecialOrder(aid string, id int64, now time.Time) (store.ButlerOrder, error) {
	store.AccountsMu.Lock()
	defer store.AccountsMu.Unlock()
	acc := store.AccountByAidLocked(aid)
	if _, ok := ButlerByID(id); !ok || acc == nil {
		return store.ButlerOrder{}, ErrButlerRefused
	}
	book := butlerBookOf(acc)
	if _, ok := butlerHireOf(&book, id, now); !ok {
		return store.ButlerOrder{}, ErrButlerRefused
	}
	for _, o := range book.Orders {
		if o.Butler == id && o.End > now.UnixMilli() {
			return o, nil
		}
	}
	so := ActiveButlers().SpecialOrder
	day, end := specialOrderDay(now)
	if ordersUsed(&book, day) >= so.PerDay {
		return store.ButlerOrder{}, ErrOrderDaily
	}
	o := store.ButlerOrder{ID: specialOrderID(day, id), Butler: id, Grade: specialOrderGrade(aid, day, specialOrderGrades(so)), Count: so.Count, Day: day, End: end}
	next := book
	next.Orders = append(liveOrders(book.Orders, now), o)
	if book.OrderDay != day {
		next.OrderDay, next.OrderDone = day, 0
	}
	prev := *acc
	acc.Butlers = &next
	if err := store.SaveLocked(nil, acc); err != nil {
		*acc = prev
		return store.ButlerOrder{}, err
	}
	return o, nil
}

// SpecialOrderBag returns aid's Bag stacks of the materials of star grade grade (ch 66): the picker's list. Other stacks
// (other grades, Atelier goods) are left out.
func SpecialOrderBag(aid string, grade int64) []store.GardenBagStack {
	store.AccountsMu.Lock()
	defer store.AccountsMu.Unlock()
	acc := store.AccountByAidLocked(aid)
	if acc == nil {
		return nil
	}
	so := ActiveButlers().SpecialOrder
	var out []store.GardenBagStack
	for _, s := range acc.GardenBag {
		if specialOrderMaterial(so, s.Item, grade) {
			out = append(out, s)
		}
	}
	return out
}

// SpecialOrderPaid is a delivered order's pay-out: the butler it was for, the reward and the reward popup's line.
type SpecialOrderPaid struct {
	Butler ButlerDef
	Reward SpecialOrderReward
	Thanks string
}

// checkOrderItems checks a delivery against order o: each stack is a material of o's grade, listed once, with a count of at
// least 1 that bag owns, and the counts total o's count. The counts are the stacks' Count fields.
func checkOrderItems(so SpecialOrder, o store.ButlerOrder, items, bag []store.GardenBagStack) error {
	var total int64
	for i, it := range items {
		switch {
		case !specialOrderMaterial(so, it.Item, o.Grade), it.Count < 1, bagCount(bag, it.Item) < it.Count:
			return ErrOrderItems
		case slices.ContainsFunc(items[:i], func(x store.GardenBagStack) bool { return x.Item == it.Item }):
			return ErrOrderItems
		}
		total += it.Count
	}
	if total != o.Count {
		return ErrOrderItems
	}
	return nil
}

// DeliverSpecialOrder hands items (the Bag stacks the player picked: item and count) to butler id's open order orderID
// (ch 65). The order must run now and its butler's contract must run too (ErrOrderEnded). The items must make the order
// (checkOrderItems: ErrOrderItems). Then, in one locked save, the materials leave the Bag, the reward is paid through the
// ledger (reason butler:order:<butler>:<order id>) and the order is delivered, which counts toward per_day. Any failure
// changes nothing.
func DeliverSpecialOrder(aid string, id, orderID int64, items []store.GardenBagStack, now time.Time) (SpecialOrderPaid, error) {
	store.AccountsMu.Lock()
	defer store.AccountsMu.Unlock()
	acc := store.AccountByAidLocked(aid)
	d, ok := ButlerByID(id)
	if acc == nil || !ok {
		return SpecialOrderPaid{}, ErrOrderEnded
	}
	book := butlerBookOf(acc)
	i := slices.IndexFunc(book.Orders, func(o store.ButlerOrder) bool {
		return o.ID == orderID && o.Butler == id && o.End > now.UnixMilli()
	})
	if _, under := butlerHireOf(&book, id, now); i < 0 || !under {
		return SpecialOrderPaid{}, ErrOrderEnded
	}
	o := book.Orders[i]
	so := ActiveButlers().SpecialOrder
	if err := checkOrderItems(so, o, items, acc.GardenBag); err != nil {
		return SpecialOrderPaid{}, err
	}
	bag := acc.GardenBag
	for _, it := range items {
		bag = gardenBagChange(bag, it.Item, -it.Count, now)
	}
	var deltas store.LedgerDeltas
	switch so.Reward.Type {
	case specialOrderCash:
		deltas.Cash = so.Reward.Amount
	case specialOrderGems:
		deltas.Gems = so.Reward.Amount
	}
	next := book
	next.Orders = liveOrders(slices.DeleteFunc(slices.Clone(book.Orders), func(x store.ButlerOrder) bool { return x == o }), now)
	if book.OrderDay != o.Day {
		next.OrderDay, next.OrderDone = o.Day, 0
	}
	next.OrderDone++
	prev := *acc
	lines, err := store.SpendLocked(acc, deltas, fmt.Sprintf("butler:order:%d:%d", id, orderID))
	if err != nil {
		*acc = prev
		return SpecialOrderPaid{}, err
	}
	acc.GardenBag = bag
	acc.Butlers = &next
	if err := store.SaveLocked(lines, acc); err != nil {
		*acc = prev
		return SpecialOrderPaid{}, err
	}
	return SpecialOrderPaid{Butler: d, Reward: so.Reward, Thanks: so.Thanks}, nil
}

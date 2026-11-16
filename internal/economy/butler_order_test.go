package economy

import (
	"errors"
	"reflect"
	"slices"
	"strconv"
	"testing"
	"time"

	"cherry/internal/store"
)

// orderNow is a mid-day game time: 12:00 JST on 17 November 2026. The game day ends at 06:00 JST on the 18th.
var orderNow = time.Date(2026, 11, 17, 3, 0, 0, 0, time.UTC)

// validSpecialOrder is a minimal special_order block: the config checks start from it and the tests' butler configs use it.
func validSpecialOrder() SpecialOrder {
	return SpecialOrder{Materials: []SpecialOrderMaterial{{Item: 110000, Grade: 1}}, Count: 3,
		Reward: SpecialOrderReward{Type: 106, Amount: 15}, PerDay: 1, Thanks: "Thanks."}
}

// orderAccount installs aid with Ophelia and Ian under contract for 30 days from now, and an empty Bag.
func orderAccount(t *testing.T, aid string, now time.Time) {
	t.Helper()
	butlerAccount(t, aid, 0, 0)
	store.AccountsMu.Lock()
	defer store.AccountsMu.Unlock()
	store.AccountByAidLocked(aid).Butlers = &store.ButlerBook{Leader: ophelia, Hired: []store.ButlerHire{
		{ID: ophelia, Begin: now.UnixMilli(), End: now.UnixMilli() + 30*dayMillis},
		{ID: ian, Begin: now.UnixMilli(), End: now.UnixMilli() + 30*dayMillis},
	}}
}

// setBag replaces aid's Bag with stacks.
func setBag(aid string, stacks ...store.GardenBagStack) {
	store.AccountsMu.Lock()
	defer store.AccountsMu.Unlock()
	store.AccountByAidLocked(aid).GardenBag = stacks
}

// materialsOf returns the data's materials of star grade grade, in the data's order.
func materialsOf(grade int64) []int64 {
	var out []int64
	for _, m := range ActiveButlers().SpecialOrder.Materials {
		if m.Grade == grade {
			out = append(out, m.Item)
		}
	}
	return out
}

// orderSnapshot is what a refused request must leave alone: the Bag, the balances, the open orders, the day's delivered count
// and the ledger rows.
type orderSnapshot struct {
	bag        []store.GardenBagStack
	cash, gems int64
	orders     []store.ButlerOrder
	done       int64
	ledger     int
}

func snapshotOrders(t *testing.T, aid string) orderSnapshot {
	t.Helper()
	cash, gems, book, bag := butlerState(t, aid)
	return orderSnapshot{bag: bag, cash: cash, gems: gems, orders: book.Orders, done: book.OrderDone, ledger: len(ledgerLines(t, ""))}
}

// TestSpecialOrderIssueOncePerButlerAndDay: a butler under contract gives one order per game day; asking again returns the
// same order; a second butler is refused by the day's limit (per_day 1, the open order holds the place); the next day gives a
// new order with a new id, and a butler without a contract is refused.
func TestSpecialOrderIssueOncePerButlerAndDay(t *testing.T) {
	orderAccount(t, "3501", orderNow)
	o, err := IssueSpecialOrder("3501", ophelia, orderNow)
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	day := AttendDayKey(orderNow)
	end := time.Date(2026, 11, 17, 21, 0, 0, 0, time.UTC).UnixMilli() // 06:00 JST on the 18th
	if o.Butler != ophelia || o.Count != 3 || o.ID != day*1000+4 || o.End != end || o.Day != day || o.Grade < 1 || o.Grade > 3 {
		t.Fatalf("order = %+v, want Ophelia, 3 materials, id %d, ends %d on day %d", o, day*1000+4, end, day)
	}
	again, err := IssueSpecialOrder("3501", ophelia, orderNow.Add(2*time.Hour))
	if err != nil || again != o {
		t.Fatalf("asking again = %+v (err %v), want the open order %+v", again, err, o)
	}
	if _, err := IssueSpecialOrder("3501", ian, orderNow); !errors.Is(err, ErrOrderDaily) {
		t.Fatalf("a second butler's order: err %v, want the daily limit", err)
	}
	if s := snapshotOrders(t, "3501"); len(s.orders) != 1 || s.ledger != 0 {
		t.Fatalf("after the re-open and the refusal: %d open orders, %d ledger rows; want 1 and none", len(s.orders), s.ledger)
	}

	tomorrow := orderNow.Add(24 * time.Hour)
	next, err := IssueSpecialOrder("3501", ophelia, tomorrow)
	if err != nil || next.ID == o.ID || next.Day != day+1 {
		t.Fatalf("next day = %+v (err %v), want a new order of day %d", next, err, day+1)
	}
	if s := snapshotOrders(t, "3501"); len(s.orders) != 1 || s.orders[0] != next {
		t.Fatalf("the ended order stayed: %+v", s.orders)
	}
	if _, err := IssueSpecialOrder("3501", merry, tomorrow); !errors.Is(err, ErrButlerRefused) {
		t.Fatalf("a butler without a contract: err %v, want a refusal", err)
	}
}

// TestSpecialOrderDeliveryPaysOnceAndSpendsTheMaterials: the materials leave the Bag (a stack that reaches 0 is removed, the
// rest stays), the reward is paid once in Gems through the ledger under butler:order:<butler>:<order id>, the order counts
// as delivered, and a repeat delivery or a second order that day is refused with nothing changed.
func TestSpecialOrderDeliveryPaysOnceAndSpendsTheMaterials(t *testing.T) {
	orderAccount(t, "3502", orderNow)
	o, err := IssueSpecialOrder("3502", ophelia, orderNow)
	if err != nil {
		t.Fatal(err)
	}
	mats := materialsOf(o.Grade)
	setBag("3502", store.GardenBagStack{Item: mats[0], Count: 2}, store.GardenBagStack{Item: mats[1], Count: 1}, store.GardenBagStack{Item: 999999, Count: 4})
	items := []store.GardenBagStack{{Item: mats[0], Count: 2}, {Item: mats[1], Count: 1}}

	later := orderNow.Add(time.Hour)
	paid, err := DeliverSpecialOrder("3502", ophelia, o.ID, items, later)
	if err != nil {
		t.Fatalf("deliver: %v", err)
	}
	if paid.Reward != (SpecialOrderReward{Type: 106, Amount: 15}) || paid.Butler.ID != ophelia || paid.Thanks == "" {
		t.Fatalf("paid = %+v, want 15 Gems for Ophelia with the line", paid)
	}
	s := snapshotOrders(t, "3502")
	if s.gems != 15 || s.cash != 0 || len(s.orders) != 0 || s.done != 1 {
		t.Fatalf("after the delivery: gems %d cash %d open %d done %d; want 15, 0, 0, 1", s.gems, s.cash, len(s.orders), s.done)
	}
	if len(s.bag) != 1 || s.bag[0] != (store.GardenBagStack{Item: 999999, Count: 4}) {
		t.Fatalf("bag after the delivery = %+v, want only the untouched stack", s.bag)
	}
	reason := "butler:order:1900000004:" + strconv.FormatInt(o.ID, 10)
	if reasons := ledgerReasons(t, ""); len(reasons) != 1 || reasons[0] != reason {
		t.Fatalf("ledger reasons = %v, want one %s", reasons, reason)
	}

	if _, err := DeliverSpecialOrder("3502", ophelia, o.ID, items, later); !errors.Is(err, ErrOrderEnded) {
		t.Fatalf("repeat delivery: err %v, want ended", err)
	}
	if _, err := IssueSpecialOrder("3502", ophelia, later); !errors.Is(err, ErrOrderDaily) {
		t.Fatalf("a second order on the same day: err %v, want the daily limit", err)
	}
	if after := snapshotOrders(t, "3502"); !reflect.DeepEqual(after, s) {
		t.Fatalf("refused requests changed the state: %+v, want %+v", after, s)
	}
}

// TestSpecialOrderRefusalsChangeNothing: each wrong delivery is refused with its error and leaves the Bag, the balances, the
// open order and the ledger as they were: a wrong grade, a short count, a wrong total, an unowned count, a repeated item, a
// zero count, nothing picked, an unknown order, an expired order, an unknown butler and a butler whose contract ended.
func TestSpecialOrderRefusalsChangeNothing(t *testing.T) {
	orderAccount(t, "3503", orderNow)
	o, err := IssueSpecialOrder("3503", ophelia, orderNow)
	if err != nil {
		t.Fatal(err)
	}
	mats := materialsOf(o.Grade)
	other := materialsOf(o.Grade%3 + 1) // a material of another star grade
	setBag("3503", store.GardenBagStack{Item: mats[0], Count: 5}, store.GardenBagStack{Item: mats[1], Count: 1}, store.GardenBagStack{Item: other[0], Count: 3})
	before := snapshotOrders(t, "3503")

	cases := []struct {
		name          string
		butler, order int64
		at            time.Time
		items         []store.GardenBagStack
		want          error
	}{
		{"wrong grade", ophelia, o.ID, orderNow, []store.GardenBagStack{{Item: other[0], Count: 3}}, ErrOrderItems},
		{"short count", ophelia, o.ID, orderNow, []store.GardenBagStack{{Item: mats[0], Count: 2}}, ErrOrderItems},
		{"wrong total", ophelia, o.ID, orderNow, []store.GardenBagStack{{Item: mats[0], Count: 4}}, ErrOrderItems},
		{"unowned count", ophelia, o.ID, orderNow, []store.GardenBagStack{{Item: mats[1], Count: 3}}, ErrOrderItems},
		{"repeated item", ophelia, o.ID, orderNow, []store.GardenBagStack{{Item: mats[0], Count: 1}, {Item: mats[0], Count: 2}}, ErrOrderItems},
		{"zero count", ophelia, o.ID, orderNow, []store.GardenBagStack{{Item: mats[0], Count: 3}, {Item: mats[1], Count: 0}}, ErrOrderItems},
		{"nothing picked", ophelia, o.ID, orderNow, nil, ErrOrderItems},
		{"unknown order", ophelia, o.ID + 1, orderNow, []store.GardenBagStack{{Item: mats[0], Count: 3}}, ErrOrderEnded},
		{"expired order", ophelia, o.ID, orderNow.Add(19 * time.Hour), []store.GardenBagStack{{Item: mats[0], Count: 3}}, ErrOrderEnded},
		{"unknown butler", 1900000050, o.ID, orderNow, []store.GardenBagStack{{Item: mats[0], Count: 3}}, ErrOrderEnded},
	}
	for _, c := range cases {
		if _, err := DeliverSpecialOrder("3503", c.butler, c.order, c.items, c.at); !errors.Is(err, c.want) {
			t.Fatalf("%s: err %v, want %v", c.name, err, c.want)
		}
		if after := snapshotOrders(t, "3503"); !reflect.DeepEqual(after, before) {
			t.Fatalf("%s changed the state: %+v, want %+v", c.name, after, before)
		}
	}

	// The contract ends before the delivery (the order itself still runs).
	store.AccountsMu.Lock()
	acc := store.AccountByAidLocked("3503")
	book := *acc.Butlers
	book.Hired = []store.ButlerHire{{ID: ophelia, Begin: orderNow.UnixMilli(), End: orderNow.Add(time.Hour).UnixMilli()}}
	acc.Butlers = &book
	store.AccountsMu.Unlock()
	if _, err := DeliverSpecialOrder("3503", ophelia, o.ID, []store.GardenBagStack{{Item: mats[0], Count: 3}}, orderNow.Add(2*time.Hour)); !errors.Is(err, ErrOrderEnded) {
		t.Fatalf("ended contract: err %v, want ended", err)
	}
	if after := snapshotOrders(t, "3503"); !reflect.DeepEqual(after.bag, before.bag) || after.gems != 0 || after.ledger != 0 || len(after.orders) != 1 {
		t.Fatalf("ended contract changed the state: %+v", after)
	}
}

// TestSpecialOrderBagFiltersByGrade: the picker lists only the Bag's materials of the asked star grade.
func TestSpecialOrderBagFiltersByGrade(t *testing.T) {
	orderAccount(t, "3504", orderNow)
	g1, g2 := materialsOf(1)[0], materialsOf(2)[0]
	setBag("3504", store.GardenBagStack{Item: g1, Count: 2}, store.GardenBagStack{Item: g2, Count: 1}, store.GardenBagStack{Item: 999999, Count: 4})
	if got := SpecialOrderBag("3504", 2); len(got) != 1 || got[0].Item != g2 || got[0].Count != 1 {
		t.Fatalf("grade 2 list = %+v, want only item %d", got, g2)
	}
	if got := SpecialOrderBag("3504", 1); len(got) != 1 || got[0].Item != g1 || got[0].Count != 2 {
		t.Fatalf("grade 1 list = %+v, want only item %d", got, g1)
	}
	if got := SpecialOrderBag("3504", 3); len(got) != 0 {
		t.Fatalf("grade 3 list = %+v, want none", got)
	}
	if got := SpecialOrderBag("3599", 1); len(got) != 0 {
		t.Fatalf("an unknown account lists %+v", got)
	}
}

// TestSpecialOrderGradeRotatesPerAccountAndDay: the grades of the materials come round in turn over the game days, the
// starting grade is fixed for an account and day, and three days show all three grades.
func TestSpecialOrderGradeRotatesPerAccountAndDay(t *testing.T) {
	grades := specialOrderGrades(ActiveButlers().SpecialOrder)
	if !slices.Equal(grades, []int64{1, 2, 3}) {
		t.Fatalf("grades of the data = %v, want 1, 2, 3", grades)
	}
	seen := map[int64]bool{}
	for day := int64(20000); day < 20003; day++ {
		g := specialOrderGrade("3505", day, grades)
		if g != specialOrderGrade("3505", day, grades) {
			t.Fatalf("day %d: the grade is not fixed", day)
		}
		seen[g] = true
	}
	if len(seen) != 3 {
		t.Fatalf("three days gave grades %v, want all three", seen)
	}
}

// TestSpecialOrderDataIsChecked: the embedded block passes its checks, its grades follow the item ids' second digit, and an
// operator block that breaks a rule is refused at load.
func TestSpecialOrderDataIsChecked(t *testing.T) {
	if err := checkSpecialOrder(validSpecialOrder()); err != nil {
		t.Fatalf("valid block: %v", err)
	}
	if err := checkSpecialOrder(ActiveButlers().SpecialOrder); err != nil {
		t.Fatalf("embedded block: %v", err)
	}
	for _, m := range ActiveButlers().SpecialOrder.Materials {
		if second := (m.Item / 10000) % 10; m.Grade != second {
			t.Fatalf("material %d: grade %d, its second digit is %d", m.Item, m.Grade, second)
		}
	}
	bad := map[string]func(*SpecialOrder){
		"no materials":    func(s *SpecialOrder) { s.Materials = nil },
		"count 0":         func(s *SpecialOrder) { s.Count = 0 },
		"per_day 0":       func(s *SpecialOrder) { s.PerDay = 0 },
		"reward type 107": func(s *SpecialOrder) { s.Reward.Type = 107 },
		"reward amount 0": func(s *SpecialOrder) { s.Reward.Amount = 0 },
		"empty thanks":    func(s *SpecialOrder) { s.Thanks = " " },
		"not a drop":      func(s *SpecialOrder) { s.Materials[0].Item = 999999 },
		"grade 4":         func(s *SpecialOrder) { s.Materials[0].Grade = 4 },
		"listed twice":    func(s *SpecialOrder) { s.Materials = append(s.Materials, s.Materials[0]) },
	}
	for name, mutate := range bad {
		s := validSpecialOrder()
		mutate(&s)
		if err := checkSpecialOrder(s); err == nil {
			t.Fatalf("%s: accepted", name)
		}
	}
}

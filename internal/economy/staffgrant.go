package economy

import (
	"cmp"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"time"

	"cherry/internal/store"
)

// Staff grants (PLAN Roadmap 8, staff panel phase 5): one rule set for the loopback /admin/grant tool and the staff panel. A
// grant is a set of deltas: gems, cash, face tickets, Smiles, Boss Coins, ride points, race tickets back, extra hearts and Garden
// Bag materials. A take-away that would take a balance below zero refuses the whole grant (the panel), except that the loopback
// tool stops what it takes away at the balance, as it always has (clampLoopbackGrant).

// StaffGrant is one staff change. Every currency field is a delta (negative takes away). RaceTickets is tickets back for today
// (a delta on the tickets spent). ExtraHearts is a delta on the extra hearts to send (economy/hearts.go). Materials maps a Garden
// Bag item id to its count delta.
type StaffGrant struct {
	Gems, Cash, FaceTickets, Smiles, BossCoins, RidePoints, RaceTickets, ExtraHearts int64
	Materials                                                                        map[int64]int64
}

// GrantLocked applies g to acc at now and returns the ledger lines of its currency deltas, each with reason. On a refusal acc is
// left as it was. The caller holds store.AccountsMu and saves acc with the lines (store.SaveLocked, or SaveAuditedLocked for a
// staff change that must carry its audit row). A refusal is a store.ErrBadInput, except a currency that cannot move, which is the
// store's *store.BalanceError.
func GrantLocked(acc *store.Account, g StaffGrant, reason string, now time.Time) ([]store.LedgerLine, error) {
	prev := *acc
	lines, err := grantLocked(acc, g, reason, now)
	if err != nil {
		*acc = prev
	}
	return lines, err
}

func grantLocked(acc *store.Account, g StaffGrant, reason string, now time.Time) ([]store.LedgerLine, error) {
	if g.BossCoins != 0 {
		if acc.BossCoins+g.BossCoins < 0 {
			return nil, fmt.Errorf("%w: the Boss Coins cannot go below 0", store.ErrBadInput)
		}
		acc.BossCoins += g.BossCoins
	}
	if g.ExtraHearts != 0 {
		rec := heartRecord(acc, AttendDayKey(now))
		if rec.Extra+g.ExtraHearts < 0 {
			return nil, fmt.Errorf("%w: the extra hearts cannot go below 0", store.ErrBadInput)
		}
		rec.Extra += g.ExtraHearts
		acc.Hearts = &rec
	}
	if g.RidePoints != 0 {
		points := ridePointsLocked(acc) + g.RidePoints
		if points < 0 {
			return nil, fmt.Errorf("%w: the ride points cannot go below 0", store.ErrBadInput)
		}
		setRidePointsLocked(acc, points)
	}
	if g.RaceTickets != 0 {
		rec := raceDay(acc, AttendDayKey(now))
		used := rec.Used - g.RaceTickets
		if used < 0 {
			return nil, fmt.Errorf("%w: at most %d race tickets can be given back today", store.ErrBadInput, rec.Used)
		}
		if used > ActiveRacing().MaxTickets {
			return nil, fmt.Errorf("%w: today's race tickets are %d at most", store.ErrBadInput, ActiveRacing().MaxTickets)
		}
		rec.Used = used
		acc.Racing = &rec
	}
	for _, item := range slices.Sorted(maps.Keys(g.Materials)) {
		n := g.Materials[item]
		if _, ok := GatherMaterialByID(item); !ok {
			return nil, fmt.Errorf("%w: item %d is not a Garden material", store.ErrBadInput, item)
		}
		if n == 0 {
			continue
		}
		if bagHeld(acc.GardenBag, item)+n < 0 {
			return nil, fmt.Errorf("%w: the Garden Bag holds fewer of item %d", store.ErrBadInput, item)
		}
		acc.GardenBag = gardenBagChange(acc.GardenBag, item, n, now)
	}
	return store.SpendLocked(acc, store.LedgerDeltas{Gems: g.Gems, Cash: g.Cash, FaceTickets: g.FaceTickets, Smiles: g.Smiles}, reason)
}

// bagHeld is how many of item the Garden Bag holds.
func bagHeld(bag []store.GardenBagStack, item int64) int64 {
	for _, s := range bag {
		if s.Item == item {
			return s.Count
		}
	}
	return 0
}

// clampLoopbackGrant is the loopback tool's rule, kept as it was: Boss Coins, extra hearts, ride points, race tickets and Garden Bag
// counts that a grant takes away stop at what the account holds. Gems, cash, face tickets and Smiles are not clamped; a take-away
// of them below zero is refused by the store (BalanceError).
func clampLoopbackGrant(acc *store.Account, g StaffGrant, now time.Time) StaffGrant {
	g.BossCoins = max(acc.BossCoins+g.BossCoins, 0) - acc.BossCoins
	if g.ExtraHearts != 0 {
		cur := heartExtra(acc)
		g.ExtraHearts = max(cur+g.ExtraHearts, 0) - cur
	}
	if g.RidePoints != 0 {
		cur := ridePointsLocked(acc)
		g.RidePoints = max(cur+g.RidePoints, 0) - cur
	}
	if g.RaceTickets != 0 {
		rec := raceDay(acc, AttendDayKey(now))
		used := min(max(rec.Used-g.RaceTickets, 0), ActiveRacing().MaxTickets)
		g.RaceTickets = rec.Used - used
	}
	if len(g.Materials) > 0 {
		clamped := make(map[int64]int64, len(g.Materials))
		for item, n := range g.Materials {
			clamped[item] = max(n, -bagHeld(acc.GardenBag, item))
		}
		g.Materials = clamped
	}
	return g
}

// StaffGrantPlayer applies g to the player with row id for the staff panel. The amounts are whole numbers within ±store.MaxGrant
// and at least one is not zero; reason is 1 to 200 characters. The ledger lines carry "staff:<actor>:<reason>". The balances,
// the ledger lines and the audit row (grant, target player:<id>, the deltas and the reason) are one save. Nothing changes on error.
func StaffGrantPlayer(id int64, g StaffGrant, reason, actor string, now time.Time) error {
	reason, err := store.CleanStaffReason(reason)
	if err != nil {
		return err
	}
	if err := checkStaffAmounts(g); err != nil {
		return err
	}
	store.AccountsMu.Lock()
	defer store.AccountsMu.Unlock()
	acc := store.PlayerAccountLocked(id)
	if acc == nil {
		return store.ErrNoPlayer
	}
	prev := *acc
	lines, err := GrantLocked(acc, g, "staff:"+actor+":"+reason, now)
	if err != nil {
		var be *store.BalanceError
		if errors.As(err, &be) {
			return fmt.Errorf("%w: the balance cannot go below 0 or above %d", store.ErrBadInput, store.MaxBalance)
		}
		return err
	}
	if err := store.SaveAuditedLocked(actor, "grant", store.PlayerTarget(id), grantDetail(acc.Aid, g, reason, lines), lines, acc); err != nil {
		*acc = prev
		return err
	}
	return nil
}

// checkStaffAmounts: every amount a whole number within ±store.MaxGrant, and at least one not zero.
func checkStaffAmounts(g StaffGrant) error {
	amounts := []int64{g.Gems, g.Cash, g.FaceTickets, g.Smiles, g.BossCoins, g.RidePoints, g.RaceTickets, g.ExtraHearts}
	for _, n := range g.Materials {
		amounts = append(amounts, n)
	}
	nonzero := false
	for _, n := range amounts {
		if n > store.MaxGrant || n < -store.MaxGrant {
			return fmt.Errorf("%w: the amount is a whole number from -%d to %d", store.ErrBadInput, store.MaxGrant, store.MaxGrant)
		}
		nonzero = nonzero || n != 0
	}
	if !nonzero {
		return fmt.Errorf("%w: the amount is a whole number from -%d to %d, not zero", store.ErrBadInput, store.MaxGrant, store.MaxGrant)
	}
	return nil
}

// grantDetail is the audit row's detail of a grant: the avatar id, the reason and every nonzero change ("changes": a currency
// name, or "material:<item id>", to its delta). A single change also carries the older shape of the grant rows: currency, delta
// and, for a currency, the balance after it.
func grantDetail(aid string, g StaffGrant, reason string, lines []store.LedgerLine) map[string]any {
	changes := map[string]int64{}
	for name, n := range map[string]int64{"gems": g.Gems, "cash": g.Cash, "faceTickets": g.FaceTickets, "smiles": g.Smiles,
		"bossCoins": g.BossCoins, "ridePoints": g.RidePoints, "raceTickets": g.RaceTickets, "extraHearts": g.ExtraHearts} {
		if n != 0 {
			changes[name] = n
		}
	}
	for item, n := range g.Materials {
		if n != 0 {
			changes["material:"+strconv.FormatInt(item, 10)] = n
		}
	}
	d := map[string]any{"aid": aid, "reason": reason, "changes": changes}
	if len(changes) == 1 {
		for name, n := range changes {
			d["currency"], d["delta"] = name, n
		}
		if len(lines) == 1 {
			d["balance"] = lines[0].Balance
		}
	}
	return d
}

// GatherMaterials lists every Garden material a grant may name, by item id: the gathering drops and the Atelier goods, each with
// its name (the staff grant form's choices).
func GatherMaterials() []GatherDrop {
	seen := map[int64]bool{}
	var out []GatherDrop
	add := func(item int64, name string) {
		if !seen[item] {
			seen[item] = true
			out = append(out, GatherDrop{Item: item, Name: name})
		}
	}
	for _, kind := range slices.Sorted(maps.Keys(ActiveGathering().Drops)) {
		for _, d := range ActiveGathering().Drops[kind] {
			add(d.Item, d.Name)
		}
	}
	for _, g := range ActiveAtelier().Goods {
		add(g.ID, g.Name)
	}
	slices.SortFunc(out, func(a, b GatherDrop) int { return cmp.Compare(a.Item, b.Item) })
	return out
}

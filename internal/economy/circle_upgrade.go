package economy

import (
	"fmt"
	"time"

	"cherry/internal/store"
)

// Circle member-cap upgrades (LPN msgids 28-29 out, 40-41 in; spec circle-spec.md section 8). The leader buys the next
// step with Gems; the step raises the circle's own cap (store.Circle.MaxMembers), which every cap check reads through
// circleCapOf. A real-money id (Cpid) is refused, and so is any coin unit but GEM.

// CircleUpgradeRow is the upgrade the client shows: the product code, title and picture of the next step (empty once the
// cap is final), the current cap, the cap after the step, the final cap, the Gem price, and whether the caller may buy it
// now.
type CircleUpgradeRow struct {
	Code        string
	Name        string
	Image       string
	Current     int64
	To          int64
	Final       int64
	PriceGems   int64
	Purchasable bool
}

// circleNextUpgrade returns the first step above c's cap, and false when the cap is final.
func circleNextUpgrade(c *store.Circle) (CircleUpgrade, bool) {
	cap := int64(circleCapOf(c))
	for _, u := range ActiveCircleData().Upgrades {
		if u.To > cap {
			return u, true
		}
	}
	return CircleUpgrade{}, false
}

// circleFinalCap is the cap after the last upgrade step (the base cap when there are none).
func circleFinalCap() int64 {
	d := ActiveCircleData()
	if n := len(d.Upgrades); n > 0 {
		return d.Upgrades[n-1].To
	}
	return int64(d.Product.MaxMembers)
}

// CircleUpgradeInfo returns the upgrade row for aid's circle. A player in no circle is refused (the client shows its
// own error text for the refusal).
func CircleUpgradeInfo(aid string) (CircleUpgradeRow, error) {
	var row CircleUpgradeRow
	err := circleBookTx(false, func(b *store.CircleBook) error {
		ci := circleOf(b, aid)
		if ci < 0 {
			return circleRefuse(circleErrNotFound)
		}
		c := &b.Circles[ci]
		d := ActiveCircleData()
		row = CircleUpgradeRow{Current: int64(circleCapOf(c)), Final: circleFinalCap(), Image: circleArtPath(d.Backgrounds, d.Backgrounds[0].ID)}
		u, ok := circleNextUpgrade(c)
		if !ok {
			row.To = row.Current
			return nil
		}
		row.Code, row.Name, row.To, row.PriceGems = u.Code, u.Name, u.To, u.PriceGems
		row.Purchasable = roleOf(c, aid) == roleLeader
		return nil
	})
	return row, err
}

// BuyCircleUpgrade spends PriceGems Gems on the next cap step for the leader aid. The product code and the price must
// be the step's; a non-empty cpID, a coin unit other than GEM, a member who is not the leader, or a short balance is
// refused with nothing changed. It returns the Gem balance after the purchase.
func BuyCircleUpgrade(aid, code, coinUnit, cpID string, price int64, now time.Time) (int64, error) {
	var balance int64
	err := circleTxAid(aid, true, func(acc *store.Account, b *store.CircleBook) ([]store.LedgerLine, error) {
		if cpID != "" || coinUnit != "GEM" {
			return nil, circleRefuse(circleErrBadRequest)
		}
		ci := circleOf(b, aid)
		if ci < 0 {
			return nil, circleRefuse(circleErrNotFound)
		}
		c := &b.Circles[ci]
		if roleOf(c, aid) != roleLeader {
			return nil, circleRefuse(circleErrRights)
		}
		u, ok := circleNextUpgrade(c)
		if !ok {
			return nil, circleRefuse(circleErrGeneric)
		}
		if u.Code != code || u.PriceGems != price {
			return nil, circleRefuse(circleErrBadRequest)
		}
		lines, err := store.SpendLocked(acc, store.LedgerDeltas{Gems: -u.PriceGems},
			fmt.Sprintf("circle:upgrade:%d:%d", c.ID, u.To))
		if err != nil {
			return nil, circleRefuse(circleErrNoGems)
		}
		c.MaxMembers = int(u.To)
		c.Modified = now.UnixMilli()
		balance = acc.Gems
		return lines, nil
	})
	return balance, err
}

package economy

import (
	"slices"
	"time"

	"cherry/internal/store"
)

// Treasure World buffs (cell 3; cs 5 list, cs 22 wear): the buff pool in data/treasure.json buff is Cherry-made. An owned
// buff (in the account's inventory, so a Gacha pull grants it) worn on the board raises every Jewel credit: the worn percents
// add up, capped at buff.cap, and a credit of n Jewels becomes treasureBoost(n, percent). The worn list is kept on the board
// record and a new season clears it.

// TreasureBuffLanding is the buff a buff square shows: its item code, its BuffPercent and its state (NEW not owned yet, INVEN
// owned and not worn, WEAR worn).
type TreasureBuffLanding struct {
	Code    string
	Percent int64
	State   string
}

// TreasureBuffRow is one owned buff of the list: its code, its percent and whether it is worn.
type TreasureBuffRow struct {
	Code    string
	Percent int64
	Worn    bool
}

// TreasureBuffs is the buff list of an account: the buffs it owns (in pool order) and the worn total (capped).
type TreasureBuffs struct {
	Items   []TreasureBuffRow
	Percent int64
}

// treasureOwnedBuff reports whether acc owns the item code (a nil account owns nothing).
func treasureOwnedBuff(acc *store.Account, code string) bool {
	return acc != nil && slices.Contains(store.AccountOwnedCodes(acc), code)
}

// treasureInPool reports whether code is one of the buff pool's items.
func treasureInPool(d *TreasureData, code string) bool {
	return slices.ContainsFunc(d.Buff.Items, func(it TreasureBuffItem) bool { return it.Code == code })
}

// treasureBuffPercent is the worn buffs' total BuffPercent, capped at buff.cap.
func treasureBuffPercent(r store.TreasureRecord, d *TreasureData) int64 {
	var total int64
	for _, it := range d.Buff.Items {
		if slices.Contains(r.Worn, it.Code) {
			total += it.Percent
		}
	}
	return min(total, d.Buff.Cap)
}

// treasureBoost is n Jewels grown by percent, rounded up: 3 at 40 percent is 5.
func treasureBoost(n, percent int64) int64 { return (n*(100+percent) + 99) / 100 }

// treasureBuffLanding is the buff a buff square shows for acc: a random pool item acc does not own yet (the first item when it
// owns them all), with its state.
func treasureBuffLanding(acc *store.Account, r store.TreasureRecord, d *TreasureData) TreasureBuffLanding {
	var unowned []TreasureBuffItem
	for _, it := range d.Buff.Items {
		if !treasureOwnedBuff(acc, it.Code) {
			unowned = append(unowned, it)
		}
	}
	pick := d.Buff.Items[0]
	if len(unowned) > 0 {
		pick = unowned[TreasureRandIntn(len(unowned))]
	}
	state := "NEW"
	if treasureOwnedBuff(acc, pick.Code) {
		state = "INVEN"
		if slices.Contains(r.Worn, pick.Code) {
			state = "WEAR"
		}
	}
	return TreasureBuffLanding{Code: pick.Code, Percent: pick.Percent, State: state}
}

// treasureBuffsOf lists the buffs acc owns with their worn state, and the worn total.
func treasureBuffsOf(acc *store.Account, r store.TreasureRecord, d *TreasureData) TreasureBuffs {
	out := TreasureBuffs{Percent: treasureBuffPercent(r, d)}
	for _, it := range d.Buff.Items {
		if treasureOwnedBuff(acc, it.Code) {
			out.Items = append(out.Items, TreasureBuffRow{Code: it.Code, Percent: it.Percent, Worn: slices.Contains(r.Worn, it.Code)})
		}
	}
	return out
}

// TreasureBuffList is aid's buff list (cs 5).
func TreasureBuffList(aid string, now time.Time) (TreasureBuffs, error) {
	var out TreasureBuffs
	_, err := treasureTx(aid, now, true, func(acc *store.Account, r *store.TreasureRecord, d *TreasureData) ([]store.LedgerLine, error) {
		out = treasureBuffsOf(acc, *r, d)
		return nil, nil
	})
	return out, err
}

// TreasureBuffWear sets the worn buffs of aid from codes (cs 22). The client sends the whole outfit it will wear (device
// 2026-11-02: 13 codes for one equip), so only the pool items in it count; the rest is ignored. A pool item aid does not own
// is refused and changes nothing. An outfit without pool items takes every buff off. It returns the list after the change.
func TreasureBuffWear(aid string, codes []string, now time.Time) (TreasureBuffs, error) {
	var out TreasureBuffs
	_, err := treasureTx(aid, now, true, func(acc *store.Account, r *store.TreasureRecord, d *TreasureData) ([]store.LedgerLine, error) {
		var worn []string
		for _, code := range codes {
			if !treasureInPool(d, code) || slices.Contains(worn, code) {
				continue
			}
			if !treasureOwnedBuff(acc, code) {
				return nil, ErrTreasureRefused
			}
			worn = append(worn, code)
		}
		r.Worn = worn
		out = treasureBuffsOf(acc, *r, d)
		return nil, nil
	})
	return out, err
}

package economy

import (
	"slices"
	"time"

	"cherry/internal/itemcode"
	"cherry/internal/store"
)

// Treasure World buffs (cell 3; cs 5 list, cs 22 wear): the buff pool in data/treasure.json buff is Cherry-made. An owned
// buff worn on the board raises every Jewel credit: the worn percents add up, capped at buff.cap, and a credit of n Jewels
// becomes treasureBoost(n, percent). A buff is worn when it is one of the account's equipped codes (its saved outfit, owned):
// cs 22 saves the outfit as a Closet save does, so wearing it in the Closet starts the buff and taking it off ends it. The
// board keeps no worn list (TreasureRecord.Worn is a legacy field, read by no rule).

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

// TreasureBuffs is the buff list of an account: the buffs it owns (in pool order) and the worn total (capped). After a wear
// (cs 22) Outfit is the number of codes the outfit saved; a list leaves it 0.
type TreasureBuffs struct {
	Items   []TreasureBuffRow
	Percent int64
	Outfit  int
}

// treasureOwnedBuff reports whether acc owns the item code (a nil account owns nothing).
func treasureOwnedBuff(acc *store.Account, code string) bool {
	return acc != nil && slices.Contains(store.AccountOwnedCodes(acc), code)
}

// treasureWears reports whether acc wears the buff item code: it is in the account's equipped codes and it owns it.
func treasureWears(acc *store.Account, code string) bool {
	return acc != nil && slices.Contains(acc.ItemCodes, code) && treasureOwnedBuff(acc, code)
}

// treasureBuffPercent is the BuffPercent of the buffs acc wears, capped at buff.cap.
func treasureBuffPercent(acc *store.Account, d *TreasureData) int64 {
	var total int64
	for _, it := range d.Buff.Items {
		if treasureWears(acc, it.Code) {
			total += it.Percent
		}
	}
	return min(total, d.Buff.Cap)
}

// treasureBoost is n Jewels grown by percent, rounded up: 3 at 40 percent is 5.
func treasureBoost(n, percent int64) int64 { return (n*(100+percent) + 99) / 100 }

// treasureBuffLanding is the buff a buff square shows for acc: a random pool item acc does not own yet (the first item when it
// owns them all), with its state.
func treasureBuffLanding(acc *store.Account, d *TreasureData) TreasureBuffLanding {
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
		if treasureWears(acc, pick.Code) {
			state = "WEAR"
		}
	}
	return TreasureBuffLanding{Code: pick.Code, Percent: pick.Percent, State: state}
}

// treasureBuffsOf lists the buffs acc owns with their worn state, and the worn total.
func treasureBuffsOf(acc *store.Account, d *TreasureData) TreasureBuffs {
	out := TreasureBuffs{Percent: treasureBuffPercent(acc, d)}
	for _, it := range d.Buff.Items {
		if treasureOwnedBuff(acc, it.Code) {
			out.Items = append(out.Items, TreasureBuffRow{Code: it.Code, Percent: it.Percent, Worn: treasureWears(acc, it.Code)})
		}
	}
	return out
}

// treasureOutfitSpecies reports whether the species of acc can wear every code of outfit that save/v2 keeps: an ANIMAL gets no
// human hair or face (itemcode.HumanOnly), a human no animal face, ear or tail part (itemcode.AnimalOnly). The Closet save has
// no such check; cs 22 applies it.
func treasureOutfitSpecies(acc *store.Account, outfit []string) bool {
	for _, code := range outfit {
		if store.AvatarOutfitSkips(acc.Gender, code) {
			continue
		}
		if acc.Gender == "ANIMAL" && itemcode.HumanOnly(code) || acc.Gender != "ANIMAL" && itemcode.AnimalOnly(code) {
			return false
		}
	}
	return true
}

// TreasureBuffList is aid's buff list (cs 5).
func TreasureBuffList(aid string, now time.Time) (TreasureBuffs, error) {
	var out TreasureBuffs
	_, err := treasureTx(aid, now, treasureGatePlay, func(acc *store.Account, _ *store.TreasureRecord, d *TreasureData) ([]store.LedgerLine, error) {
		out = treasureBuffsOf(acc, d)
		return nil, nil
	})
	return out, err
}

// TreasureBuffWear saves the outfit aid wears (cs 22). The client sends the whole outfit it will wear (device 2026-11-02: 13
// codes for one equip, skin and base items included), so it is saved as the Closet save saves it (store.SetAvatarOutfitLocked)
// and the buffs worn are the pool items of the saved outfit. An outfit that is empty, names an item aid does not own, or names
// an item the account's species cannot wear is refused and changes nothing. It returns the list after the change.
func TreasureBuffWear(aid string, outfit []string, now time.Time) (TreasureBuffs, error) {
	var out TreasureBuffs
	_, err := treasureTx(aid, now, treasureGatePlay, func(acc *store.Account, _ *store.TreasureRecord, d *TreasureData) ([]store.LedgerLine, error) {
		if acc == nil {
			return nil, ErrTreasureNoAccount
		}
		if len(outfit) == 0 || !treasureOutfitSpecies(acc, outfit) || store.SetAvatarOutfitLocked(acc, outfit) != nil {
			return nil, ErrTreasureRefused
		}
		out = treasureBuffsOf(acc, d)
		out.Outfit = len(acc.ItemCodes)
		return nil, nil
	})
	return out, err
}

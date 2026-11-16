package economy

import (
	"fmt"
	"slices"
	"time"

	"cherry/internal/store"
)

// Butler skins (Garden LPN agent 10, ch 59 equip, ch 60 list, ch 61 buy; replies hc 70 to 72; butler-robo-spec section 2).
// A butler's default skin is always owned. A further skin is bought for its earned-Cash price while the butler is under
// contract, and the butler wears one owned skin at a time. The wearing skin is what the Garden chief, the Book, the Skin
// Select and the Profile show (ButlerInfo f5, ButlerThumbnailInfo f4 and f5).

// ButlerSkinByID finds the roster butler and skin with wire skin id id.
func ButlerSkinByID(id int64) (ButlerDef, ButlerSkin, bool) {
	for _, d := range ActiveButlers().Butlers {
		if s, ok := d.skin(id); ok {
			return d, s, true
		}
	}
	return ButlerDef{}, ButlerSkin{}, false
}

// skin returns d's skin with wire id id.
func (d ButlerDef) skin(id int64) (ButlerSkin, bool) {
	for _, s := range d.Skins {
		if s.ID == id {
			return s, true
		}
	}
	return ButlerSkin{}, false
}

// ownedIn reports whether book owns skin of butler d: the default always, a further skin when it was bought.
func ownedIn(book *store.ButlerBook, d ButlerDef, skin int64) bool {
	if skin == d.Skins[0].ID {
		return true
	}
	return slices.ContainsFunc(book.Skins, func(o store.ButlerSkinOwned) bool { return o.Butler == d.ID && o.Skin == skin })
}

// butlerWornIn is the skin butler d wears in book: its equipped bought skin, else the default.
func butlerWornIn(book *store.ButlerBook, d ButlerDef) ButlerSkin {
	for _, o := range book.Skins {
		if o.Butler == d.ID && o.Equipped {
			if s, ok := d.skin(o.Skin); ok {
				return s
			}
		}
	}
	return d.Skins[0]
}

// Owns reports whether the view's account owns skin of butler d.
func (v ButlerView) Owns(d ButlerDef, skin int64) bool { return ownedIn(&v.book, d, skin) }

// Worn is the skin butler d wears in the view.
func (v ButlerView) Worn(d ButlerDef) ButlerSkin { return butlerWornIn(&v.book, d) }

// BuyButlerSkin buys skin of butler id for aid: its price in earned Cash through the ledger (reason
// butler:skin:<id>:<skin>), saved before it returns. Refused with nothing spent: an unknown butler or skin, the default
// (owned already), a skin already bought, a butler not under contract now (the Skin Shop sells to hired butlers only),
// and a short Cash balance. The skin is not worn until EquipButlerSkin.
func BuyButlerSkin(aid string, id, skin int64, now time.Time) error {
	store.AccountsMu.Lock()
	defer store.AccountsMu.Unlock()
	acc := store.AccountByAidLocked(aid)
	d, ok := ButlerByID(id)
	if acc == nil || !ok {
		return ErrButlerRefused
	}
	s, ok := d.skin(skin)
	if !ok || s.ID == d.Skins[0].ID {
		return ErrButlerRefused
	}
	book := butlerBookOf(acc)
	if _, ok := butlerHireOf(&book, id, now); !ok || ownedIn(&book, d, skin) {
		return ErrButlerRefused
	}
	next := book
	next.Skins = append(slices.Clone(book.Skins), store.ButlerSkinOwned{Butler: id, Skin: skin})
	prev := *acc
	lines, err := store.SpendLocked(acc, store.LedgerDeltas{Cash: -s.Price}, fmt.Sprintf("butler:skin:%d:%d", id, skin))
	if err != nil {
		*acc = prev
		return ErrButlerRefused
	}
	acc.Butlers = &next
	if err := store.SaveLocked(lines, acc); err != nil {
		*acc = prev
		return err
	}
	return nil
}

// EquipButlerSkin makes skin the one butler id wears for aid. The skin must be owned (the default always is) and the
// butler under contract now. Nothing changes when the save fails.
func EquipButlerSkin(aid string, id, skin int64, now time.Time) error {
	store.AccountsMu.Lock()
	defer store.AccountsMu.Unlock()
	acc := store.AccountByAidLocked(aid)
	d, ok := ButlerByID(id)
	if acc == nil || !ok {
		return ErrButlerRefused
	}
	if _, ok := d.skin(skin); !ok {
		return ErrButlerRefused
	}
	book := butlerBookOf(acc)
	if _, ok := butlerHireOf(&book, id, now); !ok || !ownedIn(&book, d, skin) {
		return ErrButlerRefused
	}
	next := book
	next.Skins = slices.Clone(book.Skins)
	for i := range next.Skins {
		if next.Skins[i].Butler == id {
			next.Skins[i].Equipped = next.Skins[i].Skin == skin
		}
	}
	prev := *acc
	acc.Butlers = &next
	if err := store.SaveLocked(nil, acc); err != nil {
		*acc = prev
		return err
	}
	return nil
}

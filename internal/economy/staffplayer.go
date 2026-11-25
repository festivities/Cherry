package economy

import (
	"fmt"
	"time"

	"cherry/internal/store"
)

// The staff player page's Garden and economy part (PLAN Roadmap 8, staff panel phase 5): what the store's balances do not
// show. It copies what the page lists and changes nothing.

// staffCodesNewest is how many owned item codes the player page lists, newest first.
const staffCodesNewest = 50

// PlayerBook is an account's Garden and economy state as the staff player page shows it.
type PlayerBook struct {
	RaceTickets, RaceMax int64    // race tickets left today and the daily maximum
	RidePoints           int64    // Riding Pet Race store points
	Smiles, FaceTickets  int64    // Smiles (Garden gathering) and Face Shop tickets
	BossCoins            int64    // Square boss coins
	ExtraHearts          int64    // extra hearts to send (economy/hearts.go)
	Owned                int      // item codes the account owns
	NewestCodes          []string // at most staffCodesNewest of them, newest first
	RoomItems            int      // room items placed or stored
	Pets                 []PetRow // pets, in the order the account has them
	Bag                  []BagRow // Garden Bag stacks, in the bag's order
	Butlers              []ButlerRow
	Chief                int64 // the chief butler's id (0 = none)
}

// PetRow is one pet of the account: its name and its code.
type PetRow struct{ Name, Code string }

// BagRow is one Garden Bag stack: the material's item id, its name and the count.
type BagRow struct {
	Item  int64
	Name  string
	Count int64
}

// ButlerRow is one hired butler: its name, the contract (epoch milliseconds, Ended once End has passed), the skin it wears
// ("default" when none), its open special order and its gathering, and the titles of the memories the roster lists for it (the
// memories are the roster's, not kept per player).
type ButlerRow struct {
	ID, Begin, End int64
	Name, Skin     string
	Ended          bool
	HasOrder       bool
	OrderGrade     int64
	OrderCount     int64
	OrderEnd       int64
	HasGathering   bool
	GatherEnd      int64
	Memories       []string
}

// PlayerBookOf returns the book of the account with row id at now, false for an unknown id.
func PlayerBookOf(id int64, now time.Time) (PlayerBook, bool) {
	store.AccountsMu.Lock()
	defer store.AccountsMu.Unlock()
	acc := store.PlayerAccountLocked(id)
	if acc == nil {
		return PlayerBook{}, false
	}
	return BookOfAccount(acc, now), true
}

// BookOfAccount is the book of acc at now. It reads acc only (a live account under AccountsMu, or a deleted avatar's record).
func BookOfAccount(acc *store.Account, now time.Time) PlayerBook {
	st := raceStatus(acc, now)
	book := PlayerBook{RaceTickets: st.Tickets, RaceMax: st.MaxTickets, RidePoints: ridePointsLocked(acc), RoomItems: len(acc.RoomItems),
		Smiles: acc.Smiles, FaceTickets: acc.FaceTickets, BossCoins: acc.BossCoins, ExtraHearts: heartExtra(acc)}
	codes := store.AccountOwnedCodes(acc)
	book.Owned = len(codes)
	for i := len(codes) - 1; i >= 0 && len(book.NewestCodes) < staffCodesNewest; i-- {
		book.NewestCodes = append(book.NewestCodes, codes[i])
	}
	for _, p := range acc.Pets {
		book.Pets = append(book.Pets, PetRow{Name: p.Name, Code: p.Cd})
	}
	for _, s := range acc.GardenBag {
		book.Bag = append(book.Bag, BagRow{Item: s.Item, Name: materialName(s.Item), Count: s.Count})
	}
	if b := acc.Butlers; b != nil {
		book.Chief = b.Leader
		for _, h := range b.Hired {
			book.Butlers = append(book.Butlers, butlerRow(b, h, now))
		}
	}
	return book
}

// materialName is the material's name, or "Item <id>" for an id the catalog does not name.
func materialName(item int64) string {
	if d, ok := GatherMaterialByID(item); ok {
		return d.Name
	}
	return fmt.Sprintf("Item %d", item)
}

// butlerRow is one hired butler of book b (its contract h) at now.
func butlerRow(b *store.ButlerBook, h store.ButlerHire, now time.Time) ButlerRow {
	row := ButlerRow{ID: h.ID, Begin: h.Begin, End: h.End, Ended: now.UnixMilli() >= h.End, Skin: "default", Name: fmt.Sprintf("Butler %d", h.ID)}
	if d, ok := ButlerByID(h.ID); ok {
		row.Name = d.Name
		for _, m := range d.Memories {
			row.Memories = append(row.Memories, m.Title)
		}
	}
	for _, sk := range b.Skins {
		if sk.Butler == h.ID && sk.Equipped {
			if _, skin, ok := ButlerSkinByID(sk.Skin); ok {
				row.Skin = skin.Name
			}
		}
	}
	for _, o := range b.Orders {
		if o.Butler == h.ID {
			row.HasOrder, row.OrderGrade, row.OrderCount, row.OrderEnd = true, o.Grade, o.Count, o.End
		}
	}
	for _, g := range b.Gathering {
		if g.ID == h.ID {
			row.HasGathering, row.GatherEnd = true, g.End
		}
	}
	return row
}

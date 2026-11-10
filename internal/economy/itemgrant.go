package economy

import (
	"bytes"
	"errors"
	"image"
	"image/png"
	"log"
	"net/http"
	"slices"
	"strconv"
	"sync"

	"cherry/internal/assets"
	"cherry/internal/itemcode"
	"cherry/internal/room"
	"cherry/internal/store"
	"cherry/internal/thumbs"
)

// Phase E: items handed out by the mailbox and the login calendar. An item is granted through the
// paths the shops use (closet inventory for clothing, room items for furniture, the pet list for
// pets). An item the account already owns pays its sell-back price in Gems instead (ItemPrice at
// 100%, as the closet and room recycle do), with its own ledger line.

var errItemUnknown = errors.New("economy: item is not sellable")

// itemOwnedLocked reports whether acc already has the item: clothing in its closet or worn, furniture
// as any room item with the code, pets as any pet with the code.
func itemOwnedLocked(acc *store.Account, code string) bool {
	_, kind := classify(code)
	switch kind {
	case kindFashion:
		return slices.Contains(store.AccountOwnedCodes(acc), code)
	case kindInterior:
		return slices.ContainsFunc(acc.RoomItems, func(it store.RoomItem) bool { return it.Cd == code })
	case kindPet:
		return slices.ContainsFunc(acc.Pets, func(p store.PetItem) bool { return p.Cd == code })
	case kindGarden:
		return slices.ContainsFunc(ownedLandscapesLocked(acc), func(l Landscape) bool { return l.Code == code })
	}
	return false
}

// sellBackGems is the Gem payout for a duplicate: the item's sell-back price (the recycle rule).
func sellBackGems(code string) int64 {
	return soldPrice(code, 0)
}

// itemAvailable reports whether the code is sellable and its item folder is in the archive with art
// (the catalog). A code the server cannot serve is never granted (mail refuses it, a season day pays
// its Gems only). Owned items without art still sell back through ItemPrice.
func itemAvailable(code string) bool {
	if _, _, _, ok := ItemPrice(code); !ok {
		return false
	}
	return loadCatalog().byCode[code] != nil
}

// artVerifiable reports whether the startup checks can ask about item art. Without the asset store (cloudflare.json
// absent) no item has art, so the checks that refuse a row without art are skipped there, logged once: Cherry still
// starts.
func artVerifiable() bool {
	if assets.Archive != nil {
		return true
	}
	artUnverifiedLogged.Do(func() { log.Printf("cherry: no asset store (cloudflare.json): item art is not checked at startup") })
	return false
}

var artUnverifiedLogged sync.Once

// seasonItemAvailable is itemAvailable for a calendar day, logging a missing code.
func seasonItemAvailable(code string) bool {
	if itemAvailable(code) {
		return true
	}
	log.Printf("cherry: season item missing code=%s", code)
	return false
}

// itemGrantLocked gives one item to acc. An item already owned grants nothing: its sell-back Gems
// (the table price, 100%) are returned for the caller to credit in its own save. An item an ANIMAL
// cannot wear (itemcode.HumanOnly) is paid the same way. It moves no currency itself, so callers can
// add the Gems to other credits in one ledger line.
func itemGrantLocked(acc *store.Account, code string) (sellBack int64, err error) {
	granted, err := grantNewItemLocked(acc, code, false)
	if err != nil || granted {
		return 0, err
	}
	return sellBackGems(code), nil
}

// giftPaid is the paid marker of an item a player sent through the Gift Shop (store.Account.PaidPrices,
// store.RoomItem.Paid): negative, so soldPrice pays nothing for it when it is recycled.
const giftPaid = -1

// grantNewItemLocked grants code unless acc already has it or an ANIMAL cannot wear it (itemcode.HumanOnly),
// and reports whether it granted. gift marks a granted item as a player's gift (giftPaid).
func grantNewItemLocked(acc *store.Account, code string, gift bool) (bool, error) {
	if !itemAvailable(code) {
		return false, errItemUnknown
	}
	_, kind := classify(code)
	switch kind {
	case kindInterior: // lab accounts get their showcase seeded first, so ownership is exact
		if err := room.EnsureRoomLocked(acc); err != nil {
			return false, err
		}
	case kindPet:
		if err := room.EnsurePetsLocked(acc); err != nil {
			return false, err
		}
	}
	if itemOwnedLocked(acc, code) || acc.Gender == "ANIMAL" && itemcode.HumanOnly(code) {
		return false, nil
	}
	switch kind {
	case kindFashion:
		acc.InventoryCodes = store.AppendUniqueItemCodes(store.AccountInventoryCodes(acc), []string{code})
		if gift {
			acc.PaidPrices = withPaid(acc.PaidPrices, map[string]int64{code: giftPaid})
		}
	case kindInterior:
		grantRoomItemsLocked(acc, []string{code})
		if gift { // the one item just granted is the last row; the slice is cloned, so the old one is untouched
			items := slices.Clone(acc.RoomItems)
			items[len(items)-1].Paid = giftPaid
			acc.RoomItems = items
		}
	default:
		acc.Pets = append(slices.Clone(acc.Pets), room.NewPetLocked(acc, code))
	}
	return true, nil
}

// grantItemLocked is the mailbox form of itemGrantLocked: a duplicate pays its sell-back Gems in
// their own ledger line. A gift (gift true: a player's Gift Shop mail) pays nothing: a duplicate grants
// nothing, and a new item is marked as a gift so it sells for nothing later. The caller saves and
// flushes the lines (nil for a grant).
func grantItemLocked(acc *store.Account, code, reason string, gift bool) ([]store.LedgerLine, error) {
	if gift {
		_, err := grantNewItemLocked(acc, code, true)
		return nil, err
	}
	sellBack, err := itemGrantLocked(acc, code)
	if err != nil || sellBack == 0 {
		return nil, err
	}
	return store.SpendLocked(acc, store.LedgerDeltas{Gems: sellBack}, reason+" sell-back "+code)
}

// itemTypeOf is the postbox itemType: ROOM selects the Room Deco message (furniture and pets),
// anything else the Closet message (clothing). See NaPostBoxPopup::ShowCheckPostPopup.
func itemTypeOf(code string) string {
	if _, kind := classify(code); kind == kindFashion {
		return "DRESS"
	}
	return "ROOM"
}

// itemThumbRef finds the item's dp.png route parts (kind dir and numeric id) in the item root.
func itemThumbRef(code string) (kind, id string, ok bool) {
	it := loadCatalog().byCode[code]
	if it == nil {
		return "", "", false
	}
	id = strconv.FormatInt(it.ID, 10)
	for _, k := range []string{"custom", "dress", "interior", "tile", "pet"} {
		if assets.Archive.Items().Has(k, it.ID) || assets.GeneratedItems().Has(k, it.ID) {
			return k, id, true
		}
	}
	return "", "", false
}

// itemImageURL is the picture a postbox item row points at: Cherry's dp.png route on the host the
// client called (the game hosts are sunk to Cherry). Empty when the item has no folder.
func itemImageURL(code, host string) string {
	kind, id, ok := itemThumbRef(code)
	if !ok || host == "" {
		return ""
	}
	return "https://" + host + "/arts_item_" + kind + "_" + id + "/dp.png"
}

// bufWriter collects one response body in memory (thumbs.ServeDpPNG writes to a ResponseWriter).
type bufWriter struct {
	h    http.Header
	code int
	buf  bytes.Buffer
}

func (b *bufWriter) Header() http.Header {
	if b.h == nil {
		b.h = http.Header{}
	}
	return b.h
}

func (b *bufWriter) WriteHeader(code int) { b.code = code }

func (b *bufWriter) Write(p []byte) (int, error) {
	if b.code == 0 {
		b.code = http.StatusOK
	}
	return b.buf.Write(p)
}

// itemDPImage decodes the item's dp.png (the generator's output) for drawing into a calendar tile.
func itemDPImage(code string) (image.Image, bool) {
	kind, id, ok := itemThumbRef(code)
	if !ok {
		return nil, false
	}
	var w bufWriter
	thumbs.ServeDpPNG(&w, kind, id)
	if w.code != 0 && w.code != http.StatusOK {
		return nil, false
	}
	img, err := png.Decode(&w.buf)
	return img, err == nil
}

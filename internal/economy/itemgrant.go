package economy

import (
	"bytes"
	"errors"
	"image"
	"image/png"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"

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
	}
	return false
}

// sellBackGems is the Gem payout for a duplicate: the item's table price (the recycle rule).
func sellBackGems(code string) int64 {
	price, _, _, _ := ItemPrice(code)
	return price
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

// seasonItemAvailable is itemAvailable for a calendar day, logging a missing code.
func seasonItemAvailable(code string) bool {
	if itemAvailable(code) {
		return true
	}
	log.Printf("cherry: season item missing code=%s", code)
	return false
}

// itemGrantLocked gives one item to acc. An item already owned grants nothing: its sell-back Gems
// (the table price, 100%) are returned for the caller to credit in its own save. It moves no
// currency itself, so callers can add the Gems to other credits in one ledger line.
func itemGrantLocked(acc *store.Account, code string) (sellBack int64, err error) {
	if !itemAvailable(code) {
		return 0, errItemUnknown
	}
	_, kind := classify(code)
	switch kind {
	case kindInterior: // lab accounts get their showcase seeded first, so ownership is exact
		if err := room.EnsureRoomLocked(acc); err != nil {
			return 0, err
		}
	case kindPet:
		if err := room.EnsurePetsLocked(acc); err != nil {
			return 0, err
		}
	}
	if itemOwnedLocked(acc, code) {
		return sellBackGems(code), nil
	}
	switch kind {
	case kindFashion:
		acc.InventoryCodes = store.AppendUniqueItemCodes(store.AccountInventoryCodes(acc), []string{code})
	case kindInterior:
		grantRoomItemsLocked(acc, []string{code})
	default:
		acc.Pets = append(slices.Clone(acc.Pets), room.NewPetLocked(acc, code))
	}
	return 0, nil
}

// grantItemLocked is the mailbox form of itemGrantLocked: a duplicate pays its sell-back Gems in
// their own ledger line. The caller saves and flushes the lines (nil for a grant).
func grantItemLocked(acc *store.Account, code, reason string) ([]store.LedgerLine, error) {
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
		if _, err := os.Stat(filepath.Join(thumbs.DpItemRoot, k, id)); err == nil {
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

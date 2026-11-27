package store

import (
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

// Custom Garden button pictures: PNGs the owner uploads on the staff panel (Garden buttons page). They are Cherry-made
// and kept apart from the game's originals: here in the meta table (the list in garden.icons, each normalised PNG in
// garden.icon.<id>), never in the asset bucket. economy/gardenicons.go delivers them to the clients and names them in
// home_icon. The caller normalises the PNG and derives the id from its bytes; each change writes its audit row in the
// same transaction.

const (
	gardenIconsKey      = "garden.icons"
	gardenIconKeyPrefix = "garden.icon."
	// GardenIconsMax is how many custom pictures the panel keeps (each change stays one small download for the clients).
	GardenIconsMax = 12
	// GardenIconNameMax is the length of a picture's staff name in characters.
	GardenIconNameMax = 40
)

// GardenIcon is one custom picture: its id (hex of the PNG's hash), the staff name, its size in pixels and bytes, when
// it was added (RFC 3339, UTC) and who added it.
type GardenIcon struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
	Bytes  int    `json:"bytes"`
	Added  string `json:"added"`
	By     string `json:"by"`
}

func gardenIconsOf(q Querier) ([]GardenIcon, error) {
	v, err := metaValue(q, gardenIconsKey)
	if err != nil || v == "" {
		return nil, err
	}
	var list []GardenIcon
	if err := json.Unmarshal([]byte(v), &list); err != nil {
		return nil, err
	}
	return list, nil
}

func writeGardenIconsTx(tx *sql.Tx, list []GardenIcon) error {
	if list == nil {
		list = []GardenIcon{}
	}
	b, err := json.Marshal(list)
	if err != nil {
		return err
	}
	return setMetaTx(tx, gardenIconsKey, string(b))
}

func validGardenIconID(id string) bool {
	return len(id) == 12 && strings.Trim(id, "0123456789abcdef") == ""
}

// GardenIconList is the custom pictures in upload order. ErrNoDatabase without a database.
func GardenIconList() ([]GardenIcon, error) {
	d, err := docDB()
	if err != nil {
		return nil, err
	}
	var list []GardenIcon
	err = d.View(func(q Querier) error {
		var err error
		list, err = gardenIconsOf(q)
		return err
	})
	return list, err
}

// GardenIconPNG is the stored PNG of picture id; ErrNoRecord when there is none.
func GardenIconPNG(id string) ([]byte, error) {
	if !validGardenIconID(id) {
		return nil, ErrNoRecord
	}
	d, err := docDB()
	if err != nil {
		return nil, err
	}
	var v string
	err = d.View(func(q Querier) error {
		var err error
		v, err = metaValue(q, gardenIconKeyPrefix+id)
		return err
	})
	if err != nil {
		return nil, err
	}
	if v == "" {
		return nil, ErrNoRecord
	}
	return base64.StdEncoding.DecodeString(v)
}

// AddGardenIcon stores png as picture ic (ID, Name, Width, Height set by the caller) with its time and actor, and writes
// the audit row in one transaction. At most GardenIconsMax; the same picture once.
func AddGardenIcon(ic GardenIcon, png []byte, actor string, now time.Time) error {
	ic.Name = strings.TrimSpace(ic.Name)
	switch {
	case !validGardenIconID(ic.ID) || len(png) == 0:
		return fmt.Errorf("%w: the picture could not be read", ErrBadInput)
	case ic.Name == "" || utf8.RuneCountInString(ic.Name) > GardenIconNameMax:
		return fmt.Errorf("%w: the name is 1 to %d characters", ErrBadInput, GardenIconNameMax)
	}
	d, err := docDB()
	if err != nil {
		return err
	}
	return d.Update(func(tx *sql.Tx) error {
		list, err := gardenIconsOf(tx)
		if err != nil {
			return err
		}
		if slices.ContainsFunc(list, func(e GardenIcon) bool { return e.ID == ic.ID }) {
			return fmt.Errorf("%w: this picture is already uploaded", ErrBadInput)
		}
		if len(list) >= GardenIconsMax {
			return fmt.Errorf("%w: at most %d custom pictures; remove one first", ErrBadInput, GardenIconsMax)
		}
		ic.Bytes, ic.Added, ic.By = len(png), now.UTC().Format(time.RFC3339), actor
		if err := setMetaTx(tx, gardenIconKeyPrefix+ic.ID, base64.StdEncoding.EncodeToString(png)); err != nil {
			return err
		}
		if err := writeGardenIconsTx(tx, append(list, ic)); err != nil {
			return err
		}
		return auditTx(tx, actor, "garden-icon-add", "garden", map[string]any{"id": ic.ID, "name": ic.Name, "bytes": ic.Bytes})
	})
}

// RemoveGardenIcon removes picture id and its PNG, and writes the audit row in one transaction. ref is the iconId a Garden
// button uses for it: a picture that a button still uses is refused, as is an unknown id.
func RemoveGardenIcon(id, ref, actor string) error {
	d, err := docDB()
	if err != nil {
		return err
	}
	return d.Update(func(tx *sql.Tx) error {
		list, err := gardenIconsOf(tx)
		if err != nil {
			return err
		}
		i := slices.IndexFunc(list, func(e GardenIcon) bool { return e.ID == id })
		if i < 0 {
			return fmt.Errorf("%w: no custom picture has that id", ErrBadInput)
		}
		buttons, err := gardenButtonsOf(tx)
		if err != nil {
			return err
		}
		if slices.ContainsFunc(buttons, func(b GardenButton) bool { return b.Icon == ref }) {
			return fmt.Errorf("%w: a Garden button uses this picture; remove the button first", ErrBadInput)
		}
		name := list[i].Name
		if err := writeGardenIconsTx(tx, slices.Delete(list, i, i+1)); err != nil {
			return err
		}
		if _, err := tx.Exec(`DELETE FROM meta WHERE key = ?`, gardenIconKeyPrefix+id); err != nil {
			return err
		}
		return auditTx(tx, actor, "garden-icon-remove", "garden", map[string]any{"id": id, "name": name})
	})
}

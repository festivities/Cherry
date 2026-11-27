package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

// Garden buttons: the event buttons the owner sets on the staff panel (Garden buttons page). Each is one row of
// home/integrated/info home_icon, which the client draws in the Garden's right-hand event column (economy/homeicon.go;
// spec garden-buttons-20261128). They live in the meta table as one JSON list, so they need no schema change; each change
// writes its audit row in the same transaction. The caller checks the icon and the link against the client's data.

const (
	gardenButtonsKey = "garden.buttons"
	// GardenButtonsMax is how many the panel keeps: NaGardenEventManager::ShowEventAniButton draws at most two home_icon rows.
	GardenButtonsMax = 2
	// GardenButtonLabelMax is the label's length in characters (staff-facing: the client parses iconName but never draws it).
	GardenButtonLabelMax = 40
	gardenButtonFieldMax = 512
)

// GardenButton is one Garden event button: the picture (a symbol of the client's icon_effect animation), the staff label,
// the client link type it opens and that link's detail (the https URL for openWithBrowser, a gacha id for the gacha rows),
// when it was added (RFC 3339, UTC) and who added it.
type GardenButton struct {
	Icon   string `json:"icon"`
	Label  string `json:"label"`
	Link   string `json:"link"`
	Detail string `json:"detail"`
	Added  string `json:"added"`
	By     string `json:"by"`
}

func gardenButtonsOf(q Querier) ([]GardenButton, error) {
	v, err := metaValue(q, gardenButtonsKey)
	if err != nil || v == "" {
		return nil, err
	}
	var list []GardenButton
	if err := json.Unmarshal([]byte(v), &list); err != nil {
		return nil, err
	}
	return list, nil
}

func writeGardenButtonsTx(tx *sql.Tx, list []GardenButton) error {
	if list == nil {
		list = []GardenButton{}
	}
	b, err := json.Marshal(list)
	if err != nil {
		return err
	}
	return setMetaTx(tx, gardenButtonsKey, string(b))
}

// GardenButtonList is the stored list in display order for the panel. ErrNoDatabase without a database.
func GardenButtonList() ([]GardenButton, error) {
	d, err := docDB()
	if err != nil {
		return nil, err
	}
	var list []GardenButton
	err = d.View(func(q Querier) error {
		var err error
		list, err = gardenButtonsOf(q)
		return err
	})
	return list, err
}

// GardenButtons is the list the game serves: nil without a database or when the stored value does not read.
func GardenButtons() []GardenButton {
	d := attachedDB()
	if d == nil {
		return nil
	}
	var list []GardenButton
	if err := d.View(func(q Querier) error {
		var err error
		list, err = gardenButtonsOf(q)
		return err
	}); err != nil {
		return nil
	}
	return list
}

// AddGardenButton appends b (Icon, Label, Link, Detail; the caller has checked icon and link) with its time and actor, and
// writes the audit row in one transaction. At most GardenButtonsMax; one icon once (the client keeps one row per icon).
func AddGardenButton(b GardenButton, actor string, now time.Time) error {
	b.Label = strings.TrimSpace(b.Label)
	switch {
	case b.Icon == "" || b.Link == "":
		return fmt.Errorf("%w: choose a picture and an action", ErrBadInput)
	case b.Label == "" || utf8.RuneCountInString(b.Label) > GardenButtonLabelMax:
		return fmt.Errorf("%w: the label is 1 to %d characters", ErrBadInput, GardenButtonLabelMax)
	case len(b.Icon) > gardenButtonFieldMax || len(b.Link) > gardenButtonFieldMax || len(b.Detail) > gardenButtonFieldMax:
		return fmt.Errorf("%w: a field is too long", ErrBadInput)
	}
	d, err := docDB()
	if err != nil {
		return err
	}
	return d.Update(func(tx *sql.Tx) error {
		list, err := gardenButtonsOf(tx)
		if err != nil {
			return err
		}
		if len(list) >= GardenButtonsMax {
			return fmt.Errorf("%w: the game shows at most %d Garden buttons; remove one first", ErrBadInput, GardenButtonsMax)
		}
		for _, e := range list {
			if e.Icon == b.Icon {
				return fmt.Errorf("%w: a Garden button already uses that picture (the game keeps one button per picture)", ErrBadInput)
			}
		}
		b.Added, b.By = now.UTC().Format(time.RFC3339), actor
		if err := writeGardenButtonsTx(tx, append(list, b)); err != nil {
			return err
		}
		return auditTx(tx, actor, "garden-button-add", "garden", map[string]any{"icon": b.Icon, "label": b.Label, "link": b.Link, "detail": b.Detail})
	})
}

// RemoveGardenButton removes the button with this icon and writes the audit row in one transaction; ErrBadInput when no
// button has it.
func RemoveGardenButton(icon, actor string) error {
	d, err := docDB()
	if err != nil {
		return err
	}
	return d.Update(func(tx *sql.Tx) error {
		list, err := gardenButtonsOf(tx)
		if err != nil {
			return err
		}
		for i, e := range list {
			if e.Icon == icon {
				if err := writeGardenButtonsTx(tx, append(list[:i:i], list[i+1:]...)); err != nil {
					return err
				}
				return auditTx(tx, actor, "garden-button-remove", "garden", map[string]any{"icon": icon, "label": e.Label})
			}
		}
		return fmt.Errorf("%w: no Garden button uses that picture", ErrBadInput)
	})
}

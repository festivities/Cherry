package store

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"cherry/data"
	"cherry/internal/itemcode"
)

// ItemNameMax is the longest display name a staff member or helper may give an item (in Unicode characters).
const ItemNameMax = 80

// ItemNameRecord is one live custom item name override stored in the item_names table (schema v13).
type ItemNameRecord struct {
	Code      string
	Name      string
	Updated   string // RFC 3339, UTC
	UpdatedBy string // staff identity
}

// ItemNamesFile is the committed data/item_names.json schema (and the exact format exported by /items/export.json so
// promoted volunteer names can be dropped straight over data/item_names.json with a clean sorted git diff).
type ItemNamesFile struct {
	Origin string            `json:"origin"`
	Note   string            `json:"note"`
	Names  map[string]string `json:"names"`
}

// Custom item names live in memory for O(1) reads on the hot game path (economy.ItemName) and write through to the
// item_names table atomically with their audit row. Built-in community defaults come from embedded data/item_names.json
// and sit below live DB overrides and above synthetic fallbacks.
var (
	itemNamesMu          sync.RWMutex
	itemNames            = map[string]ItemNameRecord{}
	builtinItemNames     = map[string]string{}
	builtinItemNamesMeta ItemNamesFile
)

func init() {
	f, err := ParseItemNamesFile(data.ItemNamesJSON)
	if err != nil {
		panic("embedded data/item_names.json: " + err.Error())
	}
	builtinItemNamesMeta = ItemNamesFile{Origin: f.Origin, Note: f.Note}
	builtinItemNames = f.Names
}

// ParseItemNamesFile strictly decodes and validates an item_names.json file.
func ParseItemNamesFile(raw []byte) (ItemNamesFile, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var f ItemNamesFile
	if err := dec.Decode(&f); err != nil {
		return ItemNamesFile{}, err
	}
	if f.Origin != "cherry" {
		return ItemNamesFile{}, fmt.Errorf(`origin must be "cherry"`)
	}
	if strings.TrimSpace(f.Note) == "" {
		return ItemNamesFile{}, errors.New("note must not be empty")
	}
	if f.Names == nil {
		f.Names = map[string]string{}
	}
	for code, name := range f.Names {
		if _, ok := itemcode.ID(code); !ok {
			return ItemNamesFile{}, fmt.Errorf("names[%q]: not a valid canonical item code", code)
		}
		clean, err := ValidItemName(name)
		if err != nil {
			return ItemNamesFile{}, fmt.Errorf("names[%s]: %w", code, err)
		}
		if clean != name {
			return ItemNamesFile{}, fmt.Errorf("names[%s]: name must not have leading or trailing whitespace", code)
		}
	}
	return f, nil
}

// loadItemNames reads every row of item_names into memory (OpenStore / ReloadStores).
func loadItemNames(d *DB) error {
	got := map[string]ItemNameRecord{}
	err := d.View(func(q Querier) error {
		rows, err := q.Query(`SELECT code, name, updated, updated_by FROM item_names`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var r ItemNameRecord
			if err := rows.Scan(&r.Code, &r.Name, &r.Updated, &r.UpdatedBy); err != nil {
				return err
			}
			got[r.Code] = r
		}
		return rows.Err()
	})
	if err != nil {
		return err
	}
	itemNamesMu.Lock()
	itemNames = got
	itemNamesMu.Unlock()
	return nil
}

// clearItemNames empties the in-memory DB override cache (CloseStore). Built-in defaults stay loaded.
func clearItemNames() {
	itemNamesMu.Lock()
	itemNames = map[string]ItemNameRecord{}
	itemNamesMu.Unlock()
}

// BuiltinItemName returns the built-in community default name from data/item_names.json for code, if any.
func BuiltinItemName(code string) (string, bool) {
	itemNamesMu.RLock()
	name, ok := builtinItemNames[code]
	itemNamesMu.RUnlock()
	return name, ok
}

// AllBuiltinItemNames returns a shallow copy of every built-in item name from data/item_names.json.
func AllBuiltinItemNames() map[string]string {
	itemNamesMu.RLock()
	out := maps.Clone(builtinItemNames)
	itemNamesMu.RUnlock()
	if out == nil {
		return map[string]string{}
	}
	return out
}

// ItemNameOverride returns the effective non-synthetic display name for code: the live DB override first, else the
// built-in community default from data/item_names.json. Safe for concurrent calls on the game path.
func ItemNameOverride(code string) (string, bool) {
	itemNamesMu.RLock()
	if r, ok := itemNames[code]; ok {
		itemNamesMu.RUnlock()
		return r.Name, true
	}
	if name, ok := builtinItemNames[code]; ok {
		itemNamesMu.RUnlock()
		return name, true
	}
	itemNamesMu.RUnlock()
	return "", false
}

// ItemNameRecordOf returns the stored live DB override record for code, if any.
func ItemNameRecordOf(code string) (ItemNameRecord, bool) {
	itemNamesMu.RLock()
	r, ok := itemNames[code]
	itemNamesMu.RUnlock()
	return r, ok
}

// AllItemNameRecords returns a shallow copy of every live DB override.
func AllItemNameRecords() map[string]ItemNameRecord {
	itemNamesMu.RLock()
	out := maps.Clone(itemNames)
	itemNamesMu.RUnlock()
	if out == nil {
		return map[string]ItemNameRecord{}
	}
	return out
}

// ItemNameCount returns how many items currently have live DB overrides stored.
func ItemNameCount() int {
	itemNamesMu.RLock()
	n := len(itemNames)
	itemNamesMu.RUnlock()
	return n
}

// ExportItemNamesJSON merges data/item_names.json with every live DB override (live overrides win) and encodes the result
// in the exact data/item_names.json format, with keys sorted alphabetically and a trailing newline.
func ExportItemNamesJSON() ([]byte, error) {
	itemNamesMu.RLock()
	meta := builtinItemNamesMeta
	merged := make(map[string]string, len(builtinItemNames)+len(itemNames))
	maps.Copy(merged, builtinItemNames)
	for code, rec := range itemNames {
		merged[code] = rec.Name
	}
	itemNamesMu.RUnlock()

	if meta.Origin == "" {
		meta.Origin = "cherry"
	}
	if meta.Note == "" {
		meta.Note = "Community-contributed display names for catalog items that have no original names in the archives."
	}
	b, err := json.MarshalIndent(ItemNamesFile{
		Origin: meta.Origin,
		Note:   meta.Note,
		Names:  merged,
	}, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

// ValidItemName trims whitespace and checks that an item name is non-empty, within ItemNameMax runes and free of control
// characters.
func ValidItemName(name string) (string, error) {
	clean := strings.TrimSpace(name)
	if clean == "" {
		return "", fmt.Errorf("%w: the item name must not be empty", ErrBadInput)
	}
	if utf8.RuneCountInString(clean) > ItemNameMax {
		return "", fmt.Errorf("%w: the item name is at most %d characters", ErrBadInput, ItemNameMax)
	}
	for _, r := range clean {
		if unicode.IsControl(r) {
			return "", fmt.Errorf("%w: the item name must not contain control characters", ErrBadInput)
		}
	}
	return clean, nil
}

// SetItemName stores a custom display name for canonical 9-character item code and writes an audit row in one transaction.
// If clean matches the built-in default in data/item_names.json, any redundant DB override row is removed cleanly. It
// updates the in-memory map on commit so the new name takes effect live without restarting Cherry. Returns the saved
// record and the previous override name (empty if none was stored).
func SetItemName(code, name, actor string, now time.Time) (ItemNameRecord, string, error) {
	code = strings.TrimSpace(code)
	if _, ok := itemcode.ID(code); !ok {
		return ItemNameRecord{}, "", fmt.Errorf("%w: %q is not a valid item code", ErrBadInput, code)
	}
	clean, err := ValidItemName(name)
	if err != nil {
		return ItemNameRecord{}, "", err
	}
	d, err := docDB()
	if err != nil {
		return ItemNameRecord{}, "", err
	}
	builtin, hasBuiltin := BuiltinItemName(code)
	matchesBuiltin := hasBuiltin && clean == builtin
	stamp := now.UTC().Format(time.RFC3339)
	rec := ItemNameRecord{Code: code, Name: clean, Updated: stamp, UpdatedBy: actor}
	var prev string
	err = d.Update(func(tx *sql.Tx) error {
		var old string
		qerr := tx.QueryRow(`SELECT name FROM item_names WHERE code = ?`, code).Scan(&old)
		hasOld := qerr == nil
		if qerr != nil && !errors.Is(qerr, sql.ErrNoRows) {
			return qerr
		}
		prev = old
		if matchesBuiltin {
			if !hasOld {
				return nil
			}
			if _, err := tx.Exec(`DELETE FROM item_names WHERE code = ?`, code); err != nil {
				return err
			}
			if prev == clean {
				return nil
			}
			return auditTx(tx, actor, "item-name", "item:"+code, map[string]any{"old": prev, "new": clean})
		}
		if _, err := tx.Exec(`INSERT INTO item_names (code, name, updated, updated_by) VALUES (?, ?, ?, ?)
			ON CONFLICT(code) DO UPDATE SET name = excluded.name, updated = excluded.updated, updated_by = excluded.updated_by`,
			rec.Code, rec.Name, rec.Updated, rec.UpdatedBy); err != nil {
			return err
		}
		oldEff := prev
		if !hasOld && hasBuiltin {
			oldEff = builtin
		}
		if oldEff == clean {
			return nil
		}
		return auditTx(tx, actor, "item-name", "item:"+code, map[string]any{"old": oldEff, "new": clean})
	})
	if err != nil {
		return ItemNameRecord{}, "", err
	}
	itemNamesMu.Lock()
	if matchesBuiltin {
		delete(itemNames, code)
	} else {
		itemNames[code] = rec
	}
	itemNamesMu.Unlock()
	return rec, prev, nil
}

// ResetItemName removes any live DB override for code so the built-in default (or synthetic fallback) applies again,
// writing an audit row in the same transaction when an override existed. Returns the removed name and whether one existed.
func ResetItemName(code, actor string) (string, bool, error) {
	code = strings.TrimSpace(code)
	if _, ok := itemcode.ID(code); !ok {
		return "", false, fmt.Errorf("%w: %q is not a valid item code", ErrBadInput, code)
	}
	d, err := docDB()
	if err != nil {
		return "", false, err
	}
	var prev string
	var existed bool
	err = d.Update(func(tx *sql.Tx) error {
		var old string
		qerr := tx.QueryRow(`SELECT name FROM item_names WHERE code = ?`, code).Scan(&old)
		if errors.Is(qerr, sql.ErrNoRows) {
			return nil
		}
		if qerr != nil {
			return qerr
		}
		prev = old
		existed = true
		if _, err := tx.Exec(`DELETE FROM item_names WHERE code = ?`, code); err != nil {
			return err
		}
		return auditTx(tx, actor, "item-name-reset", "item:"+code, map[string]any{"old": prev})
	})
	if err != nil {
		return "", false, err
	}
	itemNamesMu.Lock()
	delete(itemNames, code)
	itemNamesMu.Unlock()
	return prev, existed, nil
}

// SetItemNamesForTest replaces the in-memory live DB item name overrides (nil = none). Tests only.
func SetItemNamesForTest(m map[string]string) {
	itemNamesMu.Lock()
	defer itemNamesMu.Unlock()
	itemNames = map[string]ItemNameRecord{}
	for code, name := range m {
		itemNames[code] = ItemNameRecord{Code: code, Name: name}
	}
}

// SetBuiltinItemNamesForTest replaces the in-memory built-in item names and returns a restore function. Tests only.
func SetBuiltinItemNamesForTest(m map[string]string) func() {
	itemNamesMu.Lock()
	prev := builtinItemNames
	next := map[string]string{}
	maps.Copy(next, m)
	builtinItemNames = next
	itemNamesMu.Unlock()
	return func() {
		itemNamesMu.Lock()
		builtinItemNames = prev
		itemNamesMu.Unlock()
	}
}

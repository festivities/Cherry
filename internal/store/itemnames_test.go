package store

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestItemNamesRoundTripAndPersistence(t *testing.T) {
	path := filepath.Join(t.TempDir(), DBFileName)
	if err := OpenStore(path); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = CloseStore() })

	now := time.Date(2026, 11, 27, 5, 0, 0, 0, time.UTC)
	const code = "CUHA0066I"

	if _, ok := ItemNameOverride(code); ok {
		t.Fatal("override present before set")
	}

	rec, prev, err := SetItemName(code, "  Strawberry Twin Tails  ", "dev-helper", now)
	if err != nil {
		t.Fatal(err)
	}
	if prev != "" || rec.Name != "Strawberry Twin Tails" || rec.UpdatedBy != "dev-helper" || rec.Updated != "2026-11-27T05:00:00Z" {
		t.Fatalf("saved record: %+v prev=%q", rec, prev)
	}
	if got, ok := ItemNameOverride(code); !ok || got != "Strawberry Twin Tails" {
		t.Fatalf("ItemNameOverride = %q %t", got, ok)
	}
	if ItemNameCount() != 1 {
		t.Fatalf("ItemNameCount = %d, want 1", ItemNameCount())
	}

	// Update existing override.
	rec2, prev2, err := SetItemName(code, "Pink Twin Tails", "dev-mod", now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if prev2 != "Strawberry Twin Tails" || rec2.Name != "Pink Twin Tails" {
		t.Fatalf("updated record: %+v prev=%q", rec2, prev2)
	}

	// Reload from disk (simulates restart): override survives.
	if err := CloseStore(); err != nil {
		t.Fatal(err)
	}
	if _, ok := ItemNameOverride(code); ok {
		t.Fatal("CloseStore did not clear in-memory item names")
	}
	if err := OpenStore(path); err != nil {
		t.Fatal(err)
	}
	if got, ok := ItemNameOverride(code); !ok || got != "Pink Twin Tails" {
		t.Fatalf("after reopen ItemNameOverride = %q %t, want Pink Twin Tails", got, ok)
	}

	// Reset removes override and writes audit row; second reset is idempotent.
	old, existed, err := ResetItemName(code, "dev-owner")
	if err != nil || !existed || old != "Pink Twin Tails" {
		t.Fatalf("ResetItemName = %q %t %v", old, existed, err)
	}
	if _, ok := ItemNameOverride(code); ok {
		t.Fatal("override still set after reset")
	}
	if _, existed2, err := ResetItemName(code, "dev-owner"); err != nil || existed2 {
		t.Fatalf("second ResetItemName = %t %v, want false nil", existed2, err)
	}

	// Audit rows recorded.
	rows, err := AuditRows(0, 50)
	if err != nil {
		t.Fatal(err)
	}
	var actions []string
	for i := len(rows) - 1; i >= 0; i-- {
		if rows[i].Target == "item:"+code {
			actions = append(actions, rows[i].Action)
		}
	}
	if got := strings.Join(actions, ","); got != "item-name,item-name,item-name-reset" {
		t.Fatalf("audit actions = %q, want item-name,item-name,item-name-reset", got)
	}
}

func TestItemNamesValidation(t *testing.T) {
	docStore(t)
	now := time.Now()
	for _, badCode := range []string{"", "BAD", "cuha0066i", "ZZZZ00001", "CUHA0066I "} {
		// Note: SetItemName trims code, so test truly invalid ones separately.
		if strings.TrimSpace(badCode) == "CUHA0066I" {
			continue
		}
		if _, _, err := SetItemName(badCode, "Valid", "dev-helper", now); !errors.Is(err, ErrBadInput) {
			t.Errorf("SetItemName(%q) = %v, want ErrBadInput", badCode, err)
		}
	}
	for _, badName := range []string{"", "   ", "Line1\nLine2", "Tab\there", strings.Repeat("a", ItemNameMax+1)} {
		if _, _, err := SetItemName("CUHA0066I", badName, "dev-helper", now); !errors.Is(err, ErrBadInput) {
			t.Errorf("SetItemName(name=%q) = %v, want ErrBadInput", badName, err)
		}
	}
	if _, _, err := SetItemName("CUHA0066I", strings.Repeat("é", ItemNameMax), "dev-helper", now); err != nil {
		t.Fatalf("max-length UTF-8 name rejected: %v", err)
	}
}

func TestBuiltinItemNamesAndExportMerge(t *testing.T) {
	docStore(t)
	restore := SetBuiltinItemNamesForTest(map[string]string{
		"CUHA00001": "Built-in Bob",
		"CUTO00001": "Built-in Tee",
	})
	t.Cleanup(restore)

	now := time.Date(2026, 11, 27, 6, 0, 0, 0, time.UTC)

	// Built-in applies when no live DB override exists.
	if got, ok := ItemNameOverride("CUHA00001"); !ok || got != "Built-in Bob" {
		t.Fatalf("ItemNameOverride(builtin) = %q %t", got, ok)
	}

	// Live DB override beats built-in.
	if _, _, err := SetItemName("CUHA00001", "Edited Bob", "dev-vol", now); err != nil {
		t.Fatal(err)
	}
	if _, _, err := SetItemName("CUPA00001", "New Skirt", "dev-vol", now); err != nil {
		t.Fatal(err)
	}
	if got, ok := ItemNameOverride("CUHA00001"); !ok || got != "Edited Bob" {
		t.Fatalf("override did not beat builtin: %q %t", got, ok)
	}

	// Export merges built-ins + live overrides in exact data/item_names.json format and parses strictly.
	raw, err := ExportItemNamesJSON()
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := ParseItemNamesFile(raw)
	if err != nil {
		t.Fatalf("exported JSON failed strict parse: %v\n%s", err, raw)
	}
	if parsed.Origin != "cherry" || parsed.Names["CUHA00001"] != "Edited Bob" ||
		parsed.Names["CUTO00001"] != "Built-in Tee" || parsed.Names["CUPA00001"] != "New Skirt" {
		t.Fatalf("unexpected export: %+v", parsed)
	}

	// Setting an item back to its exact built-in name removes the redundant DB override row cleanly.
	if _, _, err := SetItemName("CUHA00001", "Built-in Bob", "dev-vol", now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, hasDB := ItemNameRecordOf("CUHA00001"); hasDB {
		t.Fatal("redundant DB override row was not pruned when set to built-in default")
	}
	if got, ok := ItemNameOverride("CUHA00001"); !ok || got != "Built-in Bob" {
		t.Fatalf("after matching built-in, ItemNameOverride = %q %t", got, ok)
	}

	// Strict validation rejects bad files.
	for _, bad := range []string{
		`{"origin":"original","note":"x","names":{}}`,
		`{"origin":"cherry","note":"","names":{}}`,
		`{"origin":"cherry","note":"x","extra":1,"names":{}}`,
		`{"origin":"cherry","note":"x","names":{"BADCODE":"Hi"}}`,
		`{"origin":"cherry","note":"x","names":{"CUHA00001":"  Padded  "}}`,
	} {
		if _, err := ParseItemNamesFile([]byte(bad)); err == nil {
			t.Errorf("ParseItemNamesFile accepted bad input: %s", bad)
		}
	}
}

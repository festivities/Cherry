package store

import (
	"database/sql"
	"errors"
	"strings"
	"testing"
)

// TestSquareRoomCapOverride: unset, the default applies; a stored 2..100 replaces it; a bad value is ErrBadInput and changes
// nothing; a stored value outside the range reads as no value; clearing brings the default back. Each change is audited.
func TestSquareRoomCapOverride(t *testing.T) {
	if got := SquareRoomCap(SquareRoomCapDefault); got != SquareRoomCapDefault {
		t.Fatalf("without a database SquareRoomCap = %d, want the default", got)
	}
	docStore(t)
	if n, set, err := SquareRoomCapSetting(); err != nil || set || n != 0 {
		t.Fatalf("unset: %d %t %v", n, set, err)
	}
	if got := SquareRoomCap(SquareRoomCapDefault); got != 20 {
		t.Fatalf("unset SquareRoomCap = %d, want the default 20", got)
	}

	if err := SetSquareRoomCap(5, "dev-owner"); err != nil {
		t.Fatal(err)
	}
	if got := SquareRoomCap(SquareRoomCapDefault); got != 5 {
		t.Fatalf("set SquareRoomCap = %d, want 5", got)
	}
	if n, set, err := SquareRoomCapSetting(); err != nil || !set || n != 5 {
		t.Fatalf("set: %d %t %v", n, set, err)
	}
	for _, bad := range []int{SquareRoomCapMin - 1, SquareRoomCapMax + 1} {
		if err := SetSquareRoomCap(bad, "dev-owner"); !errors.Is(err, ErrBadInput) {
			t.Fatalf("SetSquareRoomCap(%d) = %v, want ErrBadInput", bad, err)
		}
	}
	if got := SquareRoomCap(SquareRoomCapDefault); got != 5 {
		t.Fatalf("a refused value changed the room size to %d", got)
	}

	d := attachedDB()
	if err := d.Update(func(tx *sql.Tx) error { return setMetaTx(tx, squareRoomCapKey, "500") }); err != nil {
		t.Fatal(err)
	}
	if got := SquareRoomCap(SquareRoomCapDefault); got != SquareRoomCapDefault {
		t.Fatalf("a stored 500 reads as %d, want the default", got)
	}

	if err := ClearSquareRoomCap("dev-owner"); err != nil {
		t.Fatal(err)
	}
	if got := SquareRoomCap(SquareRoomCapDefault); got != 20 {
		t.Fatalf("cleared SquareRoomCap = %d, want the default 20", got)
	}
	if _, set, _ := SquareRoomCapSetting(); set {
		t.Fatal("cleared room size still reads as set")
	}
	row, ok := settingAudit(t, "setting", squareRoomCapKey)
	if !ok || row.Actor != "dev-owner" || !strings.Contains(row.Detail, `"new":"default"`) {
		t.Fatalf("no clear row by the owner: %+v", row)
	}
	if !strings.Contains(row.Detail, `"old":"500"`) {
		t.Fatalf("clear row does not name the stored value 500: %s", row.Detail)
	}
}

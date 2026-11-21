package store

import (
	"database/sql"
	"errors"
	"strings"
	"testing"
)

func TestUploadMaxTotalBytesOverride(t *testing.T) {
	const fallback int64 = 100 << 20
	if got := UploadMaxTotalBytes(fallback); got != fallback {
		t.Fatalf("without db = %d, want %d", got, fallback)
	}
	if err := SetUploadMaxTotalBytes(10<<20, "dev-owner"); !errors.Is(err, ErrNoDatabase) {
		t.Fatalf("without db Set = %v, want ErrNoDatabase", err)
	}
	docStore(t)
	if n, set, err := UploadMaxTotalBytesSetting(); err != nil || set || n != 0 {
		t.Fatalf("unset: %d %t %v", n, set, err)
	}
	if got := UploadMaxTotalBytes(fallback); got != fallback {
		t.Fatalf("unset = %d, want %d", got, fallback)
	}

	if err := SetUploadMaxTotalBytes(50<<20, "dev-owner"); err != nil {
		t.Fatal(err)
	}
	if got := UploadMaxTotalBytes(fallback); got != 50<<20 {
		t.Fatalf("set = %d, want %d", got, 50<<20)
	}
	if n, set, err := UploadMaxTotalBytesSetting(); err != nil || !set || n != 50<<20 {
		t.Fatalf("setting = %d %t %v", n, set, err)
	}
	for _, bad := range []int64{UploadMaxTotalBytesMin - 1, UploadMaxTotalBytesMax + 1} {
		if err := SetUploadMaxTotalBytes(bad, "dev-owner"); !errors.Is(err, ErrBadInput) {
			t.Fatalf("SetUploadMaxTotalBytes(%d) = %v, want ErrBadInput", bad, err)
		}
	}
	if got := UploadMaxTotalBytes(fallback); got != 50<<20 {
		t.Fatalf("refused value changed cap to %d", got)
	}

	d := attachedDB()
	if err := d.Update(func(tx *sql.Tx) error { return setMetaTx(tx, uploadMaxTotalBytesKey, "123") }); err != nil {
		t.Fatal(err)
	}
	if got := UploadMaxTotalBytes(fallback); got != fallback {
		t.Fatalf("out-of-range stored = %d, want fallback", got)
	}

	if err := ClearUploadMaxTotalBytes("dev-owner"); err != nil {
		t.Fatal(err)
	}
	if _, set, _ := UploadMaxTotalBytesSetting(); set {
		t.Fatal("cleared still set")
	}
	row, ok := settingAudit(t, "setting", uploadMaxTotalBytesKey)
	if !ok || row.Actor != "dev-owner" || !strings.Contains(row.Detail, `"new":"default"`) || !strings.Contains(row.Detail, `"old":"123"`) {
		t.Fatalf("audit row = %+v ok=%t", row, ok)
	}
}

func TestUploadMaxFileBytesOverride(t *testing.T) {
	const fallback int64 = 20 << 20
	if got := UploadMaxFileBytes(fallback); got != fallback {
		t.Fatalf("without db = %d, want %d", got, fallback)
	}
	docStore(t)
	if err := SetUploadMaxFileBytes(UploadMaxFileBytesMin, "dev-owner"); err != nil {
		t.Fatal(err)
	}
	if got := UploadMaxFileBytes(fallback); got != UploadMaxFileBytesMin {
		t.Fatalf("min = %d, want %d", got, UploadMaxFileBytesMin)
	}
	if err := SetUploadMaxFileBytes(UploadMaxFileBytesMax, "dev-owner"); err != nil {
		t.Fatal(err)
	}
	if got := UploadMaxFileBytes(fallback); got != UploadMaxFileBytesMax {
		t.Fatalf("max = %d, want %d", got, UploadMaxFileBytesMax)
	}
	for _, bad := range []int64{UploadMaxFileBytesMin - 1, UploadMaxFileBytesMax + 1} {
		if err := SetUploadMaxFileBytes(bad, "dev-owner"); !errors.Is(err, ErrBadInput) {
			t.Fatalf("SetUploadMaxFileBytes(%d) = %v, want ErrBadInput", bad, err)
		}
	}
	if err := ClearUploadMaxFileBytes("dev-owner"); err != nil {
		t.Fatal(err)
	}
	if _, set, _ := UploadMaxFileBytesSetting(); set {
		t.Fatal("cleared still set")
	}
	row, ok := settingAudit(t, "setting", uploadMaxFileBytesKey)
	if !ok || row.Actor != "dev-owner" || !strings.Contains(row.Detail, `"new":"default"`) {
		t.Fatalf("audit row = %+v ok=%t", row, ok)
	}
}

func TestMaxPlayersOverride(t *testing.T) {
	const fallback = 200
	if got := MaxPlayers(fallback); got != fallback {
		t.Fatalf("without db = %d, want %d", got, fallback)
	}
	docStore(t)
	if err := SetMaxPlayers(0, "dev-owner"); err != nil {
		t.Fatal(err)
	}
	if n, set, err := MaxPlayersSetting(); err != nil || !set || n != 0 {
		t.Fatalf("zero override = %d %t %v, want 0 true nil", n, set, err)
	}
	if got := MaxPlayers(fallback); got != 0 {
		t.Fatalf("MaxPlayers with 0 override = %d, want 0", got)
	}
	if err := SetMaxPlayers(MaxPlayersMax, "dev-owner"); err != nil {
		t.Fatal(err)
	}
	if got := MaxPlayers(fallback); got != MaxPlayersMax {
		t.Fatalf("MaxPlayers = %d, want %d", got, MaxPlayersMax)
	}
	for _, bad := range []int{-1, MaxPlayersMax + 1} {
		if err := SetMaxPlayers(bad, "dev-owner"); !errors.Is(err, ErrBadInput) {
			t.Fatalf("SetMaxPlayers(%d) = %v, want ErrBadInput", bad, err)
		}
	}
	if err := ClearMaxPlayers("dev-owner"); err != nil {
		t.Fatal(err)
	}
	if got := MaxPlayers(fallback); got != fallback {
		t.Fatalf("cleared = %d, want %d", got, fallback)
	}
	row, ok := settingAudit(t, "setting", maxPlayersKey)
	if !ok || row.Actor != "dev-owner" || !strings.Contains(row.Detail, `"new":"default"`) {
		t.Fatalf("audit row = %+v ok=%t", row, ok)
	}
}

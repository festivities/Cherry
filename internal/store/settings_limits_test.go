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

func TestUploadCapsOverride(t *testing.T) {
	for _, tc := range []struct {
		name      string
		key       string
		fallback  int64
		min, max  int64
		effective func(int64) int64
		setting   func() (int64, bool, error)
		set       func(int64, string) error
		clear     func(string) error
	}{
		{
			name:      "receive",
			key:       uploadMaxReceiveBytesKey,
			fallback:  20 << 20,
			min:       UploadMaxReceiveBytesMin,
			max:       UploadMaxReceiveBytesMax,
			effective: UploadMaxReceiveBytes,
			setting:   UploadMaxReceiveBytesSetting,
			set:       SetUploadMaxReceiveBytes,
			clear:     ClearUploadMaxReceiveBytes,
		},
		{
			name:      "jpeg",
			key:       uploadMaxJPEGBytesKey,
			fallback:  150 << 10,
			min:       UploadMaxJPEGBytesMin,
			max:       UploadMaxJPEGBytesMax,
			effective: UploadMaxJPEGBytes,
			setting:   UploadMaxJPEGBytesSetting,
			set:       SetUploadMaxJPEGBytes,
			clear:     ClearUploadMaxJPEGBytes,
		},
		{
			name:      "png",
			key:       uploadMaxPNGBytesKey,
			fallback:  150 << 10,
			min:       UploadMaxPNGBytesMin,
			max:       UploadMaxPNGBytesMax,
			effective: UploadMaxPNGBytes,
			setting:   UploadMaxPNGBytesSetting,
			set:       SetUploadMaxPNGBytes,
			clear:     ClearUploadMaxPNGBytes,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.effective(tc.fallback); got != tc.fallback {
				t.Fatalf("without db = %d, want %d", got, tc.fallback)
			}
			if err := tc.set(tc.min, "dev-owner"); !errors.Is(err, ErrNoDatabase) {
				t.Fatalf("without db Set = %v, want ErrNoDatabase", err)
			}
			docStore(t)
			if n, set, err := tc.setting(); err != nil || set || n != 0 {
				t.Fatalf("unset: %d %t %v", n, set, err)
			}
			if err := tc.set(tc.min, "dev-owner"); err != nil {
				t.Fatal(err)
			}
			if got := tc.effective(tc.fallback); got != tc.min {
				t.Fatalf("min = %d, want %d", got, tc.min)
			}
			if err := tc.set(tc.max, "dev-owner"); err != nil {
				t.Fatal(err)
			}
			if got := tc.effective(tc.fallback); got != tc.max {
				t.Fatalf("max = %d, want %d", got, tc.max)
			}
			for _, bad := range []int64{tc.min - 1, tc.max + 1} {
				if err := tc.set(bad, "dev-owner"); !errors.Is(err, ErrBadInput) {
					t.Fatalf("set(%d) = %v, want ErrBadInput", bad, err)
				}
			}
			d := attachedDB()
			if err := d.Update(func(tx *sql.Tx) error { return setMetaTx(tx, tc.key, "123") }); err != nil {
				t.Fatal(err)
			}
			if got := tc.effective(tc.fallback); got != tc.fallback {
				t.Fatalf("out-of-range stored = %d, want fallback", got)
			}
			if err := tc.clear("dev-owner"); err != nil {
				t.Fatal(err)
			}
			if _, set, _ := tc.setting(); set {
				t.Fatal("cleared still set")
			}
			row, ok := settingAudit(t, "setting", tc.key)
			if !ok || row.Actor != "dev-owner" || !strings.Contains(row.Detail, `"new":"default"`) {
				t.Fatalf("audit row = %+v ok=%t", row, ok)
			}
		})
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

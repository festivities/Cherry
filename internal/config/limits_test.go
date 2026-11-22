package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The limit and retention settings: lab-guest keeps them off, identity modes turn them on with the defaults of
// identityDefaults, and an explicit value wins in every mode.
func TestLimitDefaultsByMode(t *testing.T) {
	lab, err := Load(nil, none)
	if err != nil {
		t.Fatal(err)
	}
	if lab.APIRate != 0 || lab.AccountRate != 0 || lab.GuestPerMinute != 0 || lab.StaticRate != 0 || lab.FrameRate != 0 || lab.ConnsPerAddress != 1024 || lab.ConnsTotal != 4096 {
		t.Fatalf("lab limits = %+v, want off with the lab connection caps", lab)
	}
	if lab.DeletedDays != 30 || lab.IdleTokenDays != 30 || lab.ChatDays != 30 {
		t.Fatalf("retention = %d / %d / %d days, want 30 / 30 / 30", lab.DeletedDays, lab.IdleTokenDays, lab.ChatDays)
	}

	idc, err := parse([]string{"-auth-mode", "dev-fake"}, none)
	if err != nil {
		t.Fatal(err)
	}
	if idc.APIRate != 30 || idc.APIBurst != 400 || idc.AccountRate != 10 || idc.AccountBurst != 200 ||
		idc.GuestPerMinute != 20 || idc.ConnsPerAddress != 256 || idc.ConnsTotal != 4096 ||
		idc.StaticRate != 200 || idc.StaticBurst != 5000 || idc.FrameRate != 100 || idc.FrameBurst != 400 {
		t.Fatalf("identity limits = %+v, want the identityDefaults", idc)
	}

	set, err := parse([]string{"-auth-mode", "dev-fake", "-limits-api-rate", "5"}, none)
	if err != nil {
		t.Fatal(err)
	}
	if set.APIRate != 5 || set.AccountRate != 10 {
		t.Fatalf("explicit api rate = %d, account rate = %d; want 5 and the identity default 10", set.APIRate, set.AccountRate)
	}
}

func TestLimitSettingsRefusals(t *testing.T) {
	for name, args := range map[string][]string{
		"admin on all interfaces": {"-listen-admin", ":8099"},
		"admin on a LAN address":  {"-listen-admin", "192.168.1.7:8099"},
		"rate without burst":      {"-limits-api-rate", "5", "-limits-api-burst", "0"},
		"negative connections":    {"-limits-conns-per-address", "-1"},
		"words for a count":       {"-retention-deleted-days", "many"},
		"negative chat days":      {"-retention-chat-days", "-1"},
	} {
		if _, err := Load(args, none); err == nil {
			t.Errorf("%s: Load accepted %v", name, args)
		}
	}
	for _, admin := range []string{"127.0.0.1:8099", "[::1]:8099", "localhost:8099"} {
		if _, err := Load([]string{"-listen-admin", admin}, none); err != nil {
			t.Errorf("loopback admin %s refused: %v", admin, err)
		}
	}
}

func TestAdminRefusalNamesSetting(t *testing.T) {
	_, err := Load([]string{"-listen-admin", ":8099"}, none)
	if err == nil || !strings.Contains(err.Error(), "listen.admin") {
		t.Fatalf("error = %v, want one that names listen.admin", err)
	}
}

// TestRetentionDeletedDaysStaysAccepted: retention.deleted-days is unused since 2026-11-21 (deleted avatars are kept), but a
// cherry.json that still sets it loads; a value that is not a number is still refused.
func TestRetentionDeletedDaysStaysAccepted(t *testing.T) {
	dir := t.TempDir()
	ok := filepath.Join(dir, "ok.json")
	if err := os.WriteFile(ok, []byte(`{"retention.deleted-days": "7"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load([]string{"-config", ok}, none); err != nil {
		t.Fatalf("a file with the unused key: %v", err)
	}
	bad := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(bad, []byte(`{"retention.deleted-days": "many"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load([]string{"-config", bad}, none); err == nil {
		t.Fatal("a non-number was accepted")
	}
}

package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestUploadSettings: the upload settings default to on and 50 MiB of pictures in all, and a bad value names its setting.
func TestUploadSettings(t *testing.T) {
	c, err := Load(nil, none)
	if err != nil || c.UploadsOff || c.UploadQuotaBytes != 50<<20 {
		t.Fatalf("defaults: off=%t bytes=%d err=%v", c.UploadsOff, c.UploadQuotaBytes, err)
	}
	c, err = Load([]string{"-uploads-enabled=false", "-uploads-quota-bytes=1048576"}, none)
	if err != nil || !c.UploadsOff || c.UploadQuotaBytes != 1<<20 {
		t.Fatalf("overrides: off=%t bytes=%d err=%v", c.UploadsOff, c.UploadQuotaBytes, err)
	}
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"-uploads-enabled=maybe"}, "uploads.enabled"},
		{[]string{"-uploads-quota-bytes=ten"}, "uploads.quota-bytes"},
		{[]string{"-uploads-quota-bytes=1024"}, "uploads.quota-bytes must be"},
		{[]string{"-uploads-quota-bytes=10737418241"}, "uploads.quota-bytes must be"},
	} {
		if _, err := Load(tc.args, none); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%v: err = %v, want %q", tc.args, err, tc.want)
		}
	}
}

// TestUploadQuotaFileKeyIgnored: a -config file that still names the retired uploads.quota-files loads, and the key is ignored.
func TestUploadQuotaFileKeyIgnored(t *testing.T) {
	file := filepath.Join(t.TempDir(), "cherry.json")
	if err := os.WriteFile(file, []byte(`{"uploads.quota-files": "5", "uploads.quota-bytes": "1048576"}`), 0600); err != nil {
		t.Fatal(err)
	}
	c, err := Load([]string{"-config", file}, none)
	if err != nil || c.UploadQuotaBytes != 1<<20 {
		t.Fatalf("config with the retired key: bytes=%d err=%v", c.UploadQuotaBytes, err)
	}
}

// TestUploadAccessorsZeroConfig: an unactivated configuration (tests) has uploads on and the default quota.
func TestUploadAccessorsZeroConfig(t *testing.T) {
	Activate(Config{})
	if !UploadsOn() || UploadQuotaBytes() != DefaultUploadQuotaBytes {
		t.Fatalf("zero config: on=%t bytes=%d", UploadsOn(), UploadQuotaBytes())
	}
}

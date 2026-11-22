package config

import (
	"strings"
	"testing"
)

// TestUploadSettings: the three upload settings default to on and 25 MiB / 500 files, and a bad value names its setting.
func TestUploadSettings(t *testing.T) {
	c, err := Load(nil, none)
	if err != nil || c.UploadsOff || c.UploadQuotaBytes != 25<<20 || c.UploadQuotaFiles != 500 {
		t.Fatalf("defaults: off=%t bytes=%d files=%d err=%v", c.UploadsOff, c.UploadQuotaBytes, c.UploadQuotaFiles, err)
	}
	c, err = Load([]string{"-uploads-enabled=false", "-uploads-quota-bytes=1048576", "-uploads-quota-files=5"}, none)
	if err != nil || !c.UploadsOff || c.UploadQuotaBytes != 1<<20 || c.UploadQuotaFiles != 5 {
		t.Fatalf("overrides: off=%t bytes=%d files=%d err=%v", c.UploadsOff, c.UploadQuotaBytes, c.UploadQuotaFiles, err)
	}
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"-uploads-enabled=maybe"}, "uploads.enabled"},
		{[]string{"-uploads-quota-bytes=ten"}, "uploads.quota-bytes"},
		{[]string{"-uploads-quota-bytes=1024"}, "uploads.quota-bytes must be"},
		{[]string{"-uploads-quota-bytes=2147483648"}, "uploads.quota-bytes must be"},
		{[]string{"-uploads-quota-files=0"}, "uploads.quota-files must be"},
	} {
		if _, err := Load(tc.args, none); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%v: err = %v, want %q", tc.args, err, tc.want)
		}
	}
}

// TestUploadAccessorsZeroConfig: an unactivated configuration (tests) has uploads on and the default quotas.
func TestUploadAccessorsZeroConfig(t *testing.T) {
	Activate(Config{})
	bytes, files := UploadLimits()
	if !UploadsOn() || bytes != DefaultUploadQuotaBytes || files != DefaultUploadQuotaFiles {
		t.Fatalf("zero config: on=%t bytes=%d files=%d", UploadsOn(), bytes, files)
	}
}

package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// loadWith resolves the configuration from the given environment (a map) and flags.
func loadWith(t *testing.T, env map[string]string, args ...string) (Config, error) {
	t.Helper()
	return Load(args, func(k string) (string, bool) {
		v, ok := env[k]
		return v, ok
	})
}

// secretFile is a readable client secret file for the discord mode.
func secretFile(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(p, []byte("x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// discordStaff is a discord-mode environment with a working staff UI on :443 (not public).
func discordStaff(t *testing.T) map[string]string {
	return map[string]string{
		"CHERRY_AUTH_MODE":                "discord",
		"CHERRY_AUTH_SIGNUP":              "closed", // the players' sign-up gate has its own tests; this file tests the staff UI
		"CHERRY_AUTH_DISCORD_CLIENT_ID":   "cid",
		"CHERRY_AUTH_DISCORD_SECRET_FILE": secretFile(t),
		"CHERRY_AUTH_DISCORD_REDIRECT":    "https://play.example/auth/v1/discord/callback",
		"CHERRY_LISTEN_STAFF":             ":443",
		"CHERRY_STAFF_HOSTNAME":           "staff.example",
		"CHERRY_STAFF_OWNER":              "111111111111111111",
		"CHERRY_STAFF_DISCORD_REDIRECT":   "https://staff.example/auth/callback",
	}
}

// TestStaffOffByDefault: with listen.staff empty nothing about the staff UI is set, and the lab's configuration loads.
func TestStaffOffByDefault(t *testing.T) {
	c, err := loadWith(t, map[string]string{})
	if err != nil {
		t.Fatal(err)
	}
	if c.Staff != "" || c.StaffHostname != "" || c.StaffCert != "" || c.StaffOwner != "" || c.StaffRedirect != "" {
		t.Fatalf("staff settings are not empty by default: %+v", c)
	}
}

// TestStaffSettingsNeedTheListener: a staff setting without listen.staff is refused, one line per setting.
func TestStaffSettingsNeedTheListener(t *testing.T) {
	for _, args := range [][]string{{"-staff-owner=111"}, {"-staff-hostname=staff.example"}, {"-staff-tls-cert-file=/x.pem"}, {"-staff-allowlist-file=/x.txt"}} {
		_, err := loadWith(t, map[string]string{}, args...)
		if err == nil || !strings.Contains(err.Error(), "needs listen.staff") {
			t.Errorf("%v: %v", args, err)
		}
	}
}

// TestStaffRefusesLabGuest: the staff UI needs identities, so lab-guest refuses it.
func TestStaffRefusesLabGuest(t *testing.T) {
	_, err := loadWith(t, map[string]string{"CHERRY_LISTEN_STAFF": ":443", "CHERRY_STAFF_HOSTNAME": "staff.example"})
	if err == nil || !strings.Contains(err.Error(), "lab-guest") {
		t.Fatalf("lab-guest with listen.staff: %v", err)
	}
}

// TestStaffDiscordWorks: a complete discord staff UI (not public, no files) loads.
func TestStaffDiscordWorks(t *testing.T) {
	c, err := loadWith(t, discordStaff(t))
	if err != nil {
		t.Fatal(err)
	}
	if c.Staff != ":443" || c.StaffHostname != "staff.example" || c.StaffOwner != "111111111111111111" {
		t.Fatalf("staff settings: %+v", c)
	}
}

// TestPublicStaffNeedsEachPiece: a public server with a staff UI refuses a missing hostname, certificate or owner, each
// with its own line.
func TestPublicStaffNeedsEachPiece(t *testing.T) {
	public := func() map[string]string {
		env := discordStaff(t)
		env["CHERRY_SERVER_PUBLIC"] = "true"
		env["CHERRY_LISTEN_DNS"] = ""
		env["CHERRY_STAFF_TLS_CERT_FILE"] = "/a.pem"
		env["CHERRY_STAFF_TLS_KEY_FILE"] = "/a.key"
		return env
	}
	cases := map[string]struct {
		drop []string
		want string
	}{
		"hostname":    {[]string{"CHERRY_STAFF_HOSTNAME"}, "staff.hostname"},
		"certificate": {[]string{"CHERRY_STAFF_TLS_CERT_FILE", "CHERRY_STAFF_TLS_KEY_FILE"}, "tls-cert-file"},
		"owner":       {[]string{"CHERRY_STAFF_OWNER"}, "staff.owner"},
	}
	for name, tc := range cases {
		env := public()
		for _, k := range tc.drop {
			delete(env, k)
		}
		_, err := loadWith(t, env)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("public without %s: %v", name, err)
		}
	}
	if _, err := loadWith(t, public()); err != nil {
		t.Errorf("a complete public staff UI: %v", err)
	}
}

// TestCertificateFilesGoTogether: one of the two PEM files alone is refused.
func TestCertificateFilesGoTogether(t *testing.T) {
	env := discordStaff(t)
	env["CHERRY_STAFF_TLS_CERT_FILE"] = "/a.pem"
	_, err := loadWith(t, env)
	if err == nil || !strings.Contains(err.Error(), "must be set together") {
		t.Errorf("certificate without key: %v", err)
	}
}

// TestStaffRedirectIsExact: the staff callback names staff.hostname, ends in /auth/callback, carries no query, and uses the
// listen port.
func TestStaffRedirectIsExact(t *testing.T) {
	cases := map[string]string{
		"https://other.example/auth/callback":      "must name staff.hostname",
		"https://staff.example/auth/other":         "must end in /auth/callback",
		"https://staff.example/auth/callback?x=1":  "must not carry user info, a query or a fragment",
		"http://staff.example/auth/callback":       "must be an absolute https URL",
		"https://staff.example:8443/auth/callback": "must use the port of listen.staff",
	}
	for redirect, want := range cases {
		env := discordStaff(t)
		env["CHERRY_STAFF_DISCORD_REDIRECT"] = redirect
		_, err := loadWith(t, env)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("redirect %s: %v (want %q)", redirect, err, want)
		}
	}
}

// TestStaffHostnameAndOwnerForms: hostnames are DNS names; the owner is a Discord id, or a dev name in dev-fake only.
func TestStaffHostnameAndOwnerForms(t *testing.T) {
	env := discordStaff(t)
	env["CHERRY_STAFF_HOSTNAME"] = "staff_example"
	if _, err := loadWith(t, env); err == nil || !strings.Contains(err.Error(), "not a host name") {
		t.Errorf("underscore hostname: %v", err)
	}
	env = discordStaff(t)
	env["CHERRY_STAFF_OWNER"] = "dev-owner"
	if _, err := loadWith(t, env); err == nil || !strings.Contains(err.Error(), "Discord user id") {
		t.Errorf("a dev owner in discord mode: %v", err)
	}
	dev := map[string]string{"CHERRY_AUTH_MODE": "dev-fake", "CHERRY_LISTEN_STAFF": "127.0.0.1:18444", "CHERRY_STAFF_HOSTNAME": "127.0.0.1", "CHERRY_STAFF_OWNER": "dev-owner"}
	if _, err := loadWith(t, dev); err != nil {
		t.Errorf("a dev-fake owner: %v", err)
	}
	dev["CHERRY_STAFF_OWNER"] = "dev-bad name"
	if _, err := loadWith(t, dev); err == nil {
		t.Error("a dev owner with a space was accepted")
	}
	if !ValidDevName("a-b_9") || ValidDevName("") || ValidDevName("a b") || ValidDevName(strings.Repeat("x", 25)) {
		t.Error("ValidDevName accepts or refuses the wrong names")
	}
}

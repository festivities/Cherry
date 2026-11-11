package config

import (
	"flag"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func none(string) (string, bool) { return "", false }

func env(m map[string]string) func(string) (string, bool) {
	return func(k string) (string, bool) {
		v, ok := m[k]
		return v, ok
	}
}

func TestDefaultsAreTodaysConstants(t *testing.T) {
	c, err := Load(nil, none)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := Config{
		HTTPS: ":443", HTTP: ":80", Session: ":10123", Gateway: ":10000", DNS: ":53", Admin: "127.0.0.1:8099",
		AuthMode: AuthLabGuest, ServerName: "Cherry",
	}
	if c.HTTPS != want.HTTPS || c.HTTP != want.HTTP || c.Session != want.Session || c.Gateway != want.Gateway ||
		c.DNS != want.DNS || c.Admin != want.Admin || c.AuthMode != want.AuthMode || c.ServerName != want.ServerName || c.Public || len(c.Hostnames) != 0 {
		t.Fatalf("defaults = %+v, want listeners %+v", c, want)
	}
	base, err := os.UserCacheDir()
	if err != nil {
		t.Skipf("no user cache directory here: %v", err)
	}
	lab := filepath.Join(base, "Cherry")
	if c.DataDir != lab || c.CacheDir != lab {
		t.Fatalf("dirs = %q / %q, want both %q (the lab layout)", c.DataDir, c.CacheDir, lab)
	}
}

func TestPrecedenceFlagEnvFileDefault(t *testing.T) {
	file := filepath.Join(t.TempDir(), "cherry.json")
	if err := os.WriteFile(file, []byte(`{"listen.https": ":8443", "listen.http": ":8080", "listen.session": ":8123"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	e := map[string]string{"CHERRY_LISTEN_HTTPS": ":9443", "CHERRY_LISTEN_SESSION": ":9123"}
	c, err := Load([]string{"-config", file, "-listen-https=:7443"}, env(e))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.HTTPS != ":7443" {
		t.Errorf("https = %s, want the flag :7443", c.HTTPS)
	}
	if c.Session != ":9123" {
		t.Errorf("session = %s, want the environment :9123 over the file", c.Session)
	}
	if c.HTTP != ":8080" {
		t.Errorf("http = %s, want the file :8080", c.HTTP)
	}
	if c.Gateway != ":10000" {
		t.Errorf("gateway = %s, want the default :10000", c.Gateway)
	}
	if c.File != file {
		t.Errorf("file = %q, want %q", c.File, file)
	}
}

func TestConfigFileFromEnvironment(t *testing.T) {
	file := filepath.Join(t.TempDir(), "cherry.json")
	if err := os.WriteFile(file, []byte(`{"listen.gateway": ":20000"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := Load(nil, env(map[string]string{"CHERRY_CONFIG": file}))
	if err != nil || c.Gateway != ":20000" {
		t.Fatalf("gateway = %q, err %v; want :20000 from CHERRY_CONFIG", c.Gateway, err)
	}
}

func TestEmptyDNSTurnsItOff(t *testing.T) {
	for name, tc := range map[string]struct {
		args []string
		e    map[string]string
	}{
		"flag":        {[]string{"-listen-dns="}, nil},
		"environment": {nil, map[string]string{"CHERRY_LISTEN_DNS": ""}},
	} {
		c, err := Load(tc.args, env(tc.e))
		if err != nil || c.DNS != "" {
			t.Errorf("%s: DNS = %q, err %v; want off", name, c.DNS, err)
		}
	}
}

func TestDirsFromFlagsAreAbsolute(t *testing.T) {
	dir := t.TempDir()
	c, err := Load([]string{"-data-dir", dir, "-cache-dir", filepath.Join(dir, "c")}, none)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.DataDir != dir || c.CacheDir != filepath.Join(dir, "c") {
		t.Fatalf("dirs = %q / %q", c.DataDir, c.CacheDir)
	}
}

func TestHostnamesAreListed(t *testing.T) {
	c, err := Load([]string{"-server-hostnames", " Cherry.Example , ,other.example"}, none)
	if err != nil || len(c.Hostnames) != 2 || c.Hostnames[0] != "cherry.example" || c.Hostnames[1] != "other.example" {
		t.Fatalf("hostnames = %v, err %v", c.Hostnames, err)
	}
}

func TestValidationErrors(t *testing.T) {
	cases := []struct {
		name string
		args []string
		e    map[string]string
		want string
	}{
		{"public with the lab default", []string{"-server-public=true"}, nil, "server.public=true refuses auth.mode lab-guest"},
		{"public with dev-fake", []string{"-server-public=true", "-auth-mode=dev-fake"}, nil, "server.public=true refuses auth.mode dev-fake"},
		{"public with an explicit DNS address", []string{"-server-public=true", "-auth-mode=discord", "-listen-dns=:53"}, nil, "turns the DNS sink off"},
		{"public from the environment", nil, map[string]string{"CHERRY_SERVER_PUBLIC": "true"}, "server.public=true refuses auth.mode lab-guest"},
		{"discord alone", []string{"-auth-mode=discord"}, nil, "auth.mode discord needs auth.discord.client-id, auth.discord.secret-file, auth.discord.redirect"},
		{"public discord", []string{"-server-public=true", "-auth-mode=discord"}, nil, "auth.mode discord needs"},
		{"public dev-fake", []string{"-server-public=true", "-auth-mode", "dev-fake"}, nil, "server.public=true refuses auth.mode dev-fake"},
		{"unknown mode", []string{"-auth-mode=oauth"}, nil, `auth.mode "oauth" is unknown`},
		{"empty server name", []string{"-server-name="}, nil, "server.name must be 1 to 64"},
		{"long server name", []string{"-server-name=" + strings.Repeat("n", 65)}, nil, "server.name must be 1 to 64"},
		{"control character in server name", []string{"-server-name=a\tb"}, nil, "server.name must be 1 to 64"},
		{"bad boolean", []string{"-server-public=maybe"}, nil, "server.public: \"maybe\" is not true or false"},
		{"unexpected argument", []string{"stray"}, nil, "unexpected argument"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Load(tc.args, env(tc.e))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want it to contain %q", err, tc.want)
			}
		})
	}
}

// discordArgs returns a complete discord setting set; the secret file is private to the test.
func discordArgs(t *testing.T, mode os.FileMode) []string {
	t.Helper()
	file := filepath.Join(t.TempDir(), "discord.secret")
	if err := os.WriteFile(file, []byte("not-a-real-secret\n"), mode); err != nil {
		t.Fatal(err)
	}
	return []string{"-auth-mode=discord", "-auth-discord-client-id=123", "-auth-discord-secret-file=" + file,
		"-auth-discord-redirect=https://cherry.example/auth/v1/discord/callback"}
}

func TestDiscordStartsWithItsSettings(t *testing.T) {
	args := discordArgs(t, 0o600)
	c, err := Load(args, none)
	if err != nil || c.AuthMode != AuthDiscord || c.DiscordClientID != "123" || c.DiscordRedirect == "" || c.DiscordSecretFile == "" {
		t.Fatalf("Load = %+v, %v; want discord with its settings", c, err)
	}
	if c.ServerName != "Cherry" {
		t.Fatalf("server name = %q, want the default Cherry", c.ServerName)
	}
}

func TestDevFakeStarts(t *testing.T) {
	c, err := Load([]string{"-auth-mode=dev-fake"}, none)
	if err != nil || c.AuthMode != AuthDevFake {
		t.Fatalf("Load = %+v, %v; want dev-fake to start", c, err)
	}
}

func TestDiscordRefusesBadSettings(t *testing.T) {
	ok := discordArgs(t, 0o600)
	badRedirect := append([]string{}, ok...)
	badRedirect[3] = "-auth-discord-redirect=http://cherry.example/auth/v1/discord/callback"
	openSecret := discordArgs(t, 0o644)
	missingFile := append([]string{}, ok...)
	missingFile[2] = "-auth-discord-secret-file=" + filepath.Join(t.TempDir(), "absent")
	cases := map[string]struct {
		args []string
		want string
	}{
		"http redirect":   {badRedirect, "auth.discord.redirect: must be an absolute https URL"},
		"missing file":    {missingFile, "auth.discord.secret-file: cannot read the file"},
		"no client id":    {append(append([]string{}, ok[:1]...), ok[2:]...), "auth.mode discord needs auth.discord.client-id"},
		"secret in a dir": {[]string{"-auth-mode=discord", "-auth-discord-client-id=1", "-auth-discord-secret-file=" + t.TempDir(), "-auth-discord-redirect=https://a.example/cb"}, "not a regular file"},
	}
	for name, tc := range cases {
		_, err := Load(tc.args, none)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want it to contain %q", name, err, tc.want)
		}
	}
	if runtime.GOOS == "windows" {
		return // Windows has no mode bits to check; the file is still read by the server.
	}
	if _, err := Load(openSecret, none); err == nil || !strings.Contains(err.Error(), "readable by group or others") {
		t.Fatalf("0644 secret: err = %v, want a refusal", err)
	}
}

func TestCurrentFollowsActivate(t *testing.T) {
	if Current().AuthMode != "" {
		t.Fatalf("Current before Activate = %+v, want the zero value", Current())
	}
	c, err := Load([]string{"-auth-mode=dev-fake"}, none)
	if err != nil {
		t.Fatal(err)
	}
	Activate(c)
	t.Cleanup(func() { Activate(Config{}) })
	if Current().AuthMode != AuthDevFake {
		t.Fatalf("Current = %+v, want dev-fake", Current())
	}
}

func TestPublicForcesDNSOff(t *testing.T) {
	c, err := parse([]string{"-server-public=true", "-auth-mode=discord"}, none)
	if err != nil || !c.Public || c.DNS != "" {
		t.Fatalf("parse: public %v, DNS %q, err %v; want public with DNS off", c.Public, c.DNS, err)
	}
}

func TestFileRejectsUnknownKeys(t *testing.T) {
	file := filepath.Join(t.TempDir(), "cherry.json")
	if err := os.WriteFile(file, []byte(`{"listen.htps": ":443"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Load([]string{"-config", file}, none)
	if err == nil || !strings.Contains(err.Error(), `unknown key "listen.htps"`) {
		t.Fatalf("err = %v, want an unknown-key error", err)
	}
}

func TestUsageListsEveryFlag(t *testing.T) {
	if _, err := parse([]string{"-h"}, none); err != flag.ErrHelp {
		t.Fatalf("-h err = %v, want flag.ErrHelp", err)
	}
	var table strings.Builder
	usage(&table)
	for _, s := range settings {
		if !strings.Contains(table.String(), "-"+flagName(s.key)) || !strings.Contains(table.String(), envName(s.key)) {
			t.Errorf("table lacks %s", s.key)
		}
	}
}

// Two different data and cache directories put every package's paths under them. The per-package check lives in
// cmd/cherry (paths_test.go); here the resolver itself is checked.
func TestResolverFollowsActivate(t *testing.T) {
	data, cache := t.TempDir(), t.TempDir()
	c, err := Load([]string{"-data-dir", data, "-cache-dir", cache}, none)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	Activate(c)
	t.Cleanup(func() { Activate(Config{}) })
	if got, err := DataDir(); err != nil || got != data {
		t.Errorf("DataDir = %q, %v; want %q", got, err, data)
	}
	if got, err := CacheDir(); err != nil || got != cache {
		t.Errorf("CacheDir = %q, %v; want %q", got, err, cache)
	}
}

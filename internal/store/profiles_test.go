package store

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"cherry/data"
)

// profilesDir points the operator config directory at an empty temp dir (os.UserCacheDir reads
// %LOCALAPPDATA% on Windows and XDG_CACHE_HOME elsewhere) and resets the file when the test ends.
func profilesDir(t *testing.T) {
	t.Helper()
	base := t.TempDir()
	t.Setenv("LOCALAPPDATA", base)
	t.Setenv("XDG_CACHE_HOME", base)
	if err := os.MkdirAll(filepath.Join(base, "Cherry", "config"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(profilesFile.Reset)
}

// mutateProfiles returns the embedded default with each old/new pair replaced once, in order.
func mutateProfiles(t *testing.T, pairs ...string) string {
	t.Helper()
	out := string(data.ProfilesJSON)
	for i := 0; i < len(pairs); i += 2 {
		if !strings.Contains(out, pairs[i]) {
			t.Fatalf("default has no %q to mutate", pairs[i])
		}
		out = strings.Replace(out, pairs[i], pairs[i+1], 1)
	}
	return out
}

func TestProfilesDefaults(t *testing.T) {
	profilesDir(t)
	if err := LoadOperatorData(); err != nil {
		t.Fatalf("defaults: %v", err)
	}
	if got := FriendName(); got != "Friend" {
		t.Fatalf("FriendName = %q, want Friend", got)
	}
	if got := CuratedGrantCodes(); strings.Join(got, " ") != "CUHA0036Z CUON004TV CUSH00267 CUAH004JH" {
		t.Fatalf("CuratedGrantCodes = %v", got)
	}
}

func TestProfilesFileRefusals(t *testing.T) {
	t.Cleanup(profilesFile.Reset)
	cases := []struct{ text, want string }{
		{mutateProfiles(t, `"clone_name"`, `"clone_nam"`), "unknown field"},
		{mutateProfiles(t, `"Friend"`, `"  "`), "friend_name must not be empty"},
		{mutateProfiles(t, `"1151262555912366100": "MALE"`, `"1151262555912366100": "ROBOT"`), "type must be MALE, FEMALE or ANIMAL"},
		{mutateProfiles(t, `"1151262590612422506"`, `"11512x590612422506"`), "id must be digits"},
		{mutateProfiles(t, `"CUHA0036Z", `, `"CUHA0036", `), `lab_grant_codes: "CUHA0036" must be an item code`},
		{mutateProfiles(t, `"CUON004TV", `, `"CUHA0036Z", `), "listed once"},
	}
	for _, c := range cases {
		path := filepath.Join(t.TempDir(), "profiles.json")
		if err := os.WriteFile(path, []byte(c.text), 0o600); err != nil {
			t.Fatal(err)
		}
		err := profilesFile.LoadFrom(path)
		if err == nil || !strings.Contains(err.Error(), c.want) || !strings.Contains(err.Error(), path) {
			t.Fatalf("err = %v, want %q naming %s", err, c.want, path)
		}
	}
}

func TestValidItemCode(t *testing.T) {
	for code, want := range map[string]bool{
		"CUHA0036Z": true, "RUAB00002": true, "PUPE0000G": true,
		"CUHA0036": false, "CUHA0036ZZ": false, "cuha0036z": false, "CUHA0036-": false, "": false,
	} {
		if got := ValidItemCode(code); got != want {
			t.Errorf("ValidItemCode(%q) = %t, want %t", code, got, want)
		}
	}
}

package room

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"cherry/data"
	"cherry/internal/store"
)

// starterDir points the operator config directory at an empty temp dir and resets the file when the
// test ends (os.UserCacheDir reads %LOCALAPPDATA% on Windows and XDG_CACHE_HOME elsewhere).
func starterDir(t *testing.T) {
	t.Helper()
	base := t.TempDir()
	t.Setenv("LOCALAPPDATA", base)
	t.Setenv("XDG_CACHE_HOME", base)
	if err := os.MkdirAll(filepath.Join(base, "Cherry", "config"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(starterFile.Reset)
}

func TestStarterDefaults(t *testing.T) {
	starterDir(t)
	if err := LoadOperatorData(); err != nil {
		t.Fatalf("defaults: %v", err)
	}
	want := []string{RoomFloorCode, RoomWallCode, roomDoorCode, roomDiaryCode}
	if got := ActiveStarter().StarterCodes; !slices.Equal(got, want) {
		t.Fatalf("starter codes = %v, want %v", got, want)
	}
	if got := PetName(2); got != "Minipet 2" {
		t.Fatalf("PetName(2) = %q, want Minipet 2", got)
	}
}

func TestStarterFileRefusals(t *testing.T) {
	t.Cleanup(starterFile.Reset)
	mutate := func(pairs ...string) string {
		out := string(data.StarterJSON)
		for i := 0; i < len(pairs); i += 2 {
			if !strings.Contains(out, pairs[i]) {
				t.Fatalf("default has no %q to mutate", pairs[i])
			}
			out = strings.Replace(out, pairs[i], pairs[i+1], 1)
		}
		return out
	}
	cases := []struct{ text, want string }{
		{mutate(`"pet_name"`, `"pet_nam"`), "unknown field"},
		{mutate(`"RUDO0002Z", `, ``), "starter_codes must include RUDO0002Z"},
		{mutate(`"RUAB00002"]`, `"RUAB0000"]`), `starter_codes: "RUAB0000" must be an item code`},
		{mutate(`"RUDO0002Z", `, `"RUWA000HR", `), `starter_codes: "RUWA000HR" must be an item code, listed once`},
		{mutate(`"Minipet {n}"`, `"Minipet"`), "pet_name must contain {n}"},
	}
	for _, c := range cases {
		path := filepath.Join(t.TempDir(), "starter.json")
		if err := os.WriteFile(path, []byte(c.text), 0o600); err != nil {
			t.Fatal(err)
		}
		err := starterFile.LoadFrom(path)
		if err == nil || !strings.Contains(err.Error(), c.want) || !strings.Contains(err.Error(), path) {
			t.Fatalf("err = %v, want %q naming %s", err, c.want, path)
		}
	}
}

// TestStarterOverrideGrantsOperatorCodes: an operator starter file decides what a new room gets (in its
// order), and the pet name pattern it names.
func TestStarterOverrideGrantsOperatorCodes(t *testing.T) {
	t.Cleanup(starterFile.Reset)
	path := filepath.Join(t.TempDir(), "starter.json")
	text := `{"origin": "mixed", "note": "test", "starter_codes": ["RUTI000EW", "RUWA000HR", "RUDO0002Z", "RUAB00002", "RUTI0007H"], "pet_name": "Pet-{n}"}`
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := starterFile.LoadFrom(path); err != nil {
		t.Fatalf("override: %v", err)
	}
	acc := &store.Account{Aid: "7101", AccessToken: "starter-test"}
	if err := EnsureRoomLocked(acc); err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, it := range acc.RoomItems {
		got = append(got, it.Cd)
	}
	if want := []string{"RUTI000EW", "RUWA000HR", "RUDO0002Z", "RUAB00002", "RUTI0007H"}; !slices.Equal(got, want) {
		t.Fatalf("granted = %v, want %v", got, want)
	}
	if name := PetName(3); name != "Pet-3" {
		t.Fatalf("PetName(3) = %q, want Pet-3", name)
	}
}

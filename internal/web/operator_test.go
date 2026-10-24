package web

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"cherry/data"
)

// webOperatorDir points the operator config directory at an empty temp dir (os.UserCacheDir reads
// %LOCALAPPDATA% on Windows and XDG_CACHE_HOME elsewhere), resets the web files when the test ends, and
// returns the directory.
func webOperatorDir(t *testing.T) string {
	t.Helper()
	base := t.TempDir()
	t.Setenv("LOCALAPPDATA", base)
	t.Setenv("XDG_CACHE_HOME", base)
	dir := filepath.Join(base, "Cherry", "config")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		menuFile.Reset()
		dressupFile.Reset()
	})
	return dir
}

// mutateWeb returns the embedded default raw with each old/new pair replaced once, in order.
func mutateWeb(t *testing.T, raw []byte, pairs ...string) string {
	t.Helper()
	out := string(raw)
	for i := 0; i < len(pairs); i += 2 {
		if !strings.Contains(out, pairs[i]) {
			t.Fatalf("default has no %q to mutate", pairs[i])
		}
		out = strings.Replace(out, pairs[i], pairs[i+1], 1)
	}
	return out
}

// refuseWeb writes text as an operator file and loads it: the load must fail and name the file and the
// bad entry (want).
func refuseWeb(t *testing.T, load func(string) error, name, text, want string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	err := load(path)
	if err == nil || !strings.Contains(err.Error(), want) || !strings.Contains(err.Error(), path) {
		t.Fatalf("%s: err = %v, want %q naming %s", name, err, want, path)
	}
}

func TestWebOperatorDefaultsLoad(t *testing.T) {
	webOperatorDir(t)
	if err := LoadOperatorData(); err != nil {
		t.Fatalf("defaults: %v", err)
	}
	if rows := homeIconRows(); rows[0].Name != "Closet" || rows[0].LinkType != "goSomewhere(closet)" {
		t.Fatalf("first home row = %+v", rows[0])
	}
}

func TestMenuFileRefusals(t *testing.T) {
	t.Cleanup(menuFile.Reset)
	raw := data.MenuJSON
	cases := []struct{ text, want string }{
		{mutateWeb(t, raw, `"labels"`, `"label"`), "unknown field"},
		{mutateWeb(t, raw, `"goSomewhere(closet)": "Closet",`, ``), "no label for the home menu row goSomewhere(closet)"},
		{mutateWeb(t, raw, `"goSomewhere(closet)": "Closet",`, `"goSomewhere(closet)": "Closet", "goSomewhere(nope)": "Nope",`), `label "goSomewhere(nope)": no home menu row has that link type`},
		{mutateWeb(t, raw, `"Closet"`, `"  "`), "label for goSomewhere(closet) must not be empty"},
	}
	for _, c := range cases {
		refuseWeb(t, menuFile.LoadFrom, "menu.json", c.text, c.want)
	}
}

func TestDressupFileRefusals(t *testing.T) {
	t.Cleanup(dressupFile.Reset)
	raw := data.DressupJSON
	cases := []struct{ text, want string }{
		{mutateWeb(t, raw, `"EAR": ["CAEA0000E", "CAEA0000M"]`, `"EAR": ["CAEA0000E", "CAEA0000M"], "PAW": []`), `animal: category "PAW" must be one of`},
		{mutateWeb(t, raw, `"CUHA00053"`, `"CUHA0005"`), `male FHAIR: "CUHA0005" is not an item code`},
		{mutateWeb(t, raw, `"name": "Animal Set 1"`, `"name": ""`), "animal: set CATO000IU needs a name"},
		{`{"male": {"sets": []}, "female": {}, "animal": {}}`, "male: sets must list at least one look"},
	}
	for _, c := range cases {
		refuseWeb(t, dressupFile.LoadFrom, "dressup.json", c.text, c.want)
	}
}

// TestOperatorMenuAndLooksReachTheClient: operator files in the config dir change what the home menu and
// the creation look sets serve.
func TestOperatorMenuAndLooksReachTheClient(t *testing.T) {
	dir := webOperatorDir(t)
	write := func(name, text string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("menu.json", mutateWeb(t, data.MenuJSON, `"Closet"`, `"Wardrobe"`))
	write("dressup.json", mutateWeb(t, data.DressupJSON, `"name": "Animal Set 1"`, `"name": "Animal Kit 1"`))
	if err := LoadOperatorData(); err != nil {
		t.Fatalf("overrides: %v", err)
	}

	home := serve(t, http.MethodGet, "/v4/home/list/ext/10.1.0.0/Android")
	var body struct {
		Result struct {
			HomeIconList []struct {
				Name     string `json:"name"`
				LinkType string `json:"linkType"`
			} `json:"homeIconList"`
		} `json:"result"`
	}
	if err := json.Unmarshal(home.Body.Bytes(), &body); err != nil {
		t.Fatalf("home list is not JSON: %v", err)
	}
	if got := body.Result.HomeIconList[0]; got.Name != "Wardrobe" || got.LinkType != "goSomewhere(closet)" {
		t.Fatalf("first home row = %+v, want the operator label on the same link", got)
	}

	roll := serve(t, http.MethodGet, "/v4/create/face/setitems/roll")
	if !strings.Contains(roll.Body.String(), `"name":"Animal Kit 1"`) || strings.Contains(roll.Body.String(), `"name":"Animal Set 1"`) {
		t.Fatalf("roll does not carry the operator look set: %s", roll.Body.String())
	}
}

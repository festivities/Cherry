package economy

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// loadCherryTipsOverride writes body as the operator file and loads it (LoadFrom keeps the active file on error).
func loadCherryTipsOverride(t *testing.T, body string) error {
	t.Helper()
	path := filepath.Join(t.TempDir(), "cherrytips.json")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return cherryTipsFile.LoadFrom(path)
}

// TestCherryTipsDefaultIsCherryMade: the embedded default is Cherry-made, holds 3 to 5 tips and has no pictures.
func TestCherryTipsDefaultIsCherryMade(t *testing.T) {
	c := ActiveCherryTips()
	if c.Origin != "cherry" || strings.TrimSpace(c.Note) == "" {
		t.Fatalf("origin %q, note %q: the tips must be marked Cherry-made", c.Origin, c.Note)
	}
	if len(c.Tips) < 3 || len(c.Tips) > 5 {
		t.Fatalf("%d tips, want 3 to 5", len(c.Tips))
	}
	for _, tip := range c.Tips {
		if tip.Image != "" {
			t.Fatalf("tip %d image %q, want none", tip.BannerID, tip.Image)
		}
	}
}

// TestCherryTipsLoadValidation: a valid operator file replaces the default; each broken file is refused and
// leaves the active file unchanged.
func TestCherryTipsLoadValidation(t *testing.T) {
	t.Cleanup(cherryTipsFile.Reset)
	const tip = `{"bannerId":1,"title":"t","description":"d","image":""}`
	good := `{"origin":"cherry","note":"n","tips":[` + tip + `]}`
	if err := loadCherryTipsOverride(t, good); err != nil {
		t.Fatalf("valid override: %v", err)
	}
	if n := len(ActiveCherryTips().Tips); n != 1 {
		t.Fatalf("override not active: %d tips", n)
	}
	bad := map[string]string{
		"origin not cherry":  `{"origin":"op","note":"n","tips":[` + tip + `]}`,
		"no note":            `{"origin":"cherry","note":" ","tips":[` + tip + `]}`,
		"no tips":            `{"origin":"cherry","note":"n","tips":[]}`,
		"duplicate bannerId": `{"origin":"cherry","note":"n","tips":[` + tip + `,` + tip + `]}`,
		"empty title":        `{"origin":"cherry","note":"n","tips":[{"bannerId":1,"title":"","description":"d","image":""}]}`,
		"relative image":     `{"origin":"cherry","note":"n","tips":[{"bannerId":1,"title":"t","description":"d","image":"menu/x.png"}]}`,
		"http image":         `{"origin":"cherry","note":"n","tips":[{"bannerId":1,"title":"t","description":"d","image":"http://x/y.png"}]}`,
		"unknown key":        `{"origin":"cherry","note":"n","tips":[` + tip + `],"extra":1}`,
	}
	for name, body := range bad {
		t.Run(name, func(t *testing.T) {
			if err := loadCherryTipsOverride(t, good); err != nil {
				t.Fatalf("reload valid override: %v", err)
			}
			if err := loadCherryTipsOverride(t, body); err == nil {
				t.Fatal("broken file accepted")
			}
			if n := len(ActiveCherryTips().Tips); n != 1 {
				t.Fatalf("a refused file changed the active tips: %d", n)
			}
		})
	}
}

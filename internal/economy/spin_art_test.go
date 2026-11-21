package economy

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"cherry/internal/assets"
	"cherry/internal/testutil"
)

// TestSpinOrigArtIsServedByteForByte: every panel in Lucky/Jackpot Art and every Gem slot icon in data/spin.json
// exists in the robo cache and is served byte-for-byte via /cherry/spin/orig/...
func TestSpinOrigArtIsServedByteForByte(t *testing.T) {
	testutil.RequireArchive(t)
	sp := ActiveSpin()
	seen := map[string]bool{}
	var paths []string
	for _, g := range []struct {
		name string
		game SpinGame
	}{{"lucky", sp.Lucky}, {"jackpot", sp.Jackpot}} {
		for _, f := range g.game.Art.fields() {
			if f.path == "" {
				t.Fatalf("%s art.%s is empty in default spin.json", g.name, f.name)
			}
			if !seen[f.path] {
				seen[f.path] = true
				paths = append(paths, f.path)
			}
		}
		for i, tier := range g.game.Tiers {
			for j, slot := range tier.Slots {
				if slot.Item != "" {
					continue
				}
				if slot.Icon == "" {
					t.Fatalf("%s tier %d slot %d (%d Gems): icon is empty in default spin.json", g.name, i+1, j+1, slot.Gems)
				}
				if !seen[slot.Icon] {
					seen[slot.Icon] = true
					paths = append(paths, slot.Icon)
				}
			}
		}
	}
	for _, rel := range paths {
		want, err := os.ReadFile(filepath.Join(testutil.RoboCaches(), "cache", "luckyspin", filepath.FromSlash(rel)))
		if err != nil {
			t.Fatalf("missing robo file %s: %v", rel, err)
		}
		rec := httptest.NewRecorder()
		HandleSpinImage(rec, httptest.NewRequest(http.MethodGet, "/cherry/spin/orig/"+rel, nil))
		if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "image/png" || !bytes.Equal(rec.Body.Bytes(), want) {
			t.Errorf("GET /cherry/spin/orig/%s: status %d type %q matched=%v", rel, rec.Code, rec.Header().Get("Content-Type"), bytes.Equal(rec.Body.Bytes(), want))
		}
	}
}

// TestSpinOrigArtRefusesUnlisted: files present in cache/luckyspin/ that are not configured in ActiveSpin() return 404.
func TestSpinOrigArtRefusesUnlisted(t *testing.T) {
	testutil.RequireArchive(t)
	unlisted := "luckyspin_0/resource_1189_BG_IMG_en.png"
	if _, err := os.Stat(filepath.Join(testutil.RoboCaches(), "cache", "luckyspin", filepath.FromSlash(unlisted))); err != nil {
		t.Fatalf("expected %s in robo cache for test: %v", unlisted, err)
	}
	for _, path := range []string{
		"/cherry/spin/orig/" + unlisted,
		"/cherry/spin/orig/",
		"/cherry/spin/orig/luckyspin/unknown.png",
		"/cherry/spin/orig/../board.png",
	} {
		rec := httptest.NewRecorder()
		HandleSpinImage(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusNotFound {
			t.Errorf("GET %s: status %d, want 404", path, rec.Code)
		}
	}
}

// TestSpinArtPathValidation: checkSpin rejects traversal, backslashes, non-.png files, wrong folders and icons on item slots.
func TestSpinArtPathValidation(t *testing.T) {
	badPaths := []string{
		"luckyspin/../secret.png",
		"luckyspin_0/../luckyspin/x.png",
		"../luckyspin/x.png",
		`luckyspin\x.png`,
		"luckyspin/x.jpg",
		"luckyspin_0/resource.plist",
		"x.png",
		"other/x.png",
		"luckyspin/sub/x.png",
		"luckyspin_0/sub/x.png",
		"luckyspin/.png",
	}
	for _, bad := range badPaths {
		for _, set := range []struct {
			name   string
			mutate func(*Spin)
		}{
			{"lucky.art.bg", func(s *Spin) { s.Lucky.Art.BG = bad }},
			{"lucky.art.deco", func(s *Spin) { s.Lucky.Art.Deco = bad }},
			{"lucky.art.list_bg", func(s *Spin) { s.Lucky.Art.ListBG = bad }},
			{"lucky.art.detail_bg", func(s *Spin) { s.Lucky.Art.DetailBG = bad }},
			{"lucky.art.title_bg", func(s *Spin) { s.Lucky.Art.TitleBG = bad }},
			{"jackpot.art.bg", func(s *Spin) { s.Jackpot.Art.BG = bad }},
			{"gem slot icon", func(s *Spin) { s.Lucky.Tiers[0].Slots[0].Icon = bad }},
		} {
			s := spinDefault(t)
			set.mutate(s)
			if err := checkSpin(s); err == nil {
				t.Errorf("%s = %q: accepted, want error", set.name, bad)
			}
		}
	}

	s := spinDefault(t)
	s.Lucky.Tiers[0].Slots[5].Icon = "luckyspin/jackpotspin_gem_1_normal.png"
	if err := checkSpin(s); err == nil || !strings.Contains(err.Error(), "icon must be empty on an item slot") {
		t.Fatalf("icon on item slot: err = %v, want icon-on-item refusal", err)
	}
}

// TestSpinArtFallbackWithoutOrig: omitting art and slot icons falls back to Cherry's drawn panels and gem.png.
func TestSpinArtFallbackWithoutOrig(t *testing.T) {
	s := spinDefault(t)
	s.Lucky.Art = SpinArt{}
	s.Jackpot.Art = SpinArt{}
	for i := range s.Lucky.Tiers[0].Slots {
		s.Lucky.Tiers[0].Slots[i].Icon = ""
	}
	for i := range s.Jackpot.Tiers {
		for j := range s.Jackpot.Tiers[i].Slots {
			s.Jackpot.Tiers[i].Slots[j].Icon = ""
		}
	}
	raw, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "spin.json")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := spinFile.LoadFrom(path); err != nil {
		t.Fatalf("spin without orig art refused: %v", err)
	}
	t.Cleanup(spinFile.Reset)

	wantPanels := []string{"board.png", "deco.png", "list_bg.png", "detail_cover.png", "title_clear.png"}
	for _, jackpot := range []bool{false, true} {
		res := spinResources(jackpot)
		for i, panel := range wantPanels {
			if got := res[1+i].ImagePath; got != spinPicPrefix+panel {
				t.Errorf("jackpot=%v slot %d: got %q, want %q", jackpot, 1+i, got, spinPicPrefix+panel)
			}
			rec := httptest.NewRecorder()
			HandleSpinImage(rec, httptest.NewRequest(http.MethodGet, "/"+spinPicPrefix+panel, nil))
			if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "image/png" {
				t.Errorf("fallback %s: status %d type %q", panel, rec.Code, rec.Header().Get("Content-Type"))
			}
		}
	}
	if got := spinPrizeOf(ActiveSpin().Lucky.Tiers[0].Slots[0]).picture; got != spinPicPrefix+"gem.png" {
		t.Errorf("gem prize fallback = %q, want %q", got, spinPicPrefix+"gem.png")
	}
	rec := httptest.NewRecorder()
	HandleSpinImage(rec, httptest.NewRequest(http.MethodGet, "/cherry/spin/orig/luckyspin_0/resource_1761_BG_IMG_en.png", nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("omitted orig art served %d, want 404", rec.Code)
	}
}

// TestSpinVerifyMissingArt: verifySpin refuses missing art or icon files when an archive is present and skips when absent.
func TestSpinVerifyMissingArt(t *testing.T) {
	testutil.SetArchive(t, map[string][]string{})

	s := spinDefault(t)
	s.Lucky.Art.BG = "luckyspin_0/missing_bg.png"
	if err := verifySpin(s); err == nil || !strings.Contains(err.Error(), "missing_bg.png") {
		t.Fatalf("missing art.bg: err = %v, want missing_bg.png", err)
	}

	s = spinDefault(t)
	s.Lucky.Art = SpinArt{}
	s.Jackpot.Art = SpinArt{}
	s.Lucky.Tiers[0].Slots[0].Icon = "luckyspin/missing_gem.png"
	if err := verifySpin(s); err == nil || !strings.Contains(err.Error(), "missing_gem.png") {
		t.Fatalf("missing slot icon: err = %v, want missing_gem.png", err)
	}

	old := assets.Archive
	assets.Archive = nil
	t.Cleanup(func() { assets.Archive = old })
	if err := verifySpin(s); err != nil {
		t.Fatalf("no archive: err = %v, want nil", err)
	}
}

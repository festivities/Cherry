package main

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"cherry/internal/testutil"
)

// TestCacheTreesFromEveryRoot: every archive root lists each cache tree as a device source (a root without the folder
// adds nothing), and the static/cache/ mount holds the robo cache and lpbackup's cache.
func TestCacheTreesFromEveryRoot(t *testing.T) {
	n := 0
	for _, src := range origSources {
		if src.top == "cache" && src.device {
			n++
		}
	}
	if n != len(archiveRoots)*len(cacheTrees) {
		t.Fatalf("device cache sources = %d, want %d", n, len(archiveRoots)*len(cacheTrees))
	}
	mounts := origMounts()["static/cache/"]
	for _, want := range []string{filepath.Join(roboDataset, "cache"), filepath.Join(extraDataset, "cache")} {
		if !slices.Contains(mounts, want) {
			t.Errorf("static/cache/ mount lacks %s", want)
		}
	}
}

// TestDeviceRendersAreNotOriginals: the circle symbol-on-background composites are the client's renders and are never
// uploaded; the symbols, the backgrounds and the client's default pair are originals.
func TestDeviceRendersAreNotOriginals(t *testing.T) {
	for name, want := range map[string]bool{
		"circle_logo_sb_36_bg_18.png": true,
		"circle_logo_sb_1_bg_3.png":   true,
		"circle_logo_sb_36.png":       false,
		"circle_logo_bg_18.png":       false,
		"circle_logo_sd_dp.png":       false,
	} {
		if got := deviceRender.MatchString(name); got != want {
			t.Errorf("deviceRender(%q) = %v, want %v", name, got, want)
		}
	}
}

// TestCacheOriginsFromTheArchive: the origin list holds the robo dye tubes, lpbackup's two dye tubes and the 41 circle
// pictures (23 symbols and 18 backgrounds, no composite); no key is listed twice, and the origin check passes.
func TestCacheOriginsFromTheArchive(t *testing.T) {
	testutil.RequireArchive(t)
	objs, err := origObjects()
	if err != nil {
		t.Fatal(err)
	}
	keys := map[string]bool{}
	counts := map[string]int{}
	for _, o := range objs {
		if keys[o.key] {
			t.Fatalf("%s listed twice", o.key)
		}
		keys[o.key] = true
		if strings.HasPrefix(o.key, "static/cache/") {
			counts[strings.SplitN(o.key, "/", 4)[2]]++
		}
	}
	for _, k := range []string{
		"static/cache/dye/dye_56_1.png", "static/cache/dye/dye_90_1.png",
		"static/cache/circle_symbol_image/circle_logo_sb_36.png", "static/cache/circle_symbol_image/circle_logo_bg_18.png",
	} {
		if !keys[k] {
			t.Errorf("%s is not listed", k)
		}
	}
	if keys["static/cache/circle_symbol_image/circle_logo_sb_36_bg_18.png"] {
		t.Error("a client render is listed")
	}
	if counts["circle_symbol_image"] != 41 || counts["dye"] != 38 {
		t.Errorf("cache objects: circle_symbol_image %d (want 41), dye %d (want 38)", counts["circle_symbol_image"], counts["dye"])
	}
	if err := checkOrigins(objs, archiveRoots); err != nil {
		t.Fatal(err)
	}
}

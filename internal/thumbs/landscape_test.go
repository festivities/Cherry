package thumbs

import (
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"cherry/data"
)

// landscapeRefDir is the v6 reference output (landscape-gen-20261026 out/v6) in the temp folder. The pictures
// are derived from game art, so they stay out of the repository and the test skips without them.
func landscapeRefDir() string {
	return filepath.Join(os.TempDir(), "opencode", "cherry", "landscape-gen-20261026", "out", "v6")
}

// v6Pick is one map's entry in v6_objects.json: the picks the Python generator made.
type v6Pick struct {
	Window [2]int `json:"window"`
	Clouds int    `json:"clouds"`
	Stars  int    `json:"stars"`
	Fence  struct {
		Right *struct {
			Code string `json:"code"`
		} `json:"right"`
		Left *struct {
			Code string `json:"code"`
		} `json:"left"`
	} `json:"fence"`
	Prop *struct {
		Code string `json:"code"`
	} `json:"prop"`
}

// TestLandscapeMatchesV6 renders the 15 reference maps with their gardens.json sky and compares each picture
// with out/v6/dp_<stem>.png (mean absolute difference over RGBA, and the share of pixels off by more than 24
// in any channel) and each pick with v6_objects.json.
func TestLandscapeMatchesV6(t *testing.T) {
	dir := landscapeRefDir()
	raw, err := os.ReadFile(filepath.Join(dir, "v6_objects.json"))
	if err != nil {
		t.Skipf("no v6 reference: %v", err)
	}
	if _, ok := mapPackPath("1908231803"); !ok {
		t.Skip("no archive map packs")
	}
	var picks map[string]v6Pick
	if err := json.Unmarshal(raw, &picks); err != nil {
		t.Fatal(err)
	}
	var gardens struct {
		Gardens []GardenSkyEntry `json:"gardens"`
	}
	if err := json.Unmarshal([]byte(data.GardensJSON), &gardens); err != nil {
		t.Fatal(err)
	}
	skies := map[string]Sky{}
	for _, g := range gardens.Gardens {
		if g.Sky != nil {
			skies[g.Map] = skyOf(t, g)
		}
	}
	stems := make([]string, 0, len(picks))
	for stem := range picks {
		stems = append(stems, stem)
	}
	sort.Strings(stems)
	for _, stem := range stems {
		t.Run(stem, func(t *testing.T) {
			sky, ok := skies[stem]
			if !ok {
				t.Fatalf("gardens.json has no sky for %s", stem)
			}
			refPNG, err := os.ReadFile(filepath.Join(dir, "dp_"+stem+".png"))
			if err != nil {
				t.Skipf("no reference picture: %v", err)
			}
			ref, err := decodeNRGBA(refPNG)
			if err != nil {
				t.Fatal(err)
			}
			got, pick, err := renderLandscape(stem, sky)
			if err != nil {
				t.Fatal(err)
			}
			mean, far := landscapeDiff(got, ref)
			t.Logf("%s: mean abs diff %.2f per channel, %.2f%% of pixels off by more than 24", stem, mean, 100*far)
			want := picks[stem]
			if pick.Window != want.Window {
				t.Errorf("window %v, want %v", pick.Window, want.Window)
			}
			if pick.Clouds != want.Clouds || pick.Stars != want.Stars {
				t.Errorf("clouds %d stars %d, want %d %d", pick.Clouds, pick.Stars, want.Clouds, want.Stars)
			}
			wantR, wantL := "", ""
			if want.Fence.Right != nil {
				wantR = want.Fence.Right.Code
			}
			if want.Fence.Left != nil {
				wantL = want.Fence.Left.Code
			}
			if pick.FenceR != wantR || pick.FenceL != wantL {
				t.Errorf("fence right %q left %q, want %q %q", pick.FenceR, pick.FenceL, wantR, wantL)
			}
			wantProp := ""
			if want.Prop != nil {
				wantProp = want.Prop.Code
			}
			if pick.Prop != wantProp {
				t.Errorf("prop %q, want %q", pick.Prop, wantProp)
			}
			if mean > landscapeMaxMean || far > landscapeMaxFar {
				t.Errorf("picture differs: mean %.2f (max %.2f), %.2f%% of pixels off by more than 24 (max %.1f%%)",
					mean, landscapeMaxMean, 100*far, 100*landscapeMaxFar)
			}
		})
	}
}

// Asserted limits for TestLandscapeMatchesV6; the numbers are per stem in the test log.
const (
	landscapeMaxMean = 1.0   // achieved: 0.15..0.36
	landscapeMaxFar  = 0.015 // achieved: 0.74..0.88% (all on the hexagon outline, see the ponytail note in landscape.go)
)

// GardenSkyEntry is the part of a gardens.json entry the test reads.
type GardenSkyEntry struct {
	Map string `json:"map"`
	Sky *struct {
		Top    string `json:"top"`
		Bottom string `json:"bottom"`
		Stars  bool   `json:"stars"`
	} `json:"sky"`
}

// skyOf turns a gardens.json sky into a Sky, failing the test on a colour that is not #rrggbb.
func skyOf(t *testing.T, g GardenSkyEntry) Sky {
	return Sky{Top: testColour(t, g.Sky.Top), Bottom: testColour(t, g.Sky.Bottom), Stars: g.Sky.Stars}
}

// testColour reads #rrggbb as an opaque colour.
func testColour(t *testing.T, s string) color.NRGBA {
	var r, g, b uint8
	if _, err := fmt.Sscanf(s, "#%02x%02x%02x", &r, &g, &b); err != nil {
		t.Fatalf("colour %q: %v", s, err)
	}
	return color.NRGBA{R: r, G: g, B: b, A: 0xff}
}

// landscapeDiff is the mean absolute difference over all four channels and the share of pixels whose channels
// differ by more than 24 in some channel.
func landscapeDiff(got, want *image.NRGBA) (mean, far float64) {
	b := got.Bounds()
	var sum float64
	var n, bad int
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			g, w := got.NRGBAAt(x, y), want.NRGBAAt(x, y)
			gc := [4]int{int(g.R), int(g.G), int(g.B), int(g.A)}
			wc := [4]int{int(w.R), int(w.G), int(w.B), int(w.A)}
			over := false
			for c := range 4 {
				d := gc[c] - wc[c]
				if d < 0 {
					d = -d
				}
				sum += float64(d)
				if d > 24 {
					over = true
				}
			}
			if over {
				bad++
			}
			n++
		}
	}
	return sum / float64(4*n), float64(bad) / float64(n)
}

// TestLandscapeDPServesCache: a cached picture under DpCacheDir is returned as it is, with no archive read.
func TestLandscapeDPServesCache(t *testing.T) {
	old := DpCacheDir
	DpCacheDir = t.TempDir()
	t.Cleanup(func() { DpCacheDir = old })
	sky := Sky{Top: color.NRGBA{R: 1, A: 0xff}, Bottom: color.NRGBA{B: 2, A: 0xff}}
	if err := os.WriteFile(dpCachePath("landscape", "1908231804-"+sky.key()), []byte("cached"), 0o644); err != nil {
		t.Fatal(err)
	}
	b, err := LandscapeDP("1908231804", sky)
	if err != nil || string(b) != "cached" {
		t.Fatalf("LandscapeDP = %q, %v; want the cached bytes", b, err)
	}
	if sky.key() == (Sky{Top: color.NRGBA{R: 9, A: 0xff}, Bottom: color.NRGBA{B: 2, A: 0xff}}).key() {
		t.Fatal("two different skies share a cache key")
	}
}

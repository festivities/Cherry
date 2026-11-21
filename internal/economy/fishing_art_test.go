package economy

import (
	"bytes"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"cherry/data"
	"cherry/internal/assets"
	"cherry/internal/opdata"
	"cherry/internal/testutil"
)

// TestFishingOrigArtIsServedByteForByte verifies that all 80 original fish PNGs and 8 original
// gear PNGs exist in testutil.RoboCaches()/cache/fishing/ and are served byte-for-byte via
// /cherry/fish/orig/... and /cherry/fishing/orig/..., while Porcupinefish (4172) serves the
// tinted silhouette fallback.
func TestFishingOrigArtIsServedByteForByte(t *testing.T) {
	testutil.RequireArchive(t)
	fishOrigCount := 0
	for _, fd := range ActiveFishing().Fish {
		if fd.Image == "" {
			if fd.ID != 4172 {
				t.Fatalf("unexpected empty image on fish %d (%s)", fd.ID, fd.Name)
			}
			rec := httptest.NewRecorder()
			HandleFishImage(rec, httptest.NewRequest(http.MethodGet, "/cherry/fish/4172.png", nil))
			if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "image/png" {
				t.Fatalf("porcupinefish fallback = %d %q", rec.Code, rec.Header().Get("Content-Type"))
			}
			if _, err := png.Decode(rec.Body); err != nil {
				t.Fatalf("porcupinefish silhouette invalid PNG: %v", err)
			}
			continue
		}
		fishOrigCount++
		want, err := os.ReadFile(filepath.Join(testutil.RoboCaches(), "cache", "fishing", fd.Image))
		if err != nil {
			t.Fatalf("missing robo fish PNG %s for %d (%s): %v", fd.Image, fd.ID, fd.Name, err)
		}
		rec := httptest.NewRecorder()
		HandleFishImage(rec, httptest.NewRequest(http.MethodGet, "/cherry/fish/orig/"+fd.Image, nil))
		if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "image/png" || !bytes.Equal(rec.Body.Bytes(), want) {
			t.Errorf("GET /cherry/fish/orig/%s: status %d type %q matched=%v", fd.Image, rec.Code, rec.Header().Get("Content-Type"), bytes.Equal(rec.Body.Bytes(), want))
		}
	}
	if fishOrigCount != 80 {
		t.Fatalf("fish with original PNGs = %d, want 80", fishOrigCount)
	}

	seenGear := map[string]bool{}
	for _, it := range ActiveFishing().Shop {
		for _, img := range []string{it.ShopImage, it.InvenImage} {
			if img == "" || seenGear[img] {
				continue
			}
			seenGear[img] = true
			want, err := os.ReadFile(filepath.Join(testutil.RoboCaches(), "cache", "fishing", img))
			if err != nil {
				t.Fatalf("missing robo gear PNG %s for %d (%s): %v", img, it.ID, it.Name, err)
			}
			rec := httptest.NewRecorder()
			HandleFishingItemImage(rec, httptest.NewRequest(http.MethodGet, "/cherry/fishing/orig/"+img, nil))
			if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "image/png" || !bytes.Equal(rec.Body.Bytes(), want) {
				t.Errorf("GET /cherry/fishing/orig/%s: status %d type %q matched=%v", img, rec.Code, rec.Header().Get("Content-Type"), bytes.Equal(rec.Body.Bytes(), want))
			}
		}
	}
	if len(seenGear) != 8 {
		t.Fatalf("gear original PNG count = %d, want 8", len(seenGear))
	}
}

// TestFishingOrigArtRefusesUnlisted ensures unlisted filenames, traversal paths, and gear/fish
// cross-requests return 404.
func TestFishingOrigArtRefusesUnlisted(t *testing.T) {
	testutil.RequireArchive(t)
	for _, path := range []string{
		"/cherry/fish/orig/",
		"/cherry/fish/orig/not_a_fish.png",
		"/cherry/fish/orig/../fish_1_1613979947.png",
		"/cherry/fish/orig/shop_item_2_1613978375.png", // gear file on fish route
	} {
		rec := httptest.NewRecorder()
		HandleFishImage(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusNotFound {
			t.Errorf("GET %s: status %d, want 404", path, rec.Code)
		}
	}
	for _, path := range []string{
		"/cherry/fishing/orig/",
		"/cherry/fishing/orig/not_gear.png",
		"/cherry/fishing/orig/../shop_item_2_1613978375.png",
		"/cherry/fishing/orig/fish_1_1613979947.png", // fish file on gear route
	} {
		rec := httptest.NewRecorder()
		HandleFishingItemImage(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusNotFound {
			t.Errorf("GET %s: status %d, want 404", path, rec.Code)
		}
	}
}

// TestFishingVerifyMissingArtOnlyWithArchive checks verifyFishing rejects missing PNGs when an
// asset store is attached, and skips when none is attached.
func TestFishingVerifyMissingArtOnlyWithArchive(t *testing.T) {
	f := *ActiveFishing()
	f.Fish = slices.Clone(f.Fish)
	f.Fish[0].Image = "fish_999_missing.png"
	testutil.SetArchive(t, map[string][]string{})
	if err := verifyFishing(&f); err == nil || !strings.Contains(err.Error(), "fish_999_missing.png") {
		t.Fatalf("missing fish PNG: err = %v", err)
	}
	f = *ActiveFishing()
	f.Fish = slices.Clone(f.Fish)
	for i := range f.Fish {
		f.Fish[i].Image = ""
	}
	f.Shop = slices.Clone(f.Shop)
	for i := range f.Shop {
		f.Shop[i].ShopImage, f.Shop[i].InvenImage = "", ""
	}
	f.Shop[2].ShopImage = "shop_missing.png"
	if err := verifyFishing(&f); err == nil || !strings.Contains(err.Error(), "shop_missing.png") {
		t.Fatalf("missing shop PNG: err = %v", err)
	}

	old := assets.Archive
	assets.Archive = nil
	t.Cleanup(func() { assets.Archive = old })
	if err := verifyFishing(&f); err != nil {
		t.Fatalf("no asset store: err = %v, want nil", err)
	}
}

// TestFishingValidation checks checkFishing rejects invalid origins, negative rewards/retry,
// unclean image filenames, and invalid basic/paid shop prices.
func TestFishingValidation(t *testing.T) {
	raw := string(data.FishingJSON)
	cases := []struct{ text, want string }{
		{strings.Replace(raw, `"origin": "mixed"`, `"origin": "cherry"`, 1), "origin"},
		{strings.Replace(raw, `"page_reward_gems": 100`, `"page_reward_gems": -1`, 1), "page_reward_gems"},
		{strings.Replace(raw, `"retry_gems": 1250`, `"retry_gems": -1`, 1), "retry_gems"},
		{strings.Replace(raw, `"fish_1_1613979947.png"`, `"../fish_1_1613979947.png"`, 1), "clean .png filename"},
		{strings.Replace(raw, `"shop_item_2_1613978375.png"`, `"sub/shop.png"`, 1), "clean .png filename"},
		{strings.Replace(raw, `"basic": true, "price": 0`, `"basic": true, "price": 10`, 1), "basic item must have price 0"},
		{strings.Replace(raw, `"price": 150, "uses": 20`, `"price": 0, "uses": 20`, 1), "non-basic item must have price at least 1"},
	}
	for _, c := range cases {
		if _, err := opdata.Decode([]byte(c.text), checkFishing); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("err = %v, want %q", err, c.want)
		}
	}
}

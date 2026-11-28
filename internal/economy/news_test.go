package economy

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"cherry/data"
	"cherry/internal/assets"
	"cherry/internal/opdata"
)

const sampleNewsBanner = `{"id": 1970000001, "category": "Event", "title": "Cherry News Banner", "link": "openWithBrowser", "url": "https://discord.gg/example", "image": "", "background": "#1c3a5e", "icon": "", "ending_soon": false, "start": "", "end": ""}`

func newsFileText(includeSeason, includeRec bool, recMax int, banners ...string) string {
	return `{"origin": "cherry", "note": "test news config", "include_season_banners": ` +
		strconv.FormatBool(includeSeason) + `, "include_recommended_posts": ` + strconv.FormatBool(includeRec) +
		`, "recommended_max": ` + strconv.Itoa(recMax) + `, "banners": [` + strings.Join(banners, ",") + `]}`
}

func TestNewsDefaultConfig(t *testing.T) {
	cfg, err := opdata.Decode(data.NewsJSON, checkNewsConfig)
	if err != nil {
		t.Fatalf("embedded news.json refused: %v", err)
	}
	if cfg.Origin != opdata.OriginCherry || strings.TrimSpace(cfg.Note) == "" {
		t.Fatalf("provenance = %+v", cfg.Provenance)
	}
	if !cfg.IncludeSeasonBanners || !cfg.IncludeRecommendedPosts || cfg.RecommendedMax != 3 || len(cfg.Banners) != 0 || cfg.BestWindowHours != 72 || cfg.BestLikePoints != 1 || cfg.BestCommentPoints != 2 {
		t.Fatalf("unexpected default config = %+v", cfg)
	}
}

func TestNewsConfigValidation(t *testing.T) {
	if _, err := opdata.Decode([]byte(newsFileText(true, true, 3, sampleNewsBanner)), checkNewsConfig); err != nil {
		t.Fatalf("valid banner refused: %v", err)
	}
	m := func(pairs ...string) string { return mutateDefault(t, []byte(sampleNewsBanner), pairs...) }
	cases := []struct {
		name string
		doc  string
		want string
	}{
		{"wrong origin", strings.Replace(newsFileText(true, true, 3), `"origin": "cherry"`, `"origin": "mixed"`, 1), `origin must be "cherry"`},
		{"recommended_max negative", `{"origin": "cherry", "note": "test", "include_season_banners": true, "include_recommended_posts": true, "recommended_max": -1, "banners": []}`, "recommended_max must be 0 to 10"},
		{"recommended_max too high", `{"origin": "cherry", "note": "test", "include_season_banners": true, "include_recommended_posts": true, "recommended_max": 11, "banners": []}`, "recommended_max must be 0 to 10"},
		{"best_window_hours negative", `{"origin": "cherry", "note": "test", "include_season_banners": true, "include_recommended_posts": true, "recommended_max": 3, "best_window_hours": -1, "banners": []}`, "best_window_hours must be 0 to 8760"},
		{"best_comment_points too high", `{"origin": "cherry", "note": "test", "include_season_banners": true, "include_recommended_posts": true, "recommended_max": 3, "best_comment_points": 101, "banners": []}`, "best_like_points and best_comment_points must be 0 to 100"},
		{"id below operator block", newsFileText(true, true, 3, m(`"id": 1970000001`, `"id": 1969999999`)), "id must be inside the operator block"},
		{"id in season reserved block", newsFileText(true, true, 3, m(`"id": 1970000001`, `"id": 1970000900`)), "id must be inside the operator block"},
		{"duplicate id", newsFileText(true, true, 3, sampleNewsBanner, sampleNewsBanner), "id must be unique"},
		{"empty category", newsFileText(true, true, 3, m(`"category": "Event"`, `"category": "  "`)), "category must be 1 to 40"},
		{"empty title", newsFileText(true, true, 3, m(`"title": "Cherry News Banner"`, `"title": ""`)), "title must be 1 to 200"},
		{"http url", newsFileText(true, true, 3, m(`"url": "https://discord.gg/example"`, `"url": "http://discord.gg/example"`)), "must be an absolute https URL"},
		{"url with userinfo", newsFileText(true, true, 3, m(`"url": "https://discord.gg/example"`, `"url": "https://user@discord.gg/example"`)), "must not carry user information"},
		{"webview link refused", newsFileText(true, true, 3, m(`"link": "openWithBrowser"`, `"link": "goSomewhere(square)"`)), "must be openWithBrowser"},
		{"native link with url", newsFileText(true, true, 3, m(`"link": "openWithBrowser"`, `"link": "goSomewhere(gachaShop)"`)), "url is only for openWithBrowser"},
		{"bad background", newsFileText(true, true, 3, m(`"background": "#1c3a5e"`, `"background": "blue"`)), "background:"},
		{"bad image path", newsFileText(true, true, 3, m(`"image": ""`, `"image": "../a.png"`)), "must be a file name in config/news"},
		{"bad icon path", newsFileText(true, true, 3, m(`"icon": ""`, `"icon": "../bad.png"`)), "bare .png filename"},
		{"start after end", newsFileText(true, true, 3, m(`"start": ""`, `"start": "2026-11-30"`, `"end": ""`, `"end": "2026-11-20"`)), "start is after end"},
	}
	for _, tc := range cases {
		if _, err := opdata.Decode([]byte(tc.doc), checkNewsConfig); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want substring %q", tc.name, err, tc.want)
		}
	}
	// Empty background defaults cleanly to #4A90D9.
	cfg, err := opdata.Decode([]byte(newsFileText(true, true, 3, m(`"background": "#1c3a5e"`, `"background": ""`))), checkNewsConfig)
	if err != nil || cfg.Banners[0].Background != "#4A90D9" {
		t.Fatalf("empty background default = %+v, err=%v", cfg, err)
	}
}

func TestNewsVerifyIcons(t *testing.T) {
	prevStore := assets.Archive
	prevFS := newsIconFS
	t.Cleanup(func() {
		assets.Archive = prevStore
		newsIconFS = prevFS
	})
	assets.Archive = &assets.Store{}
	newsIconFS = fstest.MapFS{"good_icon.png": &fstest.MapFile{Data: []byte("png")}}

	goodBanner := mutateDefault(t, []byte(sampleNewsBanner), `"icon": ""`, `"icon": "good_icon.png"`)
	cfg, err := opdata.Decode([]byte(newsFileText(true, true, 3, goodBanner)), checkNewsConfig)
	if err != nil {
		t.Fatal(err)
	}
	if err := verifyNewsIcons(cfg); err != nil {
		t.Fatalf("verifyNewsIcons valid icon = %v", err)
	}
	badBanner := mutateDefault(t, []byte(sampleNewsBanner), `"icon": ""`, `"icon": "missing_icon.png"`)
	badCfg, err := opdata.Decode([]byte(newsFileText(true, true, 3, badBanner)), checkNewsConfig)
	if err != nil {
		t.Fatal(err)
	}
	if err := verifyNewsIcons(badCfg); err == nil || !strings.Contains(err.Error(), "missing_icon.png") {
		t.Fatalf("verifyNewsIcons missing icon = %v", err)
	}
}

func TestActiveNewsBannersSeasonAndImages(t *testing.T) {
	dir := operatorDir(t)
	t.Cleanup(newsFile.Reset)
	t.Cleanup(func() { seasonOverride.Store(nil) })

	prevFS := newsIconFS
	t.Cleanup(func() { newsIconFS = prevFS })
	newsIconFS = fstest.MapFS{"news_icn.png": &fstest.MapFile{Data: []byte("ICONPNG")}}

	if err := os.MkdirAll(filepath.Join(dir, "news"), 0o755); err != nil {
		t.Fatal(err)
	}
	img := image.NewNRGBA(image.Rect(0, 0, 16, 12))
	img.SetNRGBA(0, 0, color.NRGBA{R: 255, A: 255})
	var imgBuf bytes.Buffer
	if err := png.Encode(&imgBuf, img); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "news", "custom.png"), imgBuf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}

	b1 := `{"id": 1970000001, "category": "Notice", "title": "Placeholder Banner", "link": "goSomewhere(gachaShop)", "url": "", "image": "", "background": "#1c3a5e", "icon": "news_icn.png", "ending_soon": false, "start": "2026-10-25", "end": "2026-10-31"}`
	b2 := `{"id": 1970000002, "category": "Photo", "title": "Custom Image", "link": "openWithBrowser", "url": "https://discord.gg/example", "image": "custom.png", "background": "#4A90D9", "icon": "", "ending_soon": true, "start": "", "end": ""}`
	path := filepath.Join(dir, "news.json")
	if err := os.WriteFile(path, []byte(newsFileText(true, true, 3, b1, b2)), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := newsFile.LoadFrom(path); err != nil {
		t.Fatal(err)
	}

	// Inside Halloween window (2026-10-28 12:00 JST) -> b1, b2, and halloween season banner.
	inWindow := time.Date(2026, 10, 28, 12, 0, 0, 0, jst)
	banners := ActiveNewsBanners(inWindow)
	if len(banners) != 3 {
		t.Fatalf("banners len = %d, want 3: %+v", len(banners), banners)
	}
	if banners[0].BannerID != 1970000001 || banners[0].IconPath != "cherry/news/icon/news_icn.png" {
		t.Fatalf("banner[0] = %+v", banners[0])
	}
	if banners[1].BannerID != 1970000002 || !banners[1].EndingSoon || !strings.HasPrefix(banners[1].Image, "cherry/news/1970000002-") {
		t.Fatalf("banner[1] = %+v", banners[1])
	}
	if banners[2].BannerID < CherryNewsBannerSeasonFirst || banners[2].Image != "cherry/event/halloween.png" || banners[2].EndingSoon {
		t.Fatalf("season banner = %+v", banners[2])
	}

	// Near end of Halloween window (2026-11-06 12:00 JST) -> EndingSoon true.
	nearEnd := time.Date(2026, 11, 6, 12, 0, 0, 0, jst)
	endBanners := ActiveNewsBanners(nearEnd)
	if len(endBanners) != 2 || !endBanners[1].EndingSoon { // b1 expired, b2 + halloween remain
		t.Fatalf("nearEnd banners = %+v", endBanners)
	}

	// Serve placeholder image, custom image, icon image, and reject bad hash/unreferenced icon.
	for _, b := range banners[:2] {
		rec := httptest.NewRecorder()
		HandleNewsImage(rec, httptest.NewRequest(http.MethodGet, "/"+b.Image, nil))
		if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "image/png" {
			t.Fatalf("GET /%s = %d", b.Image, rec.Code)
		}
		cfg, err := png.DecodeConfig(bytes.NewReader(rec.Body.Bytes()))
		if err != nil {
			t.Fatalf("decode /%s: %v", b.Image, err)
		}
		if b.BannerID == 1970000001 && (cfg.Width != newsPlaceholderWidth || cfg.Height != newsPlaceholderHeight) {
			t.Fatalf("placeholder size = %dx%d, want %dx%d", cfg.Width, cfg.Height, newsPlaceholderWidth, newsPlaceholderHeight)
		}
		if b.BannerID == 1970000002 && (cfg.Width != 16 || cfg.Height != 12) {
			t.Fatalf("custom image size = %dx%d, want 16x12", cfg.Width, cfg.Height)
		}
	}
	rec := httptest.NewRecorder()
	HandleNewsImage(rec, httptest.NewRequest(http.MethodGet, "/cherry/news/1970000001-deadbeef.png", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("wrong hash = %d, want 404", rec.Code)
	}
	rec = httptest.NewRecorder()
	HandleNewsImage(rec, httptest.NewRequest(http.MethodGet, "/cherry/news/icon/news_icn.png", nil))
	if rec.Code != http.StatusOK || rec.Body.String() != "ICONPNG" {
		t.Fatalf("icon = %d %q", rec.Code, rec.Body.String())
	}
	rec = httptest.NewRecorder()
	HandleNewsImage(rec, httptest.NewRequest(http.MethodGet, "/cherry/news/icon/other.png", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unreferenced icon = %d, want 404", rec.Code)
	}
}

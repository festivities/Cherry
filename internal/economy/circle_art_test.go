package economy

import (
	"io/fs"
	"path"
	"strings"
	"testing"

	"cherry/internal/store"
	"cherry/internal/testutil"
)

// TestCircleArtOK: a logo or background is a .png directly under one of the two picture folders.
func TestCircleArtOK(t *testing.T) {
	for art, want := range map[string]bool{
		"UIResource/UIImage_hd/circle_logo_sd_dp.png":       true,
		"cache/circle_symbol_image/circle_logo_sb_36.png":   true,
		"cache/circle_symbol_image/circle_logo_sb_36.jpg":   false,
		"cache/circle_symbol_image/x/circle_logo_sb_36.png": false,
		"cache/circle_symbol_image/../../ui/x.png":          false,
		"circle_logo_sb_36.png":                             false,
		"cache/dye/dye_56_1.png":                            false,
	} {
		if got := circleArtOK(art); got != want {
			t.Errorf("circleArtOK(%q) = %v, want %v", art, got, want)
		}
	}
}

// TestCircleLogoListRows: the logo and background lists offer the client's default pair first and then every symbol and
// background of the robo cache, each under its file name; every picture is in the store, and no list offers one of the
// client's own symbol-on-background renders.
func TestCircleLogoListRows(t *testing.T) {
	testutil.RequireArchive(t)
	d := ActiveCircleData()
	if len(d.Logos) != 24 || len(d.Backgrounds) != 19 {
		t.Fatalf("logos %d, backgrounds %d; want 24 and 19", len(d.Logos), len(d.Backgrounds))
	}
	rows := circleLogoView(d.Logos)
	if rows[0].LogoID != 1940001001 || rows[0].LogoImagePath != "cherry/circle/circle_logo_sd_dp.png" {
		t.Fatalf("first logo row = %+v", rows[0])
	}
	if rows[1].LogoID != 1940001002 || rows[1].LogoImagePath != "cherry/circle/circle_logo_sb_1.png" {
		t.Fatalf("second logo row = %+v", rows[1])
	}
	for _, a := range append(append([]CircleArt(nil), d.Logos...), d.Backgrounds...) {
		if _, err := fs.Stat(circleArtFS, a.Art); err != nil {
			t.Errorf("%s: %v", a.Art, err)
		}
		if strings.Contains(a.Art, "_sb_") && strings.Contains(a.Art, "_bg_") {
			t.Errorf("%s is a client render, not an offered picture", a.Art)
		}
	}
}

// TestCircleSavesANewLogoAndBackground: a circle created with a symbol and a background of the robo cache keeps them, and a
// later modify saves another pair; the circle info names each picture by its file name.
func TestCircleSavesANewLogoAndBackground(t *testing.T) {
	circFixture(t, map[string]int64{"1101": 6000})
	req := circValidCreate("Pictures")
	req.LogoID, req.LogoBgID = 1940001019, 1940001111 // circle_logo_sb_36 on circle_logo_bg_18
	if _, err := CreateCircle("ctok-1101", req, circNow); err != nil {
		t.Fatal(err)
	}
	info := func() circleInfoJSON {
		store.CirclesMu.Lock()
		defer store.CirclesMu.Unlock()
		return circleInfoView(&store.CircleState.Circles[0])
	}
	if got := info(); got.LogoID != 1940001019 || got.LogoImgPath != "cherry/circle/circle_logo_sb_36.png" ||
		got.LogoBgID != 1940001111 || got.LogoBgImgPath != "cherry/circle/circle_logo_bg_18.png" {
		t.Fatalf("after create: %+v", got)
	}
	mod := CircleModifyRequest{CircleID: 1940000001, CircleName: "Pictures", CircleIntroduce: "hi", LogoID: 1940001023, LogoBgID: 1940001102, JoinType: "OPEN", InterestCodeList: []int64{201}}
	if err := ModifyCircle("ctok-1101", mod, circNow); err != nil {
		t.Fatalf("modify: %v", err)
	}
	if got := info(); got.LogoImgPath != "cherry/circle/circle_logo_sb_43.png" || got.LogoBgImgPath != "cherry/circle/circle_logo_bg_3.png" {
		t.Fatalf("after modify: %+v", got)
	}
	if got := path.Base(info().LogoImgPath); got != "circle_logo_sb_43.png" {
		t.Fatalf("client cache name %q", got)
	}
}

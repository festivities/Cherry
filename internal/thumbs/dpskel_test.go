package thumbs

import (
	"bytes"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"testing"
)

// The idle rig turns the arm nodes: the t=0 rotation keys are +65 degrees for the left
// arm and -66 for the right (Cocos clockwise), so each sleeve hangs at its side. Drawn
// unturned, the same sleeve sticks out horizontally above the body (the doubled sleeves).

// dpNormMask crops img to its alpha>=50% bounding box and box-samples the crop to an
// n x n coverage mask, so offset and scale drop out: a 140x140 archive preview and a
// 130x100 composite compare directly.
func dpNormMask(img image.Image, n int) []bool {
	b := img.Bounds()
	minX, minY, maxX, maxY := b.Max.X, b.Max.Y, b.Min.X, b.Min.Y
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			if _, _, _, a := img.At(x, y).RGBA(); a >= 0x8000 {
				minX, minY = min(minX, x), min(minY, y)
				maxX, maxY = max(maxX, x+1), max(maxY, y+1)
			}
		}
	}
	out := make([]bool, n*n)
	if maxX <= minX || maxY <= minY {
		return out
	}
	w, h := float64(maxX-minX), float64(maxY-minY)
	cov, cnt := make([]float64, n*n), make([]float64, n*n)
	for y := minY; y < maxY; y++ {
		for x := minX; x < maxX; x++ {
			_, _, _, a := img.At(x, y).RGBA()
			i := int((float64(y-minY)+0.5)/h*float64(n))*n + int((float64(x-minX)+0.5)/w*float64(n))
			cnt[i]++
			if a >= 0x8000 {
				cov[i]++
			}
		}
	}
	for i := range out {
		out[i] = cnt[i] > 0 && cov[i]/cnt[i] >= 0.5
	}
	return out
}

func dpMaskIoU(a, b []bool) float64 {
	inter, uni := 0, 0
	for i := range a {
		if a[i] && b[i] {
			inter++
		}
		if a[i] || b[i] {
			uni++
		}
	}
	if uni == 0 {
		return 0
	}
	return float64(inter) / float64(uni)
}

// TestDpSleevesHangLikeThe2014Previews renders the 14 dress items that have arm parts and a
// 2014 preview in the archive (ids 235... use the animal rig) and compares each with its
// preview. Before the idle turn the mean was 0.675; after it 0.740. Skipped without the archives.
func TestDpSleevesHangLikeThe2014Previews(t *testing.T) {
	if _, err := os.Stat(DpZipPath); err != nil {
		t.Skipf("2014 item archive absent: %v", err)
	}
	if _, err := os.Stat(DpItemRoot); err != nil {
		t.Skipf("item archive absent: %v", err)
	}
	ids := []string{"225000263", "225000386", "225000445", "225000707", "225001047", "225101516",
		"225101705", "225101745", "225101900", "225101906", "225502692", "235000603", "235000678", "235502058"}
	sum, n := 0.0, 0
	for _, id := range ids {
		zb, ok := dpZipPreview("dress", id)
		if !ok {
			t.Fatalf("dress/%s: no 2014 preview in the archive", id)
		}
		ref, err := png.Decode(bytes.NewReader(zb))
		if err != nil {
			t.Fatalf("dress/%s: decode 2014 preview: %v", id, err)
		}
		img, ok := composeItemDP(filepath.Join(DpItemRoot, "dress", id), "dress")
		if !ok {
			t.Fatalf("dress/%s: no composite", id)
		}
		iou := dpMaskIoU(dpNormMask(ref, 48), dpNormMask(img, 48))
		t.Logf("dress/%s: normalized IoU vs 2014 preview %.3f", id, iou)
		sum += iou
		n++
	}
	if mean := sum / float64(n); mean < 0.71 {
		t.Errorf("mean normalized IoU vs 2014 previews %.3f over %d items, want >= 0.71 (sleeves raised?)", mean, n)
	} else {
		t.Logf("mean normalized IoU %.3f over %d items", mean, n)
	}
}

// TestDpIdleArmsHangDown checks the rig: each upper arm's free end (the sprite end away
// from the shoulder) sits well below the shoulder pivot once the idle turn is applied.
func TestDpIdleArmsHangDown(t *testing.T) {
	rig := dpLoadRig("h")
	if rig == nil {
		t.Skip("avatar_data archive absent")
	}
	for _, c := range []struct {
		side  string
		tag   int
		freeX float64
	}{
		{"right", dpTagRArm0, 0},
		{"left", dpTagLArm0, float64(rig.size[dpTagLArm0][0])},
	} {
		pivotY := rig.ll[c.tag][1] + rig.anc[c.tag][1]
		_, y := rig.point(c.tag, c.freeX, rig.anc[c.tag][1])
		// a 24 px upper arm turned 65 degrees drops about 21 px: require a clear drop
		if y > pivotY-10 {
			t.Errorf("%s upper arm free end at y=%.1f, shoulder y=%.1f: arm not hanging (turn %.1f)",
				c.side, y, pivotY, rig.rot[c.tag])
		}
	}
}

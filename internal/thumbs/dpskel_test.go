package thumbs

import (
	"image"
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

// TestDpIdleArmsHangDown checks the rig: each upper arm's free end (the sprite end away
// from the shoulder) sits well below the shoulder pivot once the idle turn is applied.
func TestDpIdleArmsHangDown(t *testing.T) {
	useArchive(t)
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

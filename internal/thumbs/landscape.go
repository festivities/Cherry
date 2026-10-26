package thumbs

import (
	"bytes"
	"hash/crc32"
	"image"
	"image/color"
	"image/png"
)

// landscapeHex is the outline of an item picture (the open cube of the archive's dp.png files, 130x100):
// a hexagon whose vertices run in one direction.
var landscapeHex = [6][2]float64{{14, 30.5}, {65, 5}, {116, 30.5}, {116, 70.5}, {65, 96}, {14, 70.5}}

// LandscapeDP is the dp.png of a generated Garden landscape, from the map stem it switches to. It is a
// placeholder: the 130x100 hexagon in one colour that the stem picks, with no archive art behind it. A
// later generator replaces this function; the economy serves its result (economy/landscape.go).
func LandscapeDP(stem string) ([]byte, error) {
	h := crc32.ChecksumIEEE([]byte(stem))
	fill := color.NRGBA{R: 0x80 + uint8(h)/2, G: 0x80 + uint8(h>>8)/2, B: 0x80 + uint8(h>>16)/2, A: 0xff}
	img := image.NewNRGBA(image.Rect(0, 0, 130, 100))
	for y := range 100 {
		for x := range 130 {
			if inConvex(landscapeHex, float64(x)+0.5, float64(y)+0.5) {
				img.SetNRGBA(x, y, fill)
			}
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// inConvex reports whether (x, y) lies inside the convex polygon p, whose vertices run in one direction.
func inConvex(p [6][2]float64, x, y float64) bool {
	for i := range p {
		a, b := p[i], p[(i+1)%len(p)]
		if (b[0]-a[0])*(y-a[1])-(b[1]-a[1])*(x-a[0]) < 0 {
			return false
		}
	}
	return true
}

package economy

import (
	"bytes"
	"encoding/xml"
	"errors"
	"image"
	"image/draw"
	"image/png"
	"io"
	"io/fs"
	"math"
	"slices"
	"strconv"
	"strings"
	"sync"

	"cherry/internal/assets"
)

// The client's icon_effect animation (animation/icon_effect.aniproj, an original read from the store): the pictures a
// Garden event button may show. home_icon iconId names one of its symbols; the client drops a row whose symbol it has not
// loaded (NaGardenEventManager::MsgProc msg 10740), so the staff panel offers only these names. Previews are a rough
// still of each symbol's first frame for the panel: every layer's frame-0 picture at its frame-0 position, nothing else.

var iconEffectFS fs.FS = assets.DeviceFiles("animation")

// iconLayer is one layer's frame-0 picture and position (cocos points, y up).
type iconLayer struct {
	png  string
	x, y float64
}

var iconEffect struct {
	sync.Mutex
	symbols  map[string][]iconLayer // nil until a load succeeds
	previews map[string][]byte
}

// loadIconEffect parses the aniproj once; a failed read is retried on the next call.
func loadIconEffect() (map[string][]iconLayer, error) {
	iconEffect.Lock()
	defer iconEffect.Unlock()
	if iconEffect.symbols != nil {
		return iconEffect.symbols, nil
	}
	f, err := iconEffectFS.Open("icon_effect.aniproj")
	if err != nil {
		return nil, err
	}
	defer f.Close()
	syms, err := parseIconEffect(f)
	if err != nil {
		return nil, err
	}
	iconEffect.symbols, iconEffect.previews = syms, map[string][]byte{}
	return syms, nil
}

// parseIconEffect reads <symbol szName> → <layer> → <process dwProcessKey> → <property>: key 0 carries szSymbolKey (a
// picture or a nested symbol), key 3 the position. A layer whose frame-0 key is not a picture keeps no picture.
func parseIconEffect(r io.Reader) (map[string][]iconLayer, error) {
	dec := xml.NewDecoder(r)
	syms := map[string][]iconLayer{}
	var sym string
	var layer *iconLayer
	var hasPic, hasPos bool
	procKey := ""
	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			attr := func(n string) string {
				for _, a := range t.Attr {
					if a.Name.Local == n {
						return a.Value
					}
				}
				return ""
			}
			switch t.Name.Local {
			case "symbol":
				sym = attr("szName")
				if sym != "" {
					syms[sym] = []iconLayer{}
				}
			case "layer":
				layer, hasPic, hasPos = &iconLayer{}, false, false
			case "process":
				procKey = attr("dwProcessKey")
			case "property":
				if layer == nil || attr("dwFrame") != "0" {
					continue
				}
				switch {
				case procKey == "0" && !hasPic:
					hasPic = true
					if k := attr("szSymbolKey"); strings.HasSuffix(k, ".png") && !strings.ContainsAny(k, `/\`) {
						layer.png = k
					}
				case procKey == "3" && !hasPos:
					hasPos = true
					layer.x, _ = strconv.ParseFloat(attr("fPointX"), 64)
					layer.y, _ = strconv.ParseFloat(attr("fPointY"), 64)
				}
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "layer":
				if sym != "" && layer != nil && layer.png != "" {
					syms[sym] = append(syms[sym], *layer)
				}
				layer = nil
			case "symbol":
				sym = ""
			}
		}
	}
	if len(syms) == 0 {
		return nil, errors.New("icon_effect.aniproj: no symbols")
	}
	return syms, nil
}

// IconEffectSymbols lists the symbols with at least one picture, sorted: the pictures a Garden button can show.
func IconEffectSymbols() ([]string, error) {
	syms, err := loadIconEffect()
	if err != nil {
		return nil, err
	}
	var out []string
	for name, layers := range syms {
		if len(layers) > 0 {
			out = append(out, name)
		}
	}
	slices.Sort(out)
	return out, nil
}

// IsIconEffectSymbol reports whether name is a symbol with a picture (false when the file cannot be read).
func IsIconEffectSymbol(name string) bool {
	syms, err := loadIconEffect()
	return err == nil && len(syms[name]) > 0
}

// IconEffectPreview is the PNG still of symbol name (cached); ok false for an unknown name or unreadable pictures.
func IconEffectPreview(name string) ([]byte, bool) {
	syms, err := loadIconEffect()
	if err != nil || len(syms[name]) == 0 {
		return nil, false
	}
	iconEffect.Lock()
	b, ok := iconEffect.previews[name]
	iconEffect.Unlock()
	if ok {
		return b, b != nil
	}
	b = drawIconPreview(syms[name])
	iconEffect.Lock()
	iconEffect.previews[name] = b
	iconEffect.Unlock()
	return b, b != nil
}

// drawIconPreview stacks the layers' pictures, first layer at the bottom, each centred on its position (y up).
func drawIconPreview(layers []iconLayer) []byte {
	type placed struct {
		img  image.Image
		rect image.Rectangle
	}
	var parts []placed
	var bounds image.Rectangle
	for _, l := range layers {
		f, err := iconEffectFS.Open("icon_effect/" + l.png)
		if err != nil {
			continue
		}
		img, err := png.Decode(f)
		f.Close()
		if err != nil {
			continue
		}
		sz := img.Bounds().Size()
		min := image.Pt(int(math.Round(l.x))-sz.X/2, -int(math.Round(l.y))-sz.Y/2)
		r := image.Rectangle{Min: min, Max: min.Add(sz)}
		parts = append(parts, placed{img, r})
		bounds = bounds.Union(r)
	}
	if len(parts) == 0 || bounds.Dx() > 2048 || bounds.Dy() > 2048 {
		return nil
	}
	canvas := image.NewNRGBA(image.Rect(0, 0, bounds.Dx(), bounds.Dy()))
	for _, p := range parts {
		draw.Draw(canvas, p.rect.Sub(bounds.Min), p.img, p.img.Bounds().Min, draw.Over)
	}
	var buf bytes.Buffer
	if png.Encode(&buf, canvas) != nil {
		return nil
	}
	return buf.Bytes()
}

// CheckBrowserURL is the openWithBrowser URL rule of the login popups (checkPopupURL): absolute https with a host, no user
// information, at most 512 bytes.
func CheckBrowserURL(raw string) error { return checkPopupURL(raw) }

// LinkBrowser is the client link type that opens its detail in the phone's browser.
const LinkBrowser = linkBrowser

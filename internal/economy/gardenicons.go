package economy

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"image"
	"image/draw"
	"image/png"
	"io"
	"log"
	"strings"
	"sync"
	"time"

	"cherry/internal/assets"
	"cherry/internal/store"
)

// Custom Garden button pictures (Cherry-made; spec garden-buttons-20261128, "Custom pictures: carrier check"). The
// client draws a home_icon row only from an animation symbol it has loaded, and it loads animation files by fixed names
// only, all of them originals. So the custom pictures ride in a Cherry-made superset of the original
// animation/mainhome_button.aniproj (2 KB, preloaded by NaGardenEventManager; its four original symbols carry the Garden
// event buttons): the original text verbatim, plus one cherry_gb_<id> symbol per picture whose PNG sits in Cherry's own
// device folder animation/-cherry/. Cherry serves both through the patcher's animation list (assets overlay), never from
// the bucket; R2 static/ keeps the original. A row names a picture as mainhome_button/cherry_gb_<id>. With no custom
// picture the list names the original again and clients download it back. Without the patcher lists (streaming off)
// there is no way to deliver a picture, and custom pictures stay off.

const (
	gardenIconCarrier  = "mainhome_button.aniproj" // the original the superset is built on (animation list path)
	gardenIconProject  = "mainhome_button"         // its symbol prefix: the client keys symbols <file base>/<name>
	gardenIconPrefix   = "cherry_gb_"
	gardenIconDir      = "-cherry/"
	gardenIconMaxSide  = 256
	gardenIconMaxBytes = 64 << 10
	gardenIconRawMax   = 1 << 20
)

// GardenIconKey is the home_icon iconId of custom picture id.
func GardenIconKey(id string) string { return gardenIconProject + "/" + gardenIconPrefix + id }

var gardenIconState = struct {
	sync.Mutex
	live   map[string]bool // iconIds the current overlay carries
	reason string          // why custom pictures are off; "" when they are on
}{reason: "not checked yet"}

// GardenIconLive reports whether the current overlay carries iconId (a row that names it can show).
func GardenIconLive(iconID string) bool {
	gardenIconState.Lock()
	defer gardenIconState.Unlock()
	return gardenIconState.live[iconID]
}

// GardenIconsOff is why custom pictures cannot reach the clients now; "" when they can.
func GardenIconsOff() string {
	gardenIconState.Lock()
	defer gardenIconState.Unlock()
	return gardenIconState.reason
}

// RefreshGardenIcons builds the overlay from the stored pictures and hands it to the asset store. Called at startup and
// after every upload or removal. On failure custom pictures are off (GardenIconsOff says why) and the lists carry the
// originals only.
func RefreshGardenIcons() error {
	live, err := buildGardenIconOverlay()
	gardenIconState.Lock()
	defer gardenIconState.Unlock()
	if err != nil {
		gardenIconState.live, gardenIconState.reason = nil, err.Error()
		_ = assets.Archive.SetAnimationOverlay(nil)
		return err
	}
	gardenIconState.live, gardenIconState.reason = live, ""
	return nil
}

func buildGardenIconOverlay() (map[string]bool, error) {
	if !assets.Archive.Streaming() {
		return nil, errors.New("this server does not serve the client's patcher lists (streaming.json off or no asset bucket), so it cannot deliver custom pictures")
	}
	icons, err := store.GardenIconList()
	if err != nil {
		return nil, err
	}
	if len(icons) == 0 {
		return map[string]bool{}, assets.Archive.SetAnimationOverlay(nil)
	}
	orig, err := assets.Archive.OriginalAnimation(gardenIconCarrier)
	if err != nil {
		return nil, err
	}
	ids := make([]string, len(icons))
	for i, ic := range icons {
		ids[i] = ic.ID
	}
	carrier, err := buildGardenIconCarrier(orig, ids)
	if err != nil {
		return nil, err
	}
	files := []assets.OverlayFile{{Path: gardenIconCarrier, Data: carrier}}
	live := map[string]bool{}
	for _, id := range ids {
		b, err := store.GardenIconPNG(id)
		if err != nil {
			return nil, fmt.Errorf("custom picture %s: %w", id, err)
		}
		files = append(files, assets.OverlayFile{Path: gardenIconDir + gardenIconPrefix + id + ".png", Data: b})
		live[GardenIconKey(id)] = true
	}
	if err := assets.Archive.SetAnimationOverlay(files); err != nil {
		return nil, err
	}
	return live, nil
}

// buildGardenIconCarrier inserts one picture line before </PNGS> and one symbol before </SYMBOLS> per id, in the file's
// own dialect and line endings, and checks the result: well-formed, every original symbol kept, every new one present.
// An original of another shape, or one that already holds Cherry symbols, is refused.
func buildGardenIconCarrier(orig []byte, ids []string) ([]byte, error) {
	s := string(orig)
	switch {
	case strings.Count(s, "</PNGS>") != 1 || strings.Count(s, "</SYMBOLS>") != 1:
		return nil, errors.New(gardenIconCarrier + " does not have the expected shape")
	case strings.Contains(s, gardenIconPrefix) || strings.Contains(s, gardenIconDir):
		return nil, errors.New(gardenIconCarrier + " already holds Cherry symbols: it is not the original")
	}
	before, err := aniprojSymbols(orig)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", gardenIconCarrier, err)
	}
	nl := "\n"
	if strings.Contains(s, "\r\n") {
		nl = "\r\n"
	}
	oldDialect := strings.Contains(s, "PROCESS_SYMBOL")
	var pngs, syms strings.Builder
	for _, id := range ids {
		name := gardenIconPrefix + id
		fmt.Fprintf(&pngs, `        <png path="./%s%s.png" />%s`, gardenIconDir, name, nl)
		if oldDialect { // mainhome_button.aniproj's own dialect (button_00): PROCESS_SYMBOL, "l" counts
			fmt.Fprintf(&syms, `        <symbol szName="%s" dwTotalFrame="30l" dwFrameTime="33l">%s`+
				`            <layer dwIndex="0" szName="Layer000">%s`+
				`                <PROCESS_SYMBOL dwFrame="0" szSymbolKey="%s.png" dwKeepFrame="29" />%s`+
				`            </layer>%s        </symbol>%s`, name, nl, nl, name, nl, nl, nl)
		} else { // the newer process/property dialect (icon_effect.aniproj)
			fmt.Fprintf(&syms, `        <symbol szName="%s" dwTotalFrame="30" dwFrameTime="33">%s`+
				`            <layer dwIndex="0" dwOption="0" szName="Layer000">%s`+
				`                <process dwProcessKey="0" numChild="1">%s`+
				`                    <property dwFrame="0" szSymbolKey="%s.png" dwKeepFrame="29" dwBlendType="0" />%s`+
				`                </process>%s            </layer>%s        </symbol>%s`, name, nl, nl, nl, name, nl, nl, nl, nl)
		}
	}
	s = insertBeforeLine(s, "</PNGS>", pngs.String())
	s = insertBeforeLine(s, "</SYMBOLS>", syms.String())
	after, err := aniprojSymbols([]byte(s))
	if err != nil {
		return nil, fmt.Errorf("built %s: %w", gardenIconCarrier, err)
	}
	if len(after) != len(before)+len(ids) {
		return nil, fmt.Errorf("built %s has %d symbols, want %d", gardenIconCarrier, len(after), len(before)+len(ids))
	}
	for i, name := range before {
		if after[i] != name {
			return nil, fmt.Errorf("built %s changed original symbol %q", gardenIconCarrier, name)
		}
	}
	return []byte(s), nil
}

// insertBeforeLine inserts text at the start of the line that holds the only tag.
func insertBeforeLine(s, tag, text string) string {
	i := strings.Index(s, tag)
	start := strings.LastIndexByte(s[:i], '\n') + 1
	return s[:start] + text + s[start:]
}

// aniprojSymbols reads an animation file to the end (so it must be well-formed) and lists its symbol names in order.
func aniprojSymbols(b []byte) ([]string, error) {
	dec := xml.NewDecoder(bytes.NewReader(b))
	var names []string
	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			return names, nil
		}
		if err != nil {
			return nil, err
		}
		if t, ok := tok.(xml.StartElement); ok && t.Name.Local == "symbol" {
			name := ""
			for _, a := range t.Attr {
				if a.Name.Local == "szName" {
					name = a.Value
				}
			}
			names = append(names, name)
		}
	}
}

// NormalizeGardenIcon checks an uploaded picture and re-encodes it as a plain RGBA PNG (no extra chunks for the
// client's decoder): PNG only, at most gardenIconMaxSide pixels a side, at most gardenIconMaxBytes once encoded. It
// returns the PNG, its id (the first 12 hex digits of its SHA-256) and its size.
func NormalizeGardenIcon(raw []byte) (b []byte, id string, w, h int, err error) {
	if len(raw) > gardenIconRawMax || !bytes.HasPrefix(raw, []byte("\x89PNG\r\n\x1a\n")) {
		return nil, "", 0, 0, errors.New("the picture must be a PNG file of at most 1 MiB")
	}
	cfg, err := png.DecodeConfig(bytes.NewReader(raw))
	if err != nil {
		return nil, "", 0, 0, errors.New("the PNG file could not be read")
	}
	if cfg.Width < 1 || cfg.Height < 1 || cfg.Width > gardenIconMaxSide || cfg.Height > gardenIconMaxSide {
		return nil, "", 0, 0, fmt.Errorf("the picture is %d×%d; at most %d×%d pixels", cfg.Width, cfg.Height, gardenIconMaxSide, gardenIconMaxSide)
	}
	img, err := png.Decode(bytes.NewReader(raw))
	if err != nil {
		return nil, "", 0, 0, errors.New("the PNG file could not be read")
	}
	rgba := image.NewNRGBA(image.Rect(0, 0, cfg.Width, cfg.Height))
	draw.Draw(rgba, rgba.Bounds(), img, img.Bounds().Min, draw.Src)
	var buf bytes.Buffer
	if err := (&png.Encoder{CompressionLevel: png.BestCompression}).Encode(&buf, rgba); err != nil {
		return nil, "", 0, 0, err
	}
	if buf.Len() > gardenIconMaxBytes {
		return nil, "", 0, 0, fmt.Errorf("the picture is %d KiB once saved; at most %d KiB (use fewer colours or a smaller size)", buf.Len()>>10, gardenIconMaxBytes>>10)
	}
	sum := sha256.Sum256(buf.Bytes())
	return buf.Bytes(), hex.EncodeToString(sum[:])[:12], cfg.Width, cfg.Height, nil
}

// StartGardenIcons is the startup refresh (cmd/cherry); a failure is one log line. While pictures exist and the lists
// are served, a failed refresh (the bucket briefly away) is retried every 30 s until it succeeds: until then the lists
// name the original carrier, and a client that starts meanwhile downloads it back.
func StartGardenIcons() {
	err := RefreshGardenIcons()
	if err == nil {
		return
	}
	log.Printf("cherry: custom Garden pictures off: %v", err)
	if icons, _ := store.GardenIconList(); len(icons) == 0 || !assets.Archive.Streaming() {
		return
	}
	go func() {
		t := time.NewTicker(30 * time.Second) // ponytail: fixed interval, no backoff
		defer t.Stop()
		for range t.C {
			if GardenIconsOff() == "" || RefreshGardenIcons() == nil {
				log.Printf("cherry: custom Garden pictures on")
				return
			}
		}
	}()
}

package economy

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"cherry/data"
	"cherry/internal/assets"
	"cherry/internal/opdata"
)

// CherryNewsBannerFirst..CherryNewsBannerLast reserve Cherry's News banner IDs (fits C++ int32 for bannerId).
// Operator banners use 1970000000..1970000899; active season banners use 1970000900..1970000999.
const (
	CherryNewsBannerFirst       int64 = 1970000000
	CherryNewsBannerOpLast      int64 = 1970000899
	CherryNewsBannerSeasonFirst int64 = 1970000900
	CherryNewsBannerLast        int64 = 1970000999
)

const (
	newsPlaceholderWidth  = 640
	newsPlaceholderHeight = 516
)

var newsIconFS fs.FS = assets.DeviceFiles("UIResource/UIImage_hd")

// NewsConfig is data/news.json: operator-editable settings and banners for What's NEW.
type NewsConfig struct {
	opdata.Provenance
	IncludeSeasonBanners    bool `json:"include_season_banners"`
	IncludeRecommendedPosts bool `json:"include_recommended_posts"`
	RecommendedMax          int  `json:"recommended_max"`
	// Today's Best and the News "hot post" cards rank public posts by activity: BestLikePoints per distinct liker
	// plus BestCommentPoints per distinct commenter (the owner's own likes and comments do not count), among posts
	// made within the last BestWindowHours (0 = no window). Cherry-made.
	BestWindowHours   int          `json:"best_window_hours"`
	BestLikePoints    int          `json:"best_like_points"`
	BestCommentPoints int          `json:"best_comment_points"`
	Banners           []NewsBanner `json:"banners"`
}

// NewsBanner is one operator-defined banner entry in data/news.json.
type NewsBanner struct {
	ID         int64  `json:"id"`
	Category   string `json:"category"`
	Title      string `json:"title"`
	Link       string `json:"link"`
	URL        string `json:"url"`
	Image      string `json:"image"`
	Background string `json:"background"`
	Icon       string `json:"icon"`
	EndingSoon bool   `json:"ending_soon"`
	Start      string `json:"start"`
	End        string `json:"end"`

	from, until time.Time
}

// ActiveNewsBanner is a resolved banner ready for /v4/noti/lists serialization.
type ActiveNewsBanner struct {
	BannerID   int64
	Category   string
	Title      string
	LinkType   string
	Link       string
	Image      string
	IconPath   string
	StartMs    int64
	EndMs      int64
	EndingSoon bool
}

var newsFile = opdata.New("news.json", data.NewsJSON, checkNewsConfig, verifyNewsIcons)

// ActiveNews returns the active NewsConfig document.
func ActiveNews() *NewsConfig { return newsFile.Get() }

// MailItemImageURL returns the item dp.png URL for POSTBOX_RECEIVE alerts.
func MailItemImageURL(code, host string) string { return itemImageURL(code, host) }

func checkNewsImageName(name string) error {
	ext := strings.ToLower(filepath.Ext(name))
	if len(name) > 128 || filepath.Base(name) != name || strings.ContainsAny(name, `/\`) || (ext != ".png" && ext != ".jpg" && ext != ".jpeg") {
		return fmt.Errorf("image %q must be a file name in config/news ending in .png, .jpg or .jpeg", name)
	}
	return nil
}

func checkNewsConfig(cfg *NewsConfig) error {
	if err := cfg.CheckOrigin(opdata.OriginCherry); err != nil {
		return err
	}
	if cfg.RecommendedMax < 0 || cfg.RecommendedMax > 10 {
		return fmt.Errorf("recommended_max must be 0 to 10, got %d", cfg.RecommendedMax)
	}
	if cfg.BestWindowHours < 0 || cfg.BestWindowHours > 8760 {
		return fmt.Errorf("best_window_hours must be 0 to 8760, got %d", cfg.BestWindowHours)
	}
	if cfg.BestLikePoints < 0 || cfg.BestLikePoints > 100 || cfg.BestCommentPoints < 0 || cfg.BestCommentPoints > 100 {
		return fmt.Errorf("best_like_points and best_comment_points must be 0 to 100")
	}
	seen := make(map[int64]bool, len(cfg.Banners))
	for i := range cfg.Banners {
		b := &cfg.Banners[i]
		where := fmt.Sprintf("banners[%d] (id %d)", i+1, b.ID)
		if !inCherryBlock(b.ID, CherryNewsBannerFirst, CherryNewsBannerOpLast) {
			return fmt.Errorf("%s: id must be inside the operator block %d-%d", where, CherryNewsBannerFirst, CherryNewsBannerOpLast)
		}
		if seen[b.ID] {
			return fmt.Errorf("%s: id must be unique", where)
		}
		seen[b.ID] = true
		if strings.TrimSpace(b.Category) == "" || utf8.RuneCountInString(b.Category) > 40 {
			return fmt.Errorf("%s: category must be 1 to 40 characters", where)
		}
		if strings.TrimSpace(b.Title) == "" || utf8.RuneCountInString(b.Title) > 200 {
			return fmt.Errorf("%s: title must be 1 to 200 characters", where)
		}
		switch {
		case b.Link == linkBrowser:
			if err := checkPopupURL(b.URL); err != nil {
				return fmt.Errorf("%s: url: %w", where, err)
			}
		case isNativeLink(b.Link):
			if b.URL != "" {
				return fmt.Errorf("%s: url is only for %s", where, linkBrowser)
			}
		default:
			return fmt.Errorf("%s: link %q must be %s (with url) or goSomewhere(<name>) with a native scene name", where, b.Link, linkBrowser)
		}
		if b.Background == "" {
			b.Background = "#4A90D9"
		} else if _, err := parseHexColor(b.Background); err != nil {
			return fmt.Errorf("%s: background: %w", where, err)
		}
		if b.Image != "" {
			if err := checkNewsImageName(b.Image); err != nil {
				return fmt.Errorf("%s: %w", where, err)
			}
		}
		if b.Icon != "" && !safeQuestArtName(b.Icon) {
			return fmt.Errorf("%s: icon %q must be a bare .png filename under UIResource/UIImage_hd", where, b.Icon)
		}
		var err error
		if b.from, err = parseGameDay(b.Start); err != nil {
			return fmt.Errorf("%s: start: %w", where, err)
		}
		if b.until, err = parseGameDay(b.End); err != nil {
			return fmt.Errorf("%s: end: %w", where, err)
		}
		if !b.from.IsZero() && !b.until.IsZero() && b.from.After(b.until) {
			return fmt.Errorf("%s: start is after end", where)
		}
	}
	return nil
}

func verifyNewsIcons(cfg *NewsConfig) error {
	if !artVerifiable() {
		return nil
	}
	for i, b := range cfg.Banners {
		if b.Icon == "" {
			continue
		}
		if _, err := fs.Stat(newsIconFS, b.Icon); err != nil {
			return fmt.Errorf("banners[%d] (id %d): icon %q not found in UIResource/UIImage_hd: %w", i+1, b.ID, b.Icon, err)
		}
	}
	return nil
}

// ActiveNewsBanners returns operator banners currently in force plus the active season banner when enabled.
func ActiveNewsBanners(now time.Time) []ActiveNewsBanner {
	cfg := ActiveNews()
	day := gameDay(now)
	var out []ActiveNewsBanner
	for _, b := range cfg.Banners {
		if (!b.from.IsZero() && day.Before(b.from)) || (!b.until.IsZero() && day.After(b.until)) {
			continue
		}
		_, hash := newsPicture(b)
		var startMs, endMs int64
		if !b.from.IsZero() {
			startMs = b.from.Add(attendDayStartHour * time.Hour).UnixMilli()
		}
		if !b.until.IsZero() {
			endMs = b.until.AddDate(0, 0, 1).Add(attendDayStartHour*time.Hour - time.Second).UnixMilli()
		}
		iconPath := ""
		if b.Icon != "" {
			iconPath = "cherry/news/icon/" + b.Icon
		}
		out = append(out, ActiveNewsBanner{
			BannerID:   b.ID,
			Category:   b.Category,
			Title:      b.Title,
			LinkType:   b.Link,
			Link:       b.URL,
			Image:      "cherry/news/" + strconv.FormatInt(b.ID, 10) + "-" + hash + ".png",
			IconPath:   iconPath,
			StartMs:    startMs,
			EndMs:      endMs,
			EndingSoon: b.EndingSoon,
		})
	}
	if cfg.IncludeSeasonBanners {
		if s, ok := activePopup(now); ok {
			idx := -1
			for i, cand := range seasons() {
				if cand.Name == s.Name {
					idx = i
					break
				}
			}
			if idx >= 0 && CherryNewsBannerSeasonFirst+int64(idx) <= CherryNewsBannerLast {
				startStr, endStr := popupWindowMs(s, now)
				startMs, _ := strconv.ParseInt(startStr, 10, 64)
				endMs, _ := strconv.ParseInt(endStr, 10, 64)
				title := s.Popup.Title
				if s.Popup.Sub != "" {
					title += " — " + s.Popup.Sub
				}
				endingSoon := endMs > 0 && endMs-now.UnixMilli() <= int64(48*time.Hour/time.Millisecond)
				out = append(out, ActiveNewsBanner{
					BannerID:   CherryNewsBannerSeasonFirst + int64(idx),
					Category:   s.Title,
					Title:      title,
					LinkType:   s.Popup.Link,
					Link:       "",
					Image:      "cherry/event/" + s.Name + ".png",
					StartMs:    startMs,
					EndMs:      endMs,
					EndingSoon: endingSoon,
				})
			}
		}
	}
	return out
}

var newsPics = struct {
	sync.Mutex
	m map[string]popupPic
}{m: map[string]popupPic{}}

func newsPicture(b NewsBanner) ([]byte, string) {
	var pic []byte
	if b.Image != "" {
		pic = newsOperatorFilePicture(b.Image)
	}
	if pic == nil {
		pic = newsPlaceholderPicture(b.Background)
	}
	sum := sha256.Sum256(pic)
	return pic, hex.EncodeToString(sum[:4])
}

func newsOperatorFilePicture(name string) []byte {
	dir := opdata.Dir()
	if dir == "" {
		return nil
	}
	path := filepath.Join(dir, "news", name)
	sig := ""
	if fi, err := os.Stat(path); err == nil {
		sig = strconv.FormatInt(fi.Size(), 10) + "|" + strconv.FormatInt(fi.ModTime().UnixNano(), 10)
	}
	key := "file:" + path
	newsPics.Lock()
	hit, ok := newsPics.m[key]
	newsPics.Unlock()
	if ok && hit.sig == sig {
		return hit.pic
	}
	pic, err := loadOperatorPicture(path)
	if err != nil {
		log.Printf("cherry: news picture %s not used, the drawn placeholder is served: %v", name, err)
	}
	newsPics.Lock()
	newsPics.m[key] = popupPic{sig: sig, pic: pic}
	newsPics.Unlock()
	return pic
}

func newsPlaceholderPicture(bg string) []byte {
	key := "drawn:" + bg
	newsPics.Lock()
	defer newsPics.Unlock()
	if e, ok := newsPics.m[key]; ok {
		return e.pic
	}
	c, _ := parseHexColor(bg)
	img := image.NewNRGBA(image.Rect(0, 0, newsPlaceholderWidth, newsPlaceholderHeight))
	for y := range newsPlaceholderHeight {
		t := float64(y) / (newsPlaceholderHeight - 1)
		row := color.NRGBA{R: mix(c.R, 255, 0.3*(1-t)), G: mix(c.G, 255, 0.3*(1-t)), B: mix(c.B, 255, 0.3*(1-t)), A: 255}
		for x := range newsPlaceholderWidth {
			px := row
			if x < placeholderRim || y < placeholderRim || x >= newsPlaceholderWidth-placeholderRim || y >= newsPlaceholderHeight-placeholderRim {
				px = color.NRGBA{R: 255, G: 255, B: 255, A: 255}
			}
			img.SetNRGBA(x, y, px)
		}
	}
	var buf bytes.Buffer
	_ = png.Encode(&buf, img)
	newsPics.m[key] = popupPic{pic: buf.Bytes()}
	return buf.Bytes()
}

// HandleNewsImage serves GET /cherry/news/<id>-<hash8>.png and GET /cherry/news/icon/<file>.png.
func HandleNewsImage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.NotFound(w, r)
		return
	}
	rest := strings.TrimPrefix(r.URL.Path, "/cherry/news/")
	if iconName, ok := strings.CutPrefix(rest, "icon/"); ok {
		if !safeQuestArtName(iconName) {
			http.NotFound(w, r)
			return
		}
		referenced := false
		for _, b := range ActiveNews().Banners {
			if b.Icon == iconName {
				referenced = true
				break
			}
		}
		if !referenced {
			http.NotFound(w, r)
			return
		}
		raw, err := fs.ReadFile(newsIconFS, iconName)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(raw)
		return
	}
	if !strings.HasSuffix(rest, ".png") {
		http.NotFound(w, r)
		return
	}
	name := strings.TrimSuffix(rest, ".png")
	idText, hash, ok := strings.Cut(name, "-")
	id, err := strconv.ParseInt(idText, 10, 64)
	if !ok || err != nil {
		http.NotFound(w, r)
		return
	}
	for _, b := range ActiveNews().Banners {
		if b.ID != id {
			continue
		}
		pic, h := newsPicture(b)
		if h != hash {
			break
		}
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(pic)
		return
	}
	http.NotFound(w, r)
}

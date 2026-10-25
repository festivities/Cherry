package web

import (
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"cherry/internal/assets"
	"cherry/internal/thumbs"
)

// streamBucket publishes a small bucket for the streaming tests: one animation file, the tx list, and
// the manifests (versions derive from the entries, as in production).
func streamBucket(t *testing.T) (map[string][]byte, map[string]int) {
	t.Helper()
	objs := map[string][]byte{}
	versions := map[string]int{}
	files := map[string]map[string]string{
		"animation": {"a.png": "pixels"},
		"sound":     {"BGM.ogg": "ogg"},
		"ui":        {"UIImage_hd/c.png": "cccccc"},
		"tx":        {"arts_strings.ast": "ast-bytes"},
		"subui":     {"MarbleChallengePopup.plist": "<plist/>"},
	}
	for cat, fs := range files {
		var entries []assets.Entry
		for p, body := range fs {
			sum := md5.Sum([]byte(body))
			entries = append(entries, assets.Entry{Path: p, Size: int64(len(body)), MD5: strings.ToUpper(hex.EncodeToString(sum[:]))})
			objs["static/"+cat+"/"+p] = []byte(body)
		}
		sortEntries(entries)
		versions[cat] = assets.Version(entries)
		b, err := json.Marshal(assets.Manifest{Cat: cat, Version: versions[cat], Entries: entries})
		if err != nil {
			t.Fatal(err)
		}
		objs["manifest/"+cat+".json"] = b
	}
	return objs, versions
}

func sortEntries(entries []assets.Entry) {
	for i := 1; i < len(entries); i++ {
		for j := i; j > 0 && entries[j].Path < entries[j-1].Path; j-- {
			entries[j], entries[j-1] = entries[j-1], entries[j]
		}
	}
}

// streamServer serves objs as the test bucket and makes streaming on for the test.
func streamServer(t *testing.T, objs map[string][]byte) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key, ok := strings.CutPrefix(r.URL.Path, "/cherry-assets/")
		body, found := objs[key]
		if !ok || !found {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		sum := md5.Sum(body)
		w.Header().Set("ETag", `"`+hex.EncodeToString(sum[:])+`"`)
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	streaming = assets.New(assets.Config{Endpoint: srv.URL, Bucket: "cherry-assets", AccessKey: "AKTEST", SecretKey: "S"}, t.TempDir())
	t.Cleanup(func() { streaming = nil })
}

// localArchive points the thumbnail generator at a temp item tree; it holds the local dp.png fallback.
func localArchive(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	old := thumbs.DpItemRoot
	thumbs.DpItemRoot = root
	t.Cleanup(func() { thumbs.DpItemRoot = old })
	return root
}

func bodyOf(t *testing.T, method, target string) (int, string) {
	t.Helper()
	rec := serve(t, method, target)
	return rec.Code, rec.Body.String()
}

// With streaming off (no streaming.json enabled, no cloudflare.json) every patcher body is today's.
func TestStreamingOffKeepsBodies(t *testing.T) {
	webOperatorDir(t)
	if err := LoadOperatorData(); err != nil {
		t.Fatalf("load: %v", err)
	}
	if streaming != nil {
		t.Fatal("streaming opened without a flag and cloudflare.json")
	}
	if code, body := bodyOf(t, http.MethodGet, "/notice/adr/checkresource2.json"); code != http.StatusOK || body != checkResource2Body {
		t.Fatalf("checkresource2 = %d %s", code, body)
	}
	if code, body := bodyOf(t, http.MethodGet, "/arts_tx_ini_00543/update_en.ini"); code != http.StatusOK || body != tx543UpdateIniBody {
		t.Fatalf("tx list = %d %q", code, body)
	}
	if code, body := bodyOf(t, http.MethodGet, "/arts_animation_ini_00531/update_en.ini"); code != http.StatusOK || body != emptyUpdateIniBody {
		t.Fatalf("animation list = %d %q", code, body)
	}
	if code, _ := bodyOf(t, http.MethodGet, "/arts_animation_ini_00531/update_tw.ini"); code != http.StatusNotFound {
		t.Fatalf("tw list = %d, want the legacy 404", code)
	}
}

func TestStreamedPatcherAndFiles(t *testing.T) {
	webOperatorDir(t)
	objs, versions := streamBucket(t)
	streamServer(t, objs)

	code, body := bodyOf(t, http.MethodGet, "/notice/adr/checkresource2.json")
	if code != http.StatusOK || !strings.Contains(body, `"animation":"`+strconv.Itoa(versions["animation"])+`"`) ||
		!strings.Contains(body, `"tx":"`+strconv.Itoa(versions["tx"])+`"`) {
		t.Fatalf("checkresource2 = %d %s", code, body)
	}
	for _, lang := range []string{"en", "tw", "cn", "kr", "jp", "th", "ja"} {
		code, body := bodyOf(t, http.MethodGet, "/arts_tx_ini_00543/update_"+lang+".ini")
		if code != http.StatusOK || !strings.HasPrefix(body, "[start=1,end=1,size=9,/arts_strings.ast:version=") {
			t.Fatalf("tx list %s = %d %q", lang, code, body)
		}
	}
	if code, body := bodyOf(t, http.MethodGet, "/arts_animation_tw_00531/a.png"); code != http.StatusOK || body != "pixels" {
		t.Fatalf("animation file = %d %q", code, body)
	}
	if code, body := bodyOf(t, http.MethodGet, "/arts_tx_jp_00543/arts_strings.ast"); code != http.StatusOK || body != "ast-bytes" {
		t.Fatalf("string table = %d %q", code, body)
	}
}

func TestStreamedDpPNGWinsThenGeneratedFallback(t *testing.T) {
	webOperatorDir(t)
	objs, _ := streamBucket(t)
	objs["static/item/custom/123/dp.png"] = []byte("R2THUMB")
	streamServer(t, objs)
	root := localArchive(t)
	if err := os.MkdirAll(filepath.Join(root, "custom", "456"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "custom", "456", "dp.png"), []byte("LOCALTHUMB"), 0o600); err != nil {
		t.Fatal(err)
	}

	if code, body := bodyOf(t, http.MethodGet, "/arts_item_custom_123/dp.png"); code != http.StatusOK || body != "R2THUMB" {
		t.Fatalf("R2 dp.png = %d %q", code, body)
	}
	if code, body := bodyOf(t, http.MethodGet, "/img/read/arts_item_custom_123/dp.png"); code != http.StatusOK || body != "R2THUMB" {
		t.Fatalf("thumbnail-form R2 dp.png = %d %q", code, body)
	}
	// No dp.png in R2: the generated path answers, here the archive's own dp.png.
	if code, body := bodyOf(t, http.MethodGet, "/arts_item_custom_456/dp.png"); code != http.StatusOK || body != "LOCALTHUMB" {
		t.Fatalf("fallback dp.png = %d %q", code, body)
	}
	// Neither R2 nor the archive has it: 404, as before.
	if code, _ := bodyOf(t, http.MethodGet, "/arts_item_custom_789/dp.png"); code != http.StatusNotFound {
		t.Fatalf("missing item = %d, want 404", code)
	}
}

func TestStreamedRoutesFallThrough(t *testing.T) {
	webOperatorDir(t)
	objs, _ := streamBucket(t)
	streamServer(t, objs)
	// A file R2 does not have falls through to the existing 404.
	if code, _ := bodyOf(t, http.MethodGet, "/arts_animation_en_00531/absent.png"); code != http.StatusNotFound {
		t.Fatalf("absent streamed file = %d, want 404", code)
	}
	// Unrelated routes never touch the stream.
	if code, _ := bodyOf(t, http.MethodGet, "/v4/setInitConf"); code != http.StatusOK {
		t.Fatalf("setInitConf = %d", code)
	}
}

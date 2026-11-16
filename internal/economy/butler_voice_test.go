package economy

import (
	"archive/zip"
	"bytes"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"cherry/internal/testutil"
)

// TestButlerVoiceZipIsFlatDeterministicAndComplete: Merry's pack zip holds the 47 lines as flat, stored entries named
// voice_NNN.ogg with the fixed modification time, each with the original file's bytes, and a fresh build gives the same
// bytes. The archive is read in place, so the test skips where it is absent.
func TestButlerVoiceZipIsFlatDeterministicAndComplete(t *testing.T) {
	testutil.UseArchive(t)
	d, ok := ButlerByID(merry)
	if !ok || d.Voices == nil {
		t.Fatal("merry has no voice pack")
	}
	lines := d.Voices.Lines
	if len(lines) != 47 {
		t.Fatalf("merry has %d lines, want 47 (the donation's files)", len(lines))
	}
	built, ok := ButlerVoiceZip(d)
	if !ok {
		t.Fatal("the voice zip could not be built")
	}
	zr, err := zip.NewReader(bytes.NewReader(built), int64(len(built)))
	if err != nil {
		t.Fatal(err)
	}
	if len(zr.File) != len(lines) {
		t.Fatalf("zip holds %d entries, want %d", len(zr.File), len(lines))
	}
	for i, f := range zr.File {
		n := lines[i]
		if want := fmt.Sprintf("voice_%03d.ogg", n); f.Name != want {
			t.Fatalf("entry %d is %q, want %q", i, f.Name, want)
		}
		if strings.Contains(f.Name, "/") || f.Method != zip.Store || !f.Modified.Equal(butlerVoiceMTime) {
			t.Fatalf("entry %s: folder, method %d or time %v is not flat, stored and fixed", f.Name, f.Method, f.Modified)
		}
		rc, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		got, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			t.Fatal(err)
		}
		want, err := fs.ReadFile(butlerSkinFS, d.Voices.storeFile(n))
		if err != nil || !bytes.Equal(got, want) {
			t.Fatalf("entry %s is not the original file (%v)", f.Name, err)
		}
	}

	// Deterministic: a fresh build (the cache dropped) gives the same bytes.
	butlerVoiceZips.Lock()
	butlerVoiceZips.files = map[string][]byte{}
	butlerVoiceZips.Unlock()
	again, ok := ButlerVoiceZip(d)
	if !ok || !bytes.Equal(again, built) {
		t.Fatal("a fresh build differs from the first")
	}
	if d.Voices.ZipPath() != "butler_voice_master/3/cherry_voice_v1.zip" || d.Voices.FilePath(1) != "butler_voice_master/3/voice_001.ogg" {
		t.Fatalf("paths %q and %q", d.Voices.ZipPath(), d.Voices.FilePath(1))
	}
}

// TestButlerVoiceZipRouteServesThePackOnly: GET /<pack>/cherry_voice_v<revision>.zip serves the zip of the butler that names
// it; another revision, another folder and a POST are 404; a butler without voices has no zip.
func TestButlerVoiceZipRouteServesThePackOnly(t *testing.T) {
	testutil.UseArchive(t)
	get := func(method, path string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		HandleButlerVoiceZip(rec, httptest.NewRequest(method, path, nil))
		return rec
	}
	d, _ := ButlerByID(merry)
	want, ok := ButlerVoiceZip(d)
	if !ok {
		t.Fatal("no zip")
	}
	rec := get(http.MethodGet, "/butler_voice_master/3/cherry_voice_v1.zip")
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "application/zip" || !bytes.Equal(rec.Body.Bytes(), want) {
		t.Fatalf("pack GET: status %d type %q", rec.Code, rec.Header().Get("Content-Type"))
	}
	for _, c := range []struct{ method, path string }{
		{http.MethodGet, "/butler_voice_master/3/cherry_voice_v2.zip"},
		{http.MethodGet, "/butler_voice_master/4/cherry_voice_v1.zip"},
		{http.MethodGet, "/butler_voice_master/3/voice_001.ogg"},
		{http.MethodPost, "/butler_voice_master/3/cherry_voice_v1.zip"},
	} {
		if rec := get(c.method, c.path); rec.Code != http.StatusNotFound {
			t.Fatalf("%s %s: status %d, want 404", c.method, c.path, rec.Code)
		}
	}
	if _, ok := ButlerVoiceZip(ButlerDef{ID: ophelia}); ok {
		t.Fatal("a butler without voices has a zip")
	}
}

// TestButlerSkinPictureRoutes: a further skin's picture is served at /cherry/butler/skin/<skin id>.png as its original file;
// the default's picture stays at /cherry/butler/<id>.png; an unknown skin is 404.
func TestButlerSkinPictureRoutes(t *testing.T) {
	testutil.UseArchive(t)
	get := func(path string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		HandleButlerImage(rec, httptest.NewRequest(http.MethodGet, path, nil))
		return rec
	}
	want, err := fs.ReadFile(butlerSkinFS, "cache/butler/butler_skin_master/skin_1604557276.png")
	if err != nil {
		t.Fatal(err)
	}
	if rec := get(fmt.Sprintf("/cherry/butler/skin/%d.png", merrySilver)); rec.Code != http.StatusOK || !bytes.Equal(rec.Body.Bytes(), want) {
		t.Fatalf("silver beret: status %d, not its original picture", rec.Code)
	}
	if rec := get("/cherry/butler/skin/1900001699.png"); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown skin: status %d", rec.Code)
	}
	if rec := get("/cherry/butler/1900000006.png"); rec.Code != http.StatusOK {
		t.Fatalf("merry's default picture: status %d", rec.Code)
	}
}

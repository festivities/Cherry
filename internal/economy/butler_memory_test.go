package economy

import (
	"bytes"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"cherry/internal/testutil"
)

// TestButlerMemoriesShipped: Merry has two memories, 1 (the bedroom) and 2 (the rose greenhouse), with their cards and scene
// pictures from the donation's folder and a top message; no other butler has memories; the shipped data passes the load check.
func TestButlerMemoriesShipped(t *testing.T) {
	if err := checkButlers(ActiveButlers()); err != nil {
		t.Fatalf("shipped butlers: %v", err)
	}
	d, ok := ButlerByID(merry)
	if !ok || len(d.Memories) != 2 || d.MemoryMessage == "" {
		t.Fatalf("merry: %d memories, message %q", len(d.Memories), d.MemoryMessage)
	}
	bedroom, ok := d.Memory(1)
	if !ok || bedroom.Title != "Merry's Bedroom" || bedroom.ListPicture != memoryFolder+"Merry_Thumbnail_1630072965.png" ||
		bedroom.Thumbnail != bedroom.ListPicture || len(bedroom.Scenes) != 1 || bedroom.Scenes[0].Picture != memoryFolder+"Merry_1630072965.png" {
		t.Fatalf("memory 1: %+v", bedroom)
	}
	greenhouse, ok := d.Memory(2)
	if !ok || greenhouse.ListPicture != memoryFolder+"admin_img_list_thum_1630075205.png" ||
		greenhouse.Scenes[0].Picture != memoryFolder+"butler_memory_make_bg_1630075205.png" {
		t.Fatalf("memory 2: %+v", greenhouse)
	}
	if _, ok := d.Memory(3); ok {
		t.Fatal("merry has a memory 3")
	}
	for _, b := range ActiveButlers().Butlers {
		if b.ID != merry && (len(b.Memories) != 0 || b.MemoryMessage != "") {
			t.Fatalf("butler %d has memories", b.ID)
		}
	}
}

// TestButlerMemoryChecksRefuseBadData: a memory with its id from 1, a title, a scene with a picture and a line, and only
// .png pictures directly in the memory folder is accepted; each broken field, and a repeated id, is refused.
func TestButlerMemoryChecksRefuseBadData(t *testing.T) {
	picture := memoryFolder + "Merry_1630072965.png"
	good := func() ButlerMemory {
		return ButlerMemory{ID: 1, Title: "Bedroom", ListPicture: picture, Thumbnail: picture,
			Scenes: []ButlerScene{{Picture: picture, Line: "Welcome"}}}
	}
	check := func(ms ...ButlerMemory) error {
		d := validMerry(ButlerSkin{ID: merrySilver, Code: "cherry_merry_x", Name: "Merry: X", Outfit: []string{"CUON009CV"}, SkinImage: "cache/x.png", Price: 30})
		d.Memories = ms
		return checkButlers(butlersWith(d))
	}
	if err := check(good()); err != nil {
		t.Fatalf("a valid memory: %v", err)
	}
	if err := check(good(), good()); err == nil {
		t.Error("a memory id listed twice was accepted")
	}
	bad := map[string]func(m *ButlerMemory){
		"id 0":                            func(m *ButlerMemory) { m.ID = 0 },
		"empty title":                     func(m *ButlerMemory) { m.Title = " " },
		"no scenes":                       func(m *ButlerMemory) { m.Scenes = nil },
		"list picture outside the folder": func(m *ButlerMemory) { m.ListPicture = "cache/butler/butler_skin_master/skin_1597832311.png" },
		"thumbnail missing":               func(m *ButlerMemory) { m.Thumbnail = "" },
		"thumbnail in a subfolder":        func(m *ButlerMemory) { m.Thumbnail = memoryFolder + "sub/x.png" },
		"picture not a png":               func(m *ButlerMemory) { m.Thumbnail = memoryFolder + "x.jpg" },
		"scene without a picture":         func(m *ButlerMemory) { m.Scenes[0].Picture = "" },
		"scene without a line":            func(m *ButlerMemory) { m.Scenes[0].Line = "" },
	}
	for name, mut := range bad {
		m := good()
		mut(&m)
		if err := check(m); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// TestButlerMemoryPicturesAndRoute: Merry's memory pictures are in the archive (the startup check reads them, and a missing
// one is refused). The route serves a listed picture as its original file; another file of the folder (even a byte-identical
// copy), a path out of it, another extension and a POST are 404.
func TestButlerMemoryPicturesAndRoute(t *testing.T) {
	testutil.UseArchive(t)
	if err := verifyButlers(ActiveButlers()); err != nil {
		t.Fatalf("shipped memories: %v", err)
	}
	bad := *ActiveButlers()
	bad.Butlers = slices.Clone(bad.Butlers)
	for i := range bad.Butlers {
		if bad.Butlers[i].ID == merry {
			ms := slices.Clone(bad.Butlers[i].Memories)
			ms[0].ListPicture = memoryFolder + "absent.png"
			bad.Butlers[i].Memories = ms
		}
	}
	if err := verifyButlers(&bad); err == nil || !strings.Contains(err.Error(), "absent.png") {
		t.Fatalf("absent memory picture: err = %v", err)
	}

	get := func(method, path string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		HandleButlerImage(rec, httptest.NewRequest(method, path, nil))
		return rec
	}
	want, err := fs.ReadFile(butlerSkinFS, memoryFolder+"Merry_1630072965.png")
	if err != nil {
		t.Fatal(err)
	}
	if rec := get(http.MethodGet, "/cherry/butler/memory/Merry_1630072965.png"); rec.Code != http.StatusOK || !bytes.Equal(rec.Body.Bytes(), want) {
		t.Fatalf("listed scene picture: status %d, not the original file", rec.Code)
	}
	for _, c := range []struct{ method, path string }{
		{http.MethodGet, "/cherry/butler/memory/admin_img_list_thum_1636450873.png"}, // in the folder, not listed
		{http.MethodGet, "/cherry/butler/memory/Merry_1630074702.png"},               // a byte-identical copy, not listed
		{http.MethodGet, "/cherry/butler/memory/../../butler_skin_master/skin_1597832311.png"},
		{http.MethodGet, "/cherry/butler/memory/Merry_1630072965.jpg"},
		{http.MethodPost, "/cherry/butler/memory/Merry_1630072965.png"},
	} {
		if rec := get(c.method, c.path); rec.Code != http.StatusNotFound {
			t.Fatalf("%s %s: status %d, want 404", c.method, c.path, rec.Code)
		}
	}
}

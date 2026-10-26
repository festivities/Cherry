package itemconv

import (
	"archive/zip"
	"bytes"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"testing"
)

// writeZip builds a small item.zip: entries maps "item/<kind>/<id>/<name>" to its bytes.
func writeZip(t *testing.T, entries map[string][]byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "item.zip")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	for name, body := range entries {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write(body); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

func testPNG(t *testing.T, w, h int) []byte {
	t.Helper()
	var buf bytes.Buffer
	im := image.NewNRGBA(image.Rect(0, 0, w, h))
	for i := range im.Pix {
		im.Pix[i] = 0x80
	}
	if err := png.Encode(&buf, im); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// TestBuildSkipsAndWrites: originals, undecodable categories and items without art are counted and left out;
// the rest become folders with the rendered dp.png; a rerun writes the same bytes.
func TestBuildSkipsAndWrites(t *testing.T) {
	part := testPNG(t, 8, 8)
	zipPath := writeZip(t, map[string][]byte{
		"item/custom/224400179/iteminfo.artsitem": []byte("1;5;7;7;10;0_0.png;0;"),
		"item/custom/224400179/0_0.png":           part,
		"item/custom/224400001/iteminfo.artsitem": []byte("1;5;7;7;10;0_0.png;0;"), // a device backup has it
		"item/custom/224400001/0_0.png":           part,
		"item/custom/999999999/iteminfo.artsitem": []byte("0;0;"), // category letters undecodable
		"item/dress/225106259/iteminfo.artsitem":  []byte("5;6;1;5;7;7;10;0_0.png;0;"),
		"item/dress/225106259/0_0.png":            part,
		"item/dress/225200001/iteminfo.artsitem":  []byte("0;0;0;0;"),
	})
	out := t.TempDir()
	opt := Options{
		Zip: zipPath,
		Out: out,
		Original: func(kind string, id int64) bool {
			return id == 224400001
		},
		Render: func(dir, kind string) ([]byte, bool) {
			if filepath.Base(dir) == "225200001" {
				return nil, false
			}
			return []byte("DP " + kind + " " + filepath.Base(dir)), true
		},
	}
	rep, err := Build(opt)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]map[string]int{
		"Converted":   {"custom": 1, "dress": 1},
		"Originals":   {"custom": 1},
		"Undecodable": {"custom": 1},
		"NoArt":       {"dress": 1},
	}
	got := map[string]map[string]int{"Converted": rep.Converted, "Originals": rep.Originals, "Undecodable": rep.Undecodable, "NoArt": rep.NoArt}
	for name, counts := range want {
		for kind, n := range counts {
			if got[name][kind] != n {
				t.Errorf("%s[%s] = %d, want %d", name, kind, got[name][kind], n)
			}
		}
	}
	if len(rep.Errors) != 0 {
		t.Errorf("errors: %v", rep.Errors)
	}
	for _, rel := range []string{"custom/224400179/1409_iteminfo.artsitem", "custom/224400179/0_0.png", "custom/224400179/dp.png", "dress/225106259/dp.png"} {
		if _, err := os.Stat(filepath.Join(out, filepath.FromSlash(rel))); err != nil {
			t.Errorf("%s: %v", rel, err)
		}
	}
	for _, rel := range []string{"custom/224400001", "custom/999999999", "dress/225200001"} {
		if _, err := os.Stat(filepath.Join(out, filepath.FromSlash(rel))); err == nil {
			t.Errorf("%s was written", rel)
		}
	}
	dp := 0
	for _, f := range rep.Files {
		if f.Name == "dp.png" {
			dp++
			if f.Recipe != DpRecipe {
				t.Errorf("dp.png recipe %q", f.Recipe)
			}
		} else if f.Recipe != Recipe {
			t.Errorf("%s recipe %q", f.Name, f.Recipe)
		}
	}
	if dp != 2 || len(rep.Files) != 6 {
		t.Errorf("files %d (dp %d), want 6 (2 dp)", len(rep.Files), dp)
	}

	first := readTree(t, out)
	if _, err := Build(opt); err != nil {
		t.Fatal(err)
	}
	if second := readTree(t, out); len(second) != len(first) {
		t.Fatalf("rerun changed the tree: %d files, was %d", len(second), len(first))
	} else {
		for k, v := range first {
			if !bytes.Equal(second[k], v) {
				t.Errorf("rerun changed %s", k)
			}
		}
	}
}

func readTree(t *testing.T, root string) map[string][]byte {
	t.Helper()
	out := map[string][]byte{}
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := os.ReadFile(p)
		rel, _ := filepath.Rel(root, p)
		out[filepath.ToSlash(rel)] = b
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

package opdata

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type sample struct {
	N    int    `json:"n"`
	Name string `json:"name"`
}

func checkSample(s *sample) error {
	if s.N < 0 {
		return os.ErrInvalid
	}
	return nil
}

func TestDecodeIsStrict(t *testing.T) {
	if _, err := Decode([]byte(`{"n":1,"extra":2}`), checkSample); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("unknown key: err = %v", err)
	}
	if _, err := Decode([]byte(`{"n":1} {"n":2}`), checkSample); err == nil || !strings.Contains(err.Error(), "unexpected data") {
		t.Fatalf("trailing data: err = %v", err)
	}
	if v, err := Decode([]byte("{\"n\":3}\n"), checkSample); err != nil || v.N != 3 {
		t.Fatalf("valid document: %v %+v", err, v)
	}
}

func TestFileOverrideReplacesDefault(t *testing.T) {
	f := New("sample.json", []byte(`{"n":1,"name":"default"}`), checkSample, nil)
	t.Cleanup(f.Reset)
	if got := f.Get(); got.N != 1 || got.Name != "default" {
		t.Fatalf("default = %+v", got)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "sample.json")
	if err := os.WriteFile(path, []byte(`{"n":2,"name":"operator"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := f.LoadFrom(path); err != nil {
		t.Fatalf("override: %v", err)
	}
	if got := f.Get(); got.N != 2 || got.Name != "operator" {
		t.Fatalf("override not active: %+v", got)
	}

	bad := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(bad, []byte(`{"n":-1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := f.LoadFrom(bad); err == nil || !strings.Contains(err.Error(), bad) {
		t.Fatalf("bad override: err = %v, want the path named", err)
	}
	if got := f.Get(); got.N != 2 {
		t.Fatal("a refused override changed the active value")
	}

	if err := f.LoadFrom(filepath.Join(dir, "missing.json")); err != nil {
		t.Fatalf("missing override: %v", err)
	}
	if got := f.Get(); got.N != 1 {
		t.Fatalf("missing override should keep the default, got %+v", got)
	}
}

func TestEmbeddedDefaultMustBeValid(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("an invalid embedded default must panic at New")
		}
	}()
	New("broken.json", []byte(`{"n":-1}`), checkSample, nil)
}

// TestVerifyRunsOnlyOnLoad: the startup verify runs for an override and for LoadFrom("") (the embedded
// default), and never for the default alone.
func TestVerifyRunsOnlyOnLoad(t *testing.T) {
	refuse := func(s *sample) error {
		if s.Name == "no art" {
			return os.ErrNotExist
		}
		return nil
	}
	f := New("sample.json", []byte(`{"n":1,"name":"no art"}`), checkSample, refuse)
	t.Cleanup(f.Reset)
	if f.Get().Name != "no art" {
		t.Fatal("New must not run verify")
	}
	if err := f.LoadFrom(""); err == nil || !strings.Contains(err.Error(), "embedded data/sample.json") {
		t.Fatalf("LoadFrom(\"\") err = %v, want the embedded default refused by verify", err)
	}
}

package opdata

import (
	"strings"
	"testing"
)

// isolate points the config folder at an empty temp dir, so Load never reads the lab's operator files.
func isolate(t *testing.T) {
	t.Helper()
	base := t.TempDir()
	t.Setenv("LOCALAPPDATA", base)
	t.Setenv("XDG_CACHE_HOME", base)
	ResetOffers()
	t.Cleanup(ResetOffers)
}

func status(t *testing.T, name string) Status {
	t.Helper()
	for _, s := range Live() {
		if s.Name == name {
			return s
		}
	}
	t.Fatalf("no status for %s", name)
	return Status{}
}

// A published database version that fails its checks falls back to the next source for that document alone, and is
// listed as rejected; a valid one is used and not rejected.
func TestOfferFallsBackPerDocument(t *testing.T) {
	isolate(t)
	f := New("sample-offer.json", []byte(`{"n":1,"name":"default"}`), checkSample, nil)
	t.Cleanup(f.Reset)

	Offer("sample-offer.json", 2, []byte(`{"n":-1}`))
	if err := f.Load(); err != nil {
		t.Fatalf("a rejected database version must not fail the load: %v", err)
	}
	if got := f.Get(); got.N != 1 || got.Name != "default" {
		t.Fatalf("fallback = %+v, want the embedded default", got)
	}
	if s := status(t, "sample-offer.json"); s.Source != "embedded default" || s.Rejected == nil || s.Rejected.Version != 2 || !strings.Contains(s.Rejected.Error, "database v2") {
		t.Fatalf("status = %+v, want the default with v2 rejected", s)
	}

	ResetOffers()
	f.Reset()
	Offer("sample-offer.json", 3, []byte(`{"n":5,"name":"database"}`))
	if err := f.Load(); err != nil {
		t.Fatal(err)
	}
	if got := f.Get(); got.N != 5 || got.Name != "database" {
		t.Fatalf("database version = %+v", got)
	}
	if s := status(t, "sample-offer.json"); s.Source != "database v3" || s.Version != 3 || s.Rejected != nil {
		t.Fatalf("status = %+v, want database v3 and nothing rejected", s)
	}
}

// Validate runs the document's checks on a body and changes nothing; an unknown name is an error.
func TestValidateChangesNothing(t *testing.T) {
	isolate(t)
	f := New("sample-validate.json", []byte(`{"n":1,"name":"default"}`), checkSample, nil)
	t.Cleanup(f.Reset)
	if err := f.Load(); err != nil {
		t.Fatal(err)
	}
	if err := Validate("sample-validate.json", []byte(`{"n":7,"name":"draft"}`)); err != nil {
		t.Fatalf("valid draft: %v", err)
	}
	if err := Validate("sample-validate.json", []byte(`{"n":-1}`)); err == nil {
		t.Fatal("an invalid draft validated")
	}
	if err := Validate("sample-validate.json", []byte(`{"n":1,"extra":2}`)); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("strict decode: %v", err)
	}
	if got := f.Get(); got.N != 1 || got.Name != "default" {
		t.Fatalf("Validate changed the live value: %+v", got)
	}
	if err := Validate("no-such-document.json", []byte(`{}`)); err == nil {
		t.Fatal("an unknown name validated")
	}
}

// A switch file is loaded like a document but is not one, and a document name cannot be declared twice.
func TestSwitchIsNotADocument(t *testing.T) {
	isolate(t)
	sw := NewSwitch[sample]("sample-switch.json", []byte(`{"n":1,"name":"on"}`))
	if Known("sample-switch.json") {
		t.Fatal("a switch file is a document")
	}
	if err := sw.Load(); err != nil || sw.Get().Name != "on" {
		t.Fatalf("switch load: %v %+v", err, sw.Get())
	}
	New("sample-twice.json", []byte(`{"n":1,"name":"a"}`), checkSample, nil)
	defer func() {
		if recover() == nil {
			t.Fatal("a second declaration of a document name must panic")
		}
	}()
	New("sample-twice.json", []byte(`{"n":1,"name":"b"}`), checkSample, nil)
}

// Swap makes a hot document's publish live at once and its un-publish fall back to the config file or default; a document
// that is not hot is left on what it loaded.
func TestSwapHotOnly(t *testing.T) {
	isolate(t)
	hot := NewHot("sample-hot.json", []byte(`{"n":1,"name":"default"}`), checkSample, nil)
	cold := New("sample-cold.json", []byte(`{"n":1,"name":"default"}`), checkSample, nil)
	t.Cleanup(hot.Reset)
	t.Cleanup(cold.Reset)
	for _, f := range []*File[sample]{hot, cold} {
		if err := f.Load(); err != nil {
			t.Fatal(err)
		}
	}
	if !IsHot("sample-hot.json") || IsHot("sample-cold.json") {
		t.Fatal("IsHot does not follow NewHot")
	}

	if ok, err := Swap("sample-hot.json", 4, []byte(`{"n":4,"name":"published"}`)); !ok || err != nil {
		t.Fatalf("Swap hot = %v, %v", ok, err)
	}
	if got := hot.Get(); got.N != 4 {
		t.Fatalf("hot value = %+v, want the published v4", got)
	}
	if s := status(t, "sample-hot.json"); s.Source != "database v4" || s.Version != 4 {
		t.Fatalf("status = %+v, want database v4", s)
	}
	if ok, err := Swap("sample-hot.json", 5, []byte(`{"n":-1}`)); !ok || err == nil || hot.Get().N != 4 {
		t.Fatalf("an invalid body must be refused and leave v4 live: %v, %v, %+v", ok, err, hot.Get())
	}
	if ok, err := Swap("sample-hot.json", 0, nil); !ok || err != nil || hot.Get().N != 1 {
		t.Fatalf("un-publish = %v, %v, %+v; want the embedded default", ok, err, hot.Get())
	}

	if ok, err := Swap("sample-cold.json", 2, []byte(`{"n":2,"name":"published"}`)); ok || err != nil || cold.Get().N != 1 {
		t.Fatalf("Swap cold = %v, %v, %+v; want no change", ok, err, cold.Get())
	}
}

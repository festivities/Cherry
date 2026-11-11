package main

import (
	"testing"

	"cherry/internal/opdata"
)

// TestEveryDocumentLoadsAtStart: each operator document declared in the binary is loaded by the startup order (a document
// nobody loads would never reach its published version), the switch files are not documents, and the document count is
// the one AGENTS "Cherry-made content" lists: a new document changes this number on purpose.
func TestEveryDocumentLoadsAtStart(t *testing.T) {
	base := t.TempDir()
	t.Setenv("LOCALAPPDATA", base)
	t.Setenv("XDG_CACHE_HOME", base)
	if err := loadOperatorData(); err != nil {
		t.Fatal(err)
	}
	for _, s := range opdata.Live() {
		if s.Source == "not loaded" {
			t.Errorf("document %s is declared but no startup loader loads it", s.Name)
		}
	}
	if got := len(opdata.Names()); got != 26 {
		t.Errorf("%d documents declared, want 26: update this test and AGENTS when a document is added", got)
	}
	for _, sw := range []string{"streaming.json", "itemconv.json"} {
		if opdata.Known(sw) {
			t.Errorf("switch file %s is a document: the database would store it", sw)
		}
	}
}

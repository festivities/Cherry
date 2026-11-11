// Package opdata loads Cherry's operator-editable data files (the values Cherry invents, see AGENTS.md
// "Operator-editable fabricated content"). Each file is a document with three sources, in precedence order: a
// published version in the database (offered at start by store.OfferPublishedDocuments), an operator file with the
// same name in the config folder of the data directory (%LOCALAPPDATA%\Cherry\config by default), and the embedded
// default from data/. Files decode strictly (unknown keys are an error) and are validated at startup. A bad config
// file stops Cherry before its listeners open, naming the file and the entry. A published database version that
// fails its checks falls back to the next source for that one document, and is listed as rejected (Live).
// documents.go holds the registry the database and the staff API use.
package opdata

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"sync/atomic"

	"cherry/internal/config"
)

// Dir is the operator config directory: config under the state directory ("" when that is unknown).
func Dir() string {
	if dir, err := config.DataDir(); err == nil {
		return filepath.Join(dir, "config")
	}
	return ""
}

// Decode strictly decodes one JSON document (unknown keys and trailing data are errors) and runs
// check, which validates the value and may derive runtime fields from it.
func Decode[T any](raw []byte, check func(*T) error) (*T, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var v T
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, errors.New("unexpected data after the JSON document")
	}
	if check != nil {
		if err := check(&v); err != nil {
			return nil, err
		}
	}
	return &v, nil
}

// File is one operator data file of type T.
type File[T any] struct {
	name     string
	embedded []byte
	check    func(*T) error // structural checks (pure); also derives runtime fields
	verify   func(*T) error // startup checks against the item archive (nil = none)
	def      *T
	active   atomic.Pointer[T]
}

// New declares an operator document named name: the file name in the config dir, in data/ and in the database. The
// embedded default is decoded and checked here, so a broken default panics at start (a build error, not a request-time
// failure). verify runs at Load, LoadFrom and Validate. A name declared twice panics.
func New[T any](name string, embedded []byte, check, verify func(*T) error) *File[T] {
	f := newFile(name, embedded, check, verify)
	register(&document{name: name, hasArt: verify != nil, validate: f.validate, apply: f.apply})
	return f
}

// NewSwitch declares a configuration switch (streaming.json, itemconv.json): the same file and config-dir override as
// New, but not a document, so the database never stores it.
func NewSwitch[T any](name string, embedded []byte) *File[T] {
	return newFile[T](name, embedded, nil, nil)
}

func newFile[T any](name string, embedded []byte, check, verify func(*T) error) *File[T] {
	def, err := Decode(embedded, check)
	if err != nil {
		panic("embedded data/" + name + ": " + err.Error())
	}
	return &File[T]{name: name, embedded: embedded, check: check, verify: verify, def: def}
}

// Get returns the file in use: the loaded operator file, or the embedded default.
func (f *File[T]) Get() *T {
	if v := f.active.Load(); v != nil {
		return v
	}
	return f.def
}

// parse decodes raw and runs every check, the art checks included. It changes nothing.
func (f *File[T]) parse(raw []byte) (*T, error) {
	v, err := Decode(raw, f.check)
	if err == nil && f.verify != nil {
		err = f.verify(v)
	}
	return v, err
}

// validate is parse for the registry: every check of raw, nothing made live.
func (f *File[T]) validate(raw []byte) error {
	_, err := f.parse(raw)
	return err
}

// apply makes raw the file in use, taken from u. where names the source in errors and in the log. A refused raw leaves
// the active value unchanged.
func (f *File[T]) apply(raw []byte, where string, u use) error {
	v, err := f.parse(raw)
	if err != nil {
		return fmt.Errorf("%s: %w", where, err)
	}
	f.active.Store(v)
	setUse(f.name, u)
	log.Printf("cherry: %s loaded from %s", f.name, where)
	return nil
}

// Load is the startup load. A published database version offered for the document (Offer) is used when it passes its
// checks. Otherwise, or when it fails them (it is then listed as rejected and only this document falls back), the
// operator file in Dir() is used when it exists, else the embedded default. A refused config file is an error.
func (f *File[T]) Load() error {
	if o, ok := offered(f.name); ok {
		u := use{source: fmt.Sprintf("database v%d", o.version), version: o.version}
		err := f.apply(o.raw, u.source, u)
		if err == nil {
			return nil
		}
		reject(f.name, o.version, err)
		log.Printf("cherry: document %s rejected at startup, next source used: %v", f.name, err)
	}
	path := ""
	if dir := Dir(); dir != "" {
		path = filepath.Join(dir, f.name)
	}
	return f.LoadFrom(path)
}

// LoadFrom makes the file at path the one in use when it exists (the embedded default when path is "" or missing). A
// refused file is an error naming it, and the active value stays.
func (f *File[T]) LoadFrom(path string) error {
	raw, where, u := f.embedded, "embedded data/"+f.name, use{source: "embedded default"}
	if path != "" {
		b, err := os.ReadFile(path)
		switch {
		case err == nil:
			raw, where, u = b, path, use{source: "config file"}
		case !errors.Is(err, fs.ErrNotExist):
			return fmt.Errorf("read %s: %w", path, err)
		}
	}
	return f.apply(raw, where, u)
}

// Reset drops the loaded file and its source, so Get returns the embedded default again (tests).
func (f *File[T]) Reset() {
	f.active.Store(nil)
	forgetUse(f.name)
}

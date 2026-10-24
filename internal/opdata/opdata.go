// Package opdata loads Cherry's operator-editable data files (the values Cherry invents, see AGENTS.md
// "Operator-editable fabricated content"). The defaults are embedded from data/. An operator file
// with the same name in %LOCALAPPDATA%\Cherry\config (os.UserCacheDir) replaces the default
// entirely. Files decode strictly (unknown keys are an error) and are validated once at startup;
// a bad file stops Cherry before its listeners open, with an error naming the file and the entry.
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
)

// Dir is the operator config directory ("" when the user cache directory is unknown).
func Dir() string {
	if dir, err := os.UserCacheDir(); err == nil {
		return filepath.Join(dir, "Cherry", "config")
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

// New declares a data file named name (the file name in the config dir and in data/). The embedded
// default is decoded and checked here, so a broken default panics at start (a build error, not a
// request-time failure). verify runs only at Load/LoadFrom.
func New[T any](name string, embedded []byte, check, verify func(*T) error) *File[T] {
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

// Load is the startup load: the operator file in Dir() when it exists, else the embedded default.
func (f *File[T]) Load() error {
	path := ""
	if dir := Dir(); dir != "" {
		path = filepath.Join(dir, f.name)
	}
	return f.LoadFrom(path)
}

// LoadFrom loads the file at path when it exists (the embedded default otherwise, also for path ""),
// runs every check and makes it the one in use. A refused file leaves the active one unchanged.
func (f *File[T]) LoadFrom(path string) error {
	raw, source := f.embedded, "embedded data/"+f.name
	if path != "" {
		b, err := os.ReadFile(path)
		switch {
		case err == nil:
			raw, source = b, path
		case !errors.Is(err, fs.ErrNotExist):
			return fmt.Errorf("read %s: %w", path, err)
		}
	}
	v, err := Decode(raw, f.check)
	if err == nil && f.verify != nil {
		err = f.verify(v)
	}
	if err != nil {
		return fmt.Errorf("%s: %w", source, err)
	}
	f.active.Store(v)
	log.Printf("cherry: %s loaded from %s", f.name, source)
	return nil
}

// Reset drops the loaded file, so Get returns the embedded default again (tests).
func (f *File[T]) Reset() { f.active.Store(nil) }

package opdata

import (
	"fmt"
	"log"
	"sort"
	"sync"
)

// The registry: every document declared with New, the published database versions offered for the next load, and
// what each document runs on. Documents are registered at package init, so reads need no lock for the names; the
// mutable state is guarded by mu.

// document is one registered document, as the registry needs it. The fields are the File's methods, so the registry
// holds no interface.
type document struct {
	name     string
	hasArt   bool
	validate func(raw []byte) error
	apply    func(raw []byte, where string, u use) error
}

// use is where a document's value came from: "embedded default", "config file" or "database vN".
type use struct {
	source  string
	version int
}

// offer is a published database version handed over for the next Load.
type offer struct {
	version int
	raw     []byte
}

// Rejection is a published database version refused at startup: the document runs on its next source instead.
type Rejection struct {
	Version int    `json:"version"`
	Error   string `json:"error"`
}

// Status is what a document runs on (Live). Source is "embedded default", "config file", "database vN" (Version is N
// then), "not loaded" before the startup load, or "not a document" for a rejected name the build does not know.
type Status struct {
	Name     string     `json:"name"`
	Source   string     `json:"source"`
	Version  int        `json:"version,omitempty"`
	Rejected *Rejection `json:"rejected,omitempty"`
}

var (
	mu        sync.Mutex
	documents = map[string]*document{}
	uses      = map[string]use{}
	offers    = map[string]offer{}
	rejects   = map[string]Rejection{}
)

func register(d *document) {
	mu.Lock()
	defer mu.Unlock()
	if _, dup := documents[d.name]; dup {
		panic("opdata: document " + d.name + " is declared twice")
	}
	documents[d.name] = d
}

// Known reports whether name is a document (a configuration switch is not).
func Known(name string) bool {
	mu.Lock()
	defer mu.Unlock()
	_, ok := documents[name]
	return ok
}

// HasArtChecks reports whether document name has startup checks against the item archive. They skip themselves
// without the asset store.
func HasArtChecks(name string) bool {
	mu.Lock()
	defer mu.Unlock()
	d, ok := documents[name]
	return ok && d.hasArt
}

// Names lists the documents in name order.
func Names() []string {
	mu.Lock()
	defer mu.Unlock()
	return sortedDocumentNames()
}

func sortedDocumentNames() []string {
	names := make([]string, 0, len(documents))
	for name := range documents {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// Validate runs every check of document name on raw: decode, the structural checks and the art checks the process can
// run. Nothing becomes live. An unknown name is an error.
func Validate(name string, raw []byte) error {
	mu.Lock()
	d, ok := documents[name]
	mu.Unlock()
	if !ok {
		return fmt.Errorf("%q is not a document", name)
	}
	return d.validate(raw)
}

// Offer hands the next Load the published database version of document name (store.OfferPublishedDocuments calls it at
// start, before the operator files load). A name the build does not know is logged and listed as rejected.
func Offer(name string, version int, raw []byte) {
	mu.Lock()
	defer mu.Unlock()
	if _, ok := documents[name]; !ok {
		rejects[name] = Rejection{Version: version, Error: "not a document this build knows"}
		log.Printf("cherry: published document %s v%d is not a document this build knows; ignored", name, version)
		return
	}
	offers[name] = offer{version: version, raw: raw}
}

// ResetOffers forgets the offers and the rejections (tests and restarts in one process).
func ResetOffers() {
	mu.Lock()
	defer mu.Unlock()
	offers = map[string]offer{}
	rejects = map[string]Rejection{}
}

func offered(name string) (offer, bool) {
	mu.Lock()
	defer mu.Unlock()
	o, ok := offers[name]
	return o, ok
}

func reject(name string, version int, err error) {
	mu.Lock()
	defer mu.Unlock()
	rejects[name] = Rejection{Version: version, Error: err.Error()}
}

func setUse(name string, u use) {
	mu.Lock()
	defer mu.Unlock()
	uses[name] = u
}

func forgetUse(name string) {
	mu.Lock()
	defer mu.Unlock()
	delete(uses, name)
}

// Live reports what every document runs on: the registered documents in name order, then the rejected names the build
// does not know.
func Live() []Status {
	mu.Lock()
	defer mu.Unlock()
	var out []Status
	for _, name := range sortedDocumentNames() {
		s := Status{Name: name, Source: "not loaded"}
		if u, ok := uses[name]; ok {
			s.Source, s.Version = u.source, u.version
		}
		if r, ok := rejects[name]; ok {
			s.Rejected = &r
		}
		out = append(out, s)
	}
	var unknown []string
	for name := range rejects {
		if _, ok := documents[name]; !ok {
			unknown = append(unknown, name)
		}
	}
	sort.Strings(unknown)
	for _, name := range unknown {
		r := rejects[name]
		out = append(out, Status{Name: name, Source: "not a document", Rejected: &r})
	}
	return out
}

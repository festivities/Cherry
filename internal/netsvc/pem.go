package netsvc

import (
	"crypto/tls"
	"fmt"
	"log"
	"os"
	"slices"
	"strings"
	"sync"
	"time"
)

// pemCheckEvery is how often a handshake looks at the certificate files. A renewed pair is picked up within this time,
// without a restart.
const pemCheckEvery = 30 * time.Second

// PEMStore hands a handshake a certificate read from PEM files: the operator's publicly trusted pair for the staff UI or
// the game listener. It re-reads the pair when either file's modification time changes, checked at most every
// pemCheckEvery. A pair that fails to load keeps the previous certificate and is tried again at the next check. label
// names the store in the log ("staff", "game").
type PEMStore struct {
	certFile, keyFile string
	now               func() time.Time
	logger            *log.Logger
	label             string

	mu      sync.Mutex
	cert    *tls.Certificate
	stamp   [2]time.Time // modification times of the certificate and key files that cert was loaded from
	checked time.Time
}

// NewPEMStore loads the pair now; an error refuses the start.
func NewPEMStore(certFile, keyFile string, now func() time.Time, logger *log.Logger, label string) (*PEMStore, error) {
	stamp, err := stampOf(certFile, keyFile)
	if err != nil {
		return nil, fmt.Errorf("%s certificate: %w", label, err)
	}
	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return nil, fmt.Errorf("%s certificate: %w", label, err)
	}
	return &PEMStore{certFile: certFile, keyFile: keyFile, now: now, logger: logger, label: label,
		cert: &cert, stamp: stamp, checked: now()}, nil
}

// Get returns the certificate in use.
func (p *PEMStore) Get() *tls.Certificate {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := p.now()
	if now.Sub(p.checked) < pemCheckEvery {
		return p.cert
	}
	p.checked = now
	stamp, err := stampOf(p.certFile, p.keyFile)
	if err != nil {
		p.logger.Printf("cherry: %s certificate files: %v; the previous certificate stays", p.label, err)
		return p.cert
	}
	if stamp == p.stamp {
		return p.cert
	}
	cert, err := tls.LoadX509KeyPair(p.certFile, p.keyFile)
	if err != nil {
		p.logger.Printf("cherry: %s certificate reload failed; the previous certificate stays: %v", p.label, err)
		return p.cert
	}
	p.cert, p.stamp = &cert, stamp
	p.logger.Printf("cherry: %s certificate reloaded from %s", p.label, p.certFile)
	return p.cert
}

// stampOf reads the modification times of the two files.
func stampOf(certFile, keyFile string) ([2]time.Time, error) {
	var out [2]time.Time
	for i, p := range []string{certFile, keyFile} {
		info, err := os.Stat(p)
		if err != nil {
			return out, err
		}
		out[i] = info.ModTime()
	}
	return out, nil
}

// OperatorCert is the game listener's GetCertificate rule: the operator's PEM certificate when the ClientHello's SNI names
// one of hostnames (config lists them lower-case; the SNI is case-insensitive and one trailing dot is ignored), nil
// otherwise. A nil result makes crypto/tls use the self-signed Certificates, as it does without a PEM pair.
func OperatorCert(serverName string, hostnames []string, store *PEMStore) *tls.Certificate {
	if store == nil || !slices.Contains(hostnames, strings.ToLower(strings.TrimSuffix(serverName, "."))) {
		return nil
	}
	return store.Get()
}

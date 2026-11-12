package admin

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"fmt"
	"log"
	"math/big"
	"net"
	"os"
	"sync"
	"time"
)

// certCheckEvery is how often a handshake looks at the certificate files. A renewed pair is picked up within this time,
// without a restart.
const certCheckEvery = 30 * time.Second

// certStore hands the handshake its certificate. With files it re-reads the PEM pair when either file's modification time
// changes, checked at most every certCheckEvery. A pair that fails to load keeps the previous certificate and is tried
// again at the next check. Without files it holds a self-signed certificate for local use.
type certStore struct {
	certFile, keyFile string
	now               func() time.Time
	logger            *log.Logger

	mu      sync.Mutex
	cert    *tls.Certificate
	stamp   [2]time.Time // modification times of the certificate and key files that cert was loaded from
	checked time.Time
}

// newCertStore loads the pair now (an error refuses the start), or makes a self-signed certificate for hostname when
// certFile is empty.
func newCertStore(certFile, keyFile, hostname string, now func() time.Time, logger *log.Logger) (*certStore, error) {
	c := &certStore{certFile: certFile, keyFile: keyFile, now: now, logger: logger}
	if certFile == "" {
		cert, err := selfSigned(hostname)
		if err != nil {
			return nil, fmt.Errorf("admin: self-signed certificate: %w", err)
		}
		c.cert = &cert
		logger.Printf("cherry: staff UI certificate is SELF-SIGNED (local use only; a public server needs staff.tls-cert-file)")
		return c, nil
	}
	stamp, err := stampOf(certFile, keyFile)
	if err != nil {
		return nil, fmt.Errorf("admin: staff certificate: %w", err)
	}
	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return nil, fmt.Errorf("admin: staff certificate: %w", err)
	}
	c.cert, c.stamp, c.checked = &cert, stamp, now()
	return c, nil
}

// get is the tls.Config GetCertificate hook.
func (c *certStore) get(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.certFile == "" {
		return c.cert, nil
	}
	now := c.now()
	if now.Sub(c.checked) < certCheckEvery {
		return c.cert, nil
	}
	c.checked = now
	stamp, err := stampOf(c.certFile, c.keyFile)
	if err != nil {
		c.logger.Printf("cherry: staff certificate files: %v; the previous certificate stays", err)
		return c.cert, nil
	}
	if stamp == c.stamp {
		return c.cert, nil
	}
	cert, err := tls.LoadX509KeyPair(c.certFile, c.keyFile)
	if err != nil {
		c.logger.Printf("cherry: staff certificate reload failed; the previous certificate stays: %v", err)
		return c.cert, nil
	}
	c.cert, c.stamp = &cert, stamp
	c.logger.Printf("cherry: staff certificate reloaded from %s", c.certFile)
	return c.cert, nil
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

// selfSigned makes a certificate for local use: ECDSA P-256, one year, for localhost, 127.0.0.1, ::1 and hostname.
func selfSigned(hostname string) (tls.Certificate, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
	if err != nil {
		return tls.Certificate{}, err
	}
	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: "Cherry staff (self-signed)"},
		NotBefore:    now.Add(-time.Hour),
		NotAfter:     now.Add(365 * 24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     []string{"localhost"},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")},
	}
	if hostname != "" {
		if ip := net.ParseIP(hostname); ip != nil {
			tmpl.IPAddresses = append(tmpl.IPAddresses, ip)
		} else {
			tmpl.DNSNames = append(tmpl.DNSNames, hostname)
		}
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return tls.Certificate{}, err
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, nil
}

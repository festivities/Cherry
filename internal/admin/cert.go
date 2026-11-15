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
	"time"

	"cherry/internal/netsvc"
)

// certStore hands the staff listener its certificate: the PEM pair of staff.tls-cert-file (re-read by netsvc.PEMStore when
// it changes), or without files a self-signed certificate for local use.
type certStore struct {
	store *netsvc.PEMStore // nil without files
	self  *tls.Certificate
}

// newCertStore loads the pair now (an error refuses the start), or makes a self-signed certificate for hostname when
// certFile is empty.
func newCertStore(certFile, keyFile, hostname string, now func() time.Time, logger *log.Logger) (*certStore, error) {
	if certFile != "" {
		store, err := netsvc.NewPEMStore(certFile, keyFile, now, logger, "staff")
		if err != nil {
			return nil, fmt.Errorf("admin: %w", err)
		}
		return &certStore{store: store}, nil
	}
	cert, err := selfSigned(hostname)
	if err != nil {
		return nil, fmt.Errorf("admin: self-signed certificate: %w", err)
	}
	logger.Printf("cherry: staff UI certificate is SELF-SIGNED (local use only; a public server needs staff.tls-cert-file)")
	return &certStore{self: &cert}, nil
}

// get is the tls.Config GetCertificate hook.
func (c *certStore) get(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	if c.store != nil {
		return c.store.Get(), nil
	}
	return c.self, nil
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

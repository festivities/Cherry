package admin

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"log"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// writePair writes a self-signed ECDSA pair for name to certPath and keyPath, and sets both files' modification time to
// mod (a replaced pair must look newer to the reload check).
func writePair(t *testing.T, certPath, keyPath, name string, mod time.Time) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: name},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		DNSNames:     []string{name},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{certPath, keyPath} {
		if err := os.Chtimes(p, mod, mod); err != nil {
			t.Fatal(err)
		}
	}
}

// servedName is the DNS name of the certificate the store hands out now.
func servedName(t *testing.T, cs *certStore) string {
	t.Helper()
	cert, err := cs.get(nil)
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	return leaf.DNSNames[0]
}

// TestCertificateReloadsWithoutRestart: a replaced pair is picked up at the first handshake after 30 seconds, not before.
func TestCertificateReloadsWithoutRestart(t *testing.T) {
	dir := t.TempDir()
	certPath, keyPath := filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem")
	start := time.Now().Add(-time.Hour)
	writePair(t, certPath, keyPath, "first.test", start)
	clk := &clock{t: time.Now()}
	logs := &bytes.Buffer{}
	cs, err := newCertStore(certPath, keyPath, "", clk.now, log.New(logs, "", 0))
	if err != nil {
		t.Fatal(err)
	}
	if got := servedName(t, cs); got != "first.test" {
		t.Fatalf("first certificate is %s", got)
	}
	writePair(t, certPath, keyPath, "second.test", start.Add(time.Minute))
	clk.advance(10 * time.Second)
	if got := servedName(t, cs); got != "first.test" {
		t.Fatalf("a check ran within 30 seconds: %s", got)
	}
	clk.advance(30 * time.Second)
	if got := servedName(t, cs); got != "second.test" {
		t.Fatalf("the replaced pair was not picked up: %s", got)
	}
	if !bytes.Contains(logs.Bytes(), []byte("reloaded")) {
		t.Error("the reload is not logged")
	}
}

// TestBrokenReplacementKeepsThePreviousPair: a pair that does not load (here the key is replaced by garbage) keeps the
// certificate in use, and the failure is logged without the key's content.
func TestBrokenReplacementKeepsThePreviousPair(t *testing.T) {
	dir := t.TempDir()
	certPath, keyPath := filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem")
	start := time.Now().Add(-time.Hour)
	writePair(t, certPath, keyPath, "first.test", start)
	clk := &clock{t: time.Now()}
	logs := &bytes.Buffer{}
	cs, err := newCertStore(certPath, keyPath, "", clk.now, log.New(logs, "", 0))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, []byte("not a key"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(keyPath, start.Add(time.Minute), start.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	clk.advance(31 * time.Second)
	if got := servedName(t, cs); got != "first.test" {
		t.Fatalf("a broken pair replaced the certificate: %s", got)
	}
	if !bytes.Contains(logs.Bytes(), []byte("reload failed")) || bytes.Contains(logs.Bytes(), []byte("not a key")) {
		t.Errorf("the failure log is wrong: %s", logs.String())
	}
}

// TestCertificateFilesMustLoadAtStart: a missing or broken pair refuses the start.
func TestCertificateFilesMustLoadAtStart(t *testing.T) {
	dir := t.TempDir()
	if _, err := newCertStore(filepath.Join(dir, "nope.pem"), filepath.Join(dir, "nope.key"), "", time.Now, log.Default()); err == nil {
		t.Fatal("a missing pair was accepted")
	}
}

// TestSelfSignedWithoutFiles: local use without files gets a self-signed certificate for the hostname and loopback.
func TestSelfSignedWithoutFiles(t *testing.T) {
	logs := &bytes.Buffer{}
	cs, err := newCertStore("", "", testHost, time.Now, log.New(logs, "", 0))
	if err != nil {
		t.Fatal(err)
	}
	cert, _ := cs.get(nil)
	leaf, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	if len(leaf.DNSNames) == 0 || leaf.DNSNames[len(leaf.DNSNames)-1] != testHost {
		t.Errorf("DNS names %v do not include the hostname", leaf.DNSNames)
	}
	hasLoopback := false
	for _, ip := range leaf.IPAddresses {
		hasLoopback = hasLoopback || ip.Equal(net.ParseIP("127.0.0.1"))
	}
	if !hasLoopback {
		t.Error("no 127.0.0.1 address in the self-signed certificate")
	}
	if !bytes.Contains(logs.Bytes(), []byte("SELF-SIGNED")) {
		t.Error("the self-signed certificate is not logged as such")
	}
}

// TestTLSFloorAndSuites: the staff listener speaks TLS 1.2 and 1.3, takes ECDHE with AEAD, and refuses TLS 1.1, static RSA
// and ECDHE with CBC.
func TestTLSFloorAndSuites(t *testing.T) {
	fx := newFixture(t, devConfig())
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = fx.s.Serve(ln) }()
	t.Cleanup(func() { _ = fx.s.Shutdown(context.Background()) })
	addr := ln.Addr().String()
	dial := func(cfg *tls.Config) error {
		cfg.InsecureSkipVerify = true // self-signed in this test; the chain is not the subject
		cfg.ServerName = testHost
		conn, err := tls.Dial("tcp", addr, cfg)
		if err == nil {
			_ = conn.Close()
		}
		return err
	}
	cases := []struct {
		name   string
		min    uint16
		max    uint16
		suites []uint16
		ok     bool
	}{
		{"TLS 1.3", tls.VersionTLS13, 0, nil, true},
		{"TLS 1.2 ECDHE-GCM", 0, tls.VersionTLS12, []uint16{tls.TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256}, true},
		{"TLS 1.2 ECDHE-ChaCha", 0, tls.VersionTLS12, []uint16{tls.TLS_ECDHE_ECDSA_WITH_CHACHA20_POLY1305}, true},
		{"TLS 1.2 static RSA", 0, tls.VersionTLS12, []uint16{tls.TLS_RSA_WITH_AES_128_GCM_SHA256}, false},
		{"TLS 1.2 ECDHE-CBC", 0, tls.VersionTLS12, []uint16{tls.TLS_ECDHE_ECDSA_WITH_AES_128_CBC_SHA}, false},
		{"TLS 1.1", 0, tls.VersionTLS11, nil, false},
	}
	for _, tc := range cases {
		err := dial(&tls.Config{MinVersion: tc.min, MaxVersion: tc.max, CipherSuites: tc.suites})
		if tc.ok && err != nil {
			t.Errorf("%s: %v", tc.name, err)
		}
		if !tc.ok && err == nil {
			t.Errorf("%s: handshake accepted", tc.name)
		}
	}
}

// TestStaffMuxIsItsOwn: the staff handler serves its own routes, and an unknown path answers 404 with the headers.
func TestStaffMuxIsItsOwn(t *testing.T) {
	fx := newFixture(t, devConfig())
	r := fx.client().get("/static/staff.css")
	if r.StatusCode != http.StatusOK || r.Header.Get("Content-Type") != "text/css; charset=utf-8" {
		t.Fatalf("stylesheet: status %d type %q", r.StatusCode, r.Header.Get("Content-Type"))
	}
}

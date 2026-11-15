package netsvc

import (
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"log"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// testClock is the clock a PEMStore reads: a test moves it past the 30 second check.
type testClock struct{ t time.Time }

func (c *testClock) now() time.Time          { return c.t }
func (c *testClock) advance(d time.Duration) { c.t = c.t.Add(d) }

// writeOperatorPair writes a self-signed RSA pair for name to certPath and keyPath, both with modification time mod (a
// replaced pair must look newer to the reload check), and returns the certificate's DER.
func writeOperatorPair(t *testing.T, certPath, keyPath, name string, mod time.Time) []byte {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
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
	if err := os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{certPath, keyPath} {
		if err := os.Chtimes(p, mod, mod); err != nil {
			t.Fatal(err)
		}
	}
	return der
}

// servedLeaf runs a real handshake over net.Pipe against a server with the game listener's rule (the self-signed pair,
// OperatorCert, DenyBlockedSNI, as cmd/cherry builds its TLS config) and returns the DER of the leaf the client got.
func servedLeaf(t *testing.T, certs []tls.Certificate, hostnames []string, store *PEMStore, sni string) []byte {
	t.Helper()
	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()
	srv := tls.Server(server, &tls.Config{
		Certificates: certs,
		GetCertificate: func(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
			return OperatorCert(hello.ServerName, hostnames, store), nil
		},
		GetConfigForClient: DenyBlockedSNI,
	})
	cli := tls.Client(client, &tls.Config{ServerName: sni, InsecureSkipVerify: true})
	done := make(chan error, 1)
	go func() { done <- srv.Handshake() }()
	if err := cli.Handshake(); err != nil {
		t.Fatalf("SNI %q: handshake: %v", sni, err)
	}
	if err := <-done; err != nil {
		t.Fatalf("SNI %q: server: %v", sni, err)
	}
	return cli.ConnectionState().PeerCertificates[0].Raw
}

// TestGameListenerServesTheOperatorPair: the operator's pair goes to the server.hostnames name; every other name, and no
// SNI, gets the self-signed pair, and without a PEM store the operator name gets it too.
func TestGameListenerServesTheOperatorPair(t *testing.T) {
	dir := t.TempDir()
	certPath, keyPath := filepath.Join(dir, "op.crt"), filepath.Join(dir, "op.key")
	clk := &testClock{t: time.Now()}
	opDER := writeOperatorPair(t, certPath, keyPath, "op.example", clk.t.Add(-time.Hour))
	store, err := NewPEMStore(certPath, keyPath, clk.now, log.New(&bytes.Buffer{}, "", 0), "game")
	if err != nil {
		t.Fatal(err)
	}
	certs, err := GenerateCerts("op.example")
	if err != nil {
		t.Fatal(err)
	}
	hostnames := []string{"op.example"}
	if got := servedLeaf(t, certs, hostnames, store, "op.example"); !bytes.Equal(got, opDER) {
		t.Error("SNI op.example did not get the operator certificate")
	}
	for _, sni := range []string{"fapi.play.naver.jp", "other.example", ""} {
		got := servedLeaf(t, certs, hostnames, store, sni)
		if !bytes.Equal(got, certs[0].Certificate[0]) && !bytes.Equal(got, certs[1].Certificate[0]) {
			t.Errorf("SNI %q: not a self-signed certificate", sni)
		}
	}
	if got := servedLeaf(t, certs, hostnames, nil, "op.example"); bytes.Equal(got, opDER) {
		t.Error("no PEM store, yet the operator certificate was served")
	}
}

// TestOperatorCertNames: the SNI match ignores case and one trailing dot; other names select nothing, and so does a
// missing store or an empty hostname list.
func TestOperatorCertNames(t *testing.T) {
	dir := t.TempDir()
	certPath, keyPath := filepath.Join(dir, "op.crt"), filepath.Join(dir, "op.key")
	clk := &testClock{t: time.Now()}
	writeOperatorPair(t, certPath, keyPath, "op.example", clk.t)
	store, err := NewPEMStore(certPath, keyPath, clk.now, log.New(&bytes.Buffer{}, "", 0), "game")
	if err != nil {
		t.Fatal(err)
	}
	hostnames := []string{"op.example"}
	for sni, want := range map[string]bool{
		"op.example": true, "OP.Example": true, "op.example.": true,
		"op.example.com": false, "example": false, "fapi.play.naver.jp": false, "": false,
	} {
		if got := OperatorCert(sni, hostnames, store) != nil; got != want {
			t.Errorf("SNI %q: operator certificate %v, want %v", sni, got, want)
		}
	}
	if OperatorCert("op.example", hostnames, nil) != nil {
		t.Error("no store: a certificate was selected")
	}
	if OperatorCert("op.example", nil, store) != nil {
		t.Error("no hostnames: a certificate was selected")
	}
}

// TestGameListenerReloadsThePair: a replaced pair is served at the first handshake after 30 seconds, not before, and the
// reload is logged under the game label.
func TestGameListenerReloadsThePair(t *testing.T) {
	dir := t.TempDir()
	certPath, keyPath := filepath.Join(dir, "op.crt"), filepath.Join(dir, "op.key")
	clk := &testClock{t: time.Now()}
	first := writeOperatorPair(t, certPath, keyPath, "op.example", clk.t.Add(-time.Hour))
	logs := &bytes.Buffer{}
	store, err := NewPEMStore(certPath, keyPath, clk.now, log.New(logs, "", 0), "game")
	if err != nil {
		t.Fatal(err)
	}
	certs, err := GenerateCerts("op.example")
	if err != nil {
		t.Fatal(err)
	}
	hostnames := []string{"op.example"}
	if got := servedLeaf(t, certs, hostnames, store, "op.example"); !bytes.Equal(got, first) {
		t.Fatal("the first pair is not served")
	}
	second := writeOperatorPair(t, certPath, keyPath, "op.example", clk.t.Add(-time.Minute))
	clk.advance(10 * time.Second)
	if got := servedLeaf(t, certs, hostnames, store, "op.example"); !bytes.Equal(got, first) {
		t.Fatal("a check ran within 30 seconds")
	}
	clk.advance(30 * time.Second)
	if got := servedLeaf(t, certs, hostnames, store, "op.example"); !bytes.Equal(got, second) {
		t.Fatal("the replaced pair was not picked up")
	}
	if !bytes.Contains(logs.Bytes(), []byte("game certificate reloaded")) {
		t.Errorf("the reload is not logged under the game label: %s", logs.String())
	}
}

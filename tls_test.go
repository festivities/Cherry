package main

import (
	"crypto/tls"
	"net"
	"testing"
)

func handshake(t *testing.T, certs []tls.Certificate, sni string) error {
	t.Helper()
	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()
	srv := tls.Server(server, &tls.Config{Certificates: certs, GetConfigForClient: denyBlockedSNI})
	cli := tls.Client(client, &tls.Config{ServerName: sni, InsecureSkipVerify: true})
	done := make(chan error, 1)
	go func() { done <- srv.Handshake() }()
	err := cli.Handshake()
	<-done
	return err
}

func TestBlockedSNI(t *testing.T) {
	certs, err := generateCerts()
	if err != nil {
		t.Fatalf("generateCerts: %v", err)
	}
	for _, sni := range []string{"lan3rd.line.me", "LAN3RD.LINE.ME", "lan3rd.line.me."} {
		if err := handshake(t, certs, sni); err == nil {
			t.Errorf("%s: handshake succeeded, want TLS failure", sni)
		}
	}
	for _, sni := range []string{"fapi.play.naver.jp", "play-static.line-scdn.net"} {
		if err := handshake(t, certs, sni); err != nil {
			t.Errorf("%s: handshake failed: %v", sni, err)
		}
	}
}

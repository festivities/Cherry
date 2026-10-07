package main

import (
	"context"
	"crypto/tls"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"cherry/internal/economy"
	"cherry/internal/httpx"
	"cherry/internal/lpn"
	"cherry/internal/netsvc"
	"cherry/internal/store"
	"cherry/internal/web"
)

const (
	httpsAddr   = ":443"
	sessionAddr = ":10123"
)

func main() {
	logger := log.New(os.Stdout, "", log.LstdFlags|log.Lmicroseconds)
	if err := store.LoadAccounts(); err != nil {
		logger.Fatalf("cherry: load accounts: %v", err)
	}
	if err := store.LoadSocial(); err != nil {
		logger.Fatalf("cherry: load social: %v", err)
	}

	certs, err := netsvc.GenerateCerts()
	if err != nil {
		logger.Fatalf("cherry: generate certs: %v", err)
	}

	srv := &http.Server{
		Addr:              httpsAddr,
		Handler:           httpx.WithLogging(web.NewMux(), logger),
		ReadHeaderTimeout: 10 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       120 * time.Second,
		TLSNextProto:      map[string]func(*http.Server, *tls.Conn, http.Handler){},
		TLSConfig: &tls.Config{
			Certificates:       certs,
			MinVersion:         tls.VersionTLS12,
			NextProtos:         []string{"http/1.1"},
			GetConfigForClient: netsvc.DenyBlockedSNI,
			CipherSuites: []uint16{
				tls.TLS_RSA_WITH_AES_128_GCM_SHA256,
				tls.TLS_RSA_WITH_AES_256_GCM_SHA384,
				tls.TLS_RSA_WITH_AES_128_CBC_SHA,
				tls.TLS_RSA_WITH_AES_256_CBC_SHA,
				tls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256,
				tls.TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384,
				tls.TLS_ECDHE_RSA_WITH_CHACHA20_POLY1305,
				tls.TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256,
				tls.TLS_ECDHE_ECDSA_WITH_AES_256_GCM_SHA384,
				tls.TLS_ECDHE_ECDSA_WITH_CHACHA20_POLY1305,
			},
		},
	}

	httpsLn, err := net.Listen("tcp", httpsAddr)
	if err != nil {
		logger.Fatalf("cherry: listen https: %v", err)
	}
	httpLn, err := net.Listen("tcp", ":80")
	if err != nil {
		logger.Fatalf("cherry: listen http: %v", err)
	}
	gatewayLn, err := lpn.ListenObserver(lpn.GatewayAddr, logger)
	if err != nil {
		logger.Fatalf("cherry: listen observer %s: %v", lpn.GatewayAddr, err)
	}
	sessionLn, err := net.Listen("tcp", sessionAddr)
	if err != nil {
		logger.Fatalf("cherry: listen session %s: %v", sessionAddr, err)
	}
	go lpn.ServeSession(sessionLn, logger)

	dnsConn, err := netsvc.ListenDNS(logger)
	if err != nil {
		logger.Printf("cherry: dns unavailable, continuing without sink: %v", err)
	}

	economy.ServeAdmin(logger)
	logger.Printf("cherry: https listening addr=%s", httpsAddr)

	errc := make(chan error, 2)
	go func() {
		errc <- srv.ServeTLS(httpsLn, "", "")
	}()
	httpSrv := &http.Server{
		Addr:              ":80",
		Handler:           httpx.WithLogging(web.NewMux(), logger),
		ReadHeaderTimeout: 10 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	go func() {
		errc <- httpSrv.Serve(httpLn)
	}()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	select {
	case err := <-errc:
		logger.Fatalf("cherry: https serve: %v", err)
	case <-ctx.Done():
		logger.Printf("cherry: signal received, shutting down")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		logger.Printf("cherry: https shutdown: %v", err)
	}
	_ = gatewayLn.Close()
	_ = sessionLn.Close()
	_ = httpLn.Close()
	if dnsConn != nil {
		_ = dnsConn.Close()
	}
	logger.Printf("cherry: shutdown complete")
}

package main

import (
	"context"
	"crypto/tls"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"cherry/internal/assets"
	"cherry/internal/economy"
	"cherry/internal/httpx"
	"cherry/internal/lpn"
	"cherry/internal/netsvc"
	"cherry/internal/room"
	"cherry/internal/store"
	"cherry/internal/web"
)

const (
	httpsAddr   = ":443"
	sessionAddr = ":10123"
)

func main() {
	logger := log.New(os.Stdout, "", log.LstdFlags|log.Lmicroseconds)
	// CHERRY_FAKE_NOW (RFC 3339) moves the game clock (economy.NowFunc) for season tests. Records written meanwhile
	// carry the moved dates, so it runs only on a copy of the data: LOCALAPPDATA must point away from the real folder.
	if v := os.Getenv("CHERRY_FAKE_NOW"); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			logger.Fatalf("cherry: CHERRY_FAKE_NOW: %v", err)
		}
		if dir, _ := os.UserCacheDir(); strings.EqualFold(filepath.Clean(dir), filepath.Join(os.Getenv("USERPROFILE"), "AppData", "Local")) {
			logger.Fatalf("cherry: CHERRY_FAKE_NOW refused on the real data folder %s; set LOCALAPPDATA to a copy", dir)
		}
		off := time.Until(t)
		economy.NowFunc = func() time.Time { return time.Now().Add(off) }
		logger.Printf("cherry: FAKE CLOCK game time starts at %s (offset %s)", t.Format(time.RFC3339), off)
	}
	if err := store.LoadAccounts(); err != nil {
		logger.Fatalf("cherry: load accounts: %v", err)
	}
	if err := store.LoadSocial(); err != nil {
		logger.Fatalf("cherry: load social: %v", err)
	}
	if err := store.LoadCircles(); err != nil {
		logger.Fatalf("cherry: load circles: %v", err)
	}
	if err := store.LoadTreasure(); err != nil {
		logger.Fatalf("cherry: load treasure: %v", err)
	}
	// The original-asset store comes first: the operator checks below ask it which items have art.
	if err := assets.LoadArchive(); err != nil {
		logger.Fatalf("cherry: asset store: %v", err)
	}
	for _, load := range []func() error{store.LoadOperatorData, room.LoadOperatorData, economy.LoadOperatorData, lpn.LoadOperatorData, web.LoadOperatorData} {
		if err := load(); err != nil {
			logger.Fatalf("cherry: operator data: %v", err)
		}
	}
	// The first-use reads of requests (the shop catalog, the string table) happen now, after every operator file is
	// loaded: no request waits for them, and the catalog is built from the files requests will use.
	if err := economy.WarmArchive(); err != nil {
		logger.Fatalf("cherry: asset store: %v", err)
	}
	web.WarmArchive()
	// A failed roster migration leaves the old contracts in place (it restores them) and must not keep Cherry down.
	if err := economy.MigrateButlerRoster(time.Now()); err != nil {
		logger.Printf("cherry: butler roster migration skipped: %v", err)
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

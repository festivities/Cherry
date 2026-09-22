package main

import (
	"context"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sort"
	"strings"
	"sync/atomic"
	"syscall"
	"time"
)

const (
	httpsAddr    = ":443"
	gatewayAddr  = ":10000"
	sessionAddr  = ":10123"
	observerTTL  = 30 * time.Second
	observerMax  = 512
	observerIdle = time.Second
)

func main() {
	logger := log.New(os.Stdout, "", log.LstdFlags|log.Lmicroseconds)

	certs, err := generateCerts()
	if err != nil {
		logger.Fatalf("cherry: generate certs: %v", err)
	}

	srv := &http.Server{
		Addr:              httpsAddr,
		Handler:           withLogging(newMux(), logger),
		ReadHeaderTimeout: 10 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       120 * time.Second,
		TLSNextProto:      map[string]func(*http.Server, *tls.Conn, http.Handler){},
		TLSConfig: &tls.Config{
			Certificates: certs,
			MinVersion:   tls.VersionTLS12,
			NextProtos:   []string{"http/1.1"},
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
	gatewayLn, err := listenObserver(gatewayAddr, logger)
	if err != nil {
		logger.Fatalf("cherry: listen observer %s: %v", gatewayAddr, err)
	}
	sessionLn, err := net.Listen("tcp", sessionAddr)
	if err != nil {
		logger.Fatalf("cherry: listen session %s: %v", sessionAddr, err)
	}
	go serveSession(sessionLn, logger)

	logger.Printf("cherry: https listening addr=%s", httpsAddr)

	errc := make(chan error, 2)
	go func() {
		errc <- srv.ServeTLS(httpsLn, "", "")
	}()
	httpSrv := &http.Server{
		Addr:              ":80",
		Handler:           withLogging(newMux(), logger),
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
	logger.Printf("cherry: shutdown complete")
}

func listenObserver(addr string, logger *log.Logger) (net.Listener, error) {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}
	logger.Printf("cherry: observer listening addr=%s", addr)
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				if errors.Is(err, net.ErrClosed) {
					return
				}
				logger.Printf("OBS %s accept: %v", addr, err)
				continue
			}
			go observeConn(conn, addr, logger)
		}
	}()
	return ln, nil
}

func observeConn(conn net.Conn, addr string, logger *log.Logger) {
	remote := conn.RemoteAddr().String()
	logger.Printf("OBS %s connect remote=%s", addr, remote)
	defer conn.Close()

	hard := time.Now().Add(observerTTL)
	buf := make([]byte, 0, observerMax)
	chunk := make([]byte, observerMax)
	for len(buf) < observerMax {
		deadline := hard
		if len(buf) > 0 {
			if d := time.Now().Add(observerIdle); d.Before(deadline) {
				deadline = d
			}
		}
		_ = conn.SetReadDeadline(deadline)
		n, err := conn.Read(chunk)
		buf = append(buf, chunk[:n]...)
		if err != nil {
			if errors.Is(err, io.EOF) {
				logger.Printf("OBS %s data remote=%s bytes=%d hex=%s ascii=%q", addr, remote, len(buf), hex.EncodeToString(buf), printable(buf))
			} else {
				logger.Printf("OBS %s data remote=%s bytes=%d hex=%s ascii=%q err=%v", addr, remote, len(buf), hex.EncodeToString(buf), printable(buf), err)
			}
			logger.Printf("OBS %s close remote=%s", addr, remote)
			return
		}
	}
	logger.Printf("OBS %s data remote=%s bytes=%d hex=%s ascii=%q", addr, remote, len(buf), hex.EncodeToString(buf), printable(buf))
	logger.Printf("OBS %s close remote=%s", addr, remote)
}

func printable(b []byte) string {
	out := make([]byte, len(b))
	for i, c := range b {
		if c >= 0x20 && c < 0x7f {
			out[i] = c
		} else {
			out[i] = '.'
		}
	}
	return string(out)
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (rec *statusRecorder) WriteHeader(code int) {
	if rec.status == 0 {
		rec.status = code
	}
	rec.ResponseWriter.WriteHeader(code)
}

func (rec *statusRecorder) Write(b []byte) (int, error) {
	if rec.status == 0 {
		rec.status = http.StatusOK
	}
	return rec.ResponseWriter.Write(b)
}

func withLogging(next http.Handler, logger *log.Logger) http.Handler {
	var seq atomic.Uint64
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w}
		next.ServeHTTP(rec, r)
		status := rec.status
		if status == 0 {
			status = http.StatusOK
		}
		sni := ""
		if r.TLS != nil {
			sni = r.TLS.ServerName
		}
		logger.Printf("REQ #%d remote=%s sni=%q host=%q method=%s url=%q status=%d duration=%s headers=%s",
			seq.Add(1), r.RemoteAddr, sni, r.Host, r.Method, r.URL.RequestURI(), status, time.Since(start), formatHeaders(r.Header))
	})
}

func formatHeaders(h http.Header) string {
	names := make([]string, 0, len(h))
	for name := range h {
		names = append(names, name)
	}
	sort.Strings(names)
	var b strings.Builder
	for _, name := range names {
		for _, value := range h[name] {
			fmt.Fprintf(&b, "%s=%q ", name, value)
		}
	}
	return strings.TrimSpace(b.String())
}

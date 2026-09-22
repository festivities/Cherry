package main

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"io"
	"log"
	"net"
	"testing"
	"time"
)

const (
	configureFrameHex = "000000344c4f10100000002400000000000000007b226e6f6f705365636f6e6473223a32302c2270696e675365636f6e6473223a3132307d"
	sendInitPayload   = `{"command":"init","seq":1}`
)

func discardLogger() *log.Logger {
	return log.New(io.Discard, "", 0)
}

func decodeHex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatalf("decode hex %q: %v", s, err)
	}
	return b
}

func TestBuildLOFrameExactConfigureBytes(t *testing.T) {
	want := decodeHex(t, configureFrameHex)
	got := buildLOFrame(loOpConfigure, 0, configurePayload)
	if !bytes.Equal(got, want) {
		t.Fatalf("configure frame hex = %s, want %s", hex.EncodeToString(got), configureFrameHex)
	}
	if len(got) != 4+16+len(configurePayload) {
		t.Fatalf("frame len = %d, want %d", len(got), 4+16+len(configurePayload))
	}
}

func TestLOFrameRoundTrip(t *testing.T) {
	payloads := [][]byte{nil, {}, []byte(`{"a":{"b":[1,2,3]}}`)}
	for _, op := range []byte{0x00, 0x01, 0x10, 0xFF} {
		for _, txn := range []uint64{0, 1, 0x0102030405060708, ^uint64(0)} {
			for _, payload := range payloads {
				frame := buildLOFrame(op, txn, payload)
				if got := binary.BigEndian.Uint32(frame[0:4]); got != uint32(16+len(payload)) {
					t.Fatalf("op=%#02x txn=%d: frame len = %d, want %d", op, txn, got, 16+len(payload))
				}
				if !bytes.Equal(frame[4:6], []byte("LO")) || frame[6] != loMagic || frame[7] != op {
					t.Fatalf("op=%#02x txn=%d: bad header %s", op, txn, hex.EncodeToString(frame))
				}
				if got := binary.BigEndian.Uint32(frame[8:12]); got != uint32(len(payload)) {
					t.Fatalf("op=%#02x txn=%d: payloadLen = %d, want %d", op, txn, got, len(payload))
				}
				if got := binary.BigEndian.Uint64(frame[12:20]); got != txn {
					t.Fatalf("op=%#02x: txn = %d, want %d", op, got, txn)
				}
				gotOp, gotTxn, gotPayload, ok := parseLOFrame(frame[4:])
				if !ok {
					t.Fatalf("op=%#02x txn=%d: parse failed", op, txn)
				}
				if gotOp != op || gotTxn != txn || !bytes.Equal(gotPayload, payload) {
					t.Fatalf("parse = (%#02x, %d, %q), want (%#02x, %d, %q)", gotOp, gotTxn, gotPayload, op, txn, payload)
				}
			}
		}
	}
}

func TestParseLOFrameRejectsMalformed(t *testing.T) {
	valid := buildLOFrame(loOpSendInit, 7, []byte(`{"x":1}`))[4:]
	mutate := func(f func(b []byte)) []byte {
		b := append([]byte(nil), valid...)
		f(b)
		return b
	}
	cases := map[string][]byte{
		"nil":          nil,
		"short":        []byte("LO"),
		"bad tag":      mutate(func(b []byte) { b[0] = 'X' }),
		"bad magic":    mutate(func(b []byte) { b[2] = 0x11 }),
		"len mismatch": mutate(func(b []byte) { binary.BigEndian.PutUint32(b[4:8], 12345) }),
	}
	for name, b := range cases {
		if _, _, _, ok := parseLOFrame(b); ok {
			t.Errorf("%s: parse ok, want failure", name)
		}
	}
}

func startSessionServer(t *testing.T) (net.Conn, func()) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go serveSession(ln, discardLogger())
	conn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		_ = ln.Close()
		t.Fatalf("dial: %v", err)
	}
	return conn, func() {
		_ = conn.Close()
		_ = ln.Close()
	}
}

func readFullDeadline(t *testing.T, conn net.Conn, n int) []byte {
	t.Helper()
	if err := conn.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatalf("set deadline: %v", err)
	}
	buf := make([]byte, n)
	if _, err := io.ReadFull(conn, buf); err != nil {
		t.Fatalf("read %d bytes: %v", n, err)
	}
	return buf
}

func readSessionFrame(t *testing.T, conn net.Conn) []byte {
	t.Helper()
	prefix := readFullDeadline(t, conn, 4)
	length := binary.BigEndian.Uint32(prefix)
	if length > maxSessionFrameLen {
		t.Fatalf("frame len = %d", length)
	}
	return append(prefix, readFullDeadline(t, conn, int(length))...)
}

func writeSessionFrame(t *testing.T, conn net.Conn, payload []byte) {
	t.Helper()
	frame := binary.BigEndian.AppendUint32(nil, uint32(len(payload)))
	frame = append(frame, payload...)
	if err := conn.SetWriteDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatalf("set write deadline: %v", err)
	}
	if _, err := conn.Write(frame); err != nil {
		t.Fatalf("write frame: %v", err)
	}
}

func assertNoReply(t *testing.T, conn net.Conn) {
	t.Helper()
	if err := conn.SetReadDeadline(time.Now().Add(200 * time.Millisecond)); err != nil {
		t.Fatalf("set deadline: %v", err)
	}
	buf := make([]byte, 1)
	n, err := conn.Read(buf)
	if err == nil {
		t.Fatalf("unexpected reply byte %#02x", buf[0])
	}
	if ne, ok := err.(net.Error); !ok || !ne.Timeout() {
		t.Fatalf("read = %d, %v; want timeout", n, err)
	}
}

func TestSessionBannerHelloAndSendInit(t *testing.T) {
	conn, cleanup := startSessionServer(t)
	defer cleanup()

	banner := readFullDeadline(t, conn, len(sessionBanner))
	if !bytes.Equal(banner, sessionBanner) {
		t.Fatalf("banner = %s, want %s", hex.EncodeToString(banner), hex.EncodeToString(sessionBanner))
	}

	if _, err := conn.Write(sessionHello); err != nil {
		t.Fatalf("write hello: %v", err)
	}

	writeSessionFrame(t, conn, buildLOFrame(loOpSendInit, 0x42, []byte(sendInitPayload))[4:])
	reply := readSessionFrame(t, conn)
	want := buildLOFrame(loOpConfigure, 0, configurePayload)
	if !bytes.Equal(reply, want) {
		t.Fatalf("reply = %s, want %s", hex.EncodeToString(reply), hex.EncodeToString(want))
	}
	op, txn, payload, ok := parseLOFrame(reply[4:])
	if !ok || op != loOpConfigure || txn != 0 || string(payload) != string(configurePayload) {
		t.Fatalf("parsed reply = (%#02x, %d, %q, %v)", op, txn, payload, ok)
	}
}

func TestSessionOtherOpsIgnored(t *testing.T) {
	conn, cleanup := startSessionServer(t)
	defer cleanup()
	readFullDeadline(t, conn, len(sessionBanner))
	if _, err := conn.Write(sessionHello); err != nil {
		t.Fatalf("write hello: %v", err)
	}

	writeSessionFrame(t, conn, []byte("MQ"))
	assertNoReply(t, conn)
	writeSessionFrame(t, conn, []byte("MS"))
	assertNoReply(t, conn)
	writeSessionFrame(t, conn, buildLOFrame(0x05, 9, []byte(`{"op":"other"}`))[4:])
	assertNoReply(t, conn)

	writeSessionFrame(t, conn, buildLOFrame(loOpSendInit, 1, []byte(`{}`))[4:])
	reply := readSessionFrame(t, conn)
	if !bytes.Equal(reply, buildLOFrame(loOpConfigure, 0, configurePayload)) {
		t.Fatalf("reply = %s", hex.EncodeToString(reply))
	}
}

func TestSessionBadHelloKeepsReading(t *testing.T) {
	conn, cleanup := startSessionServer(t)
	defer cleanup()
	readFullDeadline(t, conn, len(sessionBanner))
	bad := append([]byte(nil), sessionHello...)
	bad[len(bad)-1]++
	if _, err := conn.Write(bad); err != nil {
		t.Fatalf("write hello: %v", err)
	}

	writeSessionFrame(t, conn, buildLOFrame(loOpSendInit, 0, []byte(`{}`))[4:])
	reply := readSessionFrame(t, conn)
	if !bytes.Equal(reply, buildLOFrame(loOpConfigure, 0, configurePayload)) {
		t.Fatalf("reply = %s", hex.EncodeToString(reply))
	}
}

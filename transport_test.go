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

type deadlineRecordingConn struct {
	net.Conn
	cleared chan struct{}
}

func (c deadlineRecordingConn) SetDeadline(deadline time.Time) error {
	if deadline.IsZero() {
		select {
		case c.cleared <- struct{}{}:
		default:
		}
	}
	return c.Conn.SetDeadline(deadline)
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
	if reply := readSessionFrame(t, conn); !bytes.Equal(reply, []byte{0, 0, 0, 2, 'M', 'Q'}) {
		t.Fatalf("MQ echo = %s", hex.EncodeToString(reply))
	}
	writeSessionFrame(t, conn, []byte("MS"))
	if reply := readSessionFrame(t, conn); !bytes.Equal(reply, []byte{0, 0, 0, 2, 'M', 'S'}) {
		t.Fatalf("MS echo = %s", hex.EncodeToString(reply))
	}
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

func TestGatewayObserverBannerHello(t *testing.T) {
	server, client := net.Pipe()
	cleared := make(chan struct{}, 1)
	var output bytes.Buffer
	done := make(chan struct{})
	go func() {
		observeConn(deadlineRecordingConn{server, cleared}, gatewayAddr, log.New(&output, "", 0))
		close(done)
	}()

	if banner := readFullDeadline(t, client, len(sessionBanner)); !bytes.Equal(banner, sessionBanner) {
		t.Fatalf("gateway banner = %x, want %x", banner, sessionBanner)
	}
	open := []byte{0, 0, 0, 10, 'G', 0x19, 0, 1, 0, 1, 0, 0x65, 0, 0}
	if _, err := client.Write(open); err != nil {
		t.Fatalf("gateway open write: %v", err)
	}
	ack := readFullDeadline(t, client, len(open))
	wantAck := append([]byte(nil), open...)
	wantAck[5] = 0x1a
	if !bytes.Equal(ack, wantAck) {
		t.Fatalf("gateway ack = %x, want %x", ack, wantAck)
	}
	gardenOpen := append([]byte(nil), open...)
	gardenOpen[5], gardenOpen[11] = 0x49, 0x71
	if _, err := client.Write(gardenOpen); err != nil {
		t.Fatalf("garden open write: %v", err)
	}
	gardenControl := readFullDeadline(t, client, 22)
	wantControl := append([]byte{0, 0, 0, 18}, gardenOpen[4:]...)
	wantControl = append(wantControl, make([]byte, 8)...)
	if !bytes.Equal(gardenControl, wantControl) {
		t.Fatalf("garden control = %x, want %x", gardenControl, wantControl)
	}
	if _, err := client.Write(gardenOpen); err != nil {
		t.Fatalf("garden repeat write: %v", err)
	}
	assertNoReply(t, client)
	if _, err := client.Write(sessionHello); err != nil {
		t.Fatalf("gateway hello write: %v", err)
	}
	questReq := []byte{0, 0, 0, 92, 'D', 0, 0, 1, 0, 1, 0, 0x6f, 0, 0, 5, 0, 0, 0, 0x42, 0, 0, 1}
	questReq = append(questReq, 0x0a, 2, 0x08, 1, 0x10, 0x2a, 0x1a, 66)
	questReq = append(questReq, bytes.Repeat([]byte{0x77}, 66)...)
	if err := client.SetWriteDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Write(questReq); err != nil {
		t.Fatalf("quest request write: %v", err)
	}
	questReply := readFullDeadline(t, client, 24)
	wantQuestReply := []byte{0, 0, 0, 20, 'D', 0, 0, 1, 0, 1, 0, 0x6f, 0, 0, 5, 0, 0, 0, 0x42, 0, 0, 1, 0x08, 0x2a}
	if !bytes.Equal(questReply, wantQuestReply) {
		t.Fatalf("quest reply = %x, want %x", questReply, wantQuestReply)
	}
	sessionOnly := append([]byte(nil), questReq[:22]...)
	binary.BigEndian.PutUint32(sessionOnly[:4], 25)
	binary.BigEndian.PutUint32(sessionOnly[15:19], 67)
	sessionOnly = append(sessionOnly, 0x0a, 5, 0x0a, 1, 'K', 0x10, 1)
	if _, err := client.Write(sessionOnly); err != nil {
		t.Fatalf("session-only client event write: %v", err)
	}
	sessionOnlyReply := readFullDeadline(t, client, 24)
	wantSessionOnlyReply := append([]byte(nil), wantQuestReply...)
	binary.BigEndian.PutUint32(wantSessionOnlyReply[15:19], 67)
	wantSessionOnlyReply[23] = 0
	if !bytes.Equal(sessionOnlyReply, wantSessionOnlyReply) {
		t.Fatalf("session-only client event reply = %x, want %x", sessionOnlyReply, wantSessionOnlyReply)
	}
	firstPacket := []byte{0, 0, 0, 15, 'G', 0x18, 0, 1, 0, 1, 0, 0x65, 0, 0, 0, 1, 'K', '3', 'Y'}
	if err := client.SetWriteDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Write(firstPacket); err != nil {
		t.Fatalf("gateway first packet write: %v", err)
	}
	gardenProto := []byte{0x0a, 1, 'K', 0x10, 1, 0x1a, 3, '1', '.', '0'}
	gardenReq := binary.BigEndian.AppendUint32(nil, uint32(12+len(gardenProto)))
	gardenReq = append(gardenReq, 'G', 0x48)
	gardenReq = append(gardenReq, gardenOpen[6:14]...)
	gardenReq = append(gardenReq, 0, 0)
	gardenReq = append(gardenReq, gardenProto...)
	if _, err := client.Write(gardenReq); err != nil {
		t.Fatalf("garden login request write: %v", err)
	}
	gardenReply := readFullDeadline(t, client, 20)
	wantGardenReply := binary.BigEndian.AppendUint32(nil, 16)
	wantGardenReply = append(wantGardenReply, 'G', 0x48)
	wantGardenReply = append(wantGardenReply, gardenOpen[6:14]...)
	wantGardenReply = append(wantGardenReply, 0, 0, 0x08, 0x81, 0x84, 0x0c)
	if !bytes.Equal(gardenReply, wantGardenReply) {
		t.Fatalf("garden login reply = %x, want %x", gardenReply, wantGardenReply)
	}
	relayReq := binary.BigEndian.AppendUint32(nil, 12)
	relayReq = append(relayReq, 'G', 0x48)
	relayReq = append(relayReq, gardenOpen[6:14]...)
	relayReq = append(relayReq, 0, 3)
	if _, err := client.Write(relayReq); err != nil {
		t.Fatalf("premature garden relay request write: %v", err)
	}
	assertNoReply(t, client)
	questListReq := append([]byte(nil), relayReq...)
	binary.BigEndian.PutUint16(questListReq[14:16], 15)
	if _, err := client.Write(questListReq); err != nil {
		t.Fatalf("premature garden quest-list request write: %v", err)
	}
	assertNoReply(t, client)
	roomProto := []byte{0x08, 42, 0x10, 0, 0x20, 0, 0x28, 1}
	roomReq := binary.BigEndian.AppendUint32(nil, uint32(12+len(roomProto)))
	roomReq = append(roomReq, 'G', 0x48)
	roomReq = append(roomReq, gardenOpen[6:14]...)
	roomReq = append(roomReq, 0, 1)
	roomReq = append(roomReq, roomProto...)
	if _, err := client.Write(roomReq); err != nil {
		t.Fatalf("garden room request write: %v", err)
	}
	roomReply := readFullDeadline(t, client, 34)
	wantRoomReply := binary.BigEndian.AppendUint32(nil, 30)
	wantRoomReply = append(wantRoomReply, 'G', 0x48)
	wantRoomReply = append(wantRoomReply, gardenOpen[6:14]...)
	wantRoomReply = append(wantRoomReply, 0, 1, 0x08, 0x81, 0x86, 0x0c, 0x22, 0x0a)
	wantRoomReply = append(wantRoomReply, gardenMapStem...)
	wantRoomReply = append(wantRoomReply, 0x28, 42)
	if !bytes.Equal(roomReply, wantRoomReply) {
		t.Fatalf("garden room reply = %x, want %x", roomReply, wantRoomReply)
	}
	otherTunnelRelay := append([]byte(nil), relayReq...)
	otherTunnelRelay[6]++
	if _, err := client.Write(otherTunnelRelay); err != nil {
		t.Fatalf("other tunnel garden relay request write: %v", err)
	}
	assertNoReply(t, client)
	invalidRelay := append(append([]byte(nil), relayReq...), 0x08, 1)
	binary.BigEndian.PutUint32(invalidRelay, 14)
	if _, err := client.Write(invalidRelay); err != nil {
		t.Fatalf("garden relay unexpected payload write: %v", err)
	}
	assertNoReply(t, client)
	if _, err := client.Write(relayReq); err != nil {
		t.Fatalf("garden relay request write: %v", err)
	}
	relayReply := readFullDeadline(t, client, 18)
	wantRelayReply := binary.BigEndian.AppendUint32(nil, 14)
	wantRelayReply = append(wantRelayReply, 'G', 0x48)
	wantRelayReply = append(wantRelayReply, gardenOpen[6:14]...)
	wantRelayReply = append(wantRelayReply, 0, 3, 0x08, 0)
	if !bytes.Equal(relayReply, wantRelayReply) {
		t.Fatalf("garden relay reply = %x, want %x", relayReply, wantRelayReply)
	}
	otherTunnelQuest := append([]byte(nil), questListReq...)
	otherTunnelQuest[6]++
	if _, err := client.Write(otherTunnelQuest); err != nil {
		t.Fatalf("wrong tunnel quest-list request write: %v", err)
	}
	assertNoReply(t, client)
	if _, err := client.Write(questListReq); err != nil {
		t.Fatalf("garden quest-list request write: %v", err)
	}
	questListReply := readFullDeadline(t, client, 18)
	wantQuestListReply := binary.BigEndian.AppendUint32(nil, 14)
	wantQuestListReply = append(wantQuestListReply, 'G', 0x48)
	wantQuestListReply = append(wantQuestListReply, gardenOpen[6:14]...)
	wantQuestListReply = append(wantQuestListReply, 0, 21, 0x08, 0)
	if !bytes.Equal(questListReply, wantQuestListReply) {
		t.Fatalf("garden quest-list reply = %x, want %x", questListReply, wantQuestListReply)
	}
	select {
	case <-cleared:
	case <-time.After(2 * time.Second):
		t.Fatal("entered Garden retained five-minute gateway deadline")
	}
	invalidRoom := append([]byte(nil), roomReq...)
	invalidRoom[len(invalidRoom)-len(roomProto)+1] = 0 // zero owner aid
	if _, err := client.Write(invalidRoom); err != nil {
		t.Fatalf("invalid garden room request write: %v", err)
	}
	assertNoReply(t, client)
	_ = client.Close()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("gateway observer did not return")
	}
	if !bytes.Contains(output.Bytes(), []byte("OBS :10000 open-ack remote=pipe agent=4")) ||
		!bytes.Contains(output.Bytes(), []byte("OBS :10000 open-control remote=pipe agent=10")) ||
		!bytes.Contains(output.Bytes(), []byte("OBS :10000 open-retry remote=pipe agent=10")) ||
		!bytes.Contains(output.Bytes(), []byte("OBS :10000 hello ")) ||
		!bytes.Contains(output.Bytes(), []byte("packetId=66")) ||
		!bytes.Contains(output.Bytes(), []byte("OBS :10000 garden-login remote=pipe result=197121")) ||
		!bytes.Contains(output.Bytes(), []byte("OBS :10000 garden-room-enter remote=pipe result=197377 map="+gardenMapStem)) ||
		!bytes.Contains(output.Bytes(), []byte("OBS :10000 garden-relay-start remote=pipe result=0")) ||
		!bytes.Contains(output.Bytes(), []byte("OBS :10000 garden-quest-list remote=pipe errorCode=0")) ||
		!bytes.Contains(output.Bytes(), []byte("garden-room-enter remote=pipe invalid owner aid")) ||
		!bytes.Contains(output.Bytes(), []byte("agent=4 msgid=1 bytes=15")) ||
		bytes.Contains(output.Bytes(), []byte("K3Y")) {
		t.Fatalf("gateway observation missing framed exchanges or leaked payload: %s", output.String())
	}
}

func TestGardenLoginValid(t *testing.T) {
	for _, c := range []struct {
		name string
		data []byte
		want bool
	}{
		{"session and aid", []byte{0x0a, 1, 'K', 0x10, 1}, true},
		{"unknown field skipped", []byte{0x18, 3, 0x0a, 1, 'K', 0x10, 1}, true},
		{"empty session", []byte{0x0a, 0, 0x10, 1}, false},
		{"missing aid", []byte{0x0a, 1, 'K'}, false},
		{"zero aid", []byte{0x0a, 1, 'K', 0x10, 0}, false},
		{"truncated varint", []byte{0x0a, 1, 'K', 0x10, 0x80}, false},
		{"truncated field", []byte{0x0a, 2, 'K'}, false},
	} {
		if got := gardenLoginValid(c.data); got != c.want {
			t.Errorf("%s: valid = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestClientEventPacketNo(t *testing.T) {
	for _, c := range []struct {
		name string
		data []byte
		want uint64
		ok   bool
	}{
		{"nested session skipped", []byte{0x0a, 2, 0x10, 99, 0x10, 42}, 42, true},
		{"session only", []byte{0x0a, 5, 0x0a, 1, 'K', 0x10, 1}, 0, true},
		{"empty session", []byte{0x0a, 0}, 0, false},
		{"session with other fields", []byte{0x0a, 5, 0x0a, 1, 'K', 0x10, 1, 0x18, 1}, 0, false},
		{"missing", []byte{0x0a, 1, 0}, 0, false},
		{"truncated varint", []byte{0x10, 0x80}, 0, false},
		{"truncated field", []byte{0x1a, 4, 1}, 0, false},
		{"unsupported wire type", []byte{0x13, 0x10, 42}, 0, false},
	} {
		got, ok := clientEventPacketNo(c.data)
		if got != c.want || ok != c.ok {
			t.Errorf("%s: packetNo = %d, %v; want %d, %v", c.name, got, ok, c.want, c.ok)
		}
	}
}

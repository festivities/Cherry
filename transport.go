package main

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"io"
	"log"
	"net"
)

const (
	loMagic            = 0x10
	loOpSendInit       = 0x00
	loOpConfigure      = 0x10
	maxSessionFrameLen = 64 << 20
)

var (
	sessionBanner = []byte{0x00, 0x00, 0x00, 0x08, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00}
	sessionHello  = []byte{0x00, 0x00, 0x00, 0x04, 0x00, 0x00, 0x01, 0x0A, 0x35, 0xED, 0x6A}

	configurePayload = []byte(`{"noopSeconds":20,"pingSeconds":120}`)
)

func serveSession(ln net.Listener, logger *log.Logger) {
	logger.Printf("cherry: session listening addr=%s", ln.Addr())
	for {
		conn, err := ln.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			logger.Printf("SESSION accept: %v", err)
			continue
		}
		go serveSessionConn(conn, logger)
	}
}

func serveSessionConn(conn net.Conn, logger *log.Logger) {
	remote := conn.RemoteAddr().String()
	logger.Printf("SESSION connect remote=%s", remote)
	defer func() {
		_ = conn.Close()
		logger.Printf("SESSION disconnect remote=%s", remote)
	}()

	if _, err := conn.Write(sessionBanner); err != nil {
		logger.Printf("SESSION banner remote=%s err=%v", remote, err)
		return
	}
	logger.Printf("SESSION banner remote=%s hex=%s", remote, hex.EncodeToString(sessionBanner))

	hello := make([]byte, len(sessionHello))
	if _, err := io.ReadFull(conn, hello); err != nil {
		logger.Printf("SESSION hello remote=%s err=%v", remote, err)
		return
	}
	if bytes.Equal(hello, sessionHello) {
		logger.Printf("SESSION hello remote=%s hex=%s", remote, hex.EncodeToString(hello))
	} else {
		logger.Printf("SESSION hello remote=%s protocol error hex=%s want=%s", remote, hex.EncodeToString(hello), hex.EncodeToString(sessionHello))
	}

	for {
		var prefix [4]byte
		if _, err := io.ReadFull(conn, prefix[:]); err != nil {
			if !errors.Is(err, io.EOF) {
				logger.Printf("SESSION read remote=%s err=%v", remote, err)
			}
			return
		}
		length := binary.BigEndian.Uint32(prefix[:])
		if length > maxSessionFrameLen {
			logger.Printf("SESSION frame remote=%s protocol error len=%d", remote, length)
			return
		}
		payload := make([]byte, length)
		if _, err := io.ReadFull(conn, payload); err != nil {
			logger.Printf("SESSION frame remote=%s len=%d err=%v", remote, length, err)
			return
		}
		if !handleSessionFrame(conn, remote, payload, logger) {
			return
		}
	}
}

func handleSessionFrame(conn net.Conn, remote string, payload []byte, logger *log.Logger) bool {
	if len(payload) == 2 && payload[0] == 'M' && (payload[1] == 'Q' || payload[1] == 'S') {
		logger.Printf("SESSION ping remote=%s tag=%s", remote, string(payload))
		return true
	}
	if len(payload) >= 2 && payload[0] == 'L' && payload[1] == 'O' {
		op, txn, body, ok := parseLOFrame(payload)
		if !ok {
			logger.Printf("SESSION lo remote=%s invalid len=%d hex=%s", remote, len(payload), hex.EncodeToString(payload))
			return true
		}
		logger.Printf("SESSION lo remote=%s op=0x%02x txn=%d payload=%s", remote, op, txn, body)
		if op != loOpSendInit {
			return true
		}
		frame := buildLOFrame(loOpConfigure, 0, configurePayload)
		if _, err := conn.Write(frame); err != nil {
			logger.Printf("SESSION reply remote=%s op=0x%02x txn=0 err=%v", remote, loOpConfigure, err)
			return false
		}
		logger.Printf("SESSION reply remote=%s op=0x%02x txn=0 bytes=%d", remote, loOpConfigure, len(frame))
		return true
	}
	logger.Printf("SESSION frame remote=%s len=%d hex=%s", remote, len(payload), hex.EncodeToString(payload))
	return true
}

func buildLOFrame(op byte, txn uint64, payload []byte) []byte {
	frame := make([]byte, 0, 16+len(payload))
	frame = binary.BigEndian.AppendUint32(frame, uint32(16+len(payload)))
	frame = append(frame, 'L', 'O', loMagic, op)
	frame = binary.BigEndian.AppendUint32(frame, uint32(len(payload)))
	frame = binary.BigEndian.AppendUint64(frame, txn)
	frame = append(frame, payload...)
	return frame
}

func parseLOFrame(b []byte) (op byte, txn uint64, payload []byte, ok bool) {
	if len(b) < 16 || b[0] != 'L' || b[1] != 'O' || b[2] != loMagic {
		return 0, 0, nil, false
	}
	if int(binary.BigEndian.Uint32(b[4:8])) != len(b)-16 {
		return 0, 0, nil, false
	}
	return b[3], binary.BigEndian.Uint64(b[8:16]), b[16:], true
}

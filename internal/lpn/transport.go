// Package lpn implements the game-gateway side of the client protocols: the LO
// session channel, the :10000 gateway frame observer, Garden presence, Square, Room
// Party (LPNP agent 4) and the arts_session sckey keyring.
package lpn

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net"
	"strconv"
	"sync"

	"cherry/internal/store"
)

const (
	loMagic            = 0x10
	loOpSendInit       = 0x00
	loOpConfigure      = 0x10
	loOpPush           = 0x1B // a server push (type 27): the client's processPacket case 11 (boss-mp-addendum 1d)
	maxSessionFrameLen = 64 << 20
)

var (
	sessionBanner = []byte{0x00, 0x00, 0x00, 0x08, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00}
	sessionHello  = []byte{0x00, 0x00, 0x00, 0x04, 0x00, 0x00, 0x01, 0x0A, 0x35, 0xED, 0x6A}

	configurePayload = []byte(`{"noopSeconds":20,"pingSeconds":120}`)

	// sessionConns is the live :10123 session of each account. The client's sendInit names its account (sessionKey,
	// the key the login handed out), so the session is bound there; a connection that closes is unbound. Guarded by
	// sessionConnsMu.
	sessionConnsMu sync.Mutex
	sessionConns   = map[uint64]*lockedConn{}
)

func ServeSession(ln net.Listener, logger *log.Logger) {
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
	c := &lockedConn{Conn: conn} // pushes from other connections' goroutines write through the same lock
	defer func() {
		themeChatUnbound(unbindSession(c), logger)
		_ = conn.Close()
		logger.Printf("SESSION disconnect remote=%s", remote)
	}()

	if _, err := c.Write(sessionBanner); err != nil {
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
		if !handleSessionFrame(c, remote, payload, logger) {
			return
		}
	}
}

func handleSessionFrame(conn *lockedConn, remote string, payload []byte, logger *log.Logger) bool {
	if len(payload) == 2 && payload[0] == 'M' && (payload[1] == 'Q' || payload[1] == 'S') {
		logger.Printf("SESSION ping remote=%s tag=%s", remote, string(payload))
		frame := binary.BigEndian.AppendUint32(nil, 2)
		frame = append(frame, payload...)
		if _, err := conn.Write(frame); err != nil {
			logger.Printf("SESSION ping remote=%s err=%v", remote, err)
			return false
		}
		return true
	}
	if len(payload) >= 2 && payload[0] == 'L' && payload[1] == 'O' {
		op, txn, body, ok := parseLOFrame(payload)
		if !ok {
			logger.Printf("SESSION lo remote=%s invalid len=%d hex=%s", remote, len(payload), hex.EncodeToString(payload))
			return true
		}
		logger.Printf("SESSION lo remote=%s op=0x%02x txn=%d len=%d", remote, op, txn, len(body)) // the sendInit body holds the sessionKey
		if op == loOpThemeChatReq {
			return handleThemeChat(conn, remote, txn, body, logger)
		}
		if op != loOpSendInit {
			return true
		}
		if aid := sessionAccount(body); aid != 0 { // bound before the reply, so a push right after the reply finds it
			bindSession(aid, conn)
			logger.Printf("SESSION bind remote=%s aid=%d", remote, aid)
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

// sessionAccount is the aid of the account whose SessionKey is the sendInit's sessionKey (0 when no account has it).
func sessionAccount(body []byte) uint64 {
	var req struct {
		SessionKey string `json:"sessionKey"`
	}
	if err := json.Unmarshal(bytes.TrimRight(body, "\x00"), &req); err != nil || req.SessionKey == "" {
		return 0
	}
	store.AccountsMu.Lock()
	defer store.AccountsMu.Unlock()
	for _, acc := range store.Accounts {
		if acc.SessionKey == req.SessionKey {
			aid, err := strconv.ParseUint(acc.Aid, 10, 64)
			if err != nil {
				return 0
			}
			return aid
		}
	}
	return 0
}

// bindSession makes c the live session of aid (a newer session of the same account replaces it).
func bindSession(aid uint64, c *lockedConn) {
	sessionConnsMu.Lock()
	defer sessionConnsMu.Unlock()
	sessionConns[aid] = c
}

// SessionOnline reports whether aid has a live :10123 session (friends' online markers, web session/status).
func SessionOnline(aid uint64) bool {
	sessionConnsMu.Lock()
	defer sessionConnsMu.Unlock()
	return sessionConns[aid] != nil
}

// unbindSession forgets the accounts whose live session was c and returns them.
func unbindSession(c *lockedConn) []uint64 {
	sessionConnsMu.Lock()
	defer sessionConnsMu.Unlock()
	var aids []uint64
	for aid, o := range sessionConns {
		if o == c {
			delete(sessionConns, aid)
			aids = append(aids, aid)
		}
	}
	return aids
}

// sessionAidOf is the account whose live session is c (false when c never bound one).
func sessionAidOf(c *lockedConn) (uint64, bool) {
	sessionConnsMu.Lock()
	defer sessionConnsMu.Unlock()
	for aid, o := range sessionConns {
		if o == c {
			return aid, true
		}
	}
	return 0, false
}

// sessionPush writes one LO frame (op, txn 0, payload) to aid's live :10123 session. sent is false when the account
// has no session; err is a failed write.
func sessionPush(aid uint64, op byte, payload []byte) (sent bool, err error) {
	sessionConnsMu.Lock()
	c := sessionConns[aid]
	sessionConnsMu.Unlock()
	if c == nil {
		return false, nil
	}
	if _, err := c.Write(buildLOFrame(op, 0, payload)); err != nil {
		return false, err
	}
	return true, nil
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

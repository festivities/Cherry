package lpn

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"io"
	"log"
	"net"
	"time"
)

const (
	GatewayAddr        = ":10000"
	observerTTL        = 30 * time.Second
	gatewayTTL         = 5 * time.Minute
	observerMax        = 512
	observerIdle       = time.Second
	maxGatewayFrameLen = 1 << 20
	gardenMapStem      = "2401290846"
)

func ListenObserver(addr string, logger *log.Logger) (net.Listener, error) {
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
	if addr == GatewayAddr {
		if err := conn.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
			logger.Printf("OBS %s deadline remote=%s err=%v", addr, remote, err)
			return
		}
		if _, err := conn.Write(sessionBanner); err != nil {
			logger.Printf("OBS %s banner remote=%s err=%v", addr, remote, err)
			return
		}
		observeGatewayFrames(&lockedConn{Conn: conn}, addr, remote, logger)
		return
	}

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

func observeGatewayFrames(conn *lockedConn, addr, remote string, logger *log.Logger) {
	sq := &squareSession{room: defaultSquare, conn: conn, logger: logger}
	defer sq.close()
	rp := &roomPartySession{conn: conn, logger: logger}
	defer rp.close()
	hard := time.Now().Add(gatewayTTL)
	gardenControlSent := false
	gardenRoomEntered := false
	gardenRelayStarted := false
	var gardenLoginAid, gardenOwnerAid uint64
	var gardenRoomTunnel [8]byte
	defer func() { gardenLeave(gardenLoginAid, logger) }()
	var prefix [4]byte
	for {
		if err := conn.SetDeadline(hard); err != nil {
			logger.Printf("OBS %s deadline remote=%s err=%v", addr, remote, err)
			return
		}
		if _, err := io.ReadFull(conn, prefix[:]); err != nil {
			if !errors.Is(err, io.EOF) {
				logger.Printf("OBS %s read remote=%s err=%v", addr, remote, err)
			}
			return
		}
		length := binary.BigEndian.Uint32(prefix[:])
		if length == 4 {
			hello := make([]byte, len(sessionHello))
			copy(hello, prefix[:])
			if _, err := io.ReadFull(conn, hello[4:]); err != nil || !bytes.Equal(hello, sessionHello) {
				logger.Printf("OBS %s hello remote=%s invalid err=%v", addr, remote, err)
				return
			}
			logger.Printf("OBS %s hello remote=%s hex=%s", addr, remote, hex.EncodeToString(hello))
			continue
		}
		if length < 2 || length > maxGatewayFrameLen {
			logger.Printf("OBS %s frame remote=%s invalid len=%d", addr, remote, length)
			return
		}
		frame := make([]byte, length)
		if _, err := io.ReadFull(conn, frame); err != nil {
			logger.Printf("OBS %s frame remote=%s len=%d err=%v", addr, remote, length, err)
			return
		}
		if frame[0] == 'D' && length >= 18 {
			agent := int(frame[10])
			packetId := binary.BigEndian.Uint32(frame[11:15])
			msgid := binary.BigEndian.Uint16(frame[16:18])
			logger.Printf("OBS %s direct remote=%s type=0x%02x agent=%d packetId=%d msgid=%d bytes=%d",
				addr, remote, frame[1], agent, packetId, msgid, length)
			if frame[1] != 0 || agent != 5 || msgid > 1 {
				continue
			}
			var body []byte
			if msgid == 1 {
				packetNo, ok := clientEventPacketNo(frame[18:])
				if !ok {
					logger.Printf("OBS %s direct remote=%s client-event invalid request", addr, remote)
					continue
				}
				body = binary.AppendUvarint([]byte{0x08}, packetNo)
			}
			reply := make([]byte, 22+len(body))
			binary.BigEndian.PutUint32(reply, uint32(18+len(body)))
			reply[4], reply[5] = 'D', 0x00
			copy(reply[6:14], frame[2:10])
			reply[14] = frame[10]
			copy(reply[15:19], frame[11:15])
			binary.BigEndian.PutUint16(reply[20:22], msgid)
			copy(reply[22:], body)
			if n, err := conn.Write(reply); err != nil || n != len(reply) {
				logger.Printf("OBS %s direct-reply remote=%s agent=%d err=%v", addr, remote, agent, err)
				return
			}
			logger.Printf("OBS %s direct-reply remote=%s agent=%d packetId=%d msgid=%d", addr, remote, agent, packetId, msgid)
			continue
		}
		if frame[0] == 'M' && length == 2 {
			logger.Printf("OBS %s ping remote=%s sub=%d", addr, remote, frame[1])
			// The client drops a link after ~5 s without inbound frames; echo pings
			// (sub 85 'U' is disconnect, never echoed).
			if frame[1] != 'U' {
				if _, err := conn.Write([]byte{0, 0, 0, 2, 'M', frame[1]}); err != nil {
					logger.Printf("OBS %s ping-echo remote=%s err=%v", addr, remote, err)
					return
				}
			}
			continue
		}
		if frame[0] != 'G' || length < 10 {
			logger.Printf("OBS %s frame remote=%s tag=%q len=%d", addr, remote, frame[0], length)
			continue
		}
		agent, subtype := int(frame[1]>>3)+1, frame[1]&7
		if subtype == 1 && length == 10 {
			if agent == squareAgent {
				if err := sq.handleOpen(frame); err != nil {
					return
				}
				continue
			}
			if agent == 10 {
				if gardenControlSent {
					logger.Printf("OBS %s open-retry remote=%s agent=10", addr, remote)
					continue
				}
				control := make([]byte, 22)
				binary.BigEndian.PutUint32(control, 18)
				control[4], control[5] = 'G', frame[1]
				copy(control[6:14], frame[2:10])
				if n, err := conn.Write(control); err != nil || n != len(control) {
					logger.Printf("OBS %s open-control remote=%s agent=10 err=%v", addr, remote, err)
					return
				}
				gardenControlSent = true
				logger.Printf("OBS %s open-control remote=%s agent=10", addr, remote)
				continue
			}
			if agent == roomPartyAgent {
				if err := rp.handleOpen(frame); err != nil {
					return
				}
				continue
			}
			ack := make([]byte, 14)
			binary.BigEndian.PutUint32(ack, 10)
			ack[4], ack[5] = 'G', frame[1]&0xf8|2
			copy(ack[6:], frame[2:10])
			if n, err := conn.Write(ack); err != nil || n != len(ack) {
				logger.Printf("OBS %s open-ack remote=%s agent=%d err=%v", addr, remote, agent, err)
				return
			}
			logger.Printf("OBS %s open-ack remote=%s agent=%d", addr, remote, agent)
			continue
		}
		if subtype == 0 && length >= 12 {
			msgid := binary.BigEndian.Uint16(frame[10:12])
			logger.Printf("OBS %s data remote=%s agent=%d msgid=%d bytes=%d", addr, remote, agent, msgid, length)
			if agent == squareAgent {
				if err := sq.handleData(frame); err != nil {
					return
				}
				if sq.loggedIn {
					hard = time.Time{} // Keep Square connected until the client disconnects.
				}
				continue
			}
			if agent == roomPartyAgent {
				if err := rp.handleData(frame); err != nil {
					return
				}
				if rp.loggedIn {
					hard = time.Time{} // Keep Room Party connected until the client disconnects.
				}
				continue
			}
			if agent == 10 && msgid == 0 {
				if !gardenLoginValid(frame[12:]) {
					logger.Printf("OBS %s garden-login remote=%s invalid request", addr, remote)
					continue
				}
				if aid, ok := protobufUintField(frame[12:], 2); ok {
					gardenLoginAid = aid
				}
				// ponytail: validate wire fields, accept local session until accounts persist across restarts.
				reply := make([]byte, 20)
				binary.BigEndian.PutUint32(reply, 16)
				reply[4], reply[5] = 'G', frame[1]
				copy(reply[6:14], frame[2:10])
				copy(reply[16:], []byte{0x08, 0x81, 0x84, 0x0c}) // hc_login_res.result = 197121
				if n, err := conn.Write(reply); err != nil || n != len(reply) {
					logger.Printf("OBS %s garden-login remote=%s err=%v", addr, remote, err)
					return
				}
				logger.Printf("OBS %s garden-login remote=%s result=197121", addr, remote)
			}
			if agent == 10 && msgid == 1 {
				ownerAid, ok := protobufUintField(frame[12:], 1)
				if !ok || ownerAid == 0 || ownerAid > (1<<63)-1 {
					logger.Printf("OBS %s garden-room-enter remote=%s invalid owner aid", addr, remote)
					continue
				}
				body := []byte{0x08, 0x81, 0x86, 0x0c, 0x22, byte(len(gardenMapStem))}
				body = append(body, gardenMapStem...)
				body = binary.AppendUvarint(append(body, 0x28), ownerAid)
				reply := make([]byte, 16+len(body))
				binary.BigEndian.PutUint32(reply, uint32(12+len(body)))
				reply[4], reply[5] = 'G', frame[1]
				copy(reply[6:14], frame[2:10])
				binary.BigEndian.PutUint16(reply[14:16], 1)
				copy(reply[16:], body)
				if n, err := conn.Write(reply); err != nil || n != len(reply) {
					logger.Printf("OBS %s garden-room-enter remote=%s err=%v", addr, remote, err)
					return
				}
				logger.Printf("OBS %s garden-room-enter remote=%s result=197377 map=%s", addr, remote, gardenMapStem)
				gardenLeaveIfOtherOwner(gardenLoginAid, ownerAid, logger)
				gardenOwnerAid = ownerAid
				gardenRoomEntered = true
				gardenRelayStarted = false
				copy(gardenRoomTunnel[:], frame[2:10])
			}
			if agent == 10 && msgid == 3 && gardenRoomEntered && length == 12 && bytes.Equal(frame[2:10], gardenRoomTunnel[:]) {
				body := gardenRelayStart(gardenOwnerAid, gardenLoginAid, conn, frame[1], frame[2:10], logger)
				reply := make([]byte, 16+len(body))
				binary.BigEndian.PutUint32(reply, uint32(12+len(body)))
				reply[4], reply[5] = 'G', frame[1]
				copy(reply[6:14], frame[2:10])
				binary.BigEndian.PutUint16(reply[14:16], 3)
				copy(reply[16:], body)
				if n, err := conn.Write(reply); err != nil || n != len(reply) {
					logger.Printf("OBS %s garden-relay-start remote=%s err=%v", addr, remote, err)
					return
				}
				logger.Printf("OBS %s garden-relay-start remote=%s result=0", addr, remote)
				gardenRelayStarted = true
				hard = time.Time{} // Keep the entered Garden connected until the client disconnects.
			}
			if agent == 10 && msgid == 15 && gardenRelayStarted && length == 12 && bytes.Equal(frame[2:10], gardenRoomTunnel[:]) {
				reply := make([]byte, 18)
				binary.BigEndian.PutUint32(reply, 14)
				reply[4], reply[5] = 'G', frame[1]
				copy(reply[6:14], frame[2:10])
				binary.BigEndian.PutUint16(reply[14:16], 21)
				// ponytail: empty quest list until account-backed Garden quest state exists.
				reply[16], reply[17] = 0x08, 0 // hc_quest_list_res.error_code = 0.
				if n, err := conn.Write(reply); err != nil || n != len(reply) {
					logger.Printf("OBS %s garden-quest-list remote=%s err=%v", addr, remote, err)
					return
				}
				logger.Printf("OBS %s garden-quest-list remote=%s errorCode=0", addr, remote)
			}
			continue
		}
		logger.Printf("OBS %s frame remote=%s agent=%d subtype=%d bytes=%d", addr, remote, agent, subtype, length)
	}
}

// Garden login requires a nonempty sessionKey (field 1) and positive aid
// (field 2). Other protobuf fields can be skipped without exposing their data.
func gardenLoginValid(data []byte) bool {
	hasKey, hasAid := false, false
	for len(data) > 0 {
		key, n := binary.Uvarint(data)
		if n <= 0 || key>>3 == 0 {
			return false
		}
		data = data[n:]
		switch key & 7 {
		case 0:
			value, n := binary.Uvarint(data)
			if n <= 0 {
				return false
			}
			if key>>3 == 2 {
				hasAid = value > 0 && value <= (1<<63)-1
			}
			data = data[n:]
		case 1:
			if len(data) < 8 {
				return false
			}
			data = data[8:]
		case 2:
			size, n := binary.Uvarint(data)
			if n <= 0 || size > uint64(len(data[n:])) {
				return false
			}
			if key>>3 == 1 {
				hasKey = size > 0
			}
			data = data[n+int(size):]
		case 5:
			if len(data) < 4 {
				return false
			}
			data = data[4:]
		default:
			return false
		}
	}
	return hasKey && hasAid
}

// OnConnected sends a session-only request with implicit packet number zero;
// numbered events carry field 2, distinct from the direct frame's packet ID.
func clientEventPacketNo(data []byte) (uint64, bool) {
	if packetNo, ok := protobufUintField(data, 2); ok {
		return packetNo, true
	}
	if len(data) < 2 || data[0] != 0x0a {
		return 0, false
	}
	size, n := binary.Uvarint(data[1:])
	if n <= 0 || size == 0 || size != uint64(len(data[1+n:])) {
		return 0, false
	}
	return 0, gardenLoginValid(data[1+n:]) // ClientSession also has sessionKey(1) and aid(2).
}

func protobufUintField(data []byte, field uint64) (uint64, bool) {
	var value uint64
	found := false
	for len(data) > 0 {
		key, n := binary.Uvarint(data)
		if n <= 0 || key>>3 == 0 {
			return 0, false
		}
		data = data[n:]
		switch key & 7 {
		case 0:
			fieldValue, n := binary.Uvarint(data)
			if n <= 0 {
				return 0, false
			}
			if key>>3 == field {
				value, found = fieldValue, true
			}
			data = data[n:]
		case 1:
			if len(data) < 8 {
				return 0, false
			}
			data = data[8:]
		case 2:
			size, n := binary.Uvarint(data)
			if n <= 0 || size > uint64(len(data[n:])) {
				return 0, false
			}
			data = data[n+int(size):]
		case 5:
			if len(data) < 4 {
				return 0, false
			}
			data = data[4:]
		default:
			return 0, false
		}
	}
	return value, found
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

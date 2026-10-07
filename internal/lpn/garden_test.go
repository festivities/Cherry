package lpn

import (
	"bytes"
	"encoding/binary"
	"io"
	"log"
	"net"
	"testing"
	"time"
)

func gardenReset(t *testing.T) {
	t.Helper()
	clearGardens := func() {
		gardensMu.Lock()
		gardenByAid = map[uint64]*gardenViewer{}
		gardenByOwner = map[uint64][]*gardenViewer{}
		gardenWanderEnabled = false
		gardenTickNum = 0
		gardensMu.Unlock()
	}
	clearGardens()
	t.Cleanup(clearGardens)
}

func TestGardenPx(t *testing.T) {
	if x, y := gardenPx(30, 39); x != 2040 || y != 1000 {
		t.Fatalf("owner spawn = %d,%d", x, y)
	}
	if x, y := gardenPx(34, 36); x != 2320 || y != 980 {
		t.Fatalf("visitor spawn = %d,%d", x, y)
	}
}

func TestGardenRelayBodyExcludesSelf(t *testing.T) {
	a := &gardenViewer{aid: 1001, x: 2040, y: 1000}
	b := &gardenViewer{aid: 1002, x: 2320, y: 980}
	if got := gardenRosterAids(gardenRelayBody([]*gardenViewer{a}, 1001)); len(got) != 0 {
		t.Fatalf("alone = %v", got)
	}
	if !bytes.Equal(gardenRelayBody([]*gardenViewer{a}, 1001), []byte{0x08, 0}) {
		t.Fatal("empty roster must stay result=0")
	}
	if got := gardenRosterAids(gardenRelayBody([]*gardenViewer{a, b}, 1002)); len(got) != 1 || got[0] != 1001 {
		t.Fatalf("visitor roster = %v", got)
	}
	if got := gardenRosterAids(gardenRelayBody([]*gardenViewer{a, b}, 1001)); len(got) != 1 || got[0] != 1002 {
		t.Fatalf("owner roster = %v", got)
	}
}

func TestGardenTilesDistinct(t *testing.T) {
	gardenReset(t)
	logger := log.New(io.Discard, "", 0)
	a, ap := net.Pipe()
	b, bp := net.Pipe()
	defer a.Close()
	defer b.Close()
	defer ap.Close()
	defer bp.Close()
	go io.Copy(io.Discard, ap)
	go io.Copy(io.Discard, bp)
	sid := []byte{1, 2, 3, 4, 5, 6, 7, 8}
	gardenRelayStart(1001, 1001, &lockedConn{Conn: a}, 0x48, sid, logger)
	gardenRelayStart(1001, 1002, &lockedConn{Conn: b}, 0x48, sid, logger)
	gardensMu.Lock()
	defer gardensMu.Unlock()
	o, v := gardenByAid[1001], gardenByAid[1002]
	if o == nil || v == nil {
		t.Fatal("missing viewers")
	}
	if o.x != 2040 || o.y != 1000 {
		t.Fatalf("owner tile = %d,%d", o.x, o.y)
	}
	if v.x == o.x && v.y == o.y {
		t.Fatal("shared tile")
	}
}

func readGardenPush(c net.Conn) (uint16, []byte, error) {
	_ = c.SetDeadline(time.Now().Add(2 * time.Second))
	hdr := make([]byte, 4)
	if _, err := io.ReadFull(c, hdr); err != nil {
		return 0, nil, err
	}
	rest := make([]byte, int(binary.BigEndian.Uint32(hdr)))
	if _, err := io.ReadFull(c, rest); err != nil {
		return 0, nil, err
	}
	return binary.BigEndian.Uint16(rest[10:12]), rest[12:], nil
}

func TestGardenJoinLeave(t *testing.T) {
	gardenReset(t)
	logger := log.New(io.Discard, "", 0)
	a, ap := net.Pipe()
	b, bp := net.Pipe()
	defer a.Close()
	defer b.Close()
	defer ap.Close()
	defer bp.Close()
	go io.Copy(io.Discard, bp)
	sid := []byte{1, 2, 3, 4, 5, 6, 7, 8}
	if body := gardenRelayStart(1001, 1001, &lockedConn{Conn: a}, 0x48, sid, logger); !bytes.Equal(body, []byte{0x08, 0}) {
		t.Fatalf("owner first relay = %x", body)
	}
	type push struct {
		msgid uint16
		body  []byte
		err   error
	}
	ch := make(chan push, 2)
	go func() {
		for range 2 {
			m, b, err := readGardenPush(ap)
			ch <- push{m, b, err}
		}
	}()
	body := gardenRelayStart(1001, 1002, &lockedConn{Conn: b}, 0x48, sid, logger)
	if got := gardenRosterAids(body); len(got) != 1 || got[0] != 1001 {
		t.Fatalf("visitor relay = %v", got)
	}
	join := <-ch
	if join.err != nil {
		t.Fatal(join.err)
	}
	if join.msgid != 3 {
		t.Fatalf("join msgid = %d", join.msgid)
	}
	if got := gardenRosterAids(join.body); len(got) != 1 || got[0] != 1002 {
		t.Fatalf("owner join push = %v", got)
	}
	gardenLeave(1002, logger)
	leave := <-ch
	if leave.err != nil {
		t.Fatal(leave.err)
	}
	if leave.msgid != 9 {
		t.Fatalf("leave msgid = %d", leave.msgid)
	}
	var gone uint64
	_ = pbScan(leave.body, func(f, _ uint64, d []byte) {
		if f != 1 || d == nil {
			return
		}
		_ = pbScan(d, func(f2, v2 uint64, _ []byte) {
			if f2 == 2 {
				gone = v2
			}
		})
	})
	if gone != 1002 {
		t.Fatalf("delete aid = %d", gone)
	}
}

func TestGardenMovePushBody(t *testing.T) {
	body := gardenMovePushBody(1002, 2320, 980, 2360, 940)
	var key, serial, curX, curY, tgtX, tgtY, visible uint64
	var haveKey, haveMove, haveVisible bool
	_ = pbScan(body, func(f, v uint64, d []byte) {
		switch f {
		case 1:
			haveKey = d != nil
			_ = pbScan(d, func(f2, v2 uint64, _ []byte) {
				if f2 == 1 {
					key = v2
				}
				if f2 == 2 {
					serial = v2
				}
			})
		case 2:
			haveMove = d != nil
			_ = pbScan(d, func(f2, _ uint64, d2 []byte) {
				if d2 == nil {
					return
				}
				_ = pbScan(d2, func(f3, v3 uint64, _ []byte) {
					switch {
					case f2 == 1 && f3 == 1:
						curX = v3
					case f2 == 1 && f3 == 2:
						curY = v3
					case f2 == 2 && f3 == 1:
						tgtX = v3
					case f2 == 2 && f3 == 2:
						tgtY = v3
					}
				})
			})
		case 5:
			haveVisible = true
			visible = v
		}
	})
	if !haveKey || key != 1 || serial != 1002 {
		t.Fatalf("key = %d,%d have=%v", key, serial, haveKey)
	}
	if !haveMove || curX != 2320 || curY != 980 || tgtX != 2360 || tgtY != 940 {
		t.Fatalf("move_info = %d,%d -> %d,%d have=%v", curX, curY, tgtX, tgtY, haveMove)
	}
	if !haveVisible || visible != 1 {
		t.Fatalf("visible = %d have=%v", visible, haveVisible)
	}
	var receiver = uint64(1001)
	_ = pbScan(body, func(f, _ uint64, d []byte) {
		if d != nil {
			_ = pbScan(d, func(f2, v2 uint64, _ []byte) {
				if v2 == receiver && f2 == 2 {
					t.Fatalf("body carries receiver aid %d", receiver)
				}
			})
		}
	})
}

func TestGardenActionPushBody(t *testing.T) {
	for _, at := range gardenActionTypes {
		body := gardenActionPushBody(1002, at)
		var key, serial, actionType, direction uint64
		var haveKey, haveInfo, haveType, haveDir bool
		var extraFields []uint64
		_ = pbScan(body, func(f, v uint64, d []byte) {
			switch {
			case f == 1 && d != nil:
				haveKey = true
				_ = pbScan(d, func(f2, v2 uint64, _ []byte) {
					if f2 == 1 {
						key = v2
					}
					if f2 == 2 {
						serial = v2
					}
				})
			case f == 2 && d != nil:
				haveInfo = true
				_ = pbScan(d, func(f2, v2 uint64, _ []byte) {
					switch {
					case f2 == 1:
						haveType = true
						actionType = v2
					case f2 == 2:
						haveDir = true
						direction = v2
					}
				})
			case f > 2:
				extraFields = append(extraFields, f)
			}
		})
		if !haveKey || key != 1 || serial != 1002 {
			t.Fatalf("action_type %d: key = %d,%d have=%v", at, key, serial, haveKey)
		}
		if !haveInfo || !haveType || actionType != at || !haveDir || direction != 0 {
			t.Fatalf("action_type %d: info = type %d dir %d (have %v/%v)", at, actionType, direction, haveType, haveDir)
		}
		if len(extraFields) != 0 {
			t.Fatalf("action_type %d: unexpected fields %v", at, extraFields)
		}
		for _, receiver := range []uint64{1001, 1003} {
			_ = pbScan(body, func(_, _ uint64, d []byte) {
				if d != nil {
					_ = pbScan(d, func(f2, v2 uint64, _ []byte) {
						if v2 == receiver && f2 == 2 {
							t.Fatalf("body carries receiver aid %d", receiver)
						}
					})
				}
			})
		}
	}
}

// Two-pipe: with two viewers and an emote tick, the owner (join order,
// round-robin target) emotes instead of walking, so the other pipe gets
// msgid 13 carrying the owner's serial and no viewer gets its own aid.
func TestGardenActionTick(t *testing.T) {
	gardenReset(t)
	gardensMu.Lock()
	gardenActionEvery = 1
	gardensMu.Unlock()
	t.Cleanup(func() {
		gardensMu.Lock()
		gardenActionEvery = 3
		gardensMu.Unlock()
	})
	a, ap := net.Pipe()
	b, bp := net.Pipe()
	defer a.Close()
	defer b.Close()
	defer ap.Close()
	defer bp.Close()
	type push struct {
		msgid uint16
		body  []byte
		err   error
	}
	chA := make(chan push, 4)
	chB := make(chan push, 4)
	go func() {
		for {
			m, b, err := readGardenPush(ap)
			chA <- push{m, b, err}
			if err != nil {
				return
			}
		}
	}()
	go func() {
		for {
			m, b, err := readGardenPush(bp)
			chB <- push{m, b, err}
			if err != nil {
				return
			}
		}
	}()
	sid := []byte{1, 2, 3, 4, 5, 6, 7, 8}
	gardenRelayStart(4001, 4001, &lockedConn{Conn: a}, 0x48, sid, nil)
	gardenRelayStart(4001, 4002, &lockedConn{Conn: b}, 0x48, sid, nil)
	join := <-chA
	if join.err != nil {
		t.Fatal(join.err)
	}
	if join.msgid != 3 {
		t.Fatalf("join msgid = %d", join.msgid)
	}
	gardenWanderTick()
	emote := <-chA
	if emote.err != nil {
		t.Fatal(emote.err)
	}
	if emote.msgid != 13 {
		t.Fatalf("emote msgid = %d", emote.msgid)
	}
	var serial, actionType, direction uint64
	var haveInfo bool
	_ = pbScan(emote.body, func(f, _ uint64, d []byte) {
		switch {
		case f == 1 && d != nil:
			_ = pbScan(d, func(f2, v2 uint64, _ []byte) {
				if f2 == 2 {
					serial = v2
				}
			})
		case f == 2 && d != nil:
			haveInfo = true
			_ = pbScan(d, func(f2, v2 uint64, _ []byte) {
				if f2 == 1 {
					actionType = v2
				}
				if f2 == 2 {
					direction = v2
				}
			})
		}
	})
	if serial != 4002 {
		t.Fatalf("emote push to 4001 carries serial=%d, not receiver's own", serial)
	}
	if !haveInfo {
		t.Fatal("actoin_info missing")
	}
	found := false
	for _, at := range gardenActionTypes {
		if actionType == at {
			found = true
		}
	}
	if !found {
		t.Fatalf("action_type = %d, not in safe set %v", actionType, gardenActionTypes)
	}
	if direction != 0 {
		t.Fatalf("direction = %d", direction)
	}
	move := <-chB
	if move.err != nil {
		t.Fatal(move.err)
	}
	if move.msgid != 12 {
		t.Fatalf("visitor msgid = %d, want owner move 12", move.msgid)
	}
	var mover uint64
	_ = pbScan(move.body, func(f, _ uint64, d []byte) {
		if f == 1 && d != nil {
			_ = pbScan(d, func(f2, v2 uint64, _ []byte) {
				if f2 == 2 {
					mover = v2
				}
			})
		}
	})
	if mover != 4001 {
		t.Fatalf("move push to visitor carries serial=%d", mover)
	}
}

func TestGardenJoinRateLimit(t *testing.T) {
	gardenReset(t)
	logger := log.New(io.Discard, "", 0)
	a, ap := net.Pipe()
	b, bp := net.Pipe()
	c, cp := net.Pipe()
	defer a.Close()
	defer b.Close()
	defer c.Close()
	defer ap.Close()
	defer bp.Close()
	defer cp.Close()
	go io.Copy(io.Discard, bp)
	go io.Copy(io.Discard, cp)
	sid := []byte{1, 2, 3, 4, 5, 6, 7, 8}
	gardenRelayStart(3001, 3001, &lockedConn{Conn: a}, 0x48, sid, logger)
	type push struct {
		msgid uint16
		body  []byte
		err   error
	}
	ch := make(chan push, 2)
	go func() {
		for range 2 {
			m, b, err := readGardenPush(ap)
			ch <- push{m, b, err}
		}
	}()
	gardenRelayStart(3001, 3002, &lockedConn{Conn: b}, 0x48, sid, logger)
	first := <-ch
	if first.err != nil {
		t.Fatal(first.err)
	}
	if first.msgid != 3 {
		t.Fatalf("first broadcast msgid = %d", first.msgid)
	}
	gardenRelayStart(3001, 3003, &lockedConn{Conn: c}, 0x48, sid, logger)
	second := <-ch
	if second.err == nil {
		t.Fatalf("hot occupant got a second idx3 (msgid=%d)", second.msgid)
	}
}

func TestGardenWanderTick(t *testing.T) {
	gardenReset(t)
	a, ap := net.Pipe()
	b, bp := net.Pipe()
	defer a.Close()
	defer b.Close()
	defer ap.Close()
	defer bp.Close()
	type push struct {
		msgid uint16
		body  []byte
		err   error
	}
	chA := make(chan push, 4)
	chB := make(chan push, 4)
	go func() {
		for {
			m, b, err := readGardenPush(ap)
			chA <- push{m, b, err}
			if err != nil {
				return
			}
		}
	}()
	go func() {
		for {
			m, b, err := readGardenPush(bp)
			chB <- push{m, b, err}
			if err != nil {
				return
			}
		}
	}()
	sid := []byte{1, 2, 3, 4, 5, 6, 7, 8}
	gardenRelayStart(4001, 4001, &lockedConn{Conn: a}, 0x48, sid, nil)
	gardenRelayStart(4001, 4002, &lockedConn{Conn: b}, 0x48, sid, nil)
	join := <-chA
	if join.err != nil {
		t.Fatal(join.err)
	}
	if join.msgid != 3 {
		t.Fatalf("join msgid = %d", join.msgid)
	}
	gardenWanderTick()
	pb := <-chB
	if pb.err != nil {
		t.Fatal(pb.err)
	}
	if pb.msgid != 12 {
		t.Fatalf("wander msgid = %d", pb.msgid)
	}
	var serial uint64
	var tgtX, tgtY, visible uint64
	_ = pbScan(pb.body, func(f, v uint64, d []byte) {
		switch f {
		case 1:
			_ = pbScan(d, func(f2, v2 uint64, _ []byte) {
				if f2 == 2 {
					serial = v2
				}
			})
		case 2:
			_ = pbScan(d, func(f2, _ uint64, d2 []byte) {
				if f2 != 2 || d2 == nil {
					return
				}
				_ = pbScan(d2, func(f3, v3 uint64, _ []byte) {
					if f3 == 1 {
						tgtX = v3
					}
					if f3 == 2 {
						tgtY = v3
					}
				})
			})
		case 5:
			visible = v
		}
	})
	if serial != 4001 {
		t.Fatalf("wander push to 4002 carries serial=%d, not receiver's own", serial)
	}
	gardensMu.Lock()
	o := gardenByAid[4001]
	ox, oy, otx, oty := o.x, o.y, o.tx, o.ty
	gardensMu.Unlock()
	if ox != tgtX || oy != tgtY {
		t.Fatalf("stored tile %d,%d != target %d,%d", ox, oy, tgtX, tgtY)
	}
	if otx < 0 || otx > 59 || oty < 0 || oty > 59 {
		t.Fatalf("wandered off map: %d,%d", otx, oty)
	}
	if visible != 1 {
		t.Fatalf("visible = %d", visible)
	}
	pa := <-chA
	if pa.err != nil {
		t.Fatal(pa.err)
	}
	if pa.msgid != 12 {
		t.Fatalf("wander msgid to owner = %d", pa.msgid)
	}
}

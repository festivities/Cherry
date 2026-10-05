package main

import (
	"log"
	"math/rand/v2"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

// Garden visit presence: players in the same Garden see each other at
// server-chosen tiles. This client never sends walk/chat, so positions are
// static until leave/join. A slow wander ticker (idx12) keeps remote avatars
// from looking planted; every 3rd tick one member instead plays an emote
// (idx13) for the others. Join broadcasts to already-present viewers are
// debounced so rapid joins do not spam EndActivity-side effects on them.
var (
	gardensMu     sync.Mutex
	gardenByAid   = map[uint64]*gardenViewer{}
	gardenByOwner = map[uint64][]*gardenViewer{}

	gardenWanderInterval = 6 * time.Second
	gardenJoinCooldown   = 1500 * time.Millisecond
	gardenWanderEnabled  = true
	gardenWanderRunning  bool

	gardenActionEvery = 3 // emote every Nth wander tick, 0 disables
	gardenTickNum     int
)

// Emote ids from the shared avatar action table, observed from the stock
// client's own cr_player_action sends (two-device Square logs 2026-10-05);
// none appears in the client's blocked-id lists.
var gardenActionTypes = [3]uint64{13, 54, 105}

type gardenViewer struct {
	aid, owner   uint64
	conn         *lockedConn
	b1           byte
	sid          [8]byte
	x, y         uint64
	tx, ty       int
	lastJoinPush atomic.Int64
}

// Pack AVATAR_START type0=(30,39) owner, type1=(34,36) visitor; extras are
// nearby tiles. All 24 known garden packs are N=60 with those two points
// (static 2026-10-06). Parse a pack only if a future stem diverges.
var gardenSpawnTiles = [][2]int{
	{30, 39}, {34, 36}, {35, 36}, {33, 36}, {34, 37}, {32, 38}, {36, 35},
}

func gardenPx(x, y int) (uint64, uint64) {
	return uint64(40 * (60 + x - y)), uint64(20 * (119 - x - y))
}

func gardenPickTile(owner, aid uint64, used map[[2]int]bool) [2]int {
	for _, t := range gardenSpawnTiles {
		if aid != owner && t == [2]int{30, 39} {
			continue
		}
		if !used[t] {
			used[t] = true
			return t
		}
	}
	n := len(used)
	return [2]int{34 + n, 36}
}

func gardenObjectKey(aid uint64) []byte {
	b := pbVar(nil, 1, 1)
	return pbVar(b, 2, aid)
}

func gardenMoveInfo(x, y uint64) []byte {
	pos := pbVar(nil, 1, x)
	pos = pbVar(pos, 2, y)
	return pbLen(nil, 1, pos)
}

// hc_actor_move_push (idx12): key#1 {type=1, serial}, move_info#2
// {cur_pos#1, target_pos#2} each {px#1, py#2} varints, visible#5=1.
// Stock consumer (NaGardenActorManager::RecvPushActorMove) reads key +
// move_info.target_pos only; visible is never read but kept for wire realism.
// Never address the receiver's own aid — that walks its own hero.
func gardenMovePushBody(aid, curX, curY, newX, newY uint64) []byte {
	point := func(x, y uint64) []byte {
		p := pbVar(nil, 1, x)
		return pbVar(p, 2, y)
	}
	mi := pbLen(nil, 1, point(curX, curY))
	mi = pbLen(mi, 2, point(newX, newY))
	b := pbLen(nil, 1, gardenObjectKey(aid))
	b = pbLen(b, 2, mi)
	return pbVar(b, 5, 1)
}

// hc_actor_action_push (idx13): key#1 {type=1, serial}, actoin_info#2
// {action_type#1, direction#2}. Stock consumer (NaGardenActorManager::
// RecvPushActorAction) reads key + actoin_info only and hands
// (action_type, direction) to AvActor::DirRotateAction; visible#3 and
// visibleDelayTime#4 (fixed32 float) are never read and stay unset.
// Never address the receiver's own aid — that plays the emote on its hero.
func gardenActionPushBody(aid, actionType uint64) []byte {
	ai := pbVar(nil, 1, actionType)
	ai = pbVar(ai, 2, 0)
	b := pbLen(nil, 1, gardenObjectKey(aid))
	return pbLen(b, 2, ai)
}

func gardenAvatarInfo(aid uint64) []byte {
	id := strconv.FormatUint(aid, 10)
	acc, ok := accountByAvatarID(id)
	if !ok {
		acc = accountSnapshot{aid: id, name: "cherry", gender: "FEMALE", skin: "1", country: squareDefaultCountry}
	}
	if acc.skin == "" {
		acc.skin = "1"
	}
	if acc.country == "" {
		acc.country = squareDefaultCountry
	}
	if acc.name == "" {
		acc.name = "cherry"
	}
	b := pbLen(nil, 1, []byte(acc.name))
	b = pbVar(b, 2, squareSex(acc.gender))
	b = pbLen(b, 4, []byte(acc.country))
	b = pbLen(b, 7, []byte(acc.skin))
	for _, code := range acc.itemCodes {
		if n, ok := squareNumericItem(code); ok {
			b = pbLen(b, 13, pbVar(nil, 1, n))
		}
	}
	return b
}

func gardenPlayerInfo(v *gardenViewer) []byte {
	b := pbLen(nil, 1, gardenObjectKey(v.aid))
	b = pbLen(b, 2, gardenAvatarInfo(v.aid))
	return pbLen(b, 3, gardenMoveInfo(v.x, v.y))
}

func gardenRelayBody(viewers []*gardenViewer, skip uint64) []byte {
	b := pbVar(nil, 1, 0)
	for _, v := range viewers {
		if v.aid == skip {
			continue
		}
		b = pbLen(b, 2, gardenPlayerInfo(v))
	}
	return b
}

func gardenRosterAids(body []byte) []uint64 {
	var aids []uint64
	_ = pbScan(body, func(f, _ uint64, d []byte) {
		if f != 2 || d == nil {
			return
		}
		var key []byte
		_ = pbScan(d, func(f2, _ uint64, d2 []byte) {
			if f2 == 1 && d2 != nil {
				key = d2
			}
		})
		_ = pbScan(key, func(f3, v3 uint64, _ []byte) {
			if f3 == 2 {
				aids = append(aids, v3)
			}
		})
	})
	return aids
}

func gardenSend(v *gardenViewer, logger *log.Logger, msgid uint16, body []byte) {
	if v == nil || v.conn == nil {
		return
	}
	if _, err := v.conn.Write(squareFrame(v.b1, v.sid[:], msgid, body)); err != nil && logger != nil {
		logger.Printf("GARDEN send aid=%d msgid=%d err=%v", v.aid, msgid, err)
	}
}

func gardenStartWander() {
	gardensMu.Lock()
	defer gardensMu.Unlock()
	if !gardenWanderEnabled || gardenWanderRunning {
		return
	}
	gardenWanderRunning = true
	go gardenWanderLoop()
}

func gardenWanderLoop() {
	t := time.NewTicker(gardenWanderInterval)
	defer t.Stop()
	for range t.C {
		gardensMu.Lock()
		run := gardenWanderEnabled
		gardensMu.Unlock()
		if !run {
			return
		}
		gardenWanderTick()
	}
}

func gardenWanderPick(cur [2]int, occ map[[2]int]bool) ([2]int, bool) {
	var cands [][2]int
	for dx := -3; dx <= 3; dx++ {
		for dy := -3; dy <= 3; dy++ {
			if dx == 0 && dy == 0 {
				continue
			}
			t := [2]int{cur[0] + dx, cur[1] + dy}
			if t[0] < 0 || t[0] > 59 || t[1] < 0 || t[1] > 59 || occ[t] {
				continue
			}
			cands = append(cands, t)
		}
	}
	if len(cands) == 0 {
		return cur, false
	}
	return cands[rand.IntN(len(cands))], true
}

// One process-wide tick: in each garden with >=2 viewers, walk every member
// to a nearby free tile and tell all *other* viewers via idx12 (never a
// viewer its own aid). Stored tiles update so relay-start stays consistent.
// Every gardenActionEvery'th tick one member (round-robin) emotes instead of
// walking: members play idx13 to the other viewers, skipping that member's
// move so the client never has a walk cancel a just-started emote.
func gardenWanderTick() {
	type tickPush struct {
		msgid uint16
		to    *gardenViewer
		body  []byte
	}
	var pushes []tickPush
	gardensMu.Lock()
	gardenTickNum++
	emoteTurn := gardenActionEvery > 0 && gardenTickNum%gardenActionEvery == 0
	for _, vs := range gardenByOwner {
		if len(vs) < 2 {
			continue
		}
		occ := map[[2]int]bool{}
		for _, v := range vs {
			occ[[2]int{v.tx, v.ty}] = true
		}
		for i, o := range vs {
			if emoteTurn && i == gardenTickNum%len(vs) {
				body := gardenActionPushBody(o.aid, gardenActionTypes[(gardenTickNum/gardenActionEvery)%len(gardenActionTypes)])
				for _, v := range vs {
					if v.aid != o.aid {
						pushes = append(pushes, tickPush{13, v, body})
					}
				}
				continue
			}
			t, ok := gardenWanderPick([2]int{o.tx, o.ty}, occ)
			if !ok {
				continue
			}
			occ[t] = true
			px, py := gardenPx(t[0], t[1])
			body := gardenMovePushBody(o.aid, o.x, o.y, px, py)
			for _, v := range vs {
				if v.aid != o.aid {
					pushes = append(pushes, tickPush{12, v, body})
				}
			}
			o.tx, o.ty = t[0], t[1]
			o.x, o.y = px, py
		}
	}
	gardensMu.Unlock()
	for _, p := range pushes {
		gardenSend(p.to, nil, p.msgid, p.body)
	}
}

func gardenLeave(aid uint64, logger *log.Logger) {
	if aid == 0 {
		return
	}
	gardensMu.Lock()
	v := gardenByAid[aid]
	if v == nil {
		gardensMu.Unlock()
		return
	}
	delete(gardenByAid, aid)
	list := gardenByOwner[v.owner]
	kept := list[:0]
	for _, p := range list {
		if p.aid != aid {
			kept = append(kept, p)
		}
	}
	if len(kept) == 0 {
		delete(gardenByOwner, v.owner)
	} else {
		gardenByOwner[v.owner] = kept
	}
	others := append([]*gardenViewer(nil), kept...)
	gardensMu.Unlock()
	body := pbLen(nil, 1, gardenObjectKey(aid))
	for _, o := range others {
		gardenSend(o, logger, 9, body)
	}
	if logger != nil {
		logger.Printf("GARDEN leave aid=%d owner=%d remain=%d", aid, v.owner, len(others))
	}
}

func gardenLeaveIfOtherOwner(viewer, owner uint64, logger *log.Logger) {
	gardensMu.Lock()
	v := gardenByAid[viewer]
	gardensMu.Unlock()
	if v != nil && v.owner != owner {
		gardenLeave(viewer, logger)
	}
}

func gardenRelayStart(owner, viewer uint64, conn *lockedConn, b1 byte, sid []byte, logger *log.Logger) []byte {
	if viewer == 0 || owner == 0 {
		return pbVar(nil, 1, 0)
	}
	gardensMu.Lock()
	if cur := gardenByAid[viewer]; cur != nil && cur.owner == owner {
		copy(cur.sid[:], sid)
		cur.conn, cur.b1 = conn, b1
		all := append([]*gardenViewer(nil), gardenByOwner[owner]...)
		body := gardenRelayBody(all, viewer)
		gardensMu.Unlock()
		return body
	}
	if cur := gardenByAid[viewer]; cur != nil {
		gardensMu.Unlock()
		gardenLeave(viewer, logger)
		gardensMu.Lock()
	}
	used := map[[2]int]bool{}
	for _, p := range gardenByOwner[owner] {
		used[[2]int{p.tx, p.ty}] = true
	}
	v := &gardenViewer{aid: viewer, owner: owner, conn: conn, b1: b1}
	copy(v.sid[:], sid)
	t := gardenPickTile(owner, viewer, used)
	v.tx, v.ty = t[0], t[1]
	v.x, v.y = gardenPx(t[0], t[1])
	gardenByAid[viewer] = v
	gardenByOwner[owner] = append(gardenByOwner[owner], v)
	all := append([]*gardenViewer(nil), gardenByOwner[owner]...)
	body := gardenRelayBody(all, viewer)
	var sends []*gardenViewer
	var bodies [][]byte
	now := time.Now().UnixNano()
	for _, o := range all {
		if o.aid == viewer {
			continue
		}
		if last := o.lastJoinPush.Load(); last != 0 && time.Since(time.Unix(0, last)) < gardenJoinCooldown {
			if logger != nil {
				logger.Printf("GARDEN join-broadcast skip aid=%d cooldown=%v", o.aid, gardenJoinCooldown)
			}
			continue
		}
		o.lastJoinPush.Store(now)
		sends = append(sends, o)
		bodies = append(bodies, gardenRelayBody(all, o.aid))
	}
	gardensMu.Unlock()
	for i, o := range sends {
		gardenSend(o, logger, 3, bodies[i])
	}
	gardenStartWander()
	if logger != nil {
		logger.Printf("GARDEN join aid=%d owner=%d roster=%d tile=%d,%d", viewer, owner, len(all)-1, v.x, v.y)
	}
	return body
}

package lpn

import (
	"cmp"
	"encoding/json"
	"log"
	"maps"
	"slices"
	"strconv"

	"cherry/internal/economy"
	"cherry/internal/store"
)

// Square boss rooms (multiplayer, Economy Phase DE; wire notes %TEMP%\opencode\cherry\leftovers-20261026\
// boss-mp-spec.md sections 2 to 4 and 7, boss-mp-addendum.md 1d, 1e, 1j and 3; the room policy is the user's). A lobby belongs to one Square
// (squareRoom.bosses, oldest first) and holds up to max_players members. The first member is the master; when
// the master leaves, the earliest remaining member is the master. A member is a session: its seat keeps the
// session's transport (a squarePlayer: conn and frame header), so the pushes reach every member from any
// session's goroutine. Every room and seat field is guarded by the Square's squareRoom.mu. Pushes are built
// under the lock and written after it.

// squareBossRoom is one boss lobby, and its battle once it starts.
type squareBossRoom struct {
	members []*squareBossSeat // join order; members[0] is the master
	invited map[uint64]bool   // aids the master invited (cr35); an invitee's accept (cr36) uses up its invitation
	fight   bool              // the battle started (rc71 sent): no joins, no ready changes
	ended   bool              // the battle is over (HP 0, or the first finish cr58): no more touches
	caught  bool              // the boss lost all its HP
	hp      int64             // the shared boss HP
	final   []*squareBossSeat // the members of the battle when it ended, join order: ranks and rewards
}

// squareBossSeat is one member: its transport and its state in the lobby and the battle (the ready flag,
// the damage it dealt and the hits it made on each note, its result once answered).
type squareBossSeat struct {
	sq     *squareRoom
	p      *squarePlayer     // transport: pushes to this member
	room   *squareBossRoom   // nil once the member left or was kicked (guarded by sq.mu)
	ready  bool              // State3 ready (cr47 1); the master is always shown ready
	damage int64             // DamageSum: the damage this member dealt
	hits   map[uint64]uint64 // per note index: the last accepted TouchCount of this member's button
	rc65   []byte            // the rc65 body answered to this member, repeated for a repeated cr58
}

func (m *squareBossSeat) aid() uint64 { return m.p.aid }

// bossSquare is the Square the session is in: its boss lobbies are that Square's.
func (s *squareSession) bossSquare() *squareRoom {
	if s.room != nil {
		return s.room
	}
	return s.rooms.room(squareThemeID)
}

// bossSeatFor is a new seat in sq for the session, with the frame's tunnel as its transport.
func (s *squareSession) bossSeatFor(frame []byte, sq *squareRoom) *squareBossSeat {
	p := &squarePlayer{aid: s.aid, conn: s.conn, b1: frame[1]}
	copy(p.sid[:], frame[2:10])
	return &squareBossSeat{sq: sq, p: p, hits: map[uint64]uint64{}}
}

// bossLobbyOf is the Square that holds the lobby the master owner opened, by the owner's aid. An invitee accepts from
// wherever its session is (a Garden, another Square), so the lobby is searched in every Square, not in the session's.
func (rs *squareRooms) bossLobbyOf(owner uint64) *squareRoom {
	rs.mu.Lock()
	rooms := slices.Collect(maps.Values(rs.rooms))
	rs.mu.Unlock()
	for _, sq := range rooms {
		sq.mu.Lock()
		found := slices.ContainsFunc(sq.bosses, func(r *squareBossRoom) bool { return r.members[0].aid() == owner })
		sq.mu.Unlock()
		if found {
			return sq
		}
	}
	return nil
}

// entryLocked is a member's FishBossUserInLobby: Aid1, IsRoomMaster2 (the first member), State3 (ready, or
// the master, who is always ready), RodInfo4 (the rod in use), Profile5 (the picture) and Name7.
func (r *squareBossRoom) entryLocked(m *squareBossSeat) []byte {
	master := r.members[0] == m
	state := uint64(squareBossNotReady)
	if master || m.ready {
		state = squareBossReady
	}
	u := pbVar(nil, 1, m.aid())
	u = pbVar(u, 2, squareBool(master))
	u = pbVar(u, 3, state)
	if rod := squareBossRod(m.aid()); rod != nil {
		u = pbLen(u, 4, rod)
	}
	u = pbLen(u, 5, []byte(squareBossPicture(m.aid())))
	return pbLen(u, 7, []byte(squareAccountName(m.aid())))
}

// squareStaticHost is the static host of the Square's pictures: the host the client resolves to Cherry, so an absolute
// URL on it is served by the same routes as the fish cards (economy.FishImageURL).
const (
	squareStaticHost         = "https://play-static.line-scdn.net"
	squareProfilePlaceholder = squareStaticHost + "/cherry/menu/user-star.png" // Profile5 of an account with no picture
)

// squareDefaultRod is the first rod of the fish shop: the rod a member shows when none is in use.
func squareDefaultRod() (economy.FishingItem, bool) {
	for _, it := range economy.FishingShopItems() {
		if it.Kind == economy.FishKindRod {
			return it, true
		}
	}
	return economy.FishingItem{}, false
}

// squareBossRod is RodInfo4 of a lobby entry: an AssetItemClient for the rod the member has in use (the default rod
// when none). The picture is an absolute URL, because the lobby's picture download takes the URL as it is
// (boss-mp-addendum 3). nil only when the shop lists no rod at all.
func squareBossRod(aid uint64) []byte {
	_, inUse := economy.FishingBag(strconv.FormatUint(aid, 10), economy.FishKindRod)
	rod, ok := economy.FishingItemByID(inUse)
	if !ok {
		rod, ok = squareDefaultRod()
	}
	if !ok {
		return nil
	}
	return squareAsset(rod, squareStaticHost+"/"+economy.FishingItemImageURL(rod.ID))
}

// squareBossPicture is Profile5 of a lobby entry, an absolute URL that is never empty: the member's uploaded picture
// (its OBS path, served by the profile route) or the placeholder icon for an account with none.
func squareBossPicture(aid uint64) string {
	if acc, ok := store.AccountByAvatarID(strconv.FormatUint(aid, 10)); ok && acc.ProfileImage.Image != "" {
		return squareStaticHost + acc.ProfileImage.Image
	}
	return squareProfilePlaceholder
}

// addLocked puts seat at the end of the lobby and returns the members it joined.
func (r *squareBossRoom) addLocked(seat *squareBossSeat) []*squareBossSeat {
	others := slices.Clone(r.members)
	seat.room = r
	r.members = append(r.members, seat)
	return others
}

// removeLocked takes seat out of the lobby and returns the members left, with the rc70 push for them: the leaver is
// erased and, when it was the master, the new master (the next member) is upserted in the same push.
func (r *squareBossRoom) removeLocked(seat *squareBossSeat) (rest []*squareBossSeat, body []byte) {
	i := slices.Index(r.members, seat)
	if i < 0 {
		return nil, nil
	}
	r.members = slices.Delete(r.members, i, i+1)
	body = pbVar(nil, 1, seat.aid())
	if i == 0 && len(r.members) > 0 {
		body = pbLen(body, 2, r.entryLocked(r.members[0]))
	}
	return slices.Clone(r.members), body
}

// allReadyLocked is the master's start rule: every member but the master is ready (a lone master passes).
func (r *squareBossRoom) allReadyLocked() bool {
	for _, m := range r.members[1:] {
		if !m.ready {
			return false
		}
	}
	return true
}

// bossLobbyLocked is the oldest lobby of the Square that has room for one more member and has not started.
func (sq *squareRoom) bossLobbyLocked(seats int) *squareBossRoom {
	for _, r := range sq.bosses {
		if !r.fight && len(r.members) < seats {
			return r
		}
	}
	return nil
}

// dropLocked deletes an empty lobby.
func (sq *squareRoom) dropLocked(r *squareBossRoom) {
	sq.bosses = slices.DeleteFunc(sq.bosses, func(o *squareBossRoom) bool { return o == r })
}

// pushMembers writes one frame to each member. It runs outside the Square's lock: each write has its own deadline.
func pushMembers(logger *log.Logger, members []*squareBossSeat, msgid uint16, body []byte) {
	for _, m := range members {
		m.p.send(logger, msgid, body)
	}
}

// bossOpen puts the session into a new lobby as its master (cr45: a start always opens its own lobby).
func (s *squareSession) bossOpen(frame []byte) {
	sq := s.bossSquare()
	seat := s.bossSeatFor(frame, sq)
	sq.mu.Lock()
	r := &squareBossRoom{invited: map[uint64]bool{}}
	sq.bosses = append(sq.bosses, r)
	r.addLocked(seat)
	sq.mu.Unlock()
	s.boss = seat
}

// bossJoinLobby puts the session into the oldest lobby of its Square that has room, or opens one when none has.
// The members already there get rc70 with the newcomer. The try is already spent.
func (s *squareSession) bossJoinLobby(frame []byte) {
	sq := s.bossSquare()
	seat := s.bossSeatFor(frame, sq)
	seats := int(economy.ActiveFishBoss().MaxPlayers)
	sq.mu.Lock()
	r := sq.bossLobbyLocked(seats)
	if r == nil {
		r = &squareBossRoom{invited: map[uint64]bool{}}
		sq.bosses = append(sq.bosses, r)
	}
	others := r.addLocked(seat)
	var body []byte
	if len(others) > 0 {
		body = pbLen(nil, 2, r.entryLocked(seat))
	}
	sq.mu.Unlock()
	s.boss = seat
	s.logger.Printf("SQUARE fishing boss lobby join aid=%d members=%d", s.aid, len(others)+1)
	pushMembers(s.logger, others, 70, body)
}

// bossLeave takes the session out of its lobby or battle (cr2, a new try, a disconnect). The others get rc70 with
// the leaver erased; the new master comes in the same push when the leaver was the master. An empty lobby is deleted.
func (s *squareSession) bossLeave() {
	seat := s.boss
	s.boss = nil
	if seat == nil {
		return
	}
	sq := seat.sq
	sq.mu.Lock()
	r := seat.room
	if r == nil {
		sq.mu.Unlock()
		return
	}
	seat.room = nil
	fight := r.fight
	rest, body := r.removeLocked(seat)
	if len(r.members) == 0 {
		sq.dropLocked(r)
	}
	sq.mu.Unlock()
	s.logger.Printf("SQUARE fishing boss leave aid=%d rest=%d fight=%v", s.aid, len(rest), fight)
	pushMembers(s.logger, rest, 70, body)
}

// bossRoster is rc_fishing_boss_roominfo_res (59) for the session's lobby: RoomState, the boss at full HP and every
// member. ok is false when the session is in no lobby.
func (s *squareSession) bossRoster() (body []byte, ok bool) {
	seat := s.boss
	if seat == nil {
		return nil, false
	}
	sq := seat.sq
	sq.mu.Lock()
	defer sq.mu.Unlock()
	r := seat.room
	if r == nil {
		return nil, false
	}
	state := uint64(squareBossRoomState)
	if r.fight {
		state = squareBossRoomRunning
	}
	body = pbVar(nil, 1, state)
	body = pbLen(body, 2, squareBossInfo(economy.ActiveFishBoss()))
	for _, m := range r.members {
		body = pbLen(body, 3, r.entryLocked(m))
	}
	return body, true
}

// bossState answers cr47: ready or unready (a non-master's button; rc70 to the lobby), or the master's start (rc71 to
// the battle, once every non-master is ready). Anything else, and any change once the battle started, changes nothing.
func (s *squareSession) bossState(state uint64) {
	seat := s.boss
	if seat == nil {
		s.logger.Printf("SQUARE fishing boss state aid=%d state=%d ignored", s.aid, state)
		return
	}
	boss := economy.ActiveFishBoss()
	sq := seat.sq
	sq.mu.Lock()
	r := seat.room
	if r == nil || r.fight {
		sq.mu.Unlock()
		s.logger.Printf("SQUARE fishing boss state aid=%d state=%d ignored", s.aid, state)
		return
	}
	master := r.members[0] == seat
	var msgid uint16
	var body []byte
	switch {
	case (state == squareBossReadyReq || state == squareBossUnready) && !master:
		seat.ready = state == squareBossReadyReq
		msgid, body = 70, pbLen(nil, 2, r.entryLocked(seat))
	case state == squareBossStateStart && master && r.allReadyLocked():
		r.fight = true
		r.hp = boss.Boss.MaxHp
		body = pbLen(nil, 1, squareBossGameData(boss))
		for _, m := range r.members {
			body = pbVar(body, 2, m.aid()) // AidList2: every member of the battle
		}
		msgid = 71
	}
	members := slices.Clone(r.members)
	n := len(members)
	sq.mu.Unlock()
	if msgid == 0 {
		s.logger.Printf("SQUARE fishing boss state aid=%d state=%d ignored", s.aid, state)
		return
	}
	if msgid == 71 {
		s.logger.Printf("SQUARE fishing boss start aid=%d members=%d notes=%d maxhp=%d", s.aid, n, len(boss.PlayNote), boss.Boss.MaxHp)
	} else {
		s.logger.Printf("SQUARE fishing boss ready aid=%d ready=%v", s.aid, state == squareBossReadyReq)
	}
	pushMembers(s.logger, members, msgid, body)
}

// bossKick answers cr48: the master removes a member of its lobby before the battle. rc61 goes back to the sender
// (Result 0, or 1 when refused); the removed member and the rest get rc70 with the member erased.
func (s *squareSession) bossKick(frame []byte, data []byte) error {
	target, _ := protobufUintField(data, 1)
	seat := s.boss
	if seat == nil {
		s.logger.Printf("SQUARE fishing boss kick aid=%d ignored no lobby", s.aid)
		return nil
	}
	sq := seat.sq
	sq.mu.Lock()
	r := seat.room
	if r == nil {
		sq.mu.Unlock()
		s.logger.Printf("SQUARE fishing boss kick aid=%d ignored no lobby", s.aid)
		return nil
	}
	var victim *squareBossSeat
	if r.members[0] == seat && !r.fight {
		for _, m := range r.members[1:] {
			if m.aid() == target {
				victim = m
				break
			}
		}
	}
	var rest []*squareBossSeat
	var body []byte
	if victim != nil {
		victim.room = nil
		rest, body = r.removeLocked(victim)
	}
	sq.mu.Unlock()
	if victim == nil {
		s.logger.Printf("SQUARE fishing boss kick refused aid=%d target=%d", s.aid, target)
		return s.reply(frame, 61, pbVar(pbVar(nil, 1, 1), 2, target))
	}
	s.logger.Printf("SQUARE fishing boss kick aid=%d target=%d rest=%d", s.aid, target, len(rest))
	if err := s.reply(frame, 61, pbVar(pbVar(nil, 1, 0), 2, target)); err != nil {
		return err
	}
	pushMembers(s.logger, append(rest, victim), 70, body)
	return nil
}

// bossInvite answers cr35: the master invites the aids in AidList1 to its lobby. Each new invitee gets the invite push
// on its :10123 session (bossInvitePush); an invited aid may accept with cr36 until it joins. There is no reply to cr35.
// bossInvitedList is rc_friend_invite_list_res {AidList1} for cr37 (empty), which the Square invite picker sends on
// open and waits for (NaSquareInviteFriendInviteLayer::MsgProc 0x1d05d48 case 0x305C, boss-mp-addendum 1g): the aids
// the session's lobby already invited, in id order. Outside a lobby the list is empty.
func (s *squareSession) bossInvitedList() []byte {
	var body []byte
	seat := s.boss
	if seat == nil {
		return body
	}
	seat.sq.mu.Lock()
	defer seat.sq.mu.Unlock()
	if seat.room == nil {
		return body
	}
	for _, a := range slices.Sorted(maps.Keys(seat.room.invited)) {
		body = pbVar(body, 1, a)
	}
	return body
}

func (s *squareSession) bossInvite(data []byte) {
	aids, ok := partyAidList(data)
	seat := s.boss
	if seat == nil || !ok {
		s.logger.Printf("SQUARE fishing boss invite aid=%d ignored", s.aid)
		return
	}
	sq := seat.sq
	sq.mu.Lock()
	r := seat.room
	if r == nil || r.fight || r.members[0] != seat {
		sq.mu.Unlock()
		s.logger.Printf("SQUARE fishing boss invite aid=%d ignored not the master of a lobby", s.aid)
		return
	}
	var invitees []uint64
	for _, a := range aids {
		if a == 0 || a == s.aid || slices.ContainsFunc(r.members, func(m *squareBossSeat) bool { return m.aid() == a }) {
			continue
		}
		r.invited[a] = true
		invitees = append(invitees, a)
	}
	sq.mu.Unlock()
	s.logger.Printf("SQUARE fishing boss invite aid=%d invited=%d of=%d", s.aid, len(invitees), len(aids))
	for _, a := range invitees {
		s.bossInvitePush(a)
	}
}

// bossInvitePush raises the invitee's invite popup (popup 33) with one LO push on its :10123 session (boss-mp-addendum
// 1j): op 0x1B, txn 0, UTF-8 JSON. sendInit named the invitee's account, so the session is found by its aid. An
// invitee with no session gets no popup; the line says so.
func (s *squareSession) bossInvitePush(to uint64) {
	master := strconv.FormatUint(s.aid, 10)
	body, err := json.Marshal(map[string]any{
		"catg":     "square",
		"msgid":    master + "-" + strconv.FormatUint(to, 10) + "-" + strconv.FormatInt(economy.NowFunc().UnixNano(), 10),
		"sender":   master,
		"content":  squareAccountName(s.aid) + " invites you to a Boss Raid!",
		"params":   map[string]any{"pType": "SIR"},
		"mapParam": map[string]any{"catg": "square", "aid": master},
	})
	sent := false
	if err == nil {
		sent, err = sessionPush(to, loOpPush, body)
	}
	if err != nil {
		s.logger.Printf("SQUARE fishing boss invite push aid=%d to=%d sent=%v err=%v", s.aid, to, sent, err)
		return
	}
	s.logger.Printf("SQUARE fishing boss invite push aid=%d to=%d sent=%v", s.aid, to, sent)
}

// bossAccept answers cr36: an invitee enters the master's lobby (the master is owner) when it has room, has not
// started and the master invited the invitee; a try is spent as for the banner. The lobby is found by the owner's aid
// in any Square and the invitation by the invitee's aid, so the invitee's session may be in any Square or in none.
// The reply is an rc6 body: theme 28, or a refusal, which has no Result 196609 and so fails the client's enter
// (spec 11.1 [I]).
func (s *squareSession) bossAccept(frame []byte, owner uint64) []byte {
	s.bossLeave()
	sq := s.rooms.bossLobbyOf(owner)
	if sq == nil {
		s.logger.Printf("SQUARE fishing boss accept refused aid=%d owner=%d no lobby", s.aid, owner)
		return pbVar(nil, 1, 0)
	}
	seat := s.bossSeatFor(frame, sq)
	seats := int(economy.ActiveFishBoss().MaxPlayers)
	sq.mu.Lock()
	var r *squareBossRoom
	for _, o := range sq.bosses {
		if o.members[0].aid() == owner {
			r = o
			break
		}
	}
	if r == nil || owner == s.aid || r.fight || len(r.members) >= seats || !r.invited[s.aid] {
		sq.mu.Unlock()
		s.logger.Printf("SQUARE fishing boss accept refused aid=%d owner=%d", s.aid, owner)
		return pbVar(nil, 1, 0)
	}
	left, ok := economy.BossTryStart(strconv.FormatUint(s.aid, 10), economy.NowFunc())
	if !ok {
		sq.mu.Unlock()
		s.logger.Printf("SQUARE fishing boss accept refused aid=%d owner=%d no try left=%d", s.aid, owner, left)
		return pbVar(nil, 1, 0)
	}
	others := r.addLocked(seat)
	delete(r.invited, s.aid) // the invitation is used up: a later leave does not leave it open
	body := pbLen(nil, 2, r.entryLocked(seat))
	sq.mu.Unlock()
	// The accept is the invitee's Square enter (it sends no cr2): its cr10 and relaystart (cr5) follow at once, so the
	// session counts as entered in the lobby's Square, leaving any other Square it was in.
	if s.player != nil && sq != s.room {
		s.close()
	}
	s.room, s.square, s.entered = sq, squaresFile.Get().entry(squareThemeID), true
	s.boss = seat
	s.logger.Printf("SQUARE fishing boss accept aid=%d owner=%d members=%d left=%d", s.aid, owner, len(others)+1, left)
	pushMembers(s.logger, others, 70, body)
	return squareRoomEnter(nil, squareBossThemeID, squareBossMapFileName)
}

// bossSticker answers cr60: a lobby sticker to every member of the session's lobby, the sender included (rc79).
func (s *squareSession) bossSticker(data []byte) {
	index, _ := protobufUintField(data, 2)
	seat := s.boss
	if seat == nil {
		return
	}
	sq := seat.sq
	sq.mu.Lock()
	r := seat.room
	if r == nil {
		sq.mu.Unlock()
		return
	}
	members := slices.Clone(r.members)
	sq.mu.Unlock()
	body := pbVar(pbVar(pbVar(nil, 1, s.aid), 2, 0), 3, index) // SendAid1, EmoticonType2 0, EmoticonIndex3
	pushMembers(s.logger, members, 79, body)
}

// bossBattling reports whether the session's lobby is in a battle that started, and whether that battle is over.
func (s *squareSession) bossBattling() (started, over bool) {
	seat := s.boss
	if seat == nil {
		return false, false
	}
	seat.sq.mu.Lock()
	defer seat.sq.mu.Unlock()
	r := seat.room
	if r == nil || !r.fight {
		return false, false
	}
	return true, r.ended
}

// bossRanks is the battle's members in rank order: by damage, ties by join order.
func bossRanks(final []*squareBossSeat) []*squareBossSeat {
	ranked := slices.Clone(final)
	slices.SortStableFunc(ranked, func(a, b *squareBossSeat) int { return cmp.Compare(b.damage, a.damage) })
	return ranked
}

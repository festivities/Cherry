package lpn

import (
	"encoding/json"
	"maps"
	"slices"
	"strconv"

	"cherry/internal/economy"
)

// handleSquareInvite answers Square friend invite requests (cr35, cr36, cr37). Sessions inside a boss lobby/battle
// (s.boss != nil) and accepts targeting a boss lobby master (bossLobbyOf(owner) != nil) use the existing boss raid
// invite paths; all other Squares use regular Square room invites.
func (s *squareSession) handleSquareInvite(frame []byte, msgid uint16, data []byte) error {
	switch msgid {
	case 37: // cr_friend_invite_list_req -> rc_friend_invite_list_res (46)
		if s.boss != nil {
			s.logger.Printf("SQUARE fishing boss invited list aid=%d", s.aid)
			return s.reply(frame, 46, s.bossInvitedList())
		}
		s.logger.Printf("SQUARE invited list aid=%d", s.aid)
		return s.reply(frame, 46, s.squareInvitedList())
	case 35: // cr_friend_invite_req {AidList1}
		if s.boss != nil {
			s.bossInvite(data)
			return nil
		}
		return s.squareInvite(frame, data)
	case 36: // cr_room_enter_friend_req {FriendAid1, Revision2} -> rc_room_enter_res (6)
		owner, _ := protobufUintField(data, 1)
		if s.rooms.bossLobbyOf(owner) != nil {
			return s.reply(frame, 6, s.bossAccept(frame, owner))
		}
		return s.reply(frame, 6, s.squareAccept(frame, owner))
	}
	return nil
}

// squareInvitedList returns rc_friend_invite_list_res (46) with the sorted AIDs this session has invited to its
// current Square room.
func (s *squareSession) squareInvitedList() []byte {
	if s.room == nil || s.player == nil {
		return nil
	}
	s.room.mu.Lock()
	defer s.room.mu.Unlock()
	var body []byte
	for _, a := range slices.Sorted(maps.Keys(s.room.invites[s.aid])) {
		body = pbVar(body, 1, a)
	}
	return body
}

// squareInvite answers cr35 outside a boss lobby: records valid friend invitees on the inviter's current Square room,
// pushes a :10123 LO "FIR" notification to each invitee, and replies with rc45 (Result=1, InvitedList).
func (s *squareSession) squareInvite(frame []byte, data []byte) error {
	aids, ok := partyAidList(data)
	if !ok || s.player == nil || s.room == nil {
		s.logger.Printf("SQUARE invite aid=%d ignored", s.aid)
		return nil
	}
	if len(aids) > 100 {
		aids = aids[:100]
	}
	// Check friendships outside s.room.mu so store.SocialMu is never nested inside s.room.mu.
	seen := make(map[uint64]bool, len(aids))
	candidates := make([]uint64, 0, len(aids))
	for _, a := range aids {
		if a == 0 || a == s.aid || seen[a] {
			continue
		}
		seen[a] = true
		if partyAccepted(s.aid, a) {
			candidates = append(candidates, a)
		}
	}

	s.room.mu.Lock()
	if s.room.invites == nil {
		s.room.invites = map[uint64]map[uint64]bool{}
	}
	if s.room.invites[s.aid] == nil {
		s.room.invites[s.aid] = map[uint64]bool{}
	}
	var invitees []uint64
	for _, a := range candidates {
		if slices.ContainsFunc(s.room.players, func(p *squarePlayer) bool { return p.aid == a }) {
			continue
		}
		s.room.invites[s.aid][a] = true
		invitees = append(invitees, a)
	}
	roomNo := s.room.number
	s.room.mu.Unlock()

	s.logger.Printf("SQUARE invite aid=%d theme=%d room=%d invited=%d of=%d", s.aid, s.square.Theme, roomNo, len(invitees), len(aids))
	for _, a := range invitees {
		s.squareInvitePush(a)
	}

	reply := pbVar(nil, 1, 1) // Result = true
	for _, a := range invitees {
		reply = pbVar(reply, 2, a) // InvitedList
	}
	return s.reply(frame, 45, reply)
}

// squareInvitePush sends an LO push (op 0x1B, pType "FIR") on the invitee's :10123 session so popup 33 opens.
func (s *squareSession) squareInvitePush(to uint64) {
	inviter := strconv.FormatUint(s.aid, 10)
	squareName := s.square.Name
	if squareName == "" {
		squareName = "the Square"
	}
	body, err := json.Marshal(map[string]any{
		"catg":     "square",
		"msgid":    inviter + "-" + strconv.FormatUint(to, 10) + "-" + strconv.FormatInt(economy.NowFunc().UnixNano(), 10),
		"sender":   inviter,
		"content":  squareAccountName(s.aid) + " invites you to " + squareName + "!",
		"params":   map[string]any{"pType": "FIR"},
		"mapParam": map[string]any{"catg": "square", "aid": inviter},
	})
	sent := false
	if err == nil {
		sent, err = sessionPush(to, loOpPush, body)
	}
	if err != nil {
		s.logger.Printf("SQUARE invite push aid=%d to=%d sent=%v err=%v", s.aid, to, sent, err)
		return
	}
	s.logger.Printf("SQUARE invite push aid=%d to=%d sent=%v", s.aid, to, sent)
}

// findPlayer locates owner in any active Square room, consuming any pending invite for invitee.
func (rs *squareRooms) findPlayer(owner, invitee uint64) (theme, number uint64, invited, found bool) {
	if rs == nil {
		return 0, 0, false, false
	}
	rs.mu.Lock()
	defer rs.mu.Unlock()
	for t, list := range rs.rooms {
		for _, r := range list {
			r.mu.Lock()
			inRoom := slices.ContainsFunc(r.players, func(p *squarePlayer) bool { return p.aid == owner })
			if inRoom {
				if inv := r.invites[owner]; inv != nil && inv[invitee] {
					invited = true
					delete(inv, invitee)
					if len(inv) == 0 {
						delete(r.invites, owner)
					}
				}
				number = r.number
				r.mu.Unlock()
				return t, number, invited, true
			}
			r.mu.Unlock()
		}
	}
	return 0, 0, false, false
}

// squareAccept answers cr36 for a regular Square invite: verifies the friend is in a Square room and that the
// requester was invited or is an accepted friend, then places the requester in the friend's Square and room (falling
// back to the next room with space if full).
func (s *squareSession) squareAccept(frame []byte, owner uint64) []byte {
	if owner == 0 || owner == s.aid {
		s.logger.Printf("SQUARE invite accept refused aid=%d owner=%d", s.aid, owner)
		return pbVar(nil, 1, 0)
	}
	targetTheme, targetNumber, invited, found := s.rooms.findPlayer(owner, s.aid)
	if !found {
		s.logger.Printf("SQUARE invite accept refused aid=%d owner=%d not in square", s.aid, owner)
		return pbVar(nil, 1, squareEnterNotInSquare)
	}
	if !invited && !partyAccepted(s.aid, owner) {
		s.logger.Printf("SQUARE invite accept refused aid=%d owner=%d not friend", s.aid, owner)
		return pbVar(nil, 1, 0)
	}
	s.bossLeave()
	s.stopRacing()
	pickAid := uint64(0)
	if s.player != nil && s.square.Theme == targetTheme && s.roomNo == targetNumber {
		pickAid = s.aid
	}
	number := s.rooms.pick(targetTheme, pickAid, targetNumber, squareRoomCap())
	if number != targetNumber {
		s.logger.Printf("SQUARE invite accept aid=%d room %d full, placed in room %d", s.aid, targetNumber, number)
	}
	if s.player != nil && (s.square.Theme != targetTheme || number != s.roomNo) {
		s.close()
	}
	sq := squaresFile.Get().entry(targetTheme)
	s.roomNo, s.square, s.entered = number, sq, true
	MarkQuest(s.aid, "square")
	s.logger.Printf("SQUARE invite accept aid=%d owner=%d theme=%d room=%d map=%s", s.aid, owner, sq.Theme, number, sq.Map)
	return squareRoomEnter(squareNpcList(sq.Theme), number, sq.Theme, sq.Map)
}

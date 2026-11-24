package lpn

import (
	"strconv"

	"cherry/internal/economy"
	"cherry/internal/store"
)

// Square follow & ride pets (agent 7; spec square-pets-20261124\spec.md).
//
// Both rc_room_relaystart_res (msgid 8) and rc_room_adduser_all (msgid 2) ignore their top-level pet_list fields;
// NaWorldManager::_AddPetToWorld reads SQUARE::_AvatarInfo.pet (field 23, repeated PetBaseInfo) inside PlayerInfo.
// When a player leaves via rc_room_deluser_all (msgid 3), _WaitDeleteWorldAvatar_2_0 deletes all actors whose
// Blackboard "owneraid" (PetFeatureInfo.avatar_id f2) matches aid.
const (
	squareCrPetAction      = 38
	squareCrPetInteraction = 39
	squareCrPetMove        = 40
	squareRcPetAction      = 47
	squareRcPetInteraction = 48
	squareRcPetMove        = 49
	squareCrPetRide        = 92
	squareRcPetRide        = 120
	squarePetRideOK        = 983041 // 0xF0001 (NaPetRideManager::MsgProc 0x2112cc0)
)

// squareFollowPet returns aid's active follow/representative pet (Rep == true), if any.
func squareFollowPet(aid uint64) (store.PetItem, bool) {
	store.AccountsMu.Lock()
	defer store.AccountsMu.Unlock()
	acc := store.AccountByAidLocked(strconv.FormatUint(aid, 10))
	if acc == nil {
		return store.PetItem{}, false
	}
	for _, p := range acc.Pets {
		if p.Rep && p.ID > 0 && p.Cd != "" {
			return p, true
		}
	}
	return store.PetItem{}, false
}

// squarePetBaseInfo encodes one SQUARE::PetBaseInfo (f1 PetFeatureInfo, f2 pet_ride_state).
func squarePetBaseInfo(owner uint64, p store.PetItem, rideState uint64) []byte {
	f := pbVar(pbVar(nil, 1, uint64(p.ID)), 2, owner)
	f = pbLen(f, 3, []byte(p.Name))
	f = pbLen(f, 4, []byte(p.Cd))
	f = pbLen(f, 5, []byte(economy.PetCategoryCode(p.Cd)))
	return pbVar(pbLen(nil, 1, f), 2, rideState)
}

// squareFollowPetBaseInfo returns the encoded PetBaseInfo for aid's follow pet, or nil if none is set.
func squareFollowPetBaseInfo(aid, ridePetID, rideState uint64) []byte {
	p, ok := squareFollowPet(aid)
	if !ok {
		return nil
	}
	rs := uint64(0)
	if rideState != 0 && uint64(p.ID) == ridePetID {
		rs = rideState
	}
	return squarePetBaseInfo(aid, p, rs)
}

func (s *squareSession) handleSquarePet(frame []byte, msgid uint16, data []byte) error {
	switch msgid {
	case squareCrPetMove: // cr_pet_move_req (f1 repeated PetMoveInfo) -> rc_pet_move_all (49: f1 owner_aid, f2 repeated PetMoveInfo)
		if len(data) > maxRelayPayload {
			return nil
		}
		var moves [][]byte
		ok := pbScan(data, func(f, _ uint64, d []byte) {
			if f == 1 && len(d) > 0 {
				moves = append(moves, d)
			}
		})
		if !ok || len(moves) == 0 {
			return nil
		}
		body := pbVar(nil, 1, s.aid)
		for _, m := range moves {
			body = pbLen(body, 2, m)
		}
		s.room.broadcast(s.logger, s.player, squareRcPetMove, body)
		return nil

	case squareCrPetAction: // cr_pet_action_req (f1 pid, f2 PlayerAction) -> rc_pet_action_all (47: f1 owner_aid, f2 pid, f3 PlayerAction)
		var pid uint64
		var action []byte
		ok := pbScan(data, func(f, v uint64, d []byte) {
			switch f {
			case 1:
				pid = v
			case 2:
				action = d
			}
		})
		if !ok || pid == 0 || len(action) == 0 || len(action) > maxRelayPayload {
			return nil
		}
		body := pbLen(pbVar(pbVar(nil, 1, s.aid), 2, pid), 3, action)
		s.room.broadcast(s.logger, s.player, squareRcPetAction, body)
		return nil

	case squareCrPetInteraction: // cr_pet_interaction_req (f1 pid, f2 interaction, f3 owner_aid) -> rc_pet_interaction_all (48)
		var pid, ownerAID uint64
		var interaction []byte
		ok := pbScan(data, func(f, v uint64, d []byte) {
			switch f {
			case 1:
				pid = v
			case 2:
				interaction = d
			case 3:
				ownerAID = v
			}
		})
		if !ok || pid == 0 || ownerAID == 0 || len(interaction) == 0 || len(interaction) > maxRelayPayload {
			return nil
		}
		body := pbLen(pbVar(pbVar(nil, 1, ownerAID), 2, pid), 3, interaction)
		s.room.broadcast(s.logger, nil, squareRcPetInteraction, body)
		return nil

	case squareCrPetRide: // cr_pet_ride_req (f1 pet_id, f2 pet_ride_state) -> rc_pet_ride_res (120) to all
		var petID, rideState uint64
		if !pbScan(data, func(f, v uint64, _ []byte) {
			switch f {
			case 1:
				petID = v
			case 2:
				rideState = v
			}
		}) {
			return nil
		}
		if rideState != 0 {
			rideState = 1
			p, ok := squareFollowPet(s.aid)
			if !ok || uint64(p.ID) != petID || economy.PetCategoryCode(p.Cd) != "URIDE" {
				s.logger.Printf("SQUARE pet-ride refused aid=%d pet=%d", s.aid, petID)
				return s.reply(frame, squareRcPetRide, pbVar(pbVar(pbVar(pbVar(nil, 1, 0), 2, s.aid), 3, petID), 4, 0))
			}
		}
		newAvatar := squareAvatarInfoWithRide(s.aid, petID, rideState)
		s.room.mu.Lock()
		if rideState != 0 {
			s.player.ridePetID, s.player.rideState = petID, rideState
			s.player.seat = nil
		} else {
			s.player.ridePetID, s.player.rideState = 0, 0
		}
		s.player.avatar = newAvatar
		s.room.mu.Unlock()
		s.logger.Printf("SQUARE pet-ride aid=%d pet=%d state=%d", s.aid, petID, rideState)
		body := pbVar(pbVar(pbVar(pbVar(nil, 1, squarePetRideOK), 2, s.aid), 3, petID), 4, rideState)
		s.room.broadcast(s.logger, nil, squareRcPetRide, body)
		return nil
	}
	return nil
}

package lpn

import (
	"log"
	"strconv"

	"cherry/internal/store"
)

// Garden enter validation (GARDEN namespace; spec topic 6): the footprint visit popup and My Room send
// ch_garden_enter_validate_req (idx42, owner_aid#1). The reply is hc_garden_enter_validate_res (idx49):
// owner_aid#1, is_brand_avatar#2, is_valid_garden#3 and result#4, all varints. The client's indicator waits for
// it and has no timeout. The check runs before the visit, so it is not gated on relay-start or the room tunnel.
const (
	gardenEnterValidateReq = 42
	gardenEnterValidateRes = 49
)

// gardenValidResult is the result of a valid enter (spec topic 6): the client changes Garden on is_valid_garden
// and shows nothing for this result.
const gardenValidResult = 197420

// gardenInvalidResult is any result other than gardenValidResult, so the client shows its message 10053 for a
// refused enter (spec topic 6). The value 1 is a choice [I]; the spec gives none.
const gardenInvalidResult = 1

// gardenOwnerExists reports whether owner is an account with an avatar. Aid 0 is an account without one.
func gardenOwnerExists(owner uint64) bool {
	if owner == 0 {
		return false
	}
	store.AccountsMu.Lock()
	defer store.AccountsMu.Unlock()
	return store.AccountByAidLocked(strconv.FormatUint(owner, 10)) != nil
}

// gardenValidateBody is hc_garden_enter_validate_res. is_brand_avatar is always false.
func gardenValidateBody(owner uint64, valid bool) []byte {
	result := uint64(gardenInvalidResult)
	if valid {
		result = gardenValidResult
	}
	body := pbVar(nil, 1, owner)
	body = pbVar(body, 2, 0)
	body = pbVar(body, 3, squareBool(valid))
	return pbVar(body, 4, result)
}

// gardenValidateHandle answers one idx42 request on the tunnel it came in on.
func gardenValidateHandle(conn *lockedConn, frame []byte, logger *log.Logger) error {
	owner, _ := protobufUintField(frame[12:], 1)
	valid := gardenOwnerExists(owner)
	logger.Printf("GARDEN idx42 validate owner=%d valid=%v", owner, valid)
	_, err := conn.Write(squareFrame(frame[1], frame[2:10], gardenEnterValidateRes, gardenValidateBody(owner, valid)))
	return err
}

package lpn

import (
	"encoding/binary"
	"encoding/json"
	"log"
	"strconv"
	"sync"
	"time"

	"cherry/internal/economy"
)

// Garden heart button over the host avatar (Economy Phase D slice 3 follow-up). The client shows the
// button for TLUP quest-action Id 101 and hides it for Id 102. Cherry pushes either one as an
// unsolicited TLUP D frame (agent 5, msgid 5 = tc_questevent_push) on the client's own TLUP session.
// Wire notes: %TEMP%\opencode\cherry\phaseDE-20261022\heart-button.md.
const (
	heartButtonShowID = 101
	heartButtonHideID = 102
	tlupQuestEventMsg = 5
)

// tlupQuestActionBody encodes tc_questevent_push {Action #1 QuestActionData {Command #1 {Id #2,
// Param #3 ParamData {Name #1 "item", Strvalue #4 "HEART"}}}}.
func tlupQuestActionBody(id uint64) []byte {
	param := pbLen(pbLen(nil, 1, []byte("item")), 4, []byte("HEART"))
	command := pbLen(pbVar(nil, 2, id), 3, param)
	return pbLen(nil, 1, pbLen(nil, 1, command))
}

// tlupDFrame is one TLUP D frame with the given session, msgid and body: type 0, agent 5 and
// packetId 0 (the unsolicited path; a reply echoes the request's packetId).
func tlupDFrame(session [8]byte, msgid uint16, body []byte) []byte {
	f := make([]byte, 22+len(body))
	binary.BigEndian.PutUint32(f, uint32(18+len(body)))
	f[4] = 'D'
	copy(f[6:14], session[:])
	f[14] = 5
	binary.BigEndian.PutUint16(f[20:22], msgid)
	copy(f[22:], body)
	return f
}

// heartButtonPush sends the show (101) or hide (102) quest action to viewer's Garden connection.
func heartButtonPush(conn *lockedConn, session [8]byte, id, viewer, owner uint64, logger *log.Logger) error {
	if _, err := conn.Write(tlupDFrame(session, tlupQuestEventMsg, tlupQuestActionBody(id))); err != nil {
		return err
	}
	verb := "show"
	if id == heartButtonHideID {
		verb = "hide"
	}
	logger.Printf("cherry: heart button %s aid=%d owner=%d", verb, viewer, owner)
	return nil
}

// tlupQuestCommandPetStroke is the QuestActionCommand Id of a petting's answer (NaQuestManager::HandleQuestCommand).
const tlupQuestCommandPetStroke = 112

// tlupParamInt and tlupParamStr encode one ParamData: Name #1, Intvalue #2 or Strvalue #4.
func tlupParamInt(name string, v int64) []byte {
	return pbVar(pbLen(nil, 1, []byte(name)), 2, uint64(v))
}

func tlupParamStr(name, v string) []byte {
	return pbLen(pbLen(nil, 1, []byte(name)), 4, []byte(v))
}

// tlupPetStrokeBody encodes the answer to a petting as tc_questevent_push {Action #1 {Command #1 {Id 112, Param #3...}}}.
// Every required param is sent (spec 3 section 1): a missing one makes the client drop the answer. The gift params
// follow only when the petting adopted the pet (levelUp 1).
func tlupPetStrokeBody(r economy.MiniPetStrokeReply) []byte {
	params := [][]byte{
		tlupParamInt("petId", r.PetID), tlupParamInt("exp", r.Exp), tlupParamInt("maxExp", r.MaxExp),
		tlupParamInt("gainedExp", r.Gained), tlupParamInt("paidTouch", 0), tlupParamInt("petTouchable", 0),
		tlupParamInt("finalLevelYn", boolInt(r.Final)), tlupParamInt("levelUp", boolInt(r.LevelUp)),
	}
	if g := r.Gift; g != nil {
		params = append(params, tlupParamInt("raceId", 1), tlupParamStr("giftItemCode", g.Item),
			tlupParamStr("giftItemName", g.ItemName), tlupParamStr("giftItemGrade", g.Grade),
			tlupParamStr("petSkinCode", g.Pet), tlupParamStr("petName", g.PetName), tlupParamInt("level", 1))
	}
	command := pbVar(nil, 2, tlupQuestCommandPetStroke)
	for _, p := range params {
		command = pbLen(command, 3, p)
	}
	return pbLen(nil, 1, pbLen(nil, 1, command))
}

func boolInt(b bool) int64 {
	if b {
		return 1
	}
	return 0
}

// petStrokePush sends the answer to a petting on the client's own TLUP session (an unsolicited D frame, msgid 5).
func petStrokePush(conn *lockedConn, session [8]byte, reply economy.MiniPetStrokeReply, logger *log.Logger) error {
	if _, err := conn.Write(tlupDFrame(session, tlupQuestEventMsg, tlupPetStrokeBody(reply))); err != nil {
		return err
	}
	logger.Printf("cherry: minipet stroke pet=%d exp=%d/%d adopted=%t", reply.PetID, reply.Exp, reply.MaxExp, reply.Gift != nil)
	return nil
}

// tlupQuestCommandDetail is QuestActionCommand Id 157 (0x9D, NaQuestManager::HandleQuestDetailInfo)
// for Popup 43 (NaFashionBuffPopupLayer, QuestId 71).
const tlupQuestCommandDetail = 157

// tlupQuestDetailBody encodes TLUP::tc_questevent_push (msgid 5) with QuestActionCommand id=157
// and ParamData entries quest_id (int_val) and quest (JSON array string). The command id and JSON
// schema are original client protocol; the showcase item ("CUAH0067V") and single-entry payload are Cherry-made.
func tlupQuestDetailBody(questID int64, now time.Time) []byte {
	nextResetSec := economy.QuestResetMillis(now) / 1000
	items := []struct {
		ID        int    `json:"id"`
		Status    bool   `json:"status"`
		StartDate int64  `json:"start_date"`
		EndDate   int64  `json:"end_date"`
		ItemGrade string `json:"item_grade"`
		ItemCode  string `json:"item_code"`
		ShopCode  string `json:"shop_code"`
		Link      string `json:"link"`
	}{{
		ID:        1,
		Status:    false,
		StartDate: 0,
		EndDate:   nextResetSec,
		ItemGrade: "NORMAL",
		ItemCode:  "CUAH0067V",
		ShopCode:  "90001",
		Link:      "SQUARE",
	}}
	rawJSON, _ := json.Marshal(items)
	command := pbVar(nil, 2, tlupQuestCommandDetail)
	command = pbLen(command, 3, tlupParamInt("quest_id", questID))
	command = pbLen(command, 3, tlupParamStr("quest", string(rawJSON)))
	return pbLen(nil, 1, pbLen(nil, 1, command))
}

// tlupQuestDetailPush sends QuestActionCommand 157 on the client's TLUP session (msgid 5).
func tlupQuestDetailPush(conn *lockedConn, session [8]byte, aid uint64, questID int64, now time.Time, logger *log.Logger) error {
	if _, err := conn.Write(tlupDFrame(session, tlupQuestEventMsg, tlupQuestDetailBody(questID, now))); err != nil {
		return err
	}
	logger.Printf("cherry: tlup quest detail push aid=%d quest_id=%d", aid, questID)
	return nil
}

// heartButtonShow shows the button for viewer's visit to owner's My Room while viewer may still send
// a heart there. The observer calls it on the client's quest-list request after the room load.
// HeartReceivable rules out viewer's own room.
func heartButtonShow(conn *lockedConn, session [8]byte, viewer, owner uint64, now time.Time, logger *log.Logger) error {
	v, o := strconv.FormatUint(viewer, 10), strconv.FormatUint(owner, 10)
	if !economy.HaveHeart(v, now) || !economy.HeartReceivableAt(v, o, now) {
		return nil
	}
	return heartButtonPush(conn, session, heartButtonShowID, viewer, owner, logger)
}

// roomVisits maps each viewer to the My Room it loaded over room/enter (owner) and whether its heart
// button show still waits for the viewer's next TLUP quest-list request. The stock heart button
// belongs to a visit to a friend's My Room, not to the Garden.
// ponytail: no expiry; a visit ends at the viewer's next own-room or Garden load, or a counted heart
// to the owner. Add a TTL if a stale show ever lands in another scene.
var roomVisits = struct {
	sync.Mutex
	m map[uint64]roomVisit
}{m: map[uint64]roomVisit{}}

type roomVisit struct {
	owner uint64
	show  bool
}

// NoteRoomVisit records that viewer loaded owner's My Room and queues the button show for its next
// quest-list request. An own room only clears the visit.
func NoteRoomVisit(viewer, owner uint64) {
	if owner == viewer {
		ClearRoomVisit(viewer)
		return
	}
	roomVisits.Lock()
	roomVisits.m[viewer] = roomVisit{owner: owner, show: true}
	roomVisits.Unlock()
}

// ClearRoomVisit forgets viewer's room visit when its own My Room or a Garden is loaded.
func ClearRoomVisit(viewer uint64) {
	roomVisits.Lock()
	delete(roomVisits.m, viewer)
	roomVisits.Unlock()
}

// takeRoomVisitShow returns the owner whose button viewer's quest-list request should show, once.
func takeRoomVisitShow(viewer uint64) (uint64, bool) {
	roomVisits.Lock()
	defer roomVisits.Unlock()
	v, ok := roomVisits.m[viewer]
	if !ok || !v.show {
		return 0, false
	}
	v.show = false
	roomVisits.m[viewer] = v
	return v.owner, true
}

// takeRoomVisitHide reports whether a counted heart from viewer to target hides the button: target is
// the room viewer is visiting. The visit ends with it.
func takeRoomVisitHide(viewer, target uint64) bool {
	roomVisits.Lock()
	defer roomVisits.Unlock()
	v, ok := roomVisits.m[viewer]
	if !ok || v.owner != target {
		return false
	}
	delete(roomVisits.m, viewer)
	return true
}

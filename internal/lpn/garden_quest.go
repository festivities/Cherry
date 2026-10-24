package lpn

import (
	"log"
	"slices"
	"strconv"
	"sync"
	"time"

	"cherry/internal/economy"
	"cherry/internal/store"
)

// Garden daily quests (Economy Phase D slice 1). Requests: list 15, info 16, accept 17,
// complete 18, reject 19. Replies: list 21, info 22, accept 24, complete 25, reject 26.
// Field numbers and required bits come from quests.md section 10.
const (
	gardenQuestGemView    = 4  // ItemViewType/ViewType of the Gem icon [I]; 3 draws the rainbow C coin (device), quests.md 10.4
	gardenQuestRewardType = 11 // EVENT_REWARD.RewardType that opens the reward popup [I]
)

// gardenQuestMsg reports whether msgid is one of the quest requests answered here.
func gardenQuestMsg(msgid uint16) bool { return msgid >= 15 && msgid <= 19 }

// gardenQuestRow returns def's status and counts on the Garden list at now. Status 1 is in
// progress, 3 claimable, 2 claimed. The hearts row counts hearts sent today out of the base and
// shows claimed once none are left: HeartSend already paid it, so it is never claimable.
func gardenQuestRow(def economy.QuestDef, rec store.QuestDay, sid string, now time.Time) (status, current, complete uint64) {
	if def.Display {
		left, ok := economy.HeartsLeft(sid, now)
		if !ok {
			left = economy.HeartBase()
		}
		complete = uint64(economy.HeartBase())
		current = uint64(economy.HeartBase() - left)
		if left == 0 {
			return 2, complete, complete
		}
		return 1, current, complete
	}
	status, current, complete = 1, 0, 1
	if slices.Contains(rec.Done, def.ID) {
		status, current = 3, 1
	}
	if slices.Contains(rec.Claimed, def.ID) {
		status, current = 2, 1
	}
	return status, current, complete
}

// gardenQuestInfo is one QUEST::QuestInfo row with the counts from gardenQuestRow.
func gardenQuestInfo(def economy.QuestDef, status, current, complete uint64) []byte {
	detail := pbVar(nil, 1, 1)          // QuestDetail.Index
	detail = pbVar(detail, 3, current)  // CurrentCount
	detail = pbVar(detail, 4, complete) // CompleteCount
	reward := pbVar(nil, 1, gardenQuestGemView)
	reward = pbVar(reward, 3, uint64(def.Gems)) // ItemCount, the amount the cell shows
	info := pbVar(nil, 1, uint64(def.ID))
	info = pbLen(info, 2, []byte(def.Name))
	info = pbLen(info, 5, detail)
	info = pbLen(info, 6, reward)
	info = pbVar(info, 7, status)
	return pbLen(info, 12, []byte(def.Desc))
}

// gardenQuestList is hc_quest_list_res (21): every quest of the catalog at now (daily rows, then the
// season's event quests) in RegularQuestInfo.
func gardenQuestList(sid string, now time.Time) []byte {
	rec := economy.QuestProgress(sid, now)
	body := pbVar(nil, 1, 0) // error_code, required
	claimed := uint64(0)
	catalog := economy.QuestCatalog(now)
	for _, def := range catalog {
		status, current, complete := gardenQuestRow(def, rec, sid, now)
		if status == 2 {
			claimed++
		}
		body = pbLen(body, 2, gardenQuestInfo(def, status, current, complete))
	}
	body = pbVar(body, 10, claimed)                               // TodayQuestAmount
	body = pbVar(body, 11, uint64(len(catalog)))                  // MaxTodayQuestAmount
	body = pbVar(body, 12, uint64(economy.QuestResetMillis(now))) // ResetTimestamp, ms
	return pbVar(body, 13, 1)                                     // TutorialCleared
}

// gardenQuestReward is one QuestRewards entry of hc_quest_complete_res (25): a reward
// popup row with one Gem coin.
func gardenQuestReward(gems int64) []byte {
	item := pbVar(nil, 1, gardenQuestGemView)
	item = pbVar(item, 4, uint64(gems)) // ViewCount
	reward := pbVar(nil, 1, gardenQuestRewardType)
	return pbLen(reward, 6, item) // RewardItems
}

// gardenQuestReply answers one quest request and returns the reply msgid and body. The
// QuestId is field 1 of every request that carries one. Accept and reject change nothing.
func gardenQuestReply(msgid uint16, aid uint64, data []byte, now time.Time, logger *log.Logger) (uint16, []byte) {
	sid := strconv.FormatUint(aid, 10)
	id, _ := protobufUintField(data, 1)
	switch msgid {
	case 15:
		body := gardenQuestList(sid, now)
		logger.Printf("garden-quest list aid=%d id=- result=0", aid)
		return 21, body
	case 16:
		def, ok := economy.QuestByID(int64(id), now)
		body := pbVar(nil, 1, id)
		if !ok {
			body = pbVar(body, 2, 1)
			body = pbLen(body, 3, nil) // QuestInfo is required even on an error
			logger.Printf("garden-quest info aid=%d id=%d result=1", aid, id)
			return 22, body
		}
		body = pbVar(body, 2, 0)
		status, current, complete := gardenQuestRow(def, economy.QuestProgress(sid, now), sid, now)
		body = pbLen(body, 3, gardenQuestInfo(def, status, current, complete))
		logger.Printf("garden-quest info aid=%d id=%d result=0", aid, id)
		return 22, body
	case 17, 19:
		reply, name := uint16(24), "accept"
		if msgid == 19 {
			reply, name = 26, "reject"
		}
		logger.Printf("garden-quest %s aid=%d id=%d result=0", name, aid, id)
		return reply, pbVar(pbVar(nil, 1, id), 2, 0)
	case 18:
		gems, code := economy.ClaimQuest(sid, int64(id), now)
		body := pbVar(pbVar(nil, 1, id), 2, uint64(code))
		if code == 0 {
			body = pbLen(body, 4, gardenQuestReward(gems))
		}
		logger.Printf("garden-quest complete aid=%d id=%d result=%d", aid, id, code)
		return 25, body
	}
	return 0, nil
}

// gardenQuestHandle answers one Garden quest request on the relayed tunnel.
func gardenQuestHandle(conn *lockedConn, frame []byte, msgid uint16, aid uint64, logger *log.Logger) error {
	reply, body := gardenQuestReply(msgid, aid, frame[12:], economy.NowFunc(), logger)
	_, err := conn.Write(squareFrame(frame[1], frame[2:10], reply, body))
	return err
}

// questNotices holds completion notices per aid until that player's next Garden relay-start,
// so a notice survives a reconnect and Square/diary completions are announced on return.
var questNotices = struct {
	sync.Mutex
	m map[uint64][]economy.QuestDef
}{m: map[uint64][]economy.QuestDef{}}

// MarkQuest records one progress key for aid (Garden visit, Square enter, diary post, gathering,
// Atelier make, Square catch: the economy.questTriggers keys) and queues a completion notice when it
// completed the quest.
func MarkQuest(aid uint64, key string) {
	def, done := economy.MarkQuest(strconv.FormatUint(aid, 10), key, economy.NowFunc())
	if done {
		queueQuestNotice(aid, def)
	}
}

// queueQuestNotice holds one completion notice for aid's next Garden relay-start.
func queueQuestNotice(aid uint64, def economy.QuestDef) {
	questNotices.Lock()
	questNotices.m[aid] = append(questNotices.m[aid], def)
	questNotices.Unlock()
}

// heartQuestNotice queues the hearts row's notice after a counted heart, once none are left today.
// HeartSend already credited the Gems.
// ponytail: HeartsLeft is read after the send, so two connections of one sender could queue it
// twice. Exact fix: have HeartSend return the completion flag.
func heartQuestNotice(aid uint64, now time.Time) {
	left, ok := economy.HeartsLeft(strconv.FormatUint(aid, 10), now)
	if def := economy.HeartQuest(); ok && left == 0 {
		queueQuestNotice(aid, def)
	}
}

// takeQuestNotices returns and clears aid's queued notices.
func takeQuestNotices(aid uint64) []economy.QuestDef {
	questNotices.Lock()
	defer questNotices.Unlock()
	defs := questNotices.m[aid]
	delete(questNotices.m, aid)
	return defs
}

// gardenQuestVisit marks the Garden visit quest for a room-enter: the viewer's own Garden or
// a friend's.
func gardenQuestVisit(viewer, owner uint64) {
	if viewer == 0 {
		return
	}
	key := "garden-visit"
	if owner == viewer {
		key = "garden-own"
	}
	MarkQuest(viewer, key)
}

// gardenQuestPush shows one completion notice (hc_quest_complete_push 50) on the viewer's
// Garden connection. Send it only after relay-start; the client lists quests itself.
func gardenQuestPush(conn *lockedConn, frame []byte, def economy.QuestDef, logger *log.Logger) error {
	notice := pbVar(nil, 1, uint64(def.ID))
	notice = pbLen(notice, 2, []byte(def.Name+" complete: +"+strconv.FormatInt(def.Gems, 10)+" Gems"))
	_, err := conn.Write(squareFrame(frame[1], frame[2:10], 50, notice))
	logger.Printf("garden-quest push id=%d msgid=50", def.ID)
	return err
}

// gardenQuestNotify sends the queued completion notices, then an unsolicited
// hc_quest_call_npc_res (28, ErrorCode 0): its handler calls NaMoneySpotManager::SyncMoneyInfo,
// which refetches coin/balance so the credited Gems reach the client without a scene change
// (quest-gaps.txt Q1; it only closes the NPC-call popup if one is open).
func gardenQuestNotify(conn *lockedConn, frame []byte, defs []economy.QuestDef, logger *log.Logger) error {
	if len(defs) == 0 {
		return nil
	}
	for _, def := range defs {
		if err := gardenQuestPush(conn, frame, def, logger); err != nil {
			return err
		}
	}
	_, err := conn.Write(squareFrame(frame[1], frame[2:10], 28, pbVar(nil, 2, 0)))
	logger.Printf("garden-quest money-sync msgid=28")
	return err
}

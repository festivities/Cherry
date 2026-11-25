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

// Garden quests (Tasks, Requests, Episode). Requests: list 15, info 16, accept 17,
// complete 18, reject 19, pick_npc 20, info_pick_npc 21, call_npc 22.
// Replies: list 21, info 22, info_pick_npc 23, accept 24, complete 25, reject 26, pick_npc 27, call_npc 28.
// Field numbers and required bits come from quests-tabs-20261125/spec.md.
const (
	gardenQuestCashView   = 3  // ItemViewType/ViewType of the rainbow C coin (device), quests.md 10.4
	gardenQuestGemView    = 4  // ItemViewType/ViewType of the Gem icon [I]
	gardenQuestRewardType = 11 // EVENT_REWARD.RewardType that opens the reward popup [I]
	gardenQuestCallGem    = 1  // charge_call_type / refresh_type: Gem (nonzero = Gem, 0 = Cash)
)

// gardenQuestMsg reports whether msgid is one of the quest requests answered here.
func gardenQuestMsg(msgid uint16) bool { return msgid >= 15 && msgid <= 22 }

// gardenQuestRow returns def's status and counts on the Garden list at now. Status 1 is in
// progress, 3 claimable, 2 claimed. The hearts row counts hearts sent today out of the quest count and
// shows claimed once that many are sent: HeartSend already paid it, so it is never claimable.
func gardenQuestRow(def economy.QuestDef, rec store.QuestDay, sid string, now time.Time) economy.EpisodeRow {
	if def.Display {
		sent, _ := economy.HeartsSentToday(sid, now)
		goal := economy.HeartQuestCount()
		complete := uint64(goal)
		current := uint64(min(sent, goal))
		status := uint64(1)
		if sent >= goal {
			status = 2
		}
		return economy.EpisodeRow{
			QuestRow: economy.QuestRow{
				ID: def.ID, Name: def.Name, Desc: def.Desc, Icon: economy.QuestIcon(def),
				Gems: def.Gems, Cash: def.Cash, Status: status, Current: current, Complete: complete,
			},
		}
	}
	status, current, complete := uint64(1), uint64(0), uint64(1)
	if slices.Contains(rec.Done, def.ID) {
		status, current = 3, 1
	}
	if slices.Contains(rec.Claimed, def.ID) {
		status, current = 2, 1
	}
	return economy.EpisodeRow{
		QuestRow: economy.QuestRow{
			ID: def.ID, Name: def.Name, Desc: def.Desc, Icon: economy.QuestIcon(def),
			Gems: def.Gems, Cash: def.Cash, Status: status, Current: current, Complete: complete,
		},
	}
}

// gardenQuestInfo is one QUEST::QuestInfo row.
func gardenQuestInfo(row economy.EpisodeRow) []byte {
	icon := row.Icon
	if icon == "" {
		icon = "cherry/quest/common_quest_today_quest_icon.png"
	}
	itemInfo := pbVar(nil, 1, 15) // item_view_type = 15 (.png + (i) item preview popup)
	itemInfo = pbLen(itemInfo, 2, []byte(icon))
	if row.Name != "" {
		itemInfo = pbLen(itemInfo, 6, []byte(row.Name))
	}
	if row.Desc != "" {
		itemInfo = pbLen(itemInfo, 7, []byte(row.Desc))
	}
	detail := pbVar(nil, 1, 1)              // QuestDetail.Index
	detail = pbLen(detail, 2, itemInfo)     // QuestDetail.item_info
	detail = pbVar(detail, 3, row.Current)  // CurrentCount
	detail = pbVar(detail, 4, row.Complete) // CompleteCount
	info := pbVar(nil, 1, uint64(row.ID))
	info = pbLen(info, 2, []byte(row.Name))
	if row.Locked && row.PreReqTitle != "" {
		preIcon := row.PreReqIcon
		if preIcon == "" {
			preIcon = "cherry/quest/common_quest_today_quest_icon.png"
		}
		pre := pbLen(nil, 1, []byte(row.PreReqTitle))
		pre = pbVar(pre, 2, row.PreReqCur)
		pre = pbVar(pre, 3, row.PreReqMax)
		pre = pbLen(pre, 4, []byte(preIcon))
		info = pbLen(info, 3, pre)
	}
	info = pbLen(info, 5, detail)
	if row.Gems > 0 || row.Cash == 0 {
		reward := pbVar(nil, 1, gardenQuestGemView)
		reward = pbVar(reward, 3, uint64(row.Gems)) // ItemCount, the amount the cell shows
		reward = pbLen(reward, 6, []byte("Gems"))
		reward = pbLen(reward, 7, []byte("Quest reward"))
		info = pbLen(info, 6, reward)
	}
	if row.Cash > 0 {
		reward := pbVar(nil, 1, gardenQuestCashView)
		reward = pbVar(reward, 3, uint64(row.Cash))
		reward = pbLen(reward, 6, []byte("Cash"))
		reward = pbLen(reward, 7, []byte("Quest reward"))
		info = pbLen(info, 6, reward)
	}
	info = pbVar(info, 7, row.Status)
	if row.NPCIcon != "" {
		info = pbLen(info, 9, []byte(row.NPCIcon))
	}
	return pbLen(info, 12, []byte(row.Desc))
}

// gardenQuestList is hc_quest_list_res (21): Tasks (#2), Requests (#3), Episode (#4),
// Neighbor Invite fields (#5-#9), daily counter (#10-#11), reset timestamp (#12), tutorial_cleared (#13).
func gardenQuestList(sid string, now time.Time) []byte {
	rec := economy.QuestProgress(sid, now)
	body := pbVar(nil, 1, 0) // error_code, required
	for _, def := range economy.QuestCatalog(now) {
		body = pbLen(body, 2, gardenQuestInfo(gardenQuestRow(def, rec, sid, now)))
	}
	reqView := economy.RequestList(sid, now)
	for _, row := range reqView.Rows {
		body = pbLen(body, 3, gardenQuestInfo(economy.EpisodeRow{QuestRow: row}))
	}
	for _, row := range economy.EpisodeList(sid) {
		body = pbLen(body, 4, gardenQuestInfo(row))
	}
	resetMillis := uint64(economy.QuestResetMillis(now))
	body = pbVar(body, 5, uint64(reqView.FreeCallsLeft)) // free_call_count
	body = pbVar(body, 6, uint64(reqView.MaxFreeCalls))  // max_free_call_count
	body = pbVar(body, 7, gardenQuestCallGem)            // charge_call_type (Gem)
	body = pbVar(body, 8, uint64(reqView.CallGems))      // charge_call_price
	if reqView.CanInvite {
		body = pbVar(body, 9, resetMillis) // next_call_timestamp, ms
	} else {
		body = pbVar(body, 9, 0)
	}
	body = pbVar(body, 10, uint64(reqView.TodayDone)) // today_quest_amount
	body = pbVar(body, 11, uint64(reqView.MaxDaily))  // max_today_quest_amount
	body = pbVar(body, 12, resetMillis)               // reset_timestamp, ms
	return pbVar(body, 13, 1)                         // tutorial_cleared
}

// gardenQuestReward is one QuestRewards entry of hc_quest_complete_res (25): a reward
// popup row with Gem and/or Cash items.
func gardenQuestReward(gems, cash int64) []byte {
	reward := pbVar(nil, 1, gardenQuestRewardType)
	reward = pbLen(reward, 5, []byte("You got rewards!"))
	if gems > 0 || cash == 0 {
		item := pbVar(nil, 1, gardenQuestGemView)
		item = pbLen(item, 3, []byte("Gems"))
		item = pbVar(item, 4, uint64(gems)) // ViewCount
		reward = pbLen(reward, 6, item)     // RewardItems
	}
	if cash > 0 {
		item := pbVar(nil, 1, gardenQuestCashView)
		item = pbLen(item, 3, []byte("Cash"))
		item = pbVar(item, 4, uint64(cash)) // ViewCount
		reward = pbLen(reward, 6, item)     // RewardItems
	}
	return pbLen(reward, 7, []byte("Quest Complete!"))
}

// gardenQuestReply answers one quest request and returns the reply msgid and body.
func gardenQuestReply(msgid uint16, aid uint64, data []byte, now time.Time, logger *log.Logger) (uint16, []byte) {
	sid := strconv.FormatUint(aid, 10)
	id, _ := protobufUintField(data, 1)
	switch msgid {
	case 15:
		body := gardenQuestList(sid, now)
		logger.Printf("garden-quest list aid=%d id=- result=0", aid)
		return 21, body
	case 16:
		row, ok := economy.QuestDetailByID(sid, int64(id), now)
		body := pbVar(nil, 1, id)
		if !ok {
			body = pbVar(body, 2, 1)
			body = pbLen(body, 3, nil) // QuestInfo is required even on an error
			logger.Printf("garden-quest info aid=%d id=%d result=1", aid, id)
			return 22, body
		}
		body = pbVar(body, 2, 0)
		body = pbLen(body, 3, gardenQuestInfo(row))
		logger.Printf("garden-quest info aid=%d id=%d result=0", aid, id)
		return 22, body
	case 17:
		code := economy.AcceptQuest(sid, int64(id), now)
		logger.Printf("garden-quest accept aid=%d id=%d result=%d", aid, id, code)
		return 24, pbVar(pbVar(nil, 1, id), 2, uint64(code))
	case 18:
		gems, cash, code := economy.ClaimQuestReward(sid, int64(id), now)
		body := pbVar(pbVar(nil, 1, id), 2, uint64(code))
		if code == 0 {
			body = pbLen(body, 4, gardenQuestReward(gems, cash))
		}
		logger.Printf("garden-quest complete aid=%d id=%d result=%d", aid, id, code)
		return 25, body
	case 19:
		code := economy.RejectQuest(sid, int64(id), now)
		logger.Printf("garden-quest reject aid=%d id=%d result=%d", aid, id, code)
		return 26, pbVar(pbVar(nil, 1, id), 2, uint64(code))
	case 20:
		refresh := id != 0
		rows, refreshGems, code := economy.PickNPCRequests(sid, refresh, now)
		var body []byte
		for _, row := range rows {
			body = pbLen(body, 1, gardenQuestInfo(economy.EpisodeRow{QuestRow: row}))
		}
		body = pbVar(body, 2, gardenQuestCallGem)
		body = pbVar(body, 3, uint64(refreshGems))
		body = pbVar(body, 4, uint64(economy.QuestResetMillis(now)))
		body = pbVar(body, 5, uint64(code))
		logger.Printf("garden-quest pick-npc aid=%d refresh=%v count=%d result=%d", aid, refresh, len(rows), code)
		return 27, body
	case 21:
		row, ok := economy.QuestDetailByID(sid, int64(id), now)
		body := pbVar(nil, 1, id)
		if !ok {
			body = pbVar(body, 2, 1)
			body = pbLen(body, 3, nil) // QuestInfo is required even on an error
			logger.Printf("garden-quest info-pick-npc aid=%d id=%d result=1", aid, id)
			return 23, body
		}
		reqView := economy.RequestList(sid, now)
		body = pbVar(body, 2, 0)
		body = pbLen(body, 3, gardenQuestInfo(row))
		body = pbVar(body, 4, uint64(reqView.FreeCallsLeft))
		body = pbVar(body, 5, uint64(reqView.MaxFreeCalls))
		body = pbVar(body, 6, gardenQuestCallGem)
		body = pbVar(body, 7, uint64(reqView.CallGems))
		logger.Printf("garden-quest info-pick-npc aid=%d id=%d result=0", aid, id)
		return 23, body
	case 22:
		callType, _ := protobufUintField(data, 2)
		paid := callType == 0 // ch_quest_call_npc_req.#2 call_type: 1=free, 0=paid
		code := economy.CallNPCRequest(sid, int64(id), paid, now)
		logger.Printf("garden-quest call-npc aid=%d id=%d paid=%v result=%d", aid, id, paid, code)
		return 28, pbVar(pbVar(nil, 1, id), 2, uint64(code))
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
// completed the quest. The same event feeds the circle points (economy.CirclePointsForQuest).
func MarkQuest(aid uint64, key string) {
	now := economy.NowFunc()
	def, done := economy.MarkQuest(strconv.FormatUint(aid, 10), key, now)
	economy.CirclePointsForQuest(strconv.FormatUint(aid, 10), key, done, now)
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

// heartQuestNotice queues the hearts row's notice for the heart that completed the quest (HeartSend's
// questDone). HeartSend already credited the Gems.
func heartQuestNotice(aid uint64) {
	queueQuestNotice(aid, economy.HeartQuest())
}

// takeQuestNotices returns and clears aid's queued notices.
func takeQuestNotices(aid uint64) []economy.QuestDef {
	questNotices.Lock()
	defer questNotices.Unlock()
	defs := questNotices.m[aid]
	delete(questNotices.m, aid)
	return defs
}

// moneySyncs holds the aids paid outside the Garden with no client balance refresh; their next Garden relay-start sends one.
var moneySyncs = struct {
	sync.Mutex
	m map[uint64]bool
}{m: map[uint64]bool{}}

func queueMoneySync(aid uint64) {
	moneySyncs.Lock()
	defer moneySyncs.Unlock()
	moneySyncs.m[aid] = true
}

// takeMoneySync reports and clears aid's pending balance refresh.
func takeMoneySync(aid uint64) bool {
	moneySyncs.Lock()
	defer moneySyncs.Unlock()
	pending := moneySyncs.m[aid]
	delete(moneySyncs.m, aid)
	return pending
}

// creditGardenVisit is the room-enter's visit credit. An owner aid with no avatar is not a visit: it earns nothing, so a
// made-up aid cannot farm the visit quest (PLAN 8.6b part 4). It reports whether the visit counted.
func creditGardenVisit(viewer, owner uint64) bool {
	if !gardenOwnerExists(owner) {
		return false
	}
	gardenQuestVisit(viewer, owner)
	if viewer != 0 && owner != 0 && viewer != owner {
		economy.RecordGardenVisit(strconv.FormatUint(viewer, 10), strconv.FormatUint(owner, 10))
	}
	return true
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
func gardenQuestNotify(conn *lockedConn, frame []byte, defs []economy.QuestDef, moneySync bool, logger *log.Logger) error {
	if len(defs) == 0 && !moneySync {
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

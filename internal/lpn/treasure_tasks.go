package lpn

import (
	"fmt"

	"cherry/internal/economy"
)

// Treasure World quests and missions (gateway agent 9; cells.md section 2, treasure-spec 9.5): the list (cs 16 → sc 17),
// the mission claim (cs 17 → sc 18), the quest claim (cs 18 → sc 19) and the free refresh (cs 19 → sc 20). A quest's
// PlayTicketQuestInfo carries its text and icon item; a mission's AchievementInfo has no text field, so the client draws its
// own wording for the mission's type.

// treasureQuestRow is one quest as the client reads a PlayTicketQuestInfo: SlotId, QuestType, CurrentCount, GoalCount, State,
// RewardTicketCount, Messqge and ItemCode (LinkInfo is not sent: the quest has no go-to button).
func treasureQuestRow(row economy.TreasureTaskRow) []byte {
	b := pbVar(nil, 1, uint64(row.Seq))
	b = pbVar(b, 2, uint64(row.Type))
	b = pbVar(b, 3, uint64(row.Count))
	b = pbVar(b, 4, uint64(row.Goal))
	b = pbVar(b, 6, uint64(row.State))
	b = pbVar(b, 7, uint64(row.Reward))
	b = pbLen(b, 8, []byte(row.Text))
	return pbLen(b, 9, []byte(row.Item))
}

// treasureMissionRow is one mission as the client reads an AchievementInfo: Seq, Type, Order, Goal, Count, Reward (Jewels),
// RewardBonus (0) and State.
func treasureMissionRow(row economy.TreasureTaskRow) []byte {
	b := pbVar(nil, 1, uint64(row.Seq))
	b = pbVar(b, 2, uint64(row.Type))
	b = pbVar(b, 3, uint64(row.Seq%100))
	b = pbVar(b, 4, uint64(row.Goal))
	b = pbVar(b, 5, uint64(row.Count))
	b = pbVar(b, 6, uint64(row.Reward))
	return pbVar(b, 8, uint64(row.State))
}

// treasureQuestRows encodes the quest set of lists as PlayTicketQuestInfo messages (the caller picks the repeated field).
func treasureQuestRows(lists economy.TreasureTaskLists) [][]byte {
	out := make([][]byte, 0, len(lists.Quests))
	for _, row := range lists.Quests {
		out = append(out, treasureQuestRow(row))
	}
	return out
}

// questList answers cs_play_ticket_quest_and_achievement_list_req (msgid 16) with both lists: the quest set (field 2), the
// mission set (field 3), the free refresh (field 4, 1 when open) and when it opens (field 5, Unix milliseconds). Each board
// entry asks, so the sets are posted there when needed.
func (s *treasureSession) questList(frame []byte) error {
	if !s.loggedIn {
		s.logger.Printf("TREASURE msgid=16 quest list refused: no login")
		return s.refuse(frame, treasureRcQuestList)
	}
	lists, err := economy.TreasureTaskList(s.aidText(), economy.NowFunc())
	if err != nil {
		s.logger.Printf("TREASURE msgid=16 quest list refused aid=%d: %v", s.aid, err)
		return s.refuse(frame, treasureRcQuestList)
	}
	body := pbVar(nil, 1, 0)
	for _, q := range treasureQuestRows(lists) {
		body = pbLen(body, 2, q)
	}
	for _, m := range lists.Missions {
		body = pbLen(body, 3, treasureMissionRow(m))
	}
	body = pbVar(body, 4, boolUint(lists.FreeRefresh))
	body = pbVar(body, 5, uint64(lists.ResetMs/1000)) // Unix seconds: the client shows QuestResetTime - time(NULL) (slice2-fixes.md C)
	s.logger.Printf("TREASURE msgid=16 quest list aid=%d quests=%d missions=%d free=%v result=0", s.aid, len(lists.Quests), len(lists.Missions), lists.FreeRefresh)
	return s.reply(frame, treasureRcQuestList, body)
}

// missionClaim answers cs_achievement_reward_req (msgid 17): the mission Seq (f1, set * 100 + order) pays its Jewels once it
// is complete. The reply carries the text (Reward f2) and the mission (Achievement f3).
func (s *treasureSession) missionClaim(frame, data []byte) error {
	seq, _ := protobufUintField(data, 1)
	if !s.loggedIn {
		s.logger.Printf("TREASURE msgid=17 mission refused: no login")
		return s.refuse(frame, treasureRcAchievementReward)
	}
	claim, err := economy.TreasureMissionClaim(s.aidText(), int64(seq), economy.NowFunc())
	if err != nil {
		s.logger.Printf("TREASURE msgid=17 mission refused aid=%d seq=%d: %v", s.aid, seq, err)
		return s.refuse(frame, treasureRcAchievementReward)
	}
	d := economy.ActiveTreasureData()
	body := pbVar(nil, 1, 0)
	body = pbLen(body, 2, treasureRewardInfo(fmt.Sprintf("+%d %s", claim.Paid, d.Names.Coin), claim.Paid))
	body = pbLen(body, 3, treasureMissionRow(claim.Row))
	s.logger.Printf("TREASURE msgid=17 mission aid=%d seq=%d jewels=%d result=0", s.aid, seq, claim.Paid)
	return s.reply(frame, treasureRcAchievementReward, body)
}

// questClaim answers cs_play_ticket_quest_reward_req (msgid 18): the quest slot (f1) pays its tickets once complete. The
// reply carries the text (RewardInfo f3), the tickets with their timer (f4..f7), the free refresh (f8, f9) and the quests (f10).
func (s *treasureSession) questClaim(frame, data []byte) error {
	slot, _ := protobufUintField(data, 1)
	if !s.loggedIn {
		s.logger.Printf("TREASURE msgid=18 quest refused: no login")
		return s.refuse(frame, treasureRcQuestReward)
	}
	claim, err := economy.TreasureQuestClaim(s.aidText(), int64(slot), economy.NowFunc())
	if err != nil {
		s.logger.Printf("TREASURE msgid=18 quest refused aid=%d slot=%d: %v", s.aid, slot, err)
		return s.refuse(frame, treasureRcQuestReward)
	}
	d := economy.ActiveTreasureData()
	body := pbVar(nil, 1, 0)
	body = pbLen(body, 3, treasureRewardInfo(fmt.Sprintf("+%d %s", claim.Paid, d.Names.Ticket), claim.Paid))
	body = pbVar(body, 4, uint64(claim.View.Tickets))
	body = pbVar(body, 5, uint64(claim.View.Max))
	body = pbVar(body, 6, uint64(claim.View.RemainSeconds))
	body = pbVar(body, 7, uint64(claim.View.ChargeSeconds))
	body = pbVar(body, 8, boolUint(claim.Lists.FreeRefresh))
	body = pbVar(body, 9, uint64(claim.Lists.ResetMs/1000)) // Unix seconds
	for _, q := range treasureQuestRows(claim.Lists) {
		body = pbLen(body, 10, q)
	}
	s.logger.Printf("TREASURE msgid=18 quest aid=%d slot=%d tickets=%d tickets_now=%d/%d result=0", s.aid, slot, claim.Paid, claim.View.Tickets, claim.View.Max)
	return s.reply(frame, treasureRcQuestReward, body)
}

// questRefresh answers cs_play_ticket_quest_refresh_req (msgid 19): a new quest set when the free refresh is open. A paid
// refresh (FreeRequest false) is refused: nothing is sold.
func (s *treasureSession) questRefresh(frame, data []byte) error {
	free, _ := protobufUintField(data, 1)
	if !s.loggedIn || free == 0 {
		s.logger.Printf("TREASURE msgid=19 quest refresh refused: loggedIn=%v free=%d", s.loggedIn, free)
		return s.refuse(frame, treasureRcQuestRefresh)
	}
	lists, err := economy.TreasureQuestRefresh(s.aidText(), economy.NowFunc())
	if err != nil {
		s.logger.Printf("TREASURE msgid=19 quest refresh refused aid=%d: %v", s.aid, err)
		return s.refuse(frame, treasureRcQuestRefresh)
	}
	body := pbVar(nil, 1, 0)
	for _, q := range treasureQuestRows(lists) {
		body = pbLen(body, 2, q)
	}
	body = pbVar(body, 3, boolUint(lists.FreeRefresh))
	body = pbVar(body, 4, uint64(lists.ResetMs/1000)) // Unix seconds: the client shows QuestResetTime - time(NULL) (slice2-fixes.md C)
	s.logger.Printf("TREASURE msgid=19 quest refresh aid=%d quests=%d result=0", s.aid, len(lists.Quests))
	return s.reply(frame, treasureRcQuestRefresh, body)
}

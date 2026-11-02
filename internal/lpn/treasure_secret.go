package lpn

import (
	"fmt"

	"cherry/internal/economy"
	"cherry/internal/store"
)

// Treasure World secret quests (gateway agent 9; slice3b-spec section B): the enable (cs 26 → sc 31) and the claim (cs 25 → sc 29).
// The square's CellDetail carries the quest (treasureSpecialDetail); the claim reward is Cherry-made (economy/treasure_secret.go).

// treasureSpecialDetail is the CellDetailInfo of secret quest square pos (CellTypeSpecialMission, f7): Availability 1 and the quest
// (f2): type 1, the goal (a Normal-grade goal of goal_pulls pulls), the goal count, Count, the reward tickets (f5), the Gacha Shop
// link (f6), EnableReward (f7: the goal reached) and the ticket picture (f8). A square without its own quest reads as a fresh one.
func treasureSpecialDetail(d *economy.TreasureData, pos int, sec *store.TreasureSecret) []byte {
	q := store.TreasureSecret{Square: pos, Goal: d.Special.GoalPulls}
	if sec != nil && sec.Square == pos {
		q = *sec
	}
	grade := pbVar(pbLen(nil, 1, []byte("Normal")), 2, uint64(q.Goal)) // SpecialQuestGoalGachaGrade: grade word, count
	quest := pbVar(nil, 1, 1)                                          // Type 1
	quest = pbLen(quest, 2, pbLen(nil, 1, grade))                      // Goal (oneof case 1)
	quest = pbVar(quest, 3, uint64(q.Goal))                            // GoalCount
	quest = pbVar(quest, 4, uint64(q.Count))                           // Count
	quest = pbVar(quest, 5, uint64(d.Special.RewardTickets))           // Reward
	quest = pbLen(quest, 6, treasureLinkInfo())                        // LinkInfo
	quest = pbVar(quest, 7, boolUint(q.Done))                          // EnableReward
	quest = pbVar(quest, 8, treasureViewTicket)                        // RewardViewType
	return pbLen(nil, 7, pbLen(pbVar(nil, 1, 1), 2, quest))
}

// specialEnable answers cs_special_quest_enable_req (msgid 26, empty) for the quest of the square the token stands on: the quest is
// enabled, and the reply (sc 31, Result f1) carries the square's detail (f2), whose LinkInfo opens the Gacha Shop. Elsewhere it
// is refused.
func (s *treasureSession) specialEnable(frame []byte) error {
	if !s.loggedIn || !s.entered {
		s.logger.Printf("TREASURE msgid=26 secret enable refused: loggedIn=%v entered=%v", s.loggedIn, s.entered)
		return s.refuse(frame, treasureRcSpecialEnable)
	}
	view, err := economy.TreasureSecretEnable(s.aidText(), economy.NowFunc())
	if err != nil {
		s.logger.Printf("TREASURE msgid=26 secret enable refused aid=%d: %v", s.aid, err)
		return s.refuse(frame, treasureRcSpecialEnable)
	}
	d := economy.ActiveTreasureData()
	body := pbVar(nil, 1, 0)
	body = pbLen(body, 2, treasureSpecialDetail(d, view.Position, view.Secret))
	s.logger.Printf("TREASURE msgid=26 secret enable aid=%d square=%d count=%d goal=%d result=0",
		s.aid, view.Position, secretCount(view.Secret), secretGoal(d, view.Secret))
	return s.reply(frame, treasureRcSpecialEnable, body)
}

// specialReward answers cs_special_quest_reward_req (msgid 25, empty) for the finished quest of the square the token stands on: the
// tickets are paid and the quest ends. The reply (sc 29, Result f1) carries a RewardInfo with the tickets (f2 item list, f5 text) and
// the square's detail with the quest cleared (f7 Availability 0). Elsewhere, or before the goal, it is refused.
func (s *treasureSession) specialReward(frame []byte) error {
	if !s.loggedIn || !s.entered {
		s.logger.Printf("TREASURE msgid=25 secret reward refused: loggedIn=%v entered=%v", s.loggedIn, s.entered)
		return s.refuse(frame, treasureRcSpecialReward)
	}
	paid, view, err := economy.TreasureSecretClaim(s.aidText(), economy.NowFunc())
	if err != nil {
		s.logger.Printf("TREASURE msgid=25 secret reward refused aid=%d: %v", s.aid, err)
		return s.refuse(frame, treasureRcSpecialReward)
	}
	d := economy.ActiveTreasureData()
	reward := treasureRewardInfo(fmt.Sprintf("+%d %s", paid, d.Names.Ticket), treasureViewTicket, paid)
	detail := pbLen(nil, 1, reward)
	detail = pbLen(detail, 7, pbVar(nil, 1, 0)) // CellTypeSpecialMission with Availability 0: no claim again
	body := pbVar(nil, 1, 0)
	body = pbLen(body, 2, detail)
	s.logger.Printf("TREASURE msgid=25 secret reward aid=%d square=%d tickets=%d tickets_now=%d/%d result=0",
		s.aid, view.Position, paid, view.Tickets, view.Max)
	return s.reply(frame, treasureRcSpecialReward, body)
}

// secretCount and secretGoal are the log's view of a quest (0 without one).
func secretCount(sec *store.TreasureSecret) int64 {
	if sec == nil {
		return 0
	}
	return sec.Count
}

func secretGoal(d *economy.TreasureData, sec *store.TreasureSecret) int64 {
	if sec == nil {
		return d.Special.GoalPulls
	}
	return sec.Goal
}

package lpn

import (
	"fmt"
	"slices"
	"strconv"

	"cherry/internal/economy"
)

const (
	squareCrHighFiveAchieve    = 18
	squareRcHighFiveTargetList = 26
	squareRcHighFiveAchieve    = 27

	tlupSquareHighFiveQuestID = 7
)

// tlupQuestListBody encodes TLUP::tc_questlist_res (msgid 0, agent 5) seeding
// TLUP::QuestId = 7 (eQUEST_CODE = 106, Square daily high-five quest) with
// viewer's current game-day progress.
func tlupQuestListBody(viewer uint64) []byte {
	count, _, goal, reward := economy.HighFiveProgress(strconv.FormatUint(viewer, 10))
	params := fmt.Sprintf(`{"display_ver":0,"completed_gem":%d,"reset_hour":0}`, reward)

	master := pbVar(nil, 1, tlupSquareHighFiveQuestID)
	master = pbVar(master, 13, uint64(goal))
	master = pbLen(master, 21, []byte(params))

	user := pbVar(nil, 1, tlupSquareHighFiveQuestID)
	user = pbVar(user, 8, uint64(max(0, min(count, goal))))

	quest := pbVar(nil, 1, tlupSquareHighFiveQuestID)
	quest = pbLen(quest, 2, master)
	quest = pbLen(quest, 3, user)

	res := pbVar(nil, 1, uint64(economy.NowFunc().Unix()))
	return pbLen(res, 2, quest)
}

// squareHighFiveTargetList encodes SQUARE::rc_highfive_target_list_res (msgid 26)
// with repeated varint field 1 (target_aid).
func squareHighFiveTargetList(aids []uint64) []byte {
	var b []byte
	for _, aid := range aids {
		b = pbVar(b, 1, aid)
	}
	return b
}

// squareHighFiveAchieveRes encodes SQUARE::rc_highfive_achieve_res (msgid 27).
// Field 5 (serial) is intentionally never encoded: its presence routes the client
// into the 11th-Anniversary NPC popup (_NpcHighfive).
func squareHighFiveAchieveRes(ok bool, myAid, targetAid, count uint64) []byte {
	var res uint64
	if ok {
		res = 1
	}
	b := pbVar(nil, 1, res)
	b = pbVar(b, 2, myAid)
	b = pbVar(b, 3, targetAid)
	return pbVar(b, 4, count)
}

func (s *squareSession) pushHighFiveTargets(rejoined bool) {
	if s.room == nil || s.player == nil {
		return
	}
	if s.boss != nil || s.racing != nil ||
		s.room.theme == squareBossThemeID || s.room.theme == squareRacingTheme ||
		s.square.Theme == squareBossThemeID || s.square.Theme == squareRacingTheme {
		return
	}

	others := s.room.members(s.player)
	if len(others) == 0 {
		return
	}

	joinerAidStr := strconv.FormatUint(s.aid, 10)
	jCount, jDone, goal, _ := economy.HighFiveProgress(joinerAidStr)

	var forJoiner []uint64
	var notifyOthers []*squarePlayer
	for _, other := range others {
		if other.aid == s.aid {
			continue
		}
		otherAidStr := strconv.FormatUint(other.aid, 10)
		if jCount < goal && !slices.Contains(jDone, otherAidStr) {
			forJoiner = append(forJoiner, other.aid)
		}
		if !rejoined {
			oCount, oDone, oGoal, _ := economy.HighFiveProgress(otherAidStr)
			if oCount < oGoal && !slices.Contains(oDone, joinerAidStr) {
				notifyOthers = append(notifyOthers, other)
			}
		}
	}

	if len(forJoiner) > 0 {
		s.player.send(s.logger, squareRcHighFiveTargetList, squareHighFiveTargetList(forJoiner))
	}
	if len(notifyOthers) > 0 {
		single := squareHighFiveTargetList([]uint64{s.aid})
		for _, other := range notifyOthers {
			other.send(s.logger, squareRcHighFiveTargetList, single)
		}
	}
}

func (s *squareSession) handleHighFive(frame, data []byte) error {
	var targetAid uint64
	if !pbScan(data, func(f, v uint64, _ []byte) {
		if f == 1 {
			targetAid = v
		}
	}) {
		targetAid = 0
	}

	var targetPlayer *squarePlayer
	if targetAid != 0 && targetAid != s.aid {
		s.room.mu.Lock()
		for _, p := range s.room.players {
			if p.aid == targetAid {
				targetPlayer = p
				break
			}
		}
		s.room.mu.Unlock()
	}

	aidStr := strconv.FormatUint(s.aid, 10)
	if targetPlayer == nil {
		curCount, _, _, _ := economy.HighFiveProgress(aidStr)
		return s.reply(frame, squareRcHighFiveAchieve, squareHighFiveAchieveRes(false, s.aid, targetAid, uint64(curCount)))
	}

	count, paidGems, ok := economy.RecordHighFive(aidStr, strconv.FormatUint(targetAid, 10))
	if !ok {
		return s.reply(frame, squareRcHighFiveAchieve, squareHighFiveAchieveRes(false, s.aid, targetAid, uint64(count)))
	}

	s.logger.Printf("SQUARE highfive aid=%d target=%d count=%d gems=%d", s.aid, targetAid, count, paidGems)
	res := squareHighFiveAchieveRes(true, s.aid, targetAid, uint64(count))
	if err := s.reply(frame, squareRcHighFiveAchieve, res); err != nil {
		return err
	}
	targetPlayer.send(s.logger, squareRcHighFiveAchieve, res)
	return nil
}

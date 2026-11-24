package lpn

import (
	"sync"

	"cherry/internal/economy"
)

const (
	squareCrHighFiveAchieve    = 18
	squareRcHighFiveTargetList = 26
	squareRcHighFiveAchieve    = 27
)

// squareHighFives tracks player-to-player Square high-fives in memory per game day
// (06:00 JST, economy.AttendDayKey), mapping initiatorAid -> set of targetAids.
var squareHighFives = struct {
	mu   sync.Mutex
	day  int64
	done map[uint64]map[uint64]struct{}
}{}

func squareHighFiveDay() int64 {
	return economy.AttendDayKey(economy.NowFunc())
}

// ensureSquareHighFiveDayLocked resets squareHighFives when the game day rolls over.
// Caller must hold squareHighFives.mu.
func ensureSquareHighFiveDayLocked(day int64) {
	if squareHighFives.done == nil || squareHighFives.day != day {
		squareHighFives.day = day
		squareHighFives.done = make(map[uint64]map[uint64]struct{})
	}
}

func resetSquareHighFives() {
	squareHighFives.mu.Lock()
	defer squareHighFives.mu.Unlock()
	squareHighFives.day = 0
	squareHighFives.done = nil
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

	day := squareHighFiveDay()
	squareHighFives.mu.Lock()
	ensureSquareHighFiveDayLocked(day)

	var forJoiner []uint64
	var notifyOthers []*squarePlayer
	for _, other := range others {
		if other.aid == s.aid {
			continue
		}
		if _, done := squareHighFives.done[s.aid][other.aid]; !done {
			forJoiner = append(forJoiner, other.aid)
		}
		if !rejoined {
			if _, done := squareHighFives.done[other.aid][s.aid]; !done {
				notifyOthers = append(notifyOthers, other)
			}
		}
	}
	squareHighFives.mu.Unlock()

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

	day := squareHighFiveDay()
	squareHighFives.mu.Lock()
	ensureSquareHighFiveDayLocked(day)
	curCount := uint64(len(squareHighFives.done[s.aid]))
	if targetPlayer == nil {
		squareHighFives.mu.Unlock()
		return s.reply(frame, squareRcHighFiveAchieve, squareHighFiveAchieveRes(false, s.aid, targetAid, curCount))
	}
	if _, already := squareHighFives.done[s.aid][targetAid]; already {
		squareHighFives.mu.Unlock()
		return s.reply(frame, squareRcHighFiveAchieve, squareHighFiveAchieveRes(false, s.aid, targetAid, curCount))
	}
	if squareHighFives.done[s.aid] == nil {
		squareHighFives.done[s.aid] = make(map[uint64]struct{})
	}
	squareHighFives.done[s.aid][targetAid] = struct{}{}
	count := uint64(len(squareHighFives.done[s.aid]))
	squareHighFives.mu.Unlock()

	s.logger.Printf("SQUARE highfive aid=%d target=%d count=%d", s.aid, targetAid, count)
	res := squareHighFiveAchieveRes(true, s.aid, targetAid, count)
	if err := s.reply(frame, squareRcHighFiveAchieve, res); err != nil {
		return err
	}
	targetPlayer.send(s.logger, squareRcHighFiveAchieve, res)
	return nil
}

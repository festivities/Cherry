package lpn

import (
	"slices"
	"strconv"

	"cherry/internal/economy"
)

// The shared battle of a boss lobby (multiplayer, Economy Phase DE; spec %TEMP%\opencode\cherry\leftovers-20261026\
// boss-mp-spec.md sections 4 to 6, and the solo rules in phaseDE-20261022\boss-fishing-spec.md section 7). The
// boss has one HP for the whole lobby (fishboss max_hp); each member's touches count on its own button (a hit is
// per member, note and TouchCount), and every accepted hit comes off the shared HP. The first finish (cr58) ends the
// battle for everyone: HP 0 is a catch, otherwise the time ran out. Every member is paid once for its own finish.

// squareBossTouch is one TouchInfo2 row of cr50: a hit or a miss on a note index.
type squareBossTouch struct {
	hit   bool
	note  uint64
	count uint64 // TouchCount4: 1-based hits on this note so far
}

// squareBossTouches reads the TouchInfo2 rows (field 2) of cr50 for TouchResult1 and NoteIndex3. ok
// is false when the message is malformed.
func squareBossTouches(data []byte) (rows []squareBossTouch, ok bool) {
	bad := false
	ok = pbScan(data, func(f, _ uint64, row []byte) {
		if f != 2 || row == nil {
			return
		}
		var t squareBossTouch
		if !pbScan(row, func(rf, v uint64, _ []byte) {
			switch rf {
			case 1:
				t.hit = v != 0
			case 3:
				t.note = v
			case 4:
				t.count = v
			}
		}) {
			bad = true
		}
		rows = append(rows, t)
	})
	return rows, ok && !bad
}

// bossTouch answers cr50 for the session's battle. A row is a hit on a visible note (digit != '0') when its count is
// the member's next one on that note and within the note's touch_max; each accepted hit takes damage_per_hit (at most
// what is left) off the shared HP and adds to the member's DamageSum. Every touch request pushes rc64 to every member,
// the sender included: TouchResult (any hit), the shared HP and the sender's cumulative DamageSum. A battle that
// ended takes no more touches.
func (s *squareSession) bossTouch(frame []byte, data []byte) error {
	seat := s.boss
	touches, ok := squareBossTouches(data)
	if seat == nil || !ok {
		s.logger.Printf("SQUARE fishing boss touch aid=%d ignored invalid=%v", s.aid, !ok)
		return nil
	}
	boss := economy.ActiveFishBoss()
	sq := seat.sq
	sq.mu.Lock()
	r := seat.room
	if r == nil || !r.fight || r.ended {
		sq.mu.Unlock()
		s.logger.Printf("SQUARE fishing boss touch aid=%d ignored", s.aid)
		return nil
	}
	accepted := 0
	for _, t := range touches {
		if r.ended {
			break
		}
		if !t.hit || t.note >= uint64(len(boss.PlayNote)) || boss.PlayNote[t.note] == '0' ||
			t.count != seat.hits[t.note]+1 || t.count > uint64(boss.TouchMax(boss.PlayNote[t.note])) {
			continue
		}
		seat.hits[t.note] = t.count
		dealt := min(boss.DamagePerHit, r.hp)
		seat.damage += dealt
		r.hp -= dealt
		accepted++
		if r.hp == 0 {
			r.ended, r.caught = true, true
			r.final = slices.Clone(r.members)
		}
	}
	hp := r.hp
	body := pbVar(nil, 1, squareBool(accepted > 0))
	body = pbLen(body, 2, pbVar(nil, 1, uint64(hp)))                           // Play2.CurrentHp1
	body = pbLen(body, 3, pbVar(pbVar(nil, 1, s.aid), 2, uint64(seat.damage))) // User3 {Aid1, DamageSum2}
	members := slices.Clone(r.members)
	sq.mu.Unlock()
	s.logger.Printf("SQUARE fishing boss touch aid=%d rows=%d accepted=%d hp=%d members=%d", s.aid, len(touches), accepted, hp, len(members))
	pushMembers(s.logger, members, 64, body)
	return nil
}

// bossFinish answers cr58 for the session's battle, once per member. The first finish fixes the battle's members
// (the participants, ranked by damage) and the outcome: caught when HP reached 0, else the time ran out. The member
// is paid once (economy.BossFinish); a repeated cr58 gets the same rc65 without a payment.
func (s *squareSession) bossFinish(frame []byte) error {
	seat := s.boss
	if seat == nil {
		s.logger.Printf("SQUARE fishing boss finish aid=%d ignored", s.aid)
		return nil
	}
	sq := seat.sq
	sq.mu.Lock()
	r := seat.room
	if r == nil || !r.fight {
		sq.mu.Unlock()
		s.logger.Printf("SQUARE fishing boss finish aid=%d ignored", s.aid)
		return nil
	}
	if seat.rc65 != nil {
		body := seat.rc65
		sq.mu.Unlock()
		s.logger.Printf("SQUARE fishing boss finish aid=%d repeated", s.aid)
		return s.reply(frame, 65, body)
	}
	if !r.ended {
		r.ended = true // the time ran out before HP 0: not caught
		r.final = slices.Clone(r.members)
	}
	caught := r.caught
	sq.mu.Unlock()

	gems, coins, balance, ok := economy.BossFinish(strconv.FormatUint(s.aid, 10), caught, economy.NowFunc())

	sq.mu.Lock()
	body := squareBossResult(caught, r.final, gems, coins, balance)
	seat.rc65 = body
	members := len(r.final)
	sq.mu.Unlock()
	s.logger.Printf("SQUARE fishing boss finish aid=%d caught=%v members=%d gems=%d coins=%d balance=%d paid=%v", s.aid, caught, members, gems, coins, balance, ok)
	return s.reply(frame, 65, body)
}

// squareBossResult is rc_fishing_boss_play_end_all (65) for one member: IsCatch, the battle's members ranked by
// damage (ties by join order, each with the Gems a member is paid), and the member's own Boss Coins
// (BosscoinAdded1, BosscoinBonus2 0, BosscoinBalance3). The caller holds the Square's lock (damage is read).
func squareBossResult(caught bool, final []*squareBossSeat, gems, coins, balance int64) []byte {
	body := pbVar(nil, 1, squareBool(caught))
	for i, m := range bossRanks(final) {
		body = pbLen(body, 2, pbVar(pbVar(pbVar(nil, 1, m.aid()), 2, uint64(i+1)), 3, uint64(gems)))
	}
	body = pbLen(body, 3, pbVar(pbVar(pbVar(nil, 1, uint64(coins)), 2, 0), 3, uint64(balance)))
	return pbVar(body, 4, 0) // IsShowUpdloadBtn4: no upload button
}

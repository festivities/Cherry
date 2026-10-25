package lpn

import (
	"strconv"

	"cherry/internal/economy"
	"cherry/internal/store"
)

// Square boss fishing, solo (Economy Phase DE; wire notes in %TEMP%\opencode\cherry\phaseDE-20261022\
// boss-fishing-spec.md, sections 2, 3, 5 to 8 and 11). A try (cr45) spends one of the day's tries and,
// when one was left, enters the boss world (rc6 theme 28) without a relaystart. The client then asks for
// the lobby (cr46 -> rc59), starts the battle (cr47 master start -> rc71), confirms loading (cr55 -> rc73),
// plays (cr49 -> rc63, cr50 touches -> rc64) and finishes (cr58 -> rc65). Reset (cr54 -> rc72) refills the
// tries for Gems when the data allows it. rc74 (test mode) is never sent. The tries, chart, boss and
// rewards are operator data (data/fishboss.json, economy/fishboss.go).
const (
	squareBossThemeID     = 28                // rc_room_enter_res ThemeId4 of the boss world (spec 11.1)
	squareBossMapFileName = squareMapFileName // the boss world loads the Square map: no boss map is in the client files (spec 11.2) [I]
	squareRcBossRooms     = 58                // rc_fish_boss_rooms_all: the boss icon on the Square
	squareBossRoomsReady  = 1                 // RoomCountReady1: the icon shows
	squareBossRoomState   = 1                 // rc59 RoomState1: meaning not identified (spec 3.2); 1 is a lone-master value (spec 4.1)
	squareBossStateStart  = 2                 // cr47 StateReq: the master's start button
	squareBossReady       = 2                 // FishBossUserInLobby.State3: ready
	squareCoinGems        = 4                 // COST CoinType of Gems (spec 2.3)
)

// squareBossBattle is one try of a session, from cr45 to the finish (cr58). fight is set by the
// master start (cr47 2); touches count only in a fight.
type squareBossBattle struct {
	fight  bool
	done   bool
	damage int64             // HP the boss has lost, capped at its max HP
	hits   map[uint64]uint64 // accepted hits per note index (0-based into the chart)
}

// handleBoss answers the boss requests. Only the try (cr45) is valid without a try in progress; the
// others are ignored without a reply when they come out of order, like the other room requests.
func (s *squareSession) handleBoss(frame []byte, msgid uint16, data []byte) error {
	aid := s.aid
	aidText := strconv.FormatUint(aid, 10)
	boss := economy.ActiveFishBoss()
	battle := s.boss
	switch msgid {
	case 45: // cr_start_fishing_boss_try_req {BossTryRequest} -> rc_start_fishing_boss_try_res (57); a start also enters the boss world (rc6)
		left, ok := economy.BossTryStart(aidText, economy.NowFunc())
		s.boss = nil
		if !ok {
			s.logger.Printf("SQUARE fishing boss try refused aid=%d left=%d", aid, left)
			return s.reply(frame, 57, squareBossTry(false, boss, left))
		}
		s.boss = &squareBossBattle{hits: map[uint64]uint64{}}
		s.logger.Printf("SQUARE fishing boss try aid=%d left=%d theme=%d", aid, left, squareBossThemeID)
		if err := s.reply(frame, 57, squareBossTry(true, boss, left)); err != nil {
			return err
		}
		return s.reply(frame, 6, squareRoomEnter(nil, squareBossThemeID, squareBossMapFileName))
	case 46: // cr_fishing_boss_roominfo_req (empty) -> rc_fishing_boss_roominfo_res (59): the lobby with the master alone
		if battle == nil {
			s.logger.Printf("SQUARE fishing boss lobby ignored aid=%d no try", aid)
			return nil
		}
		body := pbVar(nil, 1, squareBossRoomState)
		body = pbLen(body, 2, squareBossInfo(boss))
		body = pbLen(body, 3, squareBossLobbyUser(aid))
		s.logger.Printf("SQUARE fishing boss lobby aid=%d", aid)
		return s.reply(frame, 59, body)
	case 47: // cr_fishing_boss_update_room_state_req {StateReq1}: 2 = master start -> rc_fishboss_battle_start_push (71)
		state, _ := protobufUintField(data, 1)
		if battle == nil || battle.fight || state != squareBossStateStart {
			s.logger.Printf("SQUARE fishing boss state aid=%d state=%d ignored", aid, state)
			return nil
		}
		battle.fight = true
		s.logger.Printf("SQUARE fishing boss start aid=%d notes=%d maxhp=%d", aid, len(boss.PlayNote), boss.Boss.MaxHp)
		body := pbLen(nil, 1, squareBossGameData(boss))
		body = pbVar(body, 2, aid) // AidList: the one player
		return s.reply(frame, 71, body)
	case 48: // cr_fishing_boss_kickuser_req {KickAid1}: a solo lobby has nobody to kick, no reply
		s.logger.Printf("SQUARE fishing boss kick aid=%d ignored", aid)
		return nil
	case 49: // cr_fishing_boss_play_begin_req {ClientTimeMs1} -> rc_fishing_boss_play_begin_res (63)
		if battle == nil || !battle.fight || battle.done {
			s.logger.Printf("SQUARE fishing boss play begin aid=%d ignored", aid)
			return nil
		}
		return s.reply(frame, 63, pbVar(nil, 1, squareFishOK))
	case 50: // cr_fishing_boss_play_touch_req {TouchInfo2 rows} -> rc_fishing_boss_play_touch_all (64)
		if battle == nil || !battle.fight || battle.done {
			s.logger.Printf("SQUARE fishing boss touch aid=%d ignored", aid)
			return nil
		}
		touches, ok := squareBossTouches(data)
		if !ok {
			s.logger.Printf("SQUARE fishing boss touch aid=%d invalid", aid)
			return nil
		}
		accepted := 0
		for _, t := range touches {
			// A hit counts on a visible note (digit != '0') when its per-note touch count is the next one
			// and stays within the note type's touch_max (spec 13.6/13.7): repeats and replays add nothing.
			if !t.hit || t.note >= uint64(len(boss.PlayNote)) || boss.PlayNote[t.note] == '0' ||
				t.count != battle.hits[t.note]+1 || t.count > uint64(boss.TouchMax(boss.PlayNote[t.note])) {
				continue
			}
			battle.hits[t.note] = t.count
			battle.damage = min(battle.damage+boss.DamagePerHit, boss.Boss.MaxHp)
			accepted++
		}
		hp := boss.Boss.MaxHp - battle.damage
		body := pbVar(nil, 1, squareBool(accepted > 0))
		body = pbLen(body, 2, pbVar(nil, 1, uint64(hp)))                           // Play2.CurrentHp1
		body = pbLen(body, 3, pbVar(pbVar(nil, 1, aid), 2, uint64(battle.damage))) // User3 {Aid1, DamageSum2}
		s.logger.Printf("SQUARE fishing boss touch aid=%d rows=%d accepted=%d hp=%d", aid, len(touches), accepted, hp)
		return s.reply(frame, 64, body)
	case 54: // cr_move_fishboss_room_req {ResetTryCount1} -> rc_move_fishboss_room_res (72) (spec 12)
		// The BOSS! GO banner sends it empty: spend a try and enter the boss world (rc72 Result 0 is
		// silent, rc6 theme 28 does the move). ResetTryCount1 true comes only from the client's recharge
		// popup (box 1811, opened by a Result 1 / Remain 0 reply) and refills the tries for Gems.
		reset, _ := protobufUintField(data, 1)
		cost := pbVar(pbVar(nil, 1, squareCoinGems), 2, uint64(boss.ResetGems)) // TryCountResetCost4 {CoinType, CoinCount}
		rc72 := func(result uint64, left int64) error {
			body := pbVar(nil, 1, result)
			body = pbVar(body, 2, uint64(boss.MaxTries))
			body = pbVar(body, 3, uint64(left))
			return s.reply(frame, 72, pbLen(body, 4, cost))
		}
		if reset != 0 {
			left, ok := economy.BossReset(aidText, economy.NowFunc())
			s.logger.Printf("SQUARE fishing boss reset aid=%d ok=%v left=%d", aid, ok, left)
			if !ok { // Gems short or no reset offered: ask again (box 1811), nothing changed
				return rc72(1, 0)
			}
			return rc72(0, left) // the client shows "filled up" (box 1810) after its recharge popup
		}
		left, ok := economy.BossTryStart(aidText, economy.NowFunc())
		s.boss = nil
		if !ok {
			s.logger.Printf("SQUARE fishing boss try refused aid=%d left=%d reset=%d", aid, left, boss.ResetGems)
			if boss.ResetGems > 0 {
				return rc72(1, 0) // box 1811: out of tries, recharge for Gems?
			}
			// No reset offered: the client has no plain "no tries left" text; Result 1 with Remain > 0
			// shows box 1834 ("all rooms are full, try again") and hides the banner until the next enter.
			return rc72(1, 1)
		}
		s.boss = &squareBossBattle{hits: map[uint64]uint64{}}
		s.logger.Printf("SQUARE fishing boss try aid=%d left=%d theme=%d", aid, left, squareBossThemeID)
		if err := rc72(0, left); err != nil {
			return err
		}
		return s.reply(frame, 6, squareRoomEnter(nil, squareBossThemeID, squareBossMapFileName))
	case 55: // cr_inGame_loading_complete_req (empty) -> rc_inGame_loading_complete_push (73)
		if battle == nil || !battle.fight {
			s.logger.Printf("SQUARE fishing boss loaded aid=%d ignored", aid)
			return nil
		}
		return s.reply(frame, 73, pbVar(nil, 1, 1)) // Result1 [I]: the client ignores the value
	case 58: // cr_inGame_finish_req (empty) -> rc_fishing_boss_play_end_all (65): pays one battle once
		if battle == nil || !battle.fight || battle.done {
			s.logger.Printf("SQUARE fishing boss finish aid=%d ignored", aid)
			return nil
		}
		battle.done = true
		caught := battle.damage >= boss.Boss.MaxHp
		gems, coins, balance, ok := economy.BossFinish(aidText, caught, economy.NowFunc())
		s.logger.Printf("SQUARE fishing boss finish aid=%d caught=%v gems=%d coins=%d balance=%d paid=%v", aid, caught, gems, coins, balance, ok)
		body := pbVar(nil, 1, squareBool(caught))
		body = pbLen(body, 2, pbVar(pbVar(pbVar(nil, 1, aid), 2, 1), 3, uint64(gems))) // Users2 {Aid1, Rank2 1, CoinReward3 = Gems}
		body = pbLen(body, 3, pbVar(pbVar(pbVar(nil, 1, uint64(coins)), 2, 0), 3, uint64(balance)))
		return s.reply(frame, 65, pbVar(body, 4, 0)) // Reward3 {BosscoinAdded1, BosscoinBonus2 0, BosscoinBalance3}; no upload button
	}
	return nil
}

// squareBossTry is rc_start_fishing_boss_try_res (57): IsStartBossBattle2, the tries and the Boss Coins
// a catch pays (the refusal popup shows it). No AssetBossCoin and no billing fields.
func squareBossTry(started bool, boss *economy.FishBoss, left int64) []byte {
	b := pbVar(nil, 1, squareFishOK)
	b = pbVar(b, 2, squareBool(started))
	b = pbVar(b, 3, uint64(boss.MaxTries))
	b = pbVar(b, 4, uint64(left))
	return pbVar(b, 6, uint64(boss.Reward.CoinsCatch))
}

// squareBossInfo is FishBossInfo at full HP (rc59 Boss2, rc71 GameData Fish1). The picture is a
// Cherry-made fish picture (economy.FishImageURL serves the boss id too).
func squareBossInfo(boss *economy.FishBoss) []byte {
	b := pbVar(nil, 1, uint64(boss.Boss.ID))
	b = pbLen(b, 2, []byte(boss.Boss.Name))
	b = pbLen(b, 3, []byte(economy.FishImageURL(boss.Boss.ID)))
	b = pbVar(b, 4, uint64(boss.Boss.MaxHp))
	b = pbVar(b, 5, uint64(boss.Boss.MaxHp)) // CurrentHp: whole in the lobby and at the start
	return pbLen(b, 6, []byte(boss.Boss.Explain))
}

// squareBossLobbyUser is the master's FishBossUserInLobby: Aid1, IsRoomMaster2, State3 (ready), Name7.
func squareBossLobbyUser(aid uint64) []byte {
	name := "cherry"
	if acc, ok := store.AccountByAvatarID(strconv.FormatUint(aid, 10)); ok {
		name = acc.Name
	}
	u := pbVar(nil, 1, aid)
	u = pbVar(u, 2, 1)
	u = pbVar(u, 3, squareBossReady)
	return pbLen(u, 7, []byte(name))
}

// squareBossGameData is FishBossGameData (rc71 GameData1): the boss, the note timing, the chart and
// the touch count per note type (NoteDetailInfo6). The remaining play time starts at the whole time.
func squareBossGameData(boss *economy.FishBoss) []byte {
	var notes []byte
	for _, n := range boss.NoteDetail {
		notes = pbLen(notes, 1, pbVar(pbVar(nil, 1, uint64(n.NoteType)), 2, uint64(n.TouchMax)))
	}
	g := pbLen(nil, 1, squareBossInfo(boss))
	g = pbVar(g, 2, uint64(boss.NoteArriveMs))
	g = pbVar(g, 3, uint64(boss.MaxPlaytimeMs))
	g = pbVar(g, 4, uint64(boss.MaxPlaytimeMs))
	g = pbLen(g, 5, []byte(boss.PlayNote))
	g = pbLen(g, 6, notes)
	return pbVar(g, 7, uint64(boss.ComboApproxMs))
}

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

package lpn

import (
	"strconv"

	"cherry/internal/economy"
)

// Square boss fishing (Economy Phase DE). Wire notes: %TEMP%\opencode\cherry\phaseDE-20261022\boss-fishing-spec.md
// (sections 2, 3, 5 to 8 and 11) and %TEMP%\opencode\cherry\leftovers-20261026\boss-mp-spec.md (the lobby, the
// shared battle, the result). A try (cr45, or the BOSS! GO banner cr54) spends one of the day's tries and enters the
// boss world (rc6 theme 28) without a relaystart. The client then asks for the lobby (cr46 -> rc59). The lobby and
// its battle are shared by up to max_players members of one Square: square_boss_room.go holds placement, ready,
// start, leave, kick and invites; square_boss_room_battle.go holds the touches (cr50 -> rc64 to everyone), the
// loading barrier (cr55 -> rc73), the finish (cr58 -> rc65) and the results. Reset (cr54 with ResetTryCount1 ->
// rc72) refills the tries for Gems. rc74 (test mode) is never sent. The boss book (cr56 -> rc75, claim cr57 -> rc76)
// is answered here too. The tries, chart, boss, rewards and seat count are operator data (data/fishboss.json,
// economy/fishboss.go).
const (
	squareBossThemeID     = 28                // rc_room_enter_res ThemeId4 of the boss world (spec 11.1)
	squareBossMapFileName = squareMapFileName // the boss world loads the Square map: no boss map is in the client files (spec 11.2) [I]
	squareRcBossRooms     = 58                // rc_fish_boss_rooms_all: the boss icon on the Square
	squareBossRoomsReady  = 1                 // RoomCountReady1: the icon shows
	squareBossRoomState   = 1                 // rc59 RoomState1 of a lobby that can still be joined (spec 3.2; 1 is a lone-master value, spec 4.1)
	squareBossRoomRunning = 4                 // rc59 RoomState1 of a battle that started: above 3 shows "Participate" (spec 3.2)
	squareBossUnready     = 0                 // cr47 StateReq: a member leaves the ready state
	squareBossReadyReq    = 1                 // cr47 StateReq: a member is ready
	squareBossStateStart  = 2                 // cr47 StateReq: the master's start button
	squareBossNotReady    = 1                 // FishBossUserInLobby.State3: not ready
	squareBossReady       = 2                 // FishBossUserInLobby.State3: ready (the master always is)
	squareCoinGems        = 4                 // COST CoinType of Gems (spec 2.3)
	squareBossBookPages   = 1                 // boss book MaxPage: one page per boss, and fishboss.json holds one boss
	squareBossBookSeason  = 1                 // CollectionBossInfo season: fishboss.json has none, so always 1
)

// handleBoss answers the boss requests. A request that needs a lobby or a battle the session is not in is
// ignored without a reply, like the other room requests.
func (s *squareSession) handleBoss(frame []byte, msgid uint16, data []byte) error {
	aid := s.aid
	aidText := strconv.FormatUint(aid, 10)
	boss := economy.ActiveFishBoss()
	switch msgid {
	case 35: // cr_friend_invite_req {AidList1}: the master invites; no reply (the client shows its own "Invitation Complete")
		s.bossInvite(data)
		return nil
	case 36: // cr_room_enter_friend_req {FriendAid1 = the master, Revision2}: an invitee enters the master's lobby (rc6 theme 28)
		owner, _ := protobufUintField(data, 1)
		return s.reply(frame, 6, s.bossAccept(frame, owner))
	case 45: // cr_start_fishing_boss_try_req {BossTryRequest} -> rc_start_fishing_boss_try_res (57); a start opens a lobby of its own and enters the boss world (rc6)
		s.bossLeave()
		left, ok := economy.BossTryStart(aidText, economy.NowFunc())
		if !ok {
			s.logger.Printf("SQUARE fishing boss try refused aid=%d left=%d", aid, left)
			return s.reply(frame, 57, squareBossTry(false, boss, left))
		}
		s.bossOpen(frame)
		s.logger.Printf("SQUARE fishing boss try aid=%d left=%d theme=%d lobby=open", aid, left, squareBossThemeID)
		if err := s.reply(frame, 57, squareBossTry(true, boss, left)); err != nil {
			return err
		}
		return s.reply(frame, 6, squareRoomEnter(nil, squareBossThemeID, squareBossMapFileName))
	case 46: // cr_fishing_boss_roominfo_req (empty) -> rc_fishing_boss_roominfo_res (59): the lobby and its members
		body, ok := s.bossRoster()
		if !ok {
			s.logger.Printf("SQUARE fishing boss lobby ignored aid=%d no room", aid)
			return nil
		}
		s.logger.Printf("SQUARE fishing boss lobby aid=%d", aid)
		return s.reply(frame, 59, body)
	case 47: // cr_fishing_boss_update_room_state_req {StateReq1}: ready and unready (rc70); the master's start (rc71)
		state, _ := protobufUintField(data, 1)
		s.bossState(state)
		return nil
	case 48: // cr_fishing_boss_kickuser_req {KickAid1} -> rc_fishing_boss_kickuser_res (61) and rc70 for the kick
		return s.bossKick(frame, data)
	case 49: // cr_fishing_boss_play_begin_req {ClientTimeMs1} -> rc_fishing_boss_play_begin_res (63)
		if started, over := s.bossBattling(); !started || over {
			s.logger.Printf("SQUARE fishing boss play begin aid=%d ignored", aid)
			return nil
		}
		return s.reply(frame, 63, pbVar(nil, 1, squareFishOK))
	case 50: // cr_fishing_boss_play_touch_req {TouchInfo2 rows} -> rc_fishing_boss_play_touch_all (64) to every member
		return s.bossTouch(frame, data)
	case 54: // cr_move_fishboss_room_req {ResetTryCount1} -> rc_move_fishboss_room_res (72) (spec 12)
		// The BOSS! GO banner sends it empty: spend a try and join the boss lobby of this Square (rc72 Result 0 is
		// silent, rc6 theme 28 does the move). ResetTryCount1 true comes only from the client's recharge popup
		// (box 1811, opened by a Result 1 / Remain 0 reply) and refills the tries for Gems.
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
		s.bossLeave()
		left, ok := economy.BossTryStart(aidText, economy.NowFunc())
		if !ok {
			s.logger.Printf("SQUARE fishing boss try refused aid=%d left=%d reset=%d", aid, left, boss.ResetGems)
			if boss.ResetGems > 0 {
				return rc72(1, 0) // box 1811: out of tries, recharge for Gems?
			}
			// No reset offered: the client has no plain "no tries left" text; Result 1 with Remain > 0
			// shows box 1834 ("all rooms are full, try again") and hides the banner until the next enter.
			return rc72(1, 1)
		}
		s.bossJoinLobby(frame)
		s.logger.Printf("SQUARE fishing boss try aid=%d left=%d theme=%d", aid, left, squareBossThemeID)
		if err := rc72(0, left); err != nil {
			return err
		}
		return s.reply(frame, 6, squareRoomEnter(nil, squareBossThemeID, squareBossMapFileName))
	case 55: // cr_inGame_loading_complete_req (empty) -> rc_inGame_loading_complete_push (73) to this member
		if started, _ := s.bossBattling(); !started {
			s.logger.Printf("SQUARE fishing boss loaded aid=%d ignored", aid)
			return nil
		}
		return s.reply(frame, 73, pbVar(nil, 1, 1)) // Result1 [I]: the client ignores the value
	case 56: // cr_fish_boss_collection_req {PageNo1} -> rc_fish_boss_collection_res (75): one boss book page, clamped to the pages
		page, _ := protobufUintField(data, 1)
		page = min(max(page, 1), squareBossBookPages)
		caught, claimed := economy.BossBookState(aidText)
		s.logger.Printf("SQUARE fishing boss book aid=%d page=%d caught=%d claimed=%v", aid, page, caught, claimed)
		return s.reply(frame, 75, squareBossBook(boss, page, uint64(caught), claimed))
	case 57: // cr_complete_fish_boss_collection_req {PageNo1} -> rc_complete_fish_boss_collection_res (76): pays the page's book reward once
		page, _ := protobufUintField(data, 1)
		claimed := page >= 1 && page <= squareBossBookPages && economy.BossBookClaim(aidText)
		s.logger.Printf("SQUARE fishing boss book claim aid=%d page=%d paid=%v", aid, page, claimed)
		return s.reply(frame, 76, pbVar(nil, 1, squareBool(claimed)))
	case 58: // cr_inGame_finish_req (empty) -> rc_fishing_boss_play_end_all (65): this member's result, once
		return s.bossFinish(frame)
	case 60: // cr_fish_boss_lobby_emoticon_req {EmoticonIndex2} -> rc_fish_boss_lobby_emoticon_all (79) to every member
		s.bossSticker(data)
		return nil
	}
	return nil
}

// squareBossBook is rc_fish_boss_collection_res (75) for one page: MaxPage2, CurrentPage3, CompleteAlready6
// (the page's reward is claimed) and CollectionBossInfo7. The picture is the boss's fish picture, the same
// one the lobby and the battle show. Photos (CollectionBossInfo 6) and the reward picture (8) are not sent.
func squareBossBook(boss *economy.FishBoss, page, caught uint64, claimed bool) []byte {
	info := pbVar(nil, 1, squareBossBookSeason)
	info = pbVar(info, 2, caught)
	info = pbLen(info, 3, []byte(economy.FishImageURL(boss.Boss.ID)))
	info = pbLen(info, 4, []byte(boss.Boss.Name))
	info = pbLen(info, 5, []byte(boss.Boss.Explain))
	b := pbVar(nil, 1, squareFishOK)
	b = pbVar(b, 2, squareBossBookPages)
	b = pbVar(b, 3, page)
	b = pbVar(b, 6, squareBool(claimed))
	b = pbLen(b, 7, info)
	return pbLen(b, 9, []byte(economy.BossBookRewardText(boss)))
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

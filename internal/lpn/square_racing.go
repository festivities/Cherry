package lpn

import (
	"log"
	"strconv"
	"sync"
	"time"

	"cherry/internal/economy"
	"cherry/internal/room"
	"cherry/internal/store"
)

// Riding Pet Race, solo (Economy Phase DE; wire notes %TEMP%\opencode\cherry\phaseDE-20261022\racing-spec.md,
// sections 1 to 9 and the Follow-up F1 to F7). The entry is cr63 (home row "Riding Pet Info" -> rc82 with the
// owned Riding Pets), cr64 (rc83, the one game) and cr65 (rc107, no rooms). cr66 creates the solo room (rc84).
// The client's move cr94 gets rc6 into the racing world; the roster (rc110) is pushed on a timer after the
// world load, never with it. The master readies (cr86 -> rc110), starts (cr71 -> rc91 with the track and the
// position keys; a ticket is spent here), the client confirms (cr72 -> rc92, then rc97 after the countdown),
// the positions (cr76) decide the arrival (rc101, then rc102 after the finish countdown), or the race time
// ends it unranked. cr95 is never answered: its index is also rc_fashion_item_target_list_res.
const (
	squareRacingTheme      = 31           // rc6 ThemeId4 of the racing world (spec F2: the only riding-pet loader)
	squareRacingMap        = "2201202900" // petrace map files in the archive (spec F2)
	squareRacingGame       = 1            // MinigameId, MinigameMode, MinigameType and GameType of the one game [I] (spec 9)
	squareRacingMoveReason = 1            // PurposeMoveTheme.MoveReason, echoed by cr94 [I]
	squareRacingReady      = 4            // cr86 ReqType: ready (spec F1: the client sends 4 while the hero is not ready)
	squareRacingUnready    = 3            // cr86 ReqType: not ready
	squareRacingRefused    = 0x300004     // rc84 Result: box 7666 "You can't make the Room. Please try later." (spec 4.2)
)

// The race entrance is an _SQInteriorNpc NpcIndex 0x3010035 (NaWorldInteriorManager::Create case 53,
// NaAniButtonInterior): AniEventType (76) 4 draws the pet minigame bubble (world_icon 77_pet_minigame_r)
// and its tap sends cr64; AniButtonVisible (75) 1 shows it. Like the exchange office it links to a unique
// root floor the client keeps in its object cache: RUGO002JK (122003296) at tile (88,77), near the spawn
// (racing-spec "Square racing NPC"). The Square home MENU cannot reach "Riding Pet Info" from inside the
// Square, so this bubble is the entry.
const (
	squareRacingNpcIndex   = 0x3010035
	squareRacingNpcSerial  = 4
	squareRacingInteriorID = 122003296
	squareRacingNpcPX      = 6840 // X=40*(88-77+160)
	squareRacingNpcPY      = 3080 // Y=20*(2*160-1-88-77)
)

// squareRacingNpcEntry is one rc_room_enter_res NpcList entry: the race entrance.
func squareRacingNpcEntry() []byte {
	npc := squareInteriorNpc(squareRacingNpcSerial, squareRacingNpcIndex, squareRacingInteriorID, squareRacingNpcPX, squareRacingNpcPY)
	npc = pbVar(npc, 75, 1) // AniButtonVisible
	npc = pbVar(npc, 76, 4) // AniEventType 4: the minigame entrance (cr64)
	return pbLen(nil, 1, pbLen(nil, 2, npc))
}

// racePhase is where a session's race is. Every step is driven by a request or by a timer of the step before.
type racePhase int

const (
	raceCreated   racePhase = iota + 1 // rc84 sent: the move (cr94) is next
	raceLoading                        // rc6 sent: the roster follows the world load
	raceWaiting                        // roster pushed: ready and start
	raceStarting                       // rc91 sent: waiting for the client's ready-play (cr72)
	raceCountdown                      // rc92 sent: rc97 follows the countdown
	raceRacing                         // rc97 sent: positions and the race time
	raceArrived                        // rc101 sent: rc102 follows the finish countdown
	raceDone                           // rc102 sent
)

// squareRace is one solo race of a session. Its timers push on their own goroutines, so every field is
// guarded by mu; the session's handler takes mu for each request it answers in the race.
type squareRace struct {
	mu      sync.Mutex
	logger  *log.Logger
	conn    *lockedConn
	b1      byte    // frame header of the session, for pushes from timers
	sid     [8]byte // frame header of the session, for pushes from timers
	aid     uint64
	pet     store.PetItem
	phase   racePhase
	ready   bool
	outcome economy.RaceOutcome // set at the arrival, reported by rc102
	stopped bool
	timers  []*time.Timer
}

// push writes one frame of the race (the caller holds mu, or the race is not published yet).
func (r *squareRace) push(msgid uint16, body []byte) error {
	_, err := r.conn.Write(squareFrame(r.b1, r.sid[:], msgid, body))
	return err
}

// pushLogged is push for timers, which have no reply to return an error to.
func (r *squareRace) pushLogged(msgid uint16, body []byte) {
	if err := r.push(msgid, body); err != nil {
		r.logger.Printf("SQUARE racing push msgid=%d aid=%d err=%v", msgid, r.aid, err)
	}
}

// after runs fn under mu after d, unless the race has stopped by then. The caller holds mu.
func (r *squareRace) after(d time.Duration, fn func()) {
	r.timers = append(r.timers, time.AfterFunc(d, func() {
		r.mu.Lock()
		defer r.mu.Unlock()
		if !r.stopped {
			fn()
		}
	}))
}

// stop ends the race for good: no timer fires and nothing more is pushed. It returns the phase it stopped in.
func (r *squareRace) stop() racePhase {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.stopped = true
	for _, t := range r.timers {
		t.Stop()
	}
	r.timers = nil
	return r.phase
}

// squareAccountName is the account's name for a racer, "cherry" when the account is unknown.
func squareAccountName(aid uint64) string {
	if acc, ok := store.AccountByAvatarID(strconv.FormatUint(aid, 10)); ok {
		return acc.Name
	}
	return "cherry"
}

// racingPetID is the numeric id of a Riding Pet (PetRaceId, 3264xxxxx).
func racingPetID(p store.PetItem) uint64 { return uint64(room.PetSkinID(p.Cd)) }

// racingChoose is the pet a racer uses: the remembered one when still owned, else the first owned one.
func racingChoose(pets []store.PetItem, code string) (store.PetItem, bool) {
	for _, p := range pets {
		if p.Cd == code {
			return p, true
		}
	}
	if len(pets) == 0 {
		return store.PetItem{}, false
	}
	return pets[0], true
}

// racingPetStat is PetStat (power, speed, stamina, item slot), from the operator data.
func racingPetStat() []byte {
	s := economy.ActiveRacing().PetStat
	b := pbVar(nil, 1, uint64(s.Power))
	b = pbVar(b, 2, uint64(s.Speed))
	b = pbVar(b, 3, uint64(s.Stamina))
	return pbVar(b, 4, uint64(s.ItemSlot))
}

// racingPetAppearance is MiniGamePetAppearance (rc109 Mypet2, rc110 Mypet6).
func racingPetAppearance(p store.PetItem) []byte {
	b := pbVar(nil, 1, racingPetID(p))
	b = pbLen(b, 2, []byte(p.Cd))
	b = pbLen(b, 3, []byte(p.Name))
	return pbLen(b, 4, racingPetStat())
}

// racingPetInfo is MiniGamePetInfo, one row of the Riding Pet picker (rc82 PetInfo3).
func racingPetInfo(p store.PetItem) []byte {
	b := pbVar(nil, 1, racingPetID(p))
	b = pbVar(b, 2, uint64(p.ID))
	b = pbLen(b, 3, []byte(p.Cd))
	b = pbLen(b, 4, []byte(p.Name))
	return pbLen(b, 7, racingPetStat())
}

// racingTicket is MiniGamePlayTicketInfo: the tickets left, the day's allowance and, while a ticket is
// missing, the seconds to the next game day (the tickets come back at 06:00 JST).
func racingTicket(st economy.RaceStatus) []byte {
	b := pbVar(nil, 1, uint64(st.Tickets))
	b = pbVar(b, 2, uint64(st.MaxTickets))
	if st.Tickets < st.MaxTickets {
		b = pbVar(b, 3, uint64(st.ResetSec))
	}
	return b
}

// racingUserInfo is MiniGameUserInfo (rc82 UserInfo5): the racer's level and grade, the tickets and the MMR.
func racingUserInfo(aid uint64, st economy.RaceStatus) []byte {
	p := pbLen(nil, 3, []byte(squareAccountName(aid)))
	p = pbVar(p, 5, uint64(st.Level))
	p = pbVar(p, 6, uint64(st.LevelExp))
	p = pbVar(p, 7, uint64(st.LevelExpMax))
	p = pbVar(p, 8, uint64(st.Grade))
	p = pbLen(p, 9, []byte(st.GradeName))
	p = pbVar(p, 10, uint64(st.Level+1))
	b := pbLen(nil, 1, p)
	b = pbLen(b, 2, racingTicket(st))
	return pbLen(b, 4, pbVar(nil, 1, uint64(st.Mmr)))
}

// racingGrades is the rc82 Grade6 list: each grade's name and MMR range (the last one tops out at the cap).
func racingGrades() [][]byte {
	grades := economy.ActiveRacing().Grades
	out := make([][]byte, 0, len(grades))
	for i, g := range grades {
		top := uint64(store.MaxBalance)
		if i+1 < len(grades) {
			top = uint64(grades[i+1].MmrMin - 1)
		}
		b := pbLen(nil, 1, []byte(g.Name))
		b = pbVar(b, 2, uint64(g.MmrMin))
		out = append(out, pbVar(b, 3, top))
	}
	return out
}

// racingArea is MapArea (begin and end, x then y) for a track area.
func racingArea(a economy.RacingArea) []byte {
	b := pbVar(nil, 1, uint64(a.BeginX))
	b = pbVar(b, 2, uint64(a.BeginY))
	b = pbVar(b, 3, uint64(a.EndX))
	return pbVar(b, 4, uint64(a.EndY))
}

// racingPurpose is PurposeMoveTheme: the move reason and the racing world's theme.
func racingPurpose() []byte {
	return pbVar(pbVar(nil, 1, squareRacingMoveReason), 2, squareRacingTheme)
}

// racingRoster is rc_rpr_user_update_push (110) for the solo room: the master alone, with the ready flag and
// the pet. ReqType 1 replaces the roster; 3 and 4 only set the ready flag of TargetAid (spec F1).
func (r *squareRace) racingRoster(reqType uint64, now time.Time) []byte {
	st := economy.RaceStatusOf(strconv.FormatUint(r.aid, 10), now)
	info := pbLen(nil, 1, []byte("1")) // RoomId1: a string per spec F1 (section 3 said int; an unread field is skipped)
	info = pbVar(info, 2, squareRacingGame)
	info = pbVar(info, 3, squareRacingGame)
	info = pbVar(info, 5, 1) // CurrentUserCount
	info = pbVar(info, 6, 1) // MinUserCount: the start needs only the master
	info = pbVar(info, 7, 1) // MaxUserCount
	info = pbVar(info, 10, uint64(now.UnixMilli()))
	info = pbVar(info, 11, squareRacingGame)
	member := pbVar(nil, 1, r.aid)
	member = pbVar(member, 2, 1) // IsRoomMaster
	member = pbVar(member, 3, squareBool(r.ready))
	member = pbLen(member, 7, []byte(squareAccountName(r.aid)))
	member = pbVar(member, 9, racingPetID(r.pet))
	member = pbLen(member, 10, []byte(r.pet.Cd))
	member = pbVar(member, 11, uint64(st.Level))
	member = pbVar(member, 12, uint64(st.LevelExp))
	member = pbVar(member, 13, uint64(st.Grade))
	member = pbLen(member, 14, []byte(st.GradeName))
	body := pbVar(nil, 1, reqType)
	body = pbVar(body, 2, r.aid)
	body = pbVar(body, 3, r.aid)
	body = pbLen(body, 4, info)
	body = pbLen(body, 5, member)
	return pbLen(body, 6, racingPetAppearance(r.pet))
}

// racingLoading is rc_rpr_ready_play (91): the racer on the start area with its pet, the finish area, the
// position keys (one per position report) and the race time.
func (r *squareRace) racingLoading() []byte {
	racing := economy.ActiveRacing()
	pet := pbVar(nil, 1, racingPetID(r.pet))
	pet = pbLen(pet, 2, racingPetStat())
	pet = pbLen(pet, 3, []byte(r.pet.Cd))
	player := pbVar(nil, 1, 1) // IsRoomMaster
	player = pbVar(player, 2, r.aid)
	player = pbLen(player, 4, racingArea(racing.Track.Start))
	player = pbLen(player, 5, pet)
	player = pbLen(player, 6, racingArea(racing.Track.Finish))
	body := pbVar(nil, 1, 0)
	body = pbLen(body, 2, player)
	for k := 1; k <= racing.PosKeyCount; k++ {
		body = pbVar(body, 4, uint64(k))
	}
	return pbVar(body, 7, uint64(racing.MaxRaceSec))
}

// reachedFinish reports whether tile (x, y) reached the finish: along the axis where start and finish lie
// furthest apart, in the direction of the finish, up to one tile short.
func reachedFinish(start, finish economy.RacingArea, x, y int64) bool {
	dx := finish.BeginX + finish.EndX - start.BeginX - start.EndX // twice the centre distance, per axis
	dy := finish.BeginY + finish.EndY - start.BeginY - start.EndY
	pos, goal, d := x, (finish.BeginX+finish.EndX)/2, dx
	if max(dy, -dy) > max(dx, -dx) {
		pos, goal, d = y, (finish.BeginY+finish.EndY)/2, dy
	}
	if d < 0 {
		return pos-1 <= goal
	}
	return pos+1 >= goal
}

// racingResult is rc_rpr_racing_result (102): the race (rank 1 arrived, 0 unranked), the level and grade,
// the tickets left and the store points. The Gems are paid by the ledger and have no field here.
func racingResult(aid, rank uint64, o economy.RaceOutcome) []byte {
	a, b := o.After, o.Before
	levelUp := squareBool(a.Level > b.Level)
	race := pbVar(nil, 1, aid)
	race = pbVar(race, 2, rank)
	race = pbVar(race, 3, uint64(a.Level))
	race = pbVar(race, 4, uint64(a.LevelExp))
	race = pbVar(race, 5, uint64(a.LevelExpMax))
	race = pbVar(race, 6, levelUp)
	race = pbVar(race, 7, uint64(a.Mmr))
	race = pbVar(race, 8, uint64(a.Grade))
	lvl := pbVar(nil, 1, levelUp)
	lvl = pbVar(lvl, 2, uint64(a.Level))
	lvl = pbVar(lvl, 3, uint64(a.Grade))
	lvl = pbVar(lvl, 4, squareBool(a.Grade != b.Grade))
	lvl = pbLen(lvl, 5, []byte(a.GradeName))
	lvl = pbVar(lvl, 7, uint64(b.Level))
	lvl = pbVar(lvl, 8, uint64(b.LevelExp))
	lvl = pbVar(lvl, 9, uint64(b.LevelExpMax))
	ticket := pbVar(nil, 2, uint64(a.MaxTickets)) // ChargeMax2
	ticket = pbVar(ticket, 4, uint64(a.Tickets))  // BeforeCount4: the tickets left; ItemId5 and RetryNeedCount6 stay 0
	body := pbLen(nil, 1, race)
	body = pbLen(body, 2, lvl)
	body = pbLen(body, 4, ticket)
	return pbVar(body, 5, uint64(a.Points))
}

// racingGameList is rc_get_active_minigame_info_list_res (83): the one game and the ticket state.
func racingGameList(st economy.RaceStatus) []byte {
	game := pbVar(nil, 1, squareRacingGame)
	game = pbVar(game, 2, squareRacingGame)
	game = pbLen(game, 4, pbLen(nil, 2, []byte(economy.ActiveRacing().Title))) // MinigameTitleInfo: MinigameTitleString2
	game = pbLen(game, 5, pbVar(nil, 1, squareRacingGame))                     // GameInfo: GameType1, no entry cost
	body := pbVar(nil, 1, 0)
	body = pbLen(body, 4, racingTicket(st))
	return pbLen(body, 5, game)
}

// racingRoomList is rc_get_minigame_room_list_res (107): the title and no rooms (the solo room is only the
// racer's own).
func racingRoomList() []byte {
	title := []byte(economy.ActiveRacing().Title)
	body := pbVar(nil, 1, 0)
	body = pbLen(body, 2, title)
	return pbLen(body, 3, title)
}

// racingCreated is rc_update_minigame_room_res (84) for a created room.
func racingCreated() []byte {
	body := pbVar(nil, 1, 0)
	body = pbVar(body, 2, squareRacingGame)
	body = pbVar(body, 3, 1) // IsCreate
	body = pbLen(body, 4, racingPurpose())
	return pbVar(body, 5, squareRacingGame)
}

// stopRacing ends the session's race (leaving the Square, a new race): its timers stop and nothing more is
// pushed. The race state is not kept: the next race starts from cr66.
func (s *squareSession) stopRacing() {
	if s.racing == nil {
		return
	}
	phase := s.racing.stop()
	s.racing = nil
	s.logger.Printf("SQUARE racing exit aid=%d phase=%d", s.aid, phase)
}

// racingRelayStarted re-sends the roster a moment after the racing world's relay start: the timed roster can reach
// the client before its world load ends, and then the waiting room never opens (no READY; device 2026-10-27).
// ReqType 1 replaces the whole roster, so a second copy is harmless.
func (s *squareSession) racingRelayStarted() {
	r := s.racing
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.after(time.Second, func() {
		if r.phase != raceWaiting {
			return
		}
		r.logger.Printf("SQUARE racing roster aid=%d reqtype=1 resent", r.aid)
		r.pushLogged(110, r.racingRoster(1, economy.NowFunc()))
	})
}

// handleRacing answers the racing requests of a session in the Square room (dispatched from handleData).
func (s *squareSession) handleRacing(frame []byte, msgid uint16, data []byte) error {
	aid := strconv.FormatUint(s.aid, 10)
	now := economy.NowFunc()
	switch msgid {
	case 63: // cr_get_my_minigame_play_info_req -> rc_get_my_minigame_play_info_res (82)
		pets := economy.RidePets(aid)
		st := economy.RaceStatusOf(aid, now)
		pet, ok := racingChoose(pets, s.racePet)
		selected := ^uint64(0) // -1: no Riding Pet, the client's default
		if ok {
			selected = racingPetID(pet)
		}
		body := pbVar(nil, 1, 0)
		body = pbVar(body, 2, selected)
		for _, p := range pets {
			body = pbLen(body, 3, racingPetInfo(p))
		}
		body = pbLen(body, 5, racingUserInfo(s.aid, st))
		for _, g := range racingGrades() {
			body = pbLen(body, 6, g)
		}
		s.logger.Printf("SQUARE racing enter aid=%d pets=%d tickets=%d", s.aid, len(pets), st.Tickets)
		return s.reply(frame, 82, body)
	case 64: // cr_get_active_minigame_info_list_req -> rc_get_active_minigame_info_list_res (83)
		s.logger.Printf("SQUARE racing games aid=%d", s.aid)
		return s.reply(frame, 83, racingGameList(economy.RaceStatusOf(aid, now)))
	case 65: // cr_get_minigame_room_list_req -> rc_get_minigame_room_list_res (107)
		s.logger.Printf("SQUARE racing rooms aid=%d", s.aid)
		return s.reply(frame, 107, racingRoomList())
	case 66: // cr_update_minigame_room_req {IsCreate1} -> rc_update_minigame_room_res (84)
		return s.racingCreate(frame, data)
	case 82: // cr_switch_entry_riding_pet_req {PetRaceId1} -> rc_switch_entry_riding_pet_res (109)
		return s.racingPick(frame, data)
	case 68: // cr_get_minigame_tutorial_info_req (empty), sent by the game-select page -> rc86: no tutorials
		s.logger.Printf("SQUARE racing tutorial aid=%d", s.aid)
		return s.reply(frame, 86, nil)
	}
	r := s.racing
	if r == nil {
		s.logger.Printf("SQUARE racing msgid=%d ignored aid=%d no race", msgid, s.aid)
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	racing := economy.ActiveRacing()
	switch msgid {
	case 94: // cr_move_Ingame_theme_req -> rc_room_enter_res (6) into the racing world; the roster follows the load
		if r.phase != raceCreated {
			s.logger.Printf("SQUARE racing move ignored aid=%d phase=%d", s.aid, r.phase)
			return nil
		}
		r.phase = raceLoading
		s.logger.Printf("SQUARE racing move aid=%d theme=%d map=%s", s.aid, squareRacingTheme, squareRacingMap)
		if err := r.push(6, squareRoomEnter(nil, squareRacingTheme, squareRacingMap)); err != nil {
			return err
		}
		r.after(time.Duration(racing.RosterDelayMs)*time.Millisecond, func() {
			if r.phase != raceLoading {
				return
			}
			r.phase = raceWaiting
			r.logger.Printf("SQUARE racing roster aid=%d reqtype=1", r.aid)
			r.pushLogged(110, r.racingRoster(1, economy.NowFunc()))
		})
		return nil
	case 86: // cr_minigame_update_user_state_req {MinigameId1, ReqType2} -> rc_rpr_user_update_push (110): the ready flag
		req, _ := protobufUintField(data, 2)
		if r.phase != raceWaiting || req != squareRacingReady && req != squareRacingUnready {
			s.logger.Printf("SQUARE racing ready ignored aid=%d phase=%d req=%d", s.aid, r.phase, req)
			return nil
		}
		r.ready = req == squareRacingReady
		s.logger.Printf("SQUARE racing ready aid=%d ready=%v", s.aid, r.ready)
		return r.push(110, r.racingRoster(req, now))
	case 71: // cr_req_rpr_start_play -> rc_rpr_ready_play (91); the ticket is spent here
		if r.phase != raceWaiting || !r.ready {
			s.logger.Printf("SQUARE racing start ignored aid=%d phase=%d ready=%v", s.aid, r.phase, r.ready)
			return nil
		}
		left, ok := economy.RaceStart(aid, now)
		if !ok {
			s.logger.Printf("SQUARE racing start refused aid=%d no ticket", s.aid)
			return r.push(91, pbVar(nil, 1, 1)) // Result 1: the client only hides its spinner
		}
		r.phase = raceStarting
		s.logger.Printf("SQUARE racing start aid=%d tickets=%d", s.aid, left)
		return r.push(91, r.racingLoading())
	case 72: // cr_rpr_ready_play, sent by the client ~3 s after rc91 -> rc_rpr_start_countdown (92), then rc97
		if r.phase != raceStarting {
			s.logger.Printf("SQUARE racing ready-play ignored aid=%d phase=%d", s.aid, r.phase)
			return nil
		}
		r.phase = raceCountdown
		s.logger.Printf("SQUARE racing countdown aid=%d sec=%d", s.aid, racing.CountdownSec)
		if err := r.push(92, pbVar(nil, 1, uint64(racing.CountdownSec))); err != nil {
			return err
		}
		r.after(time.Duration(racing.CountdownSec)*time.Second, func() {
			if r.phase != raceCountdown {
				return
			}
			r.phase = raceRacing
			r.logger.Printf("SQUARE racing go aid=%d", r.aid)
			r.pushLogged(97, nil)
			r.after(time.Duration(racing.MaxRaceSec)*time.Second, func() {
				if r.phase != raceRacing {
					return
				}
				out, _ := economy.RaceFinish(aid, false, economy.NowFunc())
				r.phase = raceDone
				r.logger.Printf("SQUARE racing timeout aid=%d unranked", r.aid)
				r.pushLogged(102, racingResult(r.aid, 0, out))
			})
		})
		return nil
	case 76: // cr_rpr_racing_pos {MoveInfo1, TilePos2, PosKey3, Speed4, ClientTimeMs5}: the arrival test
		if r.phase != raceRacing {
			return nil
		}
		tile, found, ok := pbBytesField(data, 2)
		if !ok || !found {
			return nil
		}
		x, _ := protobufUintField(tile, 1)
		y, _ := protobufUintField(tile, 2)
		s.logger.Printf("SQUARE racing pos aid=%d tile=%d,%d", s.aid, x, y)
		// The hero runs from the start to the finish along the course's main axis (X or Y, either way); the
		// client's tile read-back truncates, so the last report may be one tile short (racing-spec "Track geometry").
		if !reachedFinish(racing.Track.Start, racing.Track.Finish, int64(x), int64(y)) {
			return nil
		}
		out, paid := economy.RaceFinish(aid, true, now)
		if !paid {
			st := economy.RaceStatusOf(aid, now)
			out = economy.RaceOutcome{Before: st, After: st}
		}
		r.outcome = out
		r.phase = raceArrived
		s.logger.Printf("SQUARE racing arrive aid=%d tile=%d gems=%d", s.aid, x, out.Gems)
		if err := r.push(101, pbVar(pbVar(pbVar(nil, 1, r.aid), 2, 1), 3, uint64(racing.FinishCountdownSec))); err != nil {
			return err
		}
		r.after(time.Duration(racing.FinishCountdownSec)*time.Second, func() {
			if r.phase != raceArrived {
				return
			}
			r.phase = raceDone
			r.logger.Printf("SQUARE racing result aid=%d rank=1 gems=%d", r.aid, r.outcome.Gems)
			r.pushLogged(102, racingResult(r.aid, 1, r.outcome))
		})
		return nil
	case 79: // cr_req_rpr_play_again {ReqType1} -> rc_rpr_play_again (106): the next move starts the race again
		if r.phase != raceDone {
			s.logger.Printf("SQUARE racing again ignored aid=%d phase=%d", s.aid, r.phase)
			return nil
		}
		if economy.RaceStatusOf(aid, now).Tickets < 1 {
			s.logger.Printf("SQUARE racing again refused aid=%d no ticket", s.aid)
			return r.push(106, pbVar(nil, 1, squareRacingRefused))
		}
		r.phase, r.ready = raceCreated, false
		s.logger.Printf("SQUARE racing again aid=%d", s.aid)
		return r.push(106, pbLen(pbVar(nil, 1, 0), 2, racingPurpose()))
	}
	return nil
}

// racingCreate answers cr66: the solo room is created (rc84 IsCreate) when the account has a Riding Pet and
// a ticket to spend. Otherwise the create is refused with box 7666 (no riding-pet text is sent, spec 4.2).
func (s *squareSession) racingCreate(frame []byte, data []byte) error {
	aid := strconv.FormatUint(s.aid, 10)
	now := economy.NowFunc()
	if isCreate, _ := protobufUintField(data, 1); isCreate != 1 {
		s.logger.Printf("SQUARE racing create ignored aid=%d not a create", s.aid)
		return nil
	}
	pets := economy.RidePets(aid)
	st := economy.RaceStatusOf(aid, now)
	pet, ok := racingChoose(pets, s.racePet)
	if !ok || st.Tickets < 1 {
		s.logger.Printf("SQUARE racing create refused aid=%d pets=%d tickets=%d", s.aid, len(pets), st.Tickets)
		return s.reply(frame, 84, pbVar(nil, 1, squareRacingRefused))
	}
	s.stopRacing()
	s.racePet = pet.Cd
	r := &squareRace{logger: s.logger, conn: s.conn, b1: frame[1], aid: s.aid, pet: pet, phase: raceCreated}
	copy(r.sid[:], frame[2:10])
	s.racing = r
	s.logger.Printf("SQUARE racing create aid=%d pet=%s tickets=%d", s.aid, pet.Cd, st.Tickets)
	return s.reply(frame, 84, racingCreated())
}

// racingPick answers cr82: the racer's pet for the next race (rc109). A pet the account does not own is refused.
func (s *squareSession) racingPick(frame []byte, data []byte) error {
	id, _ := protobufUintField(data, 1)
	for _, p := range economy.RidePets(strconv.FormatUint(s.aid, 10)) {
		if racingPetID(p) == id {
			s.racePet = p.Cd
			s.logger.Printf("SQUARE racing pet aid=%d pet=%s", s.aid, p.Cd)
			return s.reply(frame, 109, pbLen(pbVar(nil, 1, 0), 2, racingPetAppearance(p)))
		}
	}
	s.logger.Printf("SQUARE racing pet refused aid=%d id=%d", s.aid, id)
	return s.reply(frame, 109, pbVar(nil, 1, 1))
}

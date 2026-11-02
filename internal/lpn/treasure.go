package lpn

import (
	"encoding/binary"
	"errors"
	"fmt"
	"log"
	"slices"
	"strconv"
	"sync"
	"time"

	"cherry/internal/economy"
	"cherry/internal/store"
)

// Treasure World (Blue Marble board game): LPN namespace MONOPOLY, gateway agent 9, tunnel "1.1.114.0" (sid
// 0x0001000100720000). Specs in %TEMP%\opencode\cherry\treasure-20261102 (treasure-spec.md section 9, transport-money.md
// sections 1 and 7, cells.md, season-end-spec.md section 12). Inbound msgid = dispatch index, outbound msgid = GetIndex; the
// two numberings differ (the exit is cs 2 and sc 3, the relay start is cs 3 and sc 2). Slice 1: login, room enter, relay
// start, the roll, the tutorial flag, the exit and the coin count. The season cycle (economy TreasureCurrent): the play
// window is the board; the reward period (status 2, no cells) takes the claims; a closed cycle answers the 65541 texts.
// Every other request gets the answer treasureStubs names.
const (
	treasureAgent = 9

	treasureRefused         = 1      // a refused request: any non-zero Result is the client's error path
	treasureSeasonPlay      = 1      // sc_room_enter_res SeasonStatus: the board is playable (treasure-spec 9.3)
	treasureSeasonEnded     = 2      // sc_room_enter_res SeasonStatus: no board, the claims only (season-end-spec 3)
	treasureSeasonNotActive = 65541  // sc_season_active_state_res: Result 65541 clears the season-end flag (transport-money 2.5)
	treasureRewardPopup     = 786435 // sc_ranking_class_reward_res: opens the reward popup (season-end-spec 9.2)

	treasureBuffGacha     = 1950000101 // CellTypeBuff GachaId: a Cherry id in the Treasure block
	treasureBuffGachaName = "Treasure Buffs"
	// treasureShopLink is the LinkInfo of a buff's and a secret quest's button: the Gacha Shop popup (popup links keep the board).
	treasureShopLink = "goSomewhere(gachaShop)"
)

// Outbound (client to server) msgids, cs GetIndex.
const (
	treasureCsLogin         = 0
	treasureCsRoomEnter     = 1
	treasureCsRoomExit      = 2
	treasureCsRelayStart    = 3
	treasureCsRolling       = 4
	treasureCsBuffList      = 5
	treasureCsMiniGame      = 8
	treasureCsFlyTravel     = 9
	treasureCsGoldKeyUse    = 10
	treasureCsBuffWear      = 22
	treasureCsSpecialReward = 25
	treasureCsSpecialEnable = 26
	treasureCsTutorial      = 23
	treasureCsCoinRefresh   = 29
	treasureCsRewardExit    = 28 // cs_season_reward_popup_exit_check_req: the back key of the reward popup
)

// Inbound (server to client) msgids, sc dispatch index.
const (
	treasureRcLogin             = 0
	treasureRcRoomEnter         = 1
	treasureRcRelayStart        = 2
	treasureRcRoomExit          = 3
	treasureRcRolling           = 4
	treasureRcBuffList          = 5
	treasureRcPointChange       = 6
	treasureRcPointReset        = 7
	treasureRcMiniGame          = 8
	treasureRcFlyTravel         = 9
	treasureRcGoldKey           = 10
	treasureRcRankingInfo       = 11
	treasureRcPointRewardInfo   = 12
	treasureRcClassRewardInfo   = 13
	treasureRcPointReward       = 14
	treasureRcClassReward       = 15
	treasureRcQuestList         = 17
	treasureRcAchievementReward = 18
	treasureRcQuestReward       = 19
	treasureRcQuestRefresh      = 20
	treasureRcLimitInfo         = 21
	treasureRcLimitBuy          = 22
	treasureRcBuffWear          = 23
	treasureRcTutorial          = 24
	treasureRcSeasonExit        = 25 // sc_season_exit_push: the play window ended under an open board (season-end-spec 6)
	treasureRcSpecialReward     = 29
	treasureRcSpecialEnable     = 31
	treasureRcResetInfo         = 32
	treasureRcSeasonState       = 33
	treasureRcCoinRefresh       = 35
	treasureRcNoReply           = 0 // treasureStub.rc of a request that gets no reply
)

// treasureStub is the answer to a request of a later slice: the sc msgid and the body. The bodies are the shapes the
// client handles without a hang (treasure-spec 9.5, transport-money 3.6): a refusal carries Result 1, a list an empty
// success (Result 0, no rows), and the replies with no result branch are empty.
type treasureStub struct {
	rc   uint16
	body []byte
}

var (
	treasureOK = pbVar(nil, 1, 0)
	treasureNo = pbVar(nil, 1, treasureRefused)
)

// treasureStubs maps each outbound msgid of a later slice to its answer. Keys are cs msgids. The buff, mini-game and secret quest
// requests are built (buffs and secret quests in treasure_buff.go and treasure_secret.go).
var treasureStubs = map[uint16]treasureStub{
	24: {treasureRcNoReply, nil}, // cs_playticket_sync_req: no caller in the client
}

// treasureAfter schedules the season-end push. Tests replace it to fire the push without waiting for the clock.
var treasureAfter = time.AfterFunc

// treasureSession is one agent-9 connection: the account logged in on it and whether its board is entered. The season-end
// push runs on a timer goroutine, so the fields it reads (mu, the frame header, the timer) are guarded by mu.
type treasureSession struct {
	conn     *lockedConn
	logger   *log.Logger
	aid      uint64
	loggedIn bool
	entered  bool

	mu       sync.Mutex
	hdrType  byte    // the type byte of the latest data request (the push reuses it)
	hdrSid   [8]byte // the session id of the latest data request
	endGen   uint64  // bumped by every arm and stop; a timer fires only while its generation is current
	endTimer *time.Timer
}

func (s *treasureSession) aidText() string { return strconv.FormatUint(s.aid, 10) }

// handleOpen answers the client's subtype-1 open control with the one 22-byte subtype-1 echo of its type byte and session
// id (transport-money 1.2). It never sends subtype 2 to agent 9: the client would leave the board for the Garden. An open
// starts a new tunnel, so the login state goes too; the client logs in again on the echo.
func (s *treasureSession) handleOpen(frame []byte) error {
	s.loggedIn, s.entered, s.aid = false, false, 0
	s.stopSeasonEnd()
	control := make([]byte, 22)
	binary.BigEndian.PutUint32(control, 18)
	control[4], control[5] = 'G', frame[1]
	copy(control[6:14], frame[2:10])
	_, err := s.conn.Write(control)
	s.logger.Printf("TREASURE open-control err=%v", err)
	return err
}

// reply writes one data frame answering the request frame (its type byte and session id).
func (s *treasureSession) reply(frame []byte, msgid uint16, body []byte) error {
	_, err := s.conn.Write(squareFrame(frame[1], frame[2:10], msgid, body))
	return err
}

// refuse answers msgid with Result 1, the client's error path for the request.
func (s *treasureSession) refuse(frame []byte, msgid uint16) error {
	return s.reply(frame, msgid, treasureNo)
}

// handleData processes one agent-9 data frame (frame[10:12] msgid, frame[12:] protobuf).
func (s *treasureSession) handleData(frame []byte) error {
	msgid := binary.BigEndian.Uint16(frame[10:12])
	data := frame[12:]
	s.mu.Lock()
	s.hdrType = frame[1]
	copy(s.hdrSid[:], frame[2:10])
	s.mu.Unlock()
	switch msgid {
	case treasureCsLogin:
		return s.login(frame, data)
	case treasureCsRoomEnter:
		return s.enter(frame)
	case treasureCsRoomExit:
		return s.exit(frame)
	case treasureCsRelayStart:
		return s.relayStart(frame)
	case treasureCsRolling:
		return s.roll(frame)
	case treasureCsBuffList:
		return s.buffList(frame)
	case treasureCsMiniGame:
		return s.miniGame(frame, data)
	case treasureCsFlyTravel:
		return s.flyTravel(frame, data)
	case treasureCsGoldKeyUse:
		return s.goldKeyUse(frame)
	case treasureCsBuffWear:
		return s.buffWear(frame, data)
	case treasureCsSpecialReward:
		return s.specialReward(frame)
	case treasureCsSpecialEnable:
		return s.specialEnable(frame)
	case treasureCsTutorial:
		return s.tutorial(frame)
	case treasureCsCoinRefresh:
		return s.coinRefresh(frame)
	case treasureCsPointChange:
		return s.pointChange(frame, data)
	case treasureCsPointReset:
		return s.pointReset(frame, data)
	case treasureCsRankingInfo:
		return s.rankingInfo(frame, data)
	case treasureCsPointRewardInfo:
		return s.pointRewardInfo(frame)
	case treasureCsClassRewardInfo:
		return s.classRewardInfo(frame)
	case treasureCsPointRewardClaim:
		return s.pointRewardClaim(frame, data)
	case treasureCsClassRewardClaim:
		return s.classRewardClaim(frame)
	case treasureCsQuestList:
		return s.questList(frame)
	case treasureCsMissionClaim:
		return s.missionClaim(frame, data)
	case treasureCsQuestClaim:
		return s.questClaim(frame, data)
	case treasureCsQuestRefresh:
		return s.questRefresh(frame, data)
	case treasureCsCapInfo:
		return s.capInfo(frame)
	case treasureCsCapBuy:
		return s.capBuy(frame)
	case treasureCsResetInfo:
		return s.resetInfo(frame)
	case treasureCsRewardExit:
		return s.rewardExit(frame)
	}
	return s.notBuilt(frame, msgid)
}

// login answers cs_login_req (msgid 0) with Result 0, the client's success path, for every request, as Circle does. The
// account is known only when the session names an existing aid; enter refuses a board for anyone else.
func (s *treasureSession) login(frame, data []byte) error {
	aid, _ := protobufUintField(data, 2)
	s.loggedIn, s.entered, s.aid = false, false, 0
	if gardenLoginValid(data) && aid > 0 && aid <= (1<<63)-1 {
		if _, ok := store.AccountByAvatarID(strconv.FormatUint(aid, 10)); ok {
			s.aid, s.loggedIn = aid, true
		}
	}
	s.logger.Printf("TREASURE msgid=0 login aid=%d known=%v result=0", aid, s.loggedIn)
	return s.reply(frame, treasureRcLogin, treasureOK)
}

// enter answers cs_room_enter_req (msgid 1). In the play window: a playable season (status 1), the ring and the tutorial
// flag, and the board's season-end push is armed for the window's end. In the reward period or when the cycle is closed:
// status 2 with no ring, so the client goes to the claims (season-end-spec 12 (b) and (c)). A refusal (no login, a store
// error) is Result 1, which the client answers by returning to the Garden.
func (s *treasureSession) enter(frame []byte) error {
	if !s.loggedIn {
		s.logger.Printf("TREASURE msgid=1 enter refused: no login")
		return s.refuse(frame, treasureRcRoomEnter)
	}
	now := economy.NowFunc()
	cyc := economy.TreasureCurrent(now)
	d := economy.ActiveTreasureData()
	if cyc.Phase != economy.TreasurePhasePlay {
		s.entered = false
		s.stopSeasonEnd()
		view := economy.TreasurePeek(s.aidText(), now)
		s.logger.Printf("TREASURE msgid=1 enter aid=%d status=%d phase=%s season=%d cells=0 tutorial=%v result=0",
			s.aid, treasureSeasonEnded, treasurePhaseName(cyc.Phase), cyc.ID, view.Tutorial)
		return s.reply(frame, treasureRcRoomEnter, treasureEnterBody(d, cyc, treasureSeasonEnded, view))
	}
	view, err := economy.TreasureOpen(s.aidText(), now)
	if err != nil {
		s.entered = false
		s.stopSeasonEnd()
		s.logger.Printf("TREASURE msgid=1 enter refused aid=%d: %v", s.aid, err)
		return s.refuse(frame, treasureRcRoomEnter)
	}
	s.logRecovery(view)
	s.entered = true
	s.armSeasonEnd(cyc.End.Sub(now), s.aidText())
	s.logger.Printf("TREASURE msgid=1 enter aid=%d status=%d phase=%s season=%d cells=%d tickets=%d/%d tutorial=%v result=0",
		s.aid, treasureSeasonPlay, treasurePhaseName(cyc.Phase), cyc.ID, len(d.Board.Ring), view.Tickets, view.Max, view.Tutorial)
	return s.reply(frame, treasureRcRoomEnter, treasureEnterBody(d, cyc, treasureSeasonPlay, view))
}

// treasureEnterBody is sc_room_enter_res: Result 0, SeasonStatus, SeasonInfo and the TutorialComplete flag. Status 1 adds the
// ring (CellInfoList). Status 2 adds the one NPC portrait (AvatarInfo f6, the end popup's picture), the end popup's body
// text (Message f7, string: season.end_message) and no ring.
func treasureEnterBody(d *economy.TreasureData, cyc economy.TreasureCycle, status uint64, view economy.TreasureView) []byte {
	body := pbVar(nil, 1, 0)
	body = pbVar(body, 2, status)
	body = pbLen(body, 3, treasureSeasonInfo(d, cyc))
	if status == treasureSeasonPlay {
		for _, c := range d.Cells() {
			body = pbLen(body, 4, treasureCellInfo(c))
		}
	} else {
		body = pbLen(body, 6, treasureAvatar(d.NPCs[0]))
		body = pbLen(body, 7, []byte(d.Season.EndMessage))
	}
	return pbVar(body, 5, boolUint(view.Tutorial))
}

// treasurePhaseName is the phase as the log writes it.
func treasurePhaseName(p economy.TreasurePhase) string {
	switch p {
	case economy.TreasurePhasePlay:
		return "play"
	case economy.TreasurePhaseReward:
		return "reward"
	}
	return "closed"
}

// treasureSeasonInfo is SeasonInfo with all 25 fields: the names from the data, the picture paths (economy.TreasureSeasonPicture:
// the original HUD art and the Cherry-drawn buttons), the other picture paths empty (the client draws nothing for an empty
// path, treasure-spec 9.2). The dates (f23 start, f24 end of the play window) are Unix seconds: the client multiplies them by
// 1000 for its date text (season-end-spec 8).
func treasureSeasonInfo(d *economy.TreasureData, cyc economy.TreasureCycle) []byte {
	var b []byte
	text := func(field uint64, v string) { b = pbLen(b, field, []byte(v)) }
	text(1, "") // SkinUrl: the board picture (not in the archive)
	text(2, economy.TreasureSeasonPicture(2))
	text(3, "") // DiceType (no reader found)
	text(4, "") // MainBannerNomalUrl
	text(5, "") // MainBannerRewardUrl
	text(6, "") // RankingPointFontColor (no reader found)
	b = pbVar(b, 7, uint64(d.Tickets.PerRoll))
	text(8, d.Names.Ticket)
	text(9, d.Names.Coin)
	text(10, d.Season.Name)
	text(11, d.Names.RankingPoint)
	b = pbLen(b, 12, treasureAvatar(d.NPCs[0]))
	b = pbLen(b, 13, treasureAvatar(d.NPCs[1]))
	for f := uint64(14); f <= 22; f++ { // RankingPointOneUrl .. BgImgUrl .. QuestButtonUrl: image paths
		text(f, economy.TreasureSeasonPicture(f))
	}
	b = pbVar(b, 23, uint64(cyc.Start.Unix()))
	b = pbVar(b, 24, uint64(cyc.End.Unix()))
	text(25, economy.TreasureSeasonPicture(25))
	return b
}

// treasureAvatar is an NPC portrait: a name, a sex, a skin and the outfit (the data's items, no dye).
func treasureAvatar(npc economy.TreasureNPC) []byte {
	b := pbLen(nil, 2, []byte(npc.Name))
	b = pbVar(b, 3, squareSex(npc.Gender))
	b = pbLen(b, 9, []byte("1"))
	return treasureItems(b, npc.Items, nil)
}

// treasureOwnAvatar is the caller's own avatar in the relay reply: id, name, sex, skin and the equipped items with their dyes.
// The codes are the account's appearance (store.AccountByAvatarID: the base items fill the empty slots).
func treasureOwnAvatar(aid uint64) []byte {
	acc, ok := store.AccountByAvatarID(strconv.FormatUint(aid, 10))
	if !ok {
		acc = store.AccountSnapshot{Aid: strconv.FormatUint(aid, 10), Name: "cherry", Gender: "FEMALE"}
	}
	skin := acc.Skin
	if skin == "" {
		skin = "1"
	}
	b := pbVar(nil, 1, aid)
	b = pbLen(b, 2, []byte(acc.Name))
	b = pbVar(b, 3, squareSex(acc.Gender))
	b = pbLen(b, 9, []byte(skin))
	return treasureItems(b, acc.ItemCodes, acc.DyeColors)
}

// treasureItems adds one AvatarInfo f15 (MONOPOLY::ItemInfo) per code the client can draw: a canonical clothing code, as
// SquareNumericItem reads it. dyes maps a code to its colours; a dyed garment gets its dye (treasureDye).
func treasureItems(b []byte, codes []string, dyes map[string][]store.DyeColor) []byte {
	for _, code := range codes {
		n, ok := SquareNumericItem(code)
		if !ok {
			continue
		}
		item := pbVar(nil, 1, n)
		if colors, dyed := dyes[code]; dyed {
			item = treasureDye(item, colors)
		}
		b = pbLen(b, 15, item)
	}
	return b
}

// treasureDye adds a dyed garment's dye to its ItemInfo (avatar-items-spec 1.2 and 1.3): dye type f3 (2, two layers) and
// ColorAndTransparencies f5 {colorCode f1, transparency f2}, layer 0 first. Field 4 is InvenSeq in this message, so it is not used.
func treasureDye(item []byte, colors []store.DyeColor) []byte {
	item = pbVar(item, 3, store.DyeTypeTwoLayers)
	for _, c := range store.DyeLayers(colors) {
		layer := pbLen(nil, 1, []byte(c.ColorCode))
		layer = pbVar(layer, 2, uint64(c.Transparency))
		item = pbLen(item, 5, layer)
	}
	return item
}

// treasureCellInfo is CellInfo: position, id (the position: one id per square [I]), type and the facing side.
func treasureCellInfo(c economy.TreasureCell) []byte {
	b := pbVar(nil, 1, uint64(c.Position))
	b = pbVar(b, 2, uint64(c.Position))
	b = pbVar(b, 3, uint64(c.Type))
	return pbLen(b, 4, []byte(c.Direction))
}

// treasureTickets writes the four ticket fields (2 to 5) that the relay start and the roll replies share.
func treasureTickets(b []byte, v economy.TreasureView) []byte {
	b = pbVar(b, 2, uint64(v.Tickets))
	b = pbVar(b, 3, uint64(v.Max))
	b = pbVar(b, 4, uint64(v.RemainSeconds))
	return pbVar(b, 5, uint64(v.ChargeSeconds))
}

// relayStart answers cs_room_relaystart_req (msgid 3) with the tickets and their timer, the jewels, the caller's avatar and
// the square the token stands on. A board that was not entered is refused.
func (s *treasureSession) relayStart(frame []byte) error {
	if !s.loggedIn {
		s.logger.Printf("TREASURE msgid=3 relaystart refused: loggedIn=%v", s.loggedIn)
		return s.refuse(frame, treasureRcRelayStart)
	}
	now := economy.NowFunc()
	if cyc := economy.TreasureCurrent(now); cyc.Phase != economy.TreasurePhasePlay {
		// The status-2 path (season-end-spec 12 (b) and (c)): the tickets and the caller's own avatar, no square. The client
		// goes on to the claims, so the board's square is not sent.
		view := economy.TreasurePeek(s.aidText(), now)
		body := treasureTickets(pbVar(nil, 1, 0), view)
		body = pbVar(body, 6, uint64(view.Jewels))
		body = pbVar(body, 7, uint64(view.BuffPercent))
		body = pbLen(body, 8, treasureOwnAvatar(s.aid))
		body = pbLen(body, 11, treasureMyRank(s.aid))
		s.logger.Printf("TREASURE msgid=3 relaystart status=%d phase=%s aid=%d tickets=%d/%d result=0",
			treasureSeasonEnded, treasurePhaseName(cyc.Phase), s.aid, view.Tickets, view.Max)
		return s.reply(frame, treasureRcRelayStart, body)
	}
	if !s.entered {
		s.logger.Printf("TREASURE msgid=3 relaystart refused: entered=%v", s.entered)
		return s.refuse(frame, treasureRcRelayStart)
	}
	view, err := economy.TreasureOpen(s.aidText(), now)
	if err != nil {
		s.logger.Printf("TREASURE msgid=3 relaystart refused aid=%d: %v", s.aid, err)
		return s.refuse(frame, treasureRcRelayStart)
	}
	s.logRecovery(view)
	d := economy.ActiveTreasureData()
	body := treasureTickets(pbVar(nil, 1, 0), view)
	body = pbVar(body, 6, uint64(view.Jewels))
	body = pbVar(body, 7, uint64(view.BuffPercent)) // BuffPersent: the HUD's buff percent (SetBlueMarbleCoinBuffPercent)
	body = pbLen(body, 8, treasureOwnAvatar(s.aid))
	body = pbLen(body, 9, treasureCellInfo(d.Cell(view.Position)))
	// The square's CellDetail (SetMyCurDetailInfo): the client asks cs 25 at board entry only when its quest is claimable, and a
	// trap or broom square re-shows its prompt from it.
	if detail := treasureRelayDetail(d, view); detail != nil {
		body = pbLen(body, 10, detail)
	}
	body = pbLen(body, 11, treasureMyRank(s.aid)) // MyRankingInfo: f2 RankingPoint feeds the gold trophy counter (DM+64)
	s.logger.Printf("TREASURE msgid=3 relaystart aid=%d tickets=%d/%d remain=%ds jewels=%d position=%d result=0",
		s.aid, view.Tickets, view.Max, view.RemainSeconds, view.Jewels, view.Position)
	return s.reply(frame, treasureRcRelayStart, body)
}

// roll answers cs_rolling_dice_req (msgid 4): the tickets after the debit, the square landed on (MyCellInfo), its payment
// (CellDetail), the die and, after a lap, the start reward. Without tickets the reply is Result 1 with the counts, so the
// client's counts stay true.
func (s *treasureSession) roll(frame []byte) error {
	if !s.loggedIn || !s.entered {
		s.logger.Printf("TREASURE msgid=4 roll refused: loggedIn=%v entered=%v", s.loggedIn, s.entered)
		return s.refuse(frame, treasureRcRolling)
	}
	mv, err := economy.TreasureRollDice(s.aidText(), economy.NowFunc())
	if err != nil {
		s.logger.Printf("TREASURE msgid=4 roll refused aid=%d: %v", s.aid, err)
		body := pbVar(nil, 1, treasureRefused)
		if errors.Is(err, economy.ErrTreasureNoTickets) {
			body = treasureTickets(body, mv.View)
		}
		return s.reply(frame, treasureRcRolling, body)
	}
	d := economy.ActiveTreasureData()
	body := treasureTickets(pbVar(nil, 1, 0), mv.View)
	body = pbLen(body, 6, treasureCellInfo(d.Cell(mv.To)))
	if detail := treasureCellDetail(d, mv); detail != nil {
		body = pbLen(body, 7, detail)
	}
	body = pbVar(body, 8, uint64(mv.Dice))
	if mv.Lap {
		body = pbLen(body, 9, treasureRewardText(fmt.Sprintf("+%d %s", d.Rewards.Start.Tickets, d.Names.Ticket)))
	}
	s.logger.Printf("TREASURE msgid=4 roll aid=%d dice=%d from=%d to=%d cell=%d coins=%d bonus=%d lap=%v tickets=%d/%d jewels=%d result=0",
		s.aid, mv.Dice, mv.From, mv.To, mv.Cell, mv.Coins, mv.Bonus, mv.Lap, mv.View.Tickets, mv.View.Max, mv.View.Jewels)
	switch {
	case mv.Escaped:
		s.logger.Printf("TREASURE msgid=4 trap escape aid=%d dice=%d from=%d to=%d cell=%d", s.aid, mv.Dice, mv.From, mv.To, mv.Cell)
	case mv.Cell == economy.TreasureCellTrap && mv.From == mv.To:
		s.logger.Printf("TREASURE msgid=4 trap stay aid=%d dice=%d at=%d count=%d/%d", s.aid, mv.Dice, mv.To, mv.Trap.Count, mv.Trap.Max)
	case mv.Cell == economy.TreasureCellTrap:
		s.logger.Printf("TREASURE msgid=4 trap landed aid=%d dice=%d at=%d max=%d high=%d", s.aid, mv.Dice, mv.To, mv.Trap.Max, mv.Trap.High)
	}
	if err := s.reply(frame, treasureRcRolling, body); err != nil || mv.Cell != economy.TreasureCellBonus {
		return err
	}
	// The bonus popup moves no balance for a JEWEL unit and the client asks cs 29 only after a WebView closes, so the HUD
	// would keep the old count until re-entry: push sc 35 with the count (layout-audit M2; the parser has no state gate).
	return s.reply(frame, treasureRcCoinRefresh, pbVar(pbVar(nil, 1, 0), 2, uint64(mv.View.Jewels)))
}

// treasureCellDetail is the CellDetailInfo of the square landed on: the coin view (RewardInfo with ViewType 10 and the
// Jewels; CellTypeCoin available) for a coin square, the bonus (CellTypeBonus with the currency unit and the balance) for
// a bonus square. The start line and the other types have none.
func treasureCellDetail(d *economy.TreasureData, mv economy.TreasureMove) []byte {
	switch mv.Cell {
	case economy.TreasureCellCoin:
		item := pbVar(nil, 1, treasureViewCoin) // the coin view of the popup (cells.md 1.1)
		item = pbVar(item, 4, uint64(mv.Coins))
		// CellDetailInfo.RewardInfo (1) with ItemList (2) and the text (5) the coin popup draws (SetTextCoinReward, layout-audit M1)
		reward := pbLen(treasureRewardText(fmt.Sprintf("+%d %s", mv.Coins, d.Names.Coin)), 2, item)
		detail := pbLen(nil, 1, reward)
		return pbLen(detail, 3, pbVar(nil, 1, 1)) // CellTypeCoin.Availability
	case economy.TreasureCellBonus:
		// The bonus popup opens only with a non-empty ItemList (OpenPopupByCellType case 4, arrival.md): without it the
		// board never releases its busy flag and the next roll is ignored.
		item := pbVar(nil, 1, treasureViewCoin)
		item = pbVar(item, 4, uint64(mv.Bonus))
		reward := pbLen(treasureRewardText(fmt.Sprintf("+%d %s", mv.Bonus, d.Names.Coin)), 2, item)
		detail := pbLen(nil, 1, reward)
		bonus := pbVar(nil, 1, 1)
		bonus = pbLen(bonus, 3, []byte(d.Rewards.Bonus.CoinUnit))
		bonus = pbVar(bonus, 4, uint64(mv.View.Jewels))
		return pbLen(detail, 6, bonus) // CellTypeBonus
	case economy.TreasureCellExchange:
		return treasureSquareDetail(mv.View) // CellTypeRanking: the exchange popup's counts (cells.md 6.4)
	case economy.TreasureCellGoldKey:
		return pbLen(nil, 9, pbVar(nil, 1, 1)) // CellTypeGoldKey.Availability: the Mystery Card is drawn by cs 10
	case economy.TreasureCellBuff:
		// CellTypeBuff (f5): the gacha id, the item the square drew, its BuffPercent, a LinkInfo to the Gacha Shop (its button
		// shows an error box without one), the grade and the state (the popup refuses WEAR and INVEN and releases the board).
		buff := pbVar(nil, 1, treasureBuffGacha)
		buff = pbLen(buff, 2, []byte(mv.Buff.Code))
		buff = pbVar(buff, 3, uint64(mv.Buff.Percent))
		buff = pbLen(buff, 4, treasureLinkInfo())
		buff = pbLen(buff, 5, []byte("RARE"))
		buff = pbLen(buff, 6, []byte(mv.Buff.State))
		return pbLen(nil, 5, buff)
	case economy.TreasureCellSpecial:
		return treasureSpecialDetail(d, mv.To, mv.View.Secret)
	case economy.TreasureCellTravel:
		return treasureTravelDetail(d, mv.To, mv.View.ExchangeFull)
	case economy.TreasureCellMiniGame:
		return treasureMiniGameDetail(d, mv.View)
	case economy.TreasureCellTrap:
		return treasureTrapDetail(mv.Trap)
	}
	return nil
}

// treasureTravelDetail is CellTypeFlyTravle (f10) of the travel square at ring position at: Availability 1 and a RankingCellInfo
// (f2) for each other exchange square, its CellId (the ring position, as its CellInfo sends it) and IsReset (full is the
// exchange squares full today). The landing and a relay start while the prompt is pending send it, so a re-shown prompt has
// its targets (broom-spec section 5.1 (3)).
func treasureTravelDetail(d *economy.TreasureData, at int, full []int) []byte {
	b := pbVar(nil, 1, 1)
	for _, c := range d.Cells() {
		if c.Type != economy.TreasureCellExchange || c.Position == at {
			continue
		}
		row := pbVar(nil, 1, uint64(c.Position))
		row = pbVar(row, 2, boolUint(slices.Contains(full, c.Position)))
		b = pbLen(b, 2, row)
	}
	return pbLen(nil, 10, b)
}

// treasureTrapDetail is the CellDetailInfo of a trap square (CellTypeIsland, f8): ExitResult 0 (the token is held; no landing
// sends 1, the released token is walked on by its roll), the trapped rolls that free it (f2), the trapped rolls so far (f3) and
// the die that frees it (f4) (island-spec section 2.1).
func treasureTrapDetail(t economy.TreasureTrap) []byte {
	island := pbVar(nil, 1, 0)
	island = pbVar(island, 2, uint64(t.Max))
	island = pbVar(island, 3, uint64(t.Count))
	island = pbVar(island, 4, uint64(t.High))
	return pbLen(nil, 8, island)
}

// treasureRelayDetail is the CellDetailInfo the relay start sends for the square the token stands on: the secret quest of a
// secret quest square, the broom prompt's targets while its travel is pending, and the island of a trap square. Other squares
// have none.
func treasureRelayDetail(d *economy.TreasureData, v economy.TreasureView) []byte {
	switch d.Board.Ring[v.Position] {
	case economy.TreasureCellSpecial:
		return treasureSpecialDetail(d, v.Position, v.Secret)
	case economy.TreasureCellTravel:
		if v.TravelPending {
			return treasureTravelDetail(d, v.Position, v.ExchangeFull)
		}
	case economy.TreasureCellTrap:
		return treasureTrapDetail(v.Trap)
	}
	return nil
}

// treasureMiniGameDetail is CellTypeMinigame (f11): Availability 1, the Jewels the account holds, the most one game takes (f3,
// and f5 for VIPs), the payout multiples (f4 and f6, repeated int32 sent unpacked) and UseChagneCoinCount (f7), the Jewels the
// next game may take (the Jewels held, up to the square's allowance): zero shows the daily-limit box. RankingPointReset (f10)
// is never sent (it leads to a purchase); Vip (f12) and the date (f13) are not sent either.
func treasureMiniGameDetail(d *economy.TreasureData, v economy.TreasureView) []byte {
	most := uint64(d.MiniGame.MaxBet)
	b := pbVar(nil, 1, 1)
	b = pbVar(b, 2, uint64(v.Jewels))
	b = pbVar(b, 3, most)
	for _, m := range d.MiniGame.Multiples {
		b = pbVar(b, 4, uint64(m))
	}
	b = pbVar(b, 5, most)
	for _, m := range d.MiniGame.Multiples {
		b = pbVar(b, 6, uint64(m))
	}
	b = pbVar(b, 7, min(uint64(v.Jewels), uint64(v.MiniGameLeft)))
	return pbLen(nil, 11, b)
}

// treasureLinkInfo is the LinkInfo of the Gacha Shop popup (the key the client's link table names; no StrData or IntData).
func treasureLinkInfo() []byte { return pbLen(nil, 1, []byte(treasureShopLink)) }

// goldKeyUse answers cs_gold_key_use_req (msgid 10, empty body) with the Mystery Card the gold key drew: its type (f2), its
// value (f3), the square the token ends on (f5), that square's CellDetail (f6) and, after a lap, the lap text (f7). A card of
// types 1 and 2 moves the token and pays its landing as a roll does (a bonus landing also pushes sc 35); types 6 and 7 pay
// Jewels where the token stands. Without a pending key, or without a login or entered board, it is refused.
func (s *treasureSession) goldKeyUse(frame []byte) error {
	if !s.loggedIn || !s.entered {
		s.logger.Printf("TREASURE msgid=10 gold key refused: loggedIn=%v entered=%v", s.loggedIn, s.entered)
		return s.refuse(frame, treasureRcGoldKey)
	}
	use, err := economy.TreasureGoldKeyUse(s.aidText(), economy.NowFunc())
	if err != nil {
		s.logger.Printf("TREASURE msgid=10 gold key refused aid=%d: %v", s.aid, err)
		return s.refuse(frame, treasureRcGoldKey)
	}
	d := economy.ActiveTreasureData()
	mv := use.Move
	body := pbVar(nil, 1, 0)
	body = pbVar(body, 2, uint64(use.Card))
	body = pbVar(body, 3, uint64(use.Value))
	body = pbLen(body, 5, treasureCellInfo(d.Cell(mv.To)))
	switch use.Card {
	case 6, 7: // the Jewels of the card, in the coin popup the client opens from f6
		text := fmt.Sprintf("+%d %s", mv.Coins, d.Names.Coin)
		body = pbLen(body, 6, pbLen(nil, 1, treasureRewardInfo(text, treasureViewCoin, mv.Coins)))
	default:
		if detail := treasureCellDetail(d, mv); detail != nil {
			body = pbLen(body, 6, detail)
		}
	}
	if mv.Lap {
		body = pbLen(body, 7, treasureRewardText(fmt.Sprintf("+%d %s", d.Rewards.Start.Tickets, d.Names.Ticket)))
	}
	// f8..f11 are the ticket fields (count, cap, timer, charge — sc 4's f2..f5): the client overwrites its HUD from them, so
	// leaving them out showed 0/0 and refused the next roll (device 2026-11-02 15:29).
	v := mv.View
	body = pbVar(body, 8, uint64(v.Tickets))
	body = pbVar(body, 9, uint64(v.Max))
	body = pbVar(body, 10, uint64(v.RemainSeconds))
	body = pbVar(body, 11, uint64(v.ChargeSeconds))
	s.logger.Printf("TREASURE msgid=10 gold key aid=%d card=%d value=%d from=%d to=%d coins=%d bonus=%d lap=%v jewels=%d result=0",
		s.aid, use.Card, use.Value, mv.From, mv.To, mv.Coins, mv.Bonus, mv.Lap, mv.View.Jewels)
	if err := s.reply(frame, treasureRcGoldKey, body); err != nil || mv.Cell != economy.TreasureCellBonus {
		return err
	}
	return s.reply(frame, treasureRcCoinRefresh, pbVar(pbVar(nil, 1, 0), 2, uint64(mv.View.Jewels)))
}

// flyTravel answers cs_fly_travel_req (msgid 9): f1 is the MoveCellInfo (f1 CellPosition, f2 CellId, f3 CellType, f4 direction).
// A target that is an exchange square, asked for while the broom prompt is pending, moves the token there with no lap and no
// payment: the reply has that square's CellInfo (f2), its exchange detail (f3) and the tickets (f5 to f8), and no RewardInfo
// (f4). Every other request is answered Result 0 with no move (broom-spec section 3.2): f2 is the token's own square, f5 to f8
// are the tickets, and the pending prompt is cleared, so the client's alert closes and the token stays put. A non-zero Result
// would leave the alert up with no way to close it.
func (s *treasureSession) flyTravel(frame, data []byte) error {
	if !s.loggedIn || !s.entered {
		s.logger.Printf("TREASURE msgid=9 travel no board: loggedIn=%v entered=%v result=0", s.loggedIn, s.entered)
		return s.reply(frame, treasureRcFlyTravel, pbVar(nil, 1, 0))
	}
	pos := treasureMovePos(data)
	now := economy.NowFunc()
	mv, err := economy.TreasureTravel(s.aidText(), pos, now)
	if err != nil {
		view := economy.TreasurePeek(s.aidText(), now)
		d := economy.ActiveTreasureData()
		s.logger.Printf("TREASURE msgid=9 travel refused aid=%d to=%d at=%d body=%x: %v result=0 token stays", s.aid, pos, view.Position, data, err)
		body := pbLen(pbVar(nil, 1, 0), 2, treasureCellInfo(d.Cell(view.Position)))
		return s.reply(frame, treasureRcFlyTravel, treasureFlyTickets(body, view))
	}
	d := economy.ActiveTreasureData()
	body := pbVar(nil, 1, 0)
	body = pbLen(body, 2, treasureCellInfo(d.Cell(mv.To)))
	body = pbLen(body, 3, treasureSquareDetail(mv.View))
	body = treasureFlyTickets(body, mv.View)
	s.logger.Printf("TREASURE msgid=9 travel aid=%d from=%d to=%d result=0", s.aid, mv.From, mv.To)
	return s.reply(frame, treasureRcFlyTravel, body)
}

// treasureFlyTickets writes the ticket fields of sc 9 (f5 to f8: the count, the cap, the refill timer and its charge time).
func treasureFlyTickets(b []byte, v economy.TreasureView) []byte {
	b = pbVar(b, 5, uint64(v.Tickets))
	b = pbVar(b, 6, uint64(v.Max))
	b = pbVar(b, 7, uint64(v.RemainSeconds))
	return pbVar(b, 8, uint64(v.ChargeSeconds))
}

// treasureMovePos is the target of cs 9: the CellPosition (f1) of its nested MoveCellInfo, or the CellId (f2) of the same message
// when the position is not sent (0). Without a MoveCellInfo the target is 0, which is never an exchange square.
func treasureMovePos(data []byte) int {
	move, found, ok := pbBytesField(data, 1)
	if !ok || !found {
		return 0
	}
	pos, found := protobufUintField(move, 1)
	if !found {
		pos, _ = protobufUintField(move, 2)
	}
	return int(pos)
}

// logRecovery logs what a board entry cleared (economy.TreasureOpen), if anything.
func (s *treasureSession) logRecovery(view economy.TreasureView) {
	if view.Recovery != "" {
		s.logger.Printf("TREASURE recover aid=%d %s", s.aid, view.Recovery)
	}
}

// treasureRewardText is a RewardInfo that carries only its message (the text the popup shows).
func treasureRewardText(text string) []byte { return pbLen(nil, 5, []byte(text)) }

// exit answers cs_room_exit_req (msgid 2) with an empty sc_room_exit_res, then sends the subtype-5 close control with
// the request's session id (transport-money 1.6). The reply alone keeps the client on the board; the close control
// returns it to the Garden (event 14 → 12797). The next scene entry opens the tunnel again.
func (s *treasureSession) exit(frame []byte) error {
	err := s.reply(frame, treasureRcRoomExit, nil)
	s.logger.Printf("TREASURE msgid=2 exit aid=%d", s.aid)
	s.entered = false
	s.stopSeasonEnd()
	if err != nil {
		return err
	}
	closeCtl := make([]byte, 22)
	binary.BigEndian.PutUint32(closeCtl, 18)
	closeCtl[4], closeCtl[5] = 'G', frame[1]&0xf8|5
	copy(closeCtl[6:14], frame[2:10])
	_, err = s.conn.Write(closeCtl)
	s.loggedIn = false
	s.logger.Printf("TREASURE close-control err=%v", err)
	return err
}

// tutorial answers cs_tutorial_complete_req (msgid 23) with an empty sc_tutorial_complete_res and keeps the flag, so the
// next enter reports TutorialComplete.
func (s *treasureSession) tutorial(frame []byte) error {
	if s.loggedIn {
		err := economy.TreasureTutorialDone(s.aidText())
		s.logger.Printf("TREASURE msgid=23 tutorial complete aid=%d err=%v", s.aid, err)
	}
	return s.reply(frame, treasureRcTutorial, nil)
}

// coinRefresh answers cs_event_coin_refresh_req (msgid 29) with the saved jewel count: the board's coin HUD reads it.
func (s *treasureSession) coinRefresh(frame []byte) error {
	jewels := uint64(economy.TreasureJewels(s.aidText()))
	body := pbVar(pbVar(nil, 1, 0), 2, jewels)
	s.logger.Printf("TREASURE msgid=29 coin refresh aid=%d jewels=%d result=0", s.aid, jewels)
	return s.reply(frame, treasureRcCoinRefresh, body)
}

// rewardExit answers cs_season_reward_popup_exit_check_req (msgid 28), the back key of the reward popup, with sc 33: Result 0
// in the reward period (the season-end box with text 4593, then the Garden) and 65541 once the cycle is closed (text 4594).
// The popup is not opened in a play window, so that case is refused (Result 1). The request needs no login.
func (s *treasureSession) rewardExit(frame []byte) error {
	phase := economy.TreasureCurrent(economy.NowFunc()).Phase
	result := uint64(treasureRefused)
	switch phase {
	case economy.TreasurePhaseReward:
		result = 0
	case economy.TreasurePhaseClosed:
		result = treasureSeasonNotActive
	}
	s.logger.Printf("TREASURE msgid=28 reward exit aid=%d phase=%s result=%d", s.aid, treasurePhaseName(phase), result)
	return s.reply(frame, treasureRcSeasonState, pbVar(nil, 1, result))
}

// armSeasonEnd arms the season-end push for after: when the play window ends under this board, the session gets sc 25
// (empty) once (season-end-spec 12 (a)). One push is pending per session: a new enter replaces it, and exit or close stops it.
func (s *treasureSession) armSeasonEnd(after time.Duration, aid string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stopSeasonEndLocked()
	gen := s.endGen
	s.endTimer = treasureAfter(after, func() { s.seasonEnded(gen, aid) })
}

// stopSeasonEnd cancels the pending push, if any.
func (s *treasureSession) stopSeasonEnd() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stopSeasonEndLocked()
}

// stopSeasonEndLocked cancels the pending push and invalidates a timer that has fired but not yet written. Caller holds mu.
func (s *treasureSession) stopSeasonEndLocked() {
	if s.endTimer != nil {
		s.endTimer.Stop()
		s.endTimer = nil
	}
	s.endGen++
}

// seasonEnded is the timer's push: sc 25 (empty) on the board's connection, reusing the latest request's type byte and session
// id, when the board is still the one that was armed (gen is current).
func (s *treasureSession) seasonEnded(gen uint64, aid string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if gen != s.endGen {
		return
	}
	s.endTimer = nil
	s.endGen++
	_, err := s.conn.Write(squareFrame(s.hdrType, s.hdrSid[:], treasureRcSeasonExit, nil))
	s.logger.Printf("TREASURE msgid=25 season end push aid=%s err=%v", aid, err)
}

// close stops the pending push when the connection goes away.
func (s *treasureSession) close() { s.stopSeasonEnd() }

// notBuilt answers a request of a later slice from treasureStubs and logs it. An unknown msgid gets no reply.
func (s *treasureSession) notBuilt(frame []byte, msgid uint16) error {
	s.logger.Printf("TREASURE msgid=%d not built", msgid)
	stub, ok := treasureStubs[msgid]
	if !ok || stub.rc == treasureRcNoReply {
		return nil
	}
	return s.reply(frame, stub.rc, stub.body)
}

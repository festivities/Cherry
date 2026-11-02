package lpn

import (
	"encoding/binary"
	"errors"
	"fmt"
	"log"
	"strconv"

	"cherry/internal/economy"
	"cherry/internal/store"
)

// Treasure World (Blue Marble board game): LPN namespace MONOPOLY, gateway agent 9, tunnel "1.1.114.0" (sid
// 0x0001000100720000). Specs in %TEMP%\opencode\cherry\treasure-20261102 (treasure-spec.md section 9, transport-money.md
// sections 1 and 7, cells.md). Inbound msgid = dispatch index, outbound msgid = GetIndex; the two numberings differ (the
// exit is cs 2 and sc 3, the relay start is cs 3 and sc 2). Slice 1: login, room enter, relay start, the roll, the
// tutorial flag, the exit and the coin count. Every other request gets the answer treasureStubs names.
const (
	treasureAgent = 9

	treasureRefused         = 1     // a refused request: any non-zero Result is the client's error path
	treasureSeasonPlay      = 1     // sc_room_enter_res SeasonStatus: the board is playable (treasure-spec 9.3)
	treasureSeasonNotActive = 65541 // sc_season_active_state_res: Result 65541 clears the season-end flag (transport-money 2.5)
)

// Outbound (client to server) msgids, cs GetIndex.
const (
	treasureCsLogin       = 0
	treasureCsRoomEnter   = 1
	treasureCsRoomExit    = 2
	treasureCsRelayStart  = 3
	treasureCsRolling     = 4
	treasureCsGoldKeyUse  = 10
	treasureCsTutorial    = 23
	treasureCsCoinRefresh = 29
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

// treasureStubs maps each outbound msgid of a later slice to its answer. Keys are cs msgids.
var treasureStubs = map[uint16]treasureStub{
	5:  {treasureRcBuffList, treasureOK},
	8:  {treasureRcMiniGame, treasureNo},
	9:  {treasureRcFlyTravel, treasureNo},
	22: {treasureRcBuffWear, treasureNo},
	24: {treasureRcNoReply, nil}, // cs_playticket_sync_req: no caller in the client
	25: {treasureRcSpecialReward, treasureNo},
	26: {treasureRcSpecialEnable, treasureNo},
	28: {treasureRcSeasonState, pbVar(nil, 1, treasureSeasonNotActive)},
}

// treasureSession is one agent-9 connection: the account logged in on it and whether its board is entered.
type treasureSession struct {
	conn     *lockedConn
	logger   *log.Logger
	aid      uint64
	loggedIn bool
	entered  bool
}

func (s *treasureSession) aidText() string { return strconv.FormatUint(s.aid, 10) }

// handleOpen answers the client's subtype-1 open control with the one 22-byte subtype-1 echo of its type byte and session
// id (transport-money 1.2). It never sends subtype 2 to agent 9: the client would leave the board for the Garden. An open
// starts a new tunnel, so the login state goes too; the client logs in again on the echo.
func (s *treasureSession) handleOpen(frame []byte) error {
	s.loggedIn, s.entered, s.aid = false, false, 0
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
	case treasureCsGoldKeyUse:
		return s.goldKeyUse(frame)
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

// enter answers cs_room_enter_req (msgid 1): a playable season (status 1), the ring and the tutorial flag. A refusal
// (no login, no running season, a store error) is Result 1, which the client answers by returning to the Garden.
func (s *treasureSession) enter(frame []byte) error {
	if !s.loggedIn {
		s.logger.Printf("TREASURE msgid=1 enter refused: no login")
		return s.refuse(frame, treasureRcRoomEnter)
	}
	view, err := economy.TreasureOpen(s.aidText(), economy.NowFunc())
	if err != nil {
		s.entered = false
		s.logger.Printf("TREASURE msgid=1 enter refused aid=%d: %v", s.aid, err)
		return s.refuse(frame, treasureRcRoomEnter)
	}
	s.entered = true
	d := economy.ActiveTreasureData()
	s.logger.Printf("TREASURE msgid=1 enter aid=%d status=%d cells=%d tickets=%d/%d tutorial=%v result=0",
		s.aid, treasureSeasonPlay, len(d.Board.Ring), view.Tickets, view.Max, view.Tutorial)
	return s.reply(frame, treasureRcRoomEnter, treasureEnterBody(d, view))
}

// treasureEnterBody is sc_room_enter_res: Result 0, SeasonStatus 1, SeasonInfo, the ring (CellInfoList) and the
// TutorialComplete flag.
func treasureEnterBody(d *economy.TreasureData, view economy.TreasureView) []byte {
	body := pbVar(nil, 1, 0)
	body = pbVar(body, 2, treasureSeasonPlay)
	body = pbLen(body, 3, treasureSeasonInfo(d))
	for _, c := range d.Cells() {
		body = pbLen(body, 4, treasureCellInfo(c))
	}
	return pbVar(body, 5, boolUint(view.Tutorial))
}

// treasureSeasonInfo is SeasonInfo with all 25 fields: the names from the data, the picture paths (economy.TreasureSeasonPicture:
// the original HUD art and the Cherry-drawn buttons), the other picture paths empty (the client draws nothing for an empty
// path, treasure-spec 9.2). The dates are Unix milliseconds [I: the unit is not read by any path found].
func treasureSeasonInfo(d *economy.TreasureData) []byte {
	start, end := economy.TreasureSeasonWindow()
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
	b = pbVar(b, 23, uint64(start.UnixMilli()))
	b = pbVar(b, 24, uint64(end.UnixMilli()))
	text(25, economy.TreasureSeasonPicture(25))
	return b
}

// treasureAvatar is an NPC portrait: a name, a sex and a skin, no items (the portrait is the client's to draw).
func treasureAvatar(npc economy.TreasureNPC) []byte {
	b := pbLen(nil, 2, []byte(npc.Name))
	b = pbVar(b, 3, squareSex(npc.Gender))
	return pbLen(b, 9, []byte("1"))
}

// treasureOwnAvatar is the caller's own avatar in the relay reply: id, name, sex and skin. No items are sent: the
// MONOPOLY AvatarItemInfo layout is not decoded yet, so the token is drawn without the wardrobe [I, device check].
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
	return pbLen(b, 9, []byte(skin))
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
	if !s.loggedIn || !s.entered {
		s.logger.Printf("TREASURE msgid=3 relaystart refused: loggedIn=%v entered=%v", s.loggedIn, s.entered)
		return s.refuse(frame, treasureRcRelayStart)
	}
	view, err := economy.TreasureOpen(s.aidText(), economy.NowFunc())
	if err != nil {
		s.logger.Printf("TREASURE msgid=3 relaystart refused aid=%d: %v", s.aid, err)
		return s.refuse(frame, treasureRcRelayStart)
	}
	d := economy.ActiveTreasureData()
	body := treasureTickets(pbVar(nil, 1, 0), view)
	body = pbVar(body, 6, uint64(view.Jewels))
	body = pbLen(body, 8, treasureOwnAvatar(s.aid))
	body = pbLen(body, 9, treasureCellInfo(d.Cell(view.Position)))
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
	}
	return nil
}

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

// treasureRewardText is a RewardInfo that carries only its message (the text the popup shows).
func treasureRewardText(text string) []byte { return pbLen(nil, 5, []byte(text)) }

// exit answers cs_room_exit_req (msgid 2) with an empty sc_room_exit_res, then sends the subtype-5 close control with
// the request's session id (transport-money 1.6). The reply alone keeps the client on the board; the close control
// returns it to the Garden (event 14 → 12797). The next scene entry opens the tunnel again.
func (s *treasureSession) exit(frame []byte) error {
	err := s.reply(frame, treasureRcRoomExit, nil)
	s.logger.Printf("TREASURE msgid=2 exit aid=%d", s.aid)
	s.entered = false
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

// notBuilt answers a request of a later slice from treasureStubs and logs it. An unknown msgid gets no reply.
func (s *treasureSession) notBuilt(frame []byte, msgid uint16) error {
	s.logger.Printf("TREASURE msgid=%d not built", msgid)
	stub, ok := treasureStubs[msgid]
	if !ok || stub.rc == treasureRcNoReply {
		return nil
	}
	return s.reply(frame, stub.rc, stub.body)
}

package lpn

import (
	"fmt"
	"strconv"

	"cherry/internal/economy"
)

// Treasure World points, rankings, rewards and the ticket cap (gateway agent 9; cells.md sections 1.3, 1.4, 3 and 4.3,
// transport-money.md section 3). A request the rules refuse gets Result 1, the client's error path. Every Gem move is a
// ledger line in economy (treasure:reset:* and treasure:cap:*); the rewards arrive by Mailbox.

// Outbound (client to server) msgids of the requests this file answers.
const (
	treasureCsPointChange       = 6
	treasureCsPointReset        = 7
	treasureCsRankingInfo       = 11
	treasureCsPointRewardInfo   = 12
	treasureCsClassRewardInfo   = 13
	treasureCsPointRewardClaim  = 14
	treasureCsClassRewardClaim  = 15
	treasureCsQuestList         = 16
	treasureCsMissionClaim      = 17
	treasureCsQuestClaim        = 18
	treasureCsQuestRefresh      = 19
	treasureCsCapInfo           = 20
	treasureCsCapBuy            = 21
	treasureCsResetInfo         = 27
	treasureGemsCoinType        = "GEM"
	treasureRewardNoteMailbox   = "Sent to your Mailbox"
	treasureRewardDefaultPicGem = "gem"
)

// treasureRankingDetail is CellDetailRanking of an exchange square: Availability, the Jewels the account holds, the Jewels
// the square still takes today, the rate, the daily limit and whether a reset can be bought. [OPEN, device check] The
// popup's picker caps at field 3 (UseChagneCoinCount), so it carries what is left today; the popup's own "remaining" line
// then shows the limit less that, not the used count. If the device shows that line wrong, the field and the picker
// disagree (cells.md 7.3 item 4) and the fix is in this one function.
func treasureRankingDetail(v economy.TreasureView) []byte {
	b := pbVar(nil, 1, 1)
	b = pbVar(b, 2, uint64(v.Jewels))
	b = pbVar(b, 3, uint64(v.ExchangeLeft))
	b = pbVar(b, 4, uint64(v.ExchangeRate))
	b = pbVar(b, 5, uint64(v.ExchangeMax))
	// VipMultiple (6) and VipMaxChangeCoinCount (7): without them the popup reads "Up to 0 for VIPs" (slice2-fixes.md A).
	// Cherry has no VIP, so a VIP gets the same rate and limit.
	b = pbVar(b, 6, uint64(v.ExchangeRate))
	b = pbVar(b, 7, uint64(v.ExchangeMax))
	return pbVar(b, 10, boolUint(v.ExchangeReset))
}

// treasureSquareDetail is the CellDetailInfo of the exchange square under the token (oneof field 4, CellTypeRanking).
func treasureSquareDetail(v economy.TreasureView) []byte {
	return pbLen(nil, 4, treasureRankingDetail(v))
}

// treasureMyRank is MyRankingInfo: the account's place, its points, its grade and the grade's class.
func treasureMyRank(aid uint64) []byte {
	me := economy.TreasureMyRankOf(strconv.FormatUint(aid, 10), economy.NowFunc())
	b := pbVar(nil, 1, uint64(me.Rank))
	b = pbVar(b, 2, uint64(me.Points))
	b = pbLen(b, 3, []byte(me.Grade.Name))
	return pbVar(b, 5, uint64(me.Grade.Type))
}

// RewardItemInfo view types (SetShowRewardGet 0x16FA430, trade-key.md C): 9 draws the ticket picture, 10 the coin
// (Jewel) picture; any other value closes the popup at once. The gem view is not known, so Gem rewards use 10.
const (
	treasureViewTicket = 9
	treasureViewCoin   = 10
)

// treasureRewardInfo is the RewardInfo of a claim: the text (f5) and one ItemList row {ViewType, ViewCount}. The
// client creates its reward popup (MarbleCommonRewardGet) for every successful claim but draws it only with a non-empty
// ItemList; an undrawn popup has no close button and swallows all input (freeze.md A, device 2026-11-02).
func treasureRewardInfo(text string, view int, count int64) []byte {
	item := pbVar(pbVar(nil, 1, uint64(view)), 4, uint64(count))
	return pbLen(pbLen(nil, 2, item), 5, []byte(text))
}

// pointChange answers cs_ranking_point_change_req (msgid 6): the picked Jewels become Ranking Points on the exchange square
// under the token. The reply carries the points added (f2), the account's place (f3 MyRankingInfo), the square's counts
// (f4 CellDetailInfo) and the text (f5), the order of MONOPOLY::sc_ranking_point_change_res::MergePartialFromCodedStream
// 0x256EF68 (trade-key.md; text in f3 failed the decode and the client dropped the reply, so the Trade popup stayed open).
// The client takes the Jewels off its own count.
func (s *treasureSession) pointChange(frame, data []byte) error {
	if !s.loggedIn || !s.entered {
		s.logger.Printf("TREASURE msgid=6 exchange refused: loggedIn=%v entered=%v", s.loggedIn, s.entered)
		return s.refuse(frame, treasureRcPointChange)
	}
	jewels, _ := protobufUintField(data, 1)
	res, err := economy.TreasureExchange(s.aidText(), int64(jewels), economy.NowFunc())
	if err != nil {
		s.logger.Printf("TREASURE msgid=6 exchange refused aid=%d jewels=%d: %v", s.aid, jewels, err)
		return s.refuse(frame, treasureRcPointChange)
	}
	d := economy.ActiveTreasureData()
	body := pbVar(nil, 1, 0)
	body = pbVar(body, 2, uint64(res.Added))
	body = pbLen(body, 3, treasureMyRank(s.aid))
	body = pbLen(body, 4, treasureSquareDetail(res.View))
	body = pbLen(body, 5, []byte(fmt.Sprintf("+%d %s", res.Added, d.Names.RankingPoint)))
	s.logger.Printf("TREASURE msgid=6 exchange aid=%d jewels=%d added=%d points=%d left=%d result=0",
		s.aid, jewels, res.Added, res.View.Points, res.View.ExchangeLeft)
	return s.reply(frame, treasureRcPointChange, body)
}

// miniGame answers cs_ranking_point_minigame_req (msgid 8, f1 the bet in Jewels): the game of the mini-game square the token
// stands on. The reply has the Ranking Points the game gave (f2), the cup's multiple the server drew (f3, one of the first three
// multiples), the account's place (f4), a message (f5) and the square's CellDetailInfo (f6: the Jewels after the bet and the
// allowance left). A refused bet gets Result 1 and changes nothing.
func (s *treasureSession) miniGame(frame, data []byte) error {
	if !s.loggedIn || !s.entered {
		s.logger.Printf("TREASURE msgid=8 minigame refused: loggedIn=%v entered=%v", s.loggedIn, s.entered)
		return s.refuse(frame, treasureRcMiniGame)
	}
	bet, _ := protobufUintField(data, 1)
	res, err := economy.TreasureMiniGamePlay(s.aidText(), int64(bet), economy.NowFunc())
	if err != nil {
		s.logger.Printf("TREASURE msgid=8 minigame refused aid=%d bet=%d: %v", s.aid, bet, err)
		return s.refuse(frame, treasureRcMiniGame)
	}
	d := economy.ActiveTreasureData()
	body := pbVar(nil, 1, 0)
	body = pbVar(body, 2, uint64(res.Added))
	body = pbVar(body, 3, uint64(res.Multiple))
	body = pbLen(body, 4, treasureMyRank(s.aid))
	body = pbLen(body, 5, []byte(fmt.Sprintf("+%d %s", res.Added, d.Names.RankingPoint)))
	body = pbLen(body, 6, treasureMiniGameDetail(d, res.View))
	s.logger.Printf("TREASURE msgid=8 minigame aid=%d bet=%d multiple=%d added=%d points=%d jewels=%d left=%d result=0",
		s.aid, bet, res.Multiple, res.Added, res.View.Points, res.View.Jewels, res.View.MiniGameLeft)
	return s.reply(frame, treasureRcMiniGame, body)
}

// resetInfo answers cs_ranking_point_reset_info_req (msgid 27) with the reset offer for the square under the token: the coin
// type (GEM only), the price and the account's Gems. A square that is not full has no offer: Result 1.
func (s *treasureSession) resetInfo(frame []byte) error {
	if !s.loggedIn || !s.entered {
		s.logger.Printf("TREASURE msgid=27 reset info refused: loggedIn=%v entered=%v", s.loggedIn, s.entered)
		return s.refuse(frame, treasureRcResetInfo)
	}
	offer, err := economy.TreasureResetInfo(s.aidText(), economy.NowFunc())
	if err != nil || !offer.Available {
		s.logger.Printf("TREASURE msgid=27 reset info refused aid=%d available=%v err=%v", s.aid, offer.Available, err)
		return s.refuse(frame, treasureRcResetInfo)
	}
	body := pbVar(nil, 1, 0)
	body = pbLen(body, 2, []byte(offer.CoinType))
	body = pbVar(body, 3, uint64(offer.Price))
	body = pbVar(body, 4, uint64(offer.Gems))
	s.logger.Printf("TREASURE msgid=27 reset info aid=%d price=%d gems=%d result=0", s.aid, offer.Price, offer.Gems)
	return s.reply(frame, treasureRcResetInfo, body)
}

// pointReset answers cs_ranking_point_reset_req (msgid 7): the full exchange square's limit is cleared for Gems. Only a
// CoinType of GEM is spent (the earned currency, through the ledger); anything else is refused. The reply carries the
// coin type, the price, the Gems after the spend and the square's counts.
func (s *treasureSession) pointReset(frame, data []byte) error {
	coin, _, _ := pbBytesField(data, 1)
	if !s.loggedIn || !s.entered {
		s.logger.Printf("TREASURE msgid=7 reset refused: loggedIn=%v entered=%v", s.loggedIn, s.entered)
		return s.refuse(frame, treasureRcPointReset)
	}
	res, err := economy.TreasureResetBuy(s.aidText(), string(coin), economy.NowFunc())
	if err != nil {
		s.logger.Printf("TREASURE msgid=7 reset refused aid=%d coin=%q: %v", s.aid, coin, err)
		return s.refuse(frame, treasureRcPointReset)
	}
	body := pbVar(nil, 1, 0)
	body = pbLen(body, 2, []byte(treasureGemsCoinType))
	body = pbVar(body, 3, uint64(res.Price))
	body = pbVar(body, 4, uint64(res.Gems))
	body = pbLen(body, 5, treasureSquareDetail(res.View))
	s.logger.Printf("TREASURE msgid=7 reset aid=%d price=%d gems=%d result=0", s.aid, res.Price, res.Gems)
	return s.reply(frame, treasureRcPointReset, body)
}

// treasureRankRow is one row of the ranking board (RankingInfo): the aid, the rank, the points, the name, the grade and the
// grade's class.
func treasureRankRow(row economy.TreasureRankRow) []byte {
	aid, _ := strconv.ParseUint(row.Aid, 10, 64)
	b := pbVar(nil, 1, aid)
	b = pbVar(b, 2, uint64(row.Rank))
	b = pbVar(b, 3, uint64(row.Points))
	b = pbLen(b, 4, []byte(row.Name))
	b = pbLen(b, 7, []byte(row.Grade.Name))
	return pbVar(b, 8, uint64(row.Grade.Type))
}

// rankingInfo answers cs_ranking_info_req (msgid 11) with the ranks start to end of the season (1-based, inclusive, one
// page), the account's own place and the page count (sc 11: Result, MyRankingInfo, RankingInfo rows, Page).
func (s *treasureSession) rankingInfo(frame, data []byte) error {
	if !s.loggedIn {
		s.logger.Printf("TREASURE msgid=11 ranking refused: no login")
		return s.refuse(frame, treasureRcRankingInfo)
	}
	start, _ := protobufUintField(data, 2)
	end, _ := protobufUintField(data, 3)
	rows, pages := economy.TreasureRankingPage(int64(start), int64(end), economy.NowFunc())
	body := pbVar(nil, 1, 0)
	body = pbLen(body, 2, treasureMyRank(s.aid))
	for _, row := range rows {
		body = pbLen(body, 3, treasureRankRow(row))
	}
	body = pbVar(body, 4, uint64(pages))
	s.logger.Printf("TREASURE msgid=11 ranking aid=%d start=%d end=%d rows=%d pages=%d result=0", s.aid, start, end, len(rows), pages)
	return s.reply(frame, treasureRcRankingInfo, body)
}

// treasurePointReward is one point reward (RankingPointReward): an item row names the item and shows the Gems with it, a
// Gem-only row uses the Gem picture (PostType gem, the same built-in picture the Mailbox uses).
func treasurePointReward(row economy.TreasurePointRow) []byte {
	b := pbVar(nil, 1, uint64(row.ID))
	b = pbVar(b, 3, uint64(row.ID))
	if row.Item != "" {
		_, _, grade, _ := economy.ItemPrice(row.Item)
		name := economy.ItemName(row.Item)
		if row.Gems > 0 {
			name = fmt.Sprintf("%s + %d Gems", name, row.Gems)
		}
		b = pbLen(b, 4, []byte(row.Item))
		b = pbLen(b, 5, []byte(grade))
		b = pbLen(b, 7, []byte(name))
		b = pbVar(b, 9, 1)
	} else {
		b = pbLen(b, 5, []byte("N"))
		b = pbLen(b, 7, []byte(fmt.Sprintf("%d Gems", row.Gems)))
		b = pbLen(b, 12, []byte(treasureRewardDefaultPicGem))
		b = pbVar(b, 9, uint64(row.Gems))
	}
	b = pbVar(b, 10, boolUint(row.Claimed))
	return pbVar(b, 11, uint64(row.Points))
}

// pointRewardInfo answers cs_ranking_point_reward_info_req (msgid 12) with the season's point rewards and which are claimed.
func (s *treasureSession) pointRewardInfo(frame []byte) error {
	if !s.loggedIn {
		s.logger.Printf("TREASURE msgid=12 point rewards refused: no login")
		return s.refuse(frame, treasureRcPointRewardInfo)
	}
	body := pbVar(nil, 1, 0)
	rows := economy.TreasurePointRows(s.aidText(), economy.NowFunc())
	for _, row := range rows {
		body = pbLen(body, 2, treasurePointReward(row))
	}
	s.logger.Printf("TREASURE msgid=12 point rewards aid=%d rows=%d result=0", s.aid, len(rows))
	return s.reply(frame, treasureRcPointRewardInfo, body)
}

// pointRewardClaim answers cs_ranking_point_reward_req (msgid 14): the reward of RewardId f1 goes to the Mailbox once the
// account has the points. The reply carries the reward's text (RewardInfo f3) and its RewardId (f4).
func (s *treasureSession) pointRewardClaim(frame, data []byte) error {
	id, _ := protobufUintField(data, 1)
	if !s.loggedIn {
		s.logger.Printf("TREASURE msgid=14 point reward refused: no login")
		return s.refuse(frame, treasureRcPointReward)
	}
	row, _, err := economy.TreasurePointClaim(s.aidText(), int64(id), economy.NowFunc())
	if err != nil {
		s.logger.Printf("TREASURE msgid=14 point reward refused aid=%d id=%d: %v", s.aid, id, err)
		return s.refuse(frame, treasureRcPointReward)
	}
	text := fmt.Sprintf("%s: %d Gems", treasureRewardNoteMailbox, row.Gems)
	if row.Item != "" {
		text += ", " + economy.ItemName(row.Item)
	}
	body := pbVar(nil, 1, 0)
	body = pbLen(body, 3, treasureRewardInfo(text, treasureViewCoin, row.Gems))
	body = pbVar(body, 4, uint64(row.ID))
	s.logger.Printf("TREASURE msgid=14 point reward aid=%d id=%d gems=%d item=%s result=0", s.aid, row.ID, row.Gems, row.Item)
	return s.reply(frame, treasureRcPointReward, body)
}

// treasureRankTier is one rank reward (RankingClassReward): its level, its cutline and one Gem row.
func treasureRankTier(level int64, tier economy.TreasureRankTier) []byte {
	item := pbVar(nil, 1, uint64(level))
	item = pbVar(item, 2, 1)
	item = pbLen(item, 4, []byte("N"))
	item = pbLen(item, 6, []byte(fmt.Sprintf("%d Gems", tier.Gems)))
	item = pbVar(item, 8, uint64(tier.Gems))
	item = pbLen(item, 9, []byte(treasureRewardDefaultPicGem))
	b := pbVar(nil, 1, uint64(level))
	b = pbVar(b, 2, uint64(tier.Cutline))
	return pbLen(b, 3, item)
}

// classRewardInfo answers cs_ranking_class_reward_info_req (msgid 13). Inside the season the list is empty and the flag
// is 0; after it the rank tiers are listed, and the flag is 1 while the reward can still be claimed.
func (s *treasureSession) classRewardInfo(frame []byte) error {
	if !s.loggedIn {
		s.logger.Printf("TREASURE msgid=13 rank rewards refused: no login")
		return s.refuse(frame, treasureRcClassRewardInfo)
	}
	offer := economy.TreasureRankInfo(s.aidText(), economy.NowFunc())
	body := pbVar(nil, 1, 0)
	if offer.Ended {
		for i, tier := range offer.Tiers {
			body = pbLen(body, 3, treasureRankTier(int64(i+1), tier))
		}
	}
	body = pbVar(body, 4, boolUint(offer.Claimable))
	s.logger.Printf("TREASURE msgid=13 rank rewards aid=%d ended=%v tiers=%d claimable=%v result=0", s.aid, offer.Ended, len(offer.Tiers), offer.Claimable)
	return s.reply(frame, treasureRcClassRewardInfo, body)
}

// classRewardClaim answers cs_ranking_class_reward_req (msgid 15) by the season cycle (season-end-spec 12 (b) and (c)). In the
// reward period a claimable rank reward goes to the Mailbox (Result 0, "Congratulations!"); one that is not claimable (none,
// or already paid) answers 786435, which opens the reward popup for the point and rank lists. Once the cycle is closed the
// answer is 65541 ("the reward distribution period has ended"). A play window never sends it: Result 1.
func (s *treasureSession) classRewardClaim(frame []byte) error {
	if !s.loggedIn {
		s.logger.Printf("TREASURE msgid=15 rank reward refused: no login")
		return s.refuse(frame, treasureRcClassReward)
	}
	now := economy.NowFunc()
	phase := economy.TreasureCurrent(now).Phase
	switch phase {
	case economy.TreasurePhaseClosed:
		s.logger.Printf("TREASURE msgid=15 rank reward aid=%d phase=closed result=%d", s.aid, treasureSeasonNotActive)
		return s.reply(frame, treasureRcClassReward, pbVar(nil, 1, treasureSeasonNotActive))
	case economy.TreasurePhasePlay:
		s.logger.Printf("TREASURE msgid=15 rank reward refused aid=%d: phase=play", s.aid)
		return s.refuse(frame, treasureRcClassReward)
	}
	if !economy.TreasureRankInfo(s.aidText(), now).Claimable {
		s.logger.Printf("TREASURE msgid=15 rank reward aid=%d phase=reward nothing to claim result=%d", s.aid, treasureRewardPopup)
		return s.reply(frame, treasureRcClassReward, pbVar(nil, 1, treasureRewardPopup))
	}
	gems, err := economy.TreasureRankClaim(s.aidText(), now)
	if err != nil {
		s.logger.Printf("TREASURE msgid=15 rank reward refused aid=%d: %v", s.aid, err)
		return s.refuse(frame, treasureRcClassReward)
	}
	body := pbVar(nil, 1, 0)
	body = pbLen(body, 2, treasureRewardInfo(fmt.Sprintf("%s: %d Gems", treasureRewardNoteMailbox, gems), treasureViewCoin, gems))
	s.logger.Printf("TREASURE msgid=15 rank reward aid=%d phase=reward gems=%d result=0", s.aid, gems)
	return s.reply(frame, treasureRcClassReward, body)
}

// capInfo answers cs_play_ticket_autogen_limit_increase_info_req (msgid 20) with the next cap raise: the cap, the top cap,
// the coin type (GEM), the price and the account's Gems. Nothing to raise: Result 1, and the client opens no popup.
func (s *treasureSession) capInfo(frame []byte) error {
	if !s.loggedIn {
		s.logger.Printf("TREASURE msgid=20 cap info refused: no login")
		return s.refuse(frame, treasureRcLimitInfo)
	}
	offer, err := economy.TreasureCapInfo(s.aidText(), economy.NowFunc())
	if err != nil || !offer.Available {
		s.logger.Printf("TREASURE msgid=20 cap info refused aid=%d available=%v err=%v", s.aid, offer.Available, err)
		return s.refuse(frame, treasureRcLimitInfo)
	}
	body := pbVar(nil, 1, 0)
	body = pbVar(body, 2, uint64(offer.Cap))
	body = pbVar(body, 3, uint64(offer.Top))
	body = pbLen(body, 4, []byte(treasureGemsCoinType))
	body = pbVar(body, 5, uint64(offer.Price))
	body = pbVar(body, 6, uint64(offer.Gems))
	s.logger.Printf("TREASURE msgid=20 cap info aid=%d cap=%d top=%d price=%d gems=%d result=0", s.aid, offer.Cap, offer.Top, offer.Price, offer.Gems)
	return s.reply(frame, treasureRcLimitInfo, body)
}

// capBuy answers cs_play_ticket_autogen_limit_increase_buy_req (msgid 21): the cap rises one step for Gems. The reply carries
// the new cap, the tickets with their timer, the coin type, the price paid and the Gems after (sc 22; Result at field 1).
func (s *treasureSession) capBuy(frame []byte) error {
	if !s.loggedIn {
		s.logger.Printf("TREASURE msgid=21 cap buy refused: no login")
		return s.refuse(frame, treasureRcLimitBuy)
	}
	offer, view, err := economy.TreasureCapBuy(s.aidText(), economy.NowFunc())
	if err != nil {
		s.logger.Printf("TREASURE msgid=21 cap buy refused aid=%d: %v", s.aid, err)
		return s.refuse(frame, treasureRcLimitBuy)
	}
	body := pbVar(nil, 1, 0)
	body = pbVar(body, 2, uint64(offer.Cap))
	body = pbVar(body, 3, uint64(offer.Top))
	body = pbVar(body, 4, uint64(view.Tickets))
	body = pbVar(body, 5, uint64(view.Max))
	body = pbVar(body, 6, uint64(view.RemainSeconds))
	body = pbVar(body, 7, uint64(view.ChargeSeconds))
	body = pbLen(body, 8, []byte(treasureGemsCoinType))
	body = pbVar(body, 9, uint64(offer.Price))
	body = pbVar(body, 10, uint64(offer.Gems))
	s.logger.Printf("TREASURE msgid=21 cap buy aid=%d cap=%d paid=%d gems=%d result=0", s.aid, offer.Cap, offer.Price, offer.Gems)
	return s.reply(frame, treasureRcLimitBuy, body)
}

package lpn

import (
	"cherry/internal/economy"
)

// Treasure World buffs (gateway agent 9; treasure-spec and slice3b-spec section C): the list (cs 5 → sc 5) and the wear (cs 22 →
// sc 23). A buff is worn for Jewels (economy/treasure_buff.go); the reply carries the worn total for the HUD.

// treasureBuffItem is one BuffItemInfo row: the gacha id, its name, the item, its grade ("RARE", the client maps RARE..ULTRA_EPIC), IconState 1 (0 draws a coming-soon cell, buff-crash.md), its BuffPercent and its state
// (WEAR worn, INVEN owned and not worn). IconState and the picture are not sent.
func treasureBuffItem(row economy.TreasureBuffRow) []byte {
	state := "INVEN"
	if row.Worn {
		state = "WEAR"
	}
	b := pbVar(nil, 1, treasureBuffGacha)
	b = pbLen(b, 2, []byte(treasureBuffGachaName))
	b = pbLen(b, 4, []byte(row.Code))
	b = pbVar(b, 3, 1)
	b = pbLen(b, 5, []byte("RARE"))
	b = pbVar(b, 8, uint64(row.Percent))
	return pbLen(b, 9, []byte(state))
}

// buffList answers cs_buff_list_req (msgid 5, empty) with the buffs the account owns (f2) and the worn total (f3, BuffPersent).
func (s *treasureSession) buffList(frame []byte) error {
	if !s.loggedIn {
		s.logger.Printf("TREASURE msgid=5 buff list refused: no login")
		return s.refuse(frame, treasureRcBuffList)
	}
	buffs, err := economy.TreasureBuffList(s.aidText(), economy.NowFunc())
	if err != nil {
		s.logger.Printf("TREASURE msgid=5 buff list refused aid=%d: %v", s.aid, err)
		return s.refuse(frame, treasureRcBuffList)
	}
	body := pbVar(nil, 1, 0)
	for _, row := range buffs.Items {
		body = pbLen(body, 2, treasureBuffItem(row))
	}
	body = pbVar(body, 3, uint64(buffs.Percent))
	s.logger.Printf("TREASURE msgid=5 buff list aid=%d owned=%d percent=%d result=0", s.aid, len(buffs.Items), buffs.Percent)
	return s.reply(frame, treasureRcBuffList, body)
}

// buffWear answers cs_buff_item_wear_req (msgid 22): repeated BuffItem (f1, ItemCode string; f2 InvenSeq) names the buffs to wear,
// and none takes every buff off. The reply carries Result (f1), the worn total (f2), the owned buffs (f3) and the account's own
// avatar (f4): the client replaces its avatar with f4, so it is never left out. A list that names a buff the account does not
// own, or names one twice, is refused and changes nothing.
func (s *treasureSession) buffWear(frame, data []byte) error {
	if !s.loggedIn {
		s.logger.Printf("TREASURE msgid=22 buff wear refused: no login")
		return s.refuse(frame, treasureRcBuffWear)
	}
	var codes []string
	if !pbScan(data, func(f, _ uint64, b []byte) {
		if f == 1 {
			code, _, _ := pbBytesField(b, 1)
			codes = append(codes, string(code))
		}
	}) {
		s.logger.Printf("TREASURE msgid=22 buff wear refused aid=%d: malformed list", s.aid)
		return s.refuse(frame, treasureRcBuffWear)
	}
	buffs, err := economy.TreasureBuffWear(s.aidText(), codes, economy.NowFunc())
	if err != nil {
		s.logger.Printf("TREASURE msgid=22 buff wear refused aid=%d codes=%v: %v", s.aid, codes, err)
		return s.refuse(frame, treasureRcBuffWear)
	}
	body := pbVar(nil, 1, 0)
	body = pbVar(body, 2, uint64(buffs.Percent))
	for _, row := range buffs.Items {
		body = pbLen(body, 3, treasureBuffItem(row))
	}
	body = pbLen(body, 4, treasureOwnAvatar(s.aid))
	s.logger.Printf("TREASURE msgid=22 buff wear aid=%d codes=%v percent=%d result=0", s.aid, codes, buffs.Percent)
	return s.reply(frame, treasureRcBuffWear, body)
}

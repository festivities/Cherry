package store

import (
	"strconv"
)

const (
	MailTypeGems    = "gems"
	MailTypeCash    = "cash"
	MailTypeTickets = "faceTickets"
	mailTitle       = "Gift from Cherry"
)

// MailRow is one unclaimed mail; Created/Expires are epoch milliseconds.
type MailRow struct {
	Seq     int64  `json:"postSeq"`
	Type    string `json:"type"` // gems | cash | faceTickets
	Amount  int64  `json:"amount"`
	Message string `json:"message"`
	Sender  string `json:"sender"`
	Created int64  `json:"created"`
	Expires int64  `json:"expires"`
}

func (m MailRow) Deltas() LedgerDeltas {
	switch m.Type {
	case MailTypeCash:
		return LedgerDeltas{Cash: m.Amount}
	case MailTypeTickets:
		return LedgerDeltas{FaceTickets: m.Amount}
	}
	return LedgerDeltas{Gems: m.Amount}
}

// PostRow is the native Post JSON; every string field is asString, ints asInt.
func (m MailRow) PostRow() map[string]any {
	postType, data, detail := "gem", strconv.FormatInt(m.Amount, 10), ""
	switch m.Type {
	case MailTypeCash:
		postType = "cash"
	case MailTypeTickets:
		postType, data, detail = "voucher", "0", strconv.FormatInt(m.Amount, 10)
	}
	return map[string]any{
		"postSeq": strconv.FormatInt(m.Seq, 10), "postType": postType,
		"productData": data, "productDataDetail": detail, "productImageUrl": "",
		"title": mailTitle, "content": m.Message,
		"expireDate": strconv.FormatInt(m.Expires, 10), "fromBlocked": false,
		"itemType": "", "itemGradeType": "", "dyeType": 0, "itemSpecialEffects": "",
		"sent": strconv.FormatInt(m.Created, 10), "flagType": "", "recycleReward": 0,
		"canReply":     false,
		"senderAvatar": map[string]any{"avatarId": "0", "name": m.Sender, "items": []any{}},
	}
}

type PetItem struct {
	ID   int64  `json:"id"`
	Cd   string `json:"cd"`
	Name string `json:"name"`
	Rep  bool   `json:"rep,omitempty"`  // follows the owner (Garden, My Room)
	Room string `json:"room,omitempty"` // "LEVEL_n" when arranged on that My Room floor
}

type RoomItem struct {
	Seq int64  `json:"seq"`
	Cd  string `json:"cd"`
}

type RoomPlaced struct {
	Seq       int64   `json:"seq"`
	Cd        string  `json:"cd"`
	X         int     `json:"x"`
	Y         int     `json:"y"`
	Z         int     `json:"z"`
	Dir       string  `json:"dir"`
	Superpose []int64 `json:"superpose,omitempty"`
}

type RoomLayout struct {
	Floor  RoomItem     `json:"floor"`
	Wall   RoomItem     `json:"wall"`
	Placed []RoomPlaced `json:"placed"`
}

// roomNextSeq keeps the allocator above every owned seq after a load.
func roomNextSeq(items []RoomItem, next int64) int64 {
	next = max(next, FirstRoomSeq)
	for _, it := range items {
		next = max(next, it.Seq+1)
	}
	return next
}

// RoomPreset is one saved quick-pick slot: a snapshot of a level layout plus the
// uploaded thumbnail URL the client sent as representImagePath.
type RoomPreset struct {
	Layout   RoomLayout `json:"layout"`
	Level    string     `json:"level"`
	Image    string     `json:"image"`
	TileSize int        `json:"tileSize"`
}

const FirstRoomSeq = 100000

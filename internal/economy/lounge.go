package economy

import (
	"errors"
	"fmt"
	"strings"

	"cherry/data"
	"cherry/internal/opdata"
)

// Lounge Chat (the theme chat on the session channel, lpn/themechat.go): the one category and its rooms. The client has
// no lounge names and no category pictures (string table and archives checked 2026-10-30), so Cherry makes all of it.
// data/lounge.json is the operator file (an operator file with the same name in %LOCALAPPDATA%\Cherry\config replaces
// it). Strict JSON: unknown keys are an error.
//
//	origin, note   Cherry-made (opdata.Provenance)
//	catgId         the category id, inside the Cherry block and not 0 (the client sends 0 for its hot list)
//	catgName       the category name
//	historyLimit   messages kept per room, 1 to 1000; the oldest go first
//	maxTextChars   the longest text a message may carry, in characters, 1 to 500
//	rooms          1 to 20 rooms: roomId (inside the Cherry block, unique, not catgId), roomName, limitMemberCnt (1 to 100)
type Lounge struct {
	opdata.Provenance
	CatgID       int64        `json:"catgId"`
	CatgName     string       `json:"catgName"`
	HistoryLimit int          `json:"historyLimit"`
	MaxTextChars int          `json:"maxTextChars"`
	Rooms        []LoungeRoom `json:"rooms"`
}

// LoungeRoom is one room of the category.
type LoungeRoom struct {
	RoomID         int64  `json:"roomId"`
	RoomName       string `json:"roomName"`
	LimitMemberCnt int    `json:"limitMemberCnt"`
}

// CherryLoungeFirst and CherryLoungeLast bound the Lounge Chat ids Cherry makes (the category and its rooms). checkLounge
// refuses an id outside them. The block sits above the butler (1900000000) and spin (1920000000) blocks and far above
// the item ids (3264xxxxx at most).
const (
	CherryLoungeFirst int64 = 1930000000
	CherryLoungeLast  int64 = 1930000999
)

var loungeFile = opdata.New("lounge.json", data.LoungeJSON, checkLounge, nil)

// ActiveLounge is the Lounge Chat list in use.
func ActiveLounge() *Lounge { return loungeFile.Get() }

// checkLounge validates the structure at startup: the origin and note, the category, the history and text limits and
// the rooms.
func checkLounge(l *Lounge) error {
	if err := l.CheckOrigin("cherry"); err != nil {
		return err
	}
	if !inCherryBlock(l.CatgID, CherryLoungeFirst, CherryLoungeLast) {
		return fmt.Errorf("catgId %d is outside the Cherry block %d-%d", l.CatgID, CherryLoungeFirst, CherryLoungeLast)
	}
	if strings.TrimSpace(l.CatgName) == "" {
		return errors.New("catgName must not be empty")
	}
	if l.HistoryLimit < 1 || l.HistoryLimit > 1000 {
		return errors.New("historyLimit must be 1 to 1000")
	}
	if l.MaxTextChars < 1 || l.MaxTextChars > 500 {
		return errors.New("maxTextChars must be 1 to 500")
	}
	if len(l.Rooms) < 1 || len(l.Rooms) > 20 {
		return errors.New("rooms must hold 1 to 20 rooms")
	}
	seen := map[int64]bool{l.CatgID: true}
	for i, r := range l.Rooms {
		where := fmt.Sprintf("rooms[%d] (roomId %d)", i+1, r.RoomID)
		switch {
		case !inCherryBlock(r.RoomID, CherryLoungeFirst, CherryLoungeLast):
			return fmt.Errorf("%s: roomId is outside the Cherry block %d-%d", where, CherryLoungeFirst, CherryLoungeLast)
		case seen[r.RoomID]:
			return fmt.Errorf("%s: roomId must be unique and differ from catgId", where)
		case strings.TrimSpace(r.RoomName) == "":
			return fmt.Errorf("%s: roomName must not be empty", where)
		case r.LimitMemberCnt < 1 || r.LimitMemberCnt > 100:
			return fmt.Errorf("%s: limitMemberCnt must be 1 to 100", where)
		}
		seen[r.RoomID] = true
	}
	return nil
}

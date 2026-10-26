package lpn

import (
	"log"
	"strconv"

	"cherry/internal/economy"
)

// Garden landscapes: the stock Garden skin editor lists the owner's landscapes (agent 10 idx 4 -> 5),
// previews one (idx 6 -> 7) and saves the change (idx 5 -> 6). Field numbers and the flow:
// %TEMP%\opencode\cherry\garden-tour-20261026\landscape-spec.md sections 2, 5 and 9. The change request
// carries no item code, so the server applies the map of this connection's last successful preview. The
// client leaves and re-enters the Garden after a change, and room enter then shows the new map.
// Ownership and the map list are in economy/landscape.go.

// gardenLandscape is one connection's editor state: the map its last successful preview named. A change
// applies it and clears it, and a failed preview clears it too.
type gardenLandscape struct {
	stem string
}

// gardenLandscapeMsg reports whether msgid is an editor request: 4 list, 5 change, 6 preview.
func gardenLandscapeMsg(msgid uint16) bool {
	switch msgid {
	case 4, 5, 6:
		return true
	}
	return false
}

// gardenLandscapeReply answers one editor request. viewer is the account on the connection, owner the
// account whose Garden it is in; only the owner changes a Garden.
func gardenLandscapeReply(msgid uint16, viewer, owner uint64, data []byte, st *gardenLandscape, logger *log.Logger) gardenMsg {
	switch msgid {
	case 4:
		return gardenLandscapeList(viewer, logger)
	case 5:
		return gardenLandscapeChange(viewer, owner, st, logger)
	default: // 6, the preview
		return gardenLandscapePreview(viewer, data, st, logger)
	}
}

// gardenLandscapeList is hc_my_garden_item_list_res: one MyGardenItem per landscape the viewer owns,
// seq = the numeric item id and itemCode = the GULS code. The editor menu filters on the id alone.
func gardenLandscapeList(viewer uint64, logger *log.Logger) gardenMsg {
	aid := strconv.FormatUint(viewer, 10)
	owned := economy.OwnedLandscapes(aid)
	current := economy.GardenMap(aid)
	body := pbVar(nil, 1, 0) // result
	for _, l := range owned {
		item := pbVar(nil, 1, uint64(l.ID))   // seq
		item = pbLen(item, 2, []byte("LS"))   // categoryCode, a guess (landscape-spec section 8.6)
		item = pbLen(item, 3, []byte(l.Code)) // itemCode
		if l.Map == current {
			item = pbVar(item, 4, 1) // using: the editor enables SAVE only when the in-use landscape changes
		}
		body = pbLen(body, 2, item)
	}
	logger.Printf("GARDEN landscape list aid=%s items=%d result=0", aid, len(owned))
	return gardenMsg{5, body}
}

// gardenLandscapePreview is hc_preview_map_info_res for the landscape named by itemCode (field 1). A
// landscape the viewer does not own is refused, and the client shows its message 10077.
func gardenLandscapePreview(viewer uint64, data []byte, st *gardenLandscape, logger *log.Logger) gardenMsg {
	aid := strconv.FormatUint(viewer, 10)
	code, _, _ := pbBytesField(data, 1)
	st.stem = ""
	stem, ok := economy.OwnedLandscapeMap(aid, string(code))
	if !ok {
		logger.Printf("GARDEN landscape preview aid=%s item=%q result=%d", aid, code, gatherRefused)
		return gardenMsg{7, pbVar(nil, 1, gatherRefused)}
	}
	st.stem = stem
	logger.Printf("GARDEN landscape preview aid=%s item=%q map=%s result=0", aid, code, stem)
	body := pbVar(nil, 1, 0)
	body = pbLen(body, 2, []byte(stem))
	return gardenMsg{7, pbLen(body, 3, gardenSpotList(stem))}
}

// gardenLandscapeChange is hc_change_garden_item_and_map_res: the previewed map becomes the viewer's
// Garden map, saved at once. Without a preview, or in someone else's Garden, nothing changes.
func gardenLandscapeChange(viewer, owner uint64, st *gardenLandscape, logger *log.Logger) gardenMsg {
	aid := strconv.FormatUint(viewer, 10)
	stem := st.stem
	st.stem = ""
	if stem == "" || viewer != owner {
		logger.Printf("GARDEN landscape change aid=%s owner=%d map=%q result=%d", aid, owner, stem, gatherRefused)
		return gardenMsg{6, pbVar(nil, 1, gatherRefused)}
	}
	if err := economy.SetGardenMap(aid, stem); err != nil {
		logger.Printf("GARDEN landscape change aid=%s map=%s err=%v result=%d", aid, stem, err, gatherRefused)
		return gardenMsg{6, pbVar(nil, 1, gatherRefused)}
	}
	logger.Printf("GARDEN landscape change aid=%s map=%s result=0", aid, stem)
	body := pbVar(nil, 1, 0)
	body = pbLen(body, 2, gardenSpotList(stem)) // npc_info; the client ignores it after a save
	body = pbLen(body, 3, []byte(stem))         // map_file_name; ignored too
	return gardenMsg{6, pbVar(body, 4, 0)}      // client_map_revision: the client never reads it
}

// gardenLandscapeHandle answers one editor request on the relayed Garden tunnel.
func gardenLandscapeHandle(conn *lockedConn, frame []byte, msgid uint16, viewer, owner uint64, st *gardenLandscape, logger *log.Logger) error {
	m := gardenLandscapeReply(msgid, viewer, owner, frame[12:], st, logger)
	_, err := conn.Write(squareFrame(frame[1], frame[2:10], m.id, m.body))
	return err
}

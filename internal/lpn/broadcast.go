package lpn

import (
	"encoding/json"
	"log"
	"strconv"
	"strings"
	"time"

	"cherry/internal/economy"
)

type maintPushMessage struct {
	EN string `json:"en"`
	KO string `json:"ko"`
	JA string `json:"ja"`
}

type maintPushParams struct {
	Seq         int              `json:"seq"`
	Target      string           `json:"target"`
	Type        string           `json:"type"`
	Start       string           `json:"start"`
	End         string           `json:"end"`
	DisplayType string           `json:"displayType"`
	Message     maintPushMessage `json:"message"`
}

type maintPushPayload struct {
	Catg   string          `json:"catg"`
	MsgID  string          `json:"msgid"`
	Params maintPushParams `json:"params"`
}

// SessionCount returns the number of currently connected :10123 sessions.
func SessionCount() int {
	sessionConnsMu.Lock()
	defer sessionConnsMu.Unlock()
	return len(sessionConns)
}

func snapshotSessionConns() []*lockedConn {
	sessionConnsMu.Lock()
	defer sessionConnsMu.Unlock()
	conns := make([]*lockedConn, 0, len(sessionConns))
	for _, c := range sessionConns {
		if c != nil {
			conns = append(conns, c)
		}
	}
	return conns
}

func broadcastLOFrame(frame []byte) (sent, failed int) {
	for _, c := range snapshotSessionConns() {
		if _, err := c.Write(frame); err != nil {
			failed++
		} else {
			sent++
		}
	}
	return sent, failed
}

// BroadcastBanner sends a non-blocking in-game top notification banner and chime
// (catg "notice", params.pType "PBO", ShowPushMessage) to every live :10123 session.
func BroadcastBanner(message string) (sent, failed int) {
	if strings.TrimSpace(message) == "" {
		return 0, 0
	}
	payload, err := json.Marshal(pushNotiPayload{
		Catg:          "notice",
		MsgID:         strconv.FormatInt(economy.NowFunc().UnixMilli(), 10),
		Sender:        "0",
		Content:       message,
		Badge:         0,
		SID:           "",
		CustomContent: false,
		Params: pushNotiParams{
			PType:     "PBO",
			BadgeInfo: PushBadgeInfo{},
		},
	})
	if err != nil {
		return 0, 0
	}
	sent, failed = broadcastLOFrame(buildLOFrame(loOpPush, 0, payload))
	log.Printf("cherry: broadcast banner sent=%d failed=%d", sent, failed)
	return sent, failed
}

// BroadcastMaintPopup sends a blocking modal maintenance popup (catg "maint",
// displayType "POPUP", NaScheduleErrPopup) to every live :10123 session.
func BroadcastMaintPopup(message string, duration time.Duration) (sent, failed int) {
	if strings.TrimSpace(message) == "" || duration <= 0 {
		return 0, 0
	}
	now := economy.NowFunc()
	startMs := now.Add(-time.Minute).UnixMilli()
	endMs := now.Add(duration).UnixMilli()
	payload, err := json.Marshal(maintPushPayload{
		Catg:  "maint",
		MsgID: strconv.FormatInt(now.UnixMilli(), 10),
		Params: maintPushParams{
			Seq:         int(now.Unix()),
			Target:      "ALL",
			Type:        "MAINT",
			Start:       strconv.FormatInt(startMs, 10),
			End:         strconv.FormatInt(endMs, 10),
			DisplayType: "POPUP",
			Message: maintPushMessage{
				EN: message,
				KO: message,
				JA: message,
			},
		},
	})
	if err != nil {
		return 0, 0
	}
	sent, failed = broadcastLOFrame(buildLOFrame(loOpPush, 0, payload))
	log.Printf("cherry: broadcast maint-popup sent=%d failed=%d duration=%s", sent, failed, duration)
	return sent, failed
}

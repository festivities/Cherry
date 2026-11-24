package lpn

import (
	"encoding/json"
	"fmt"
	"log"
	"strconv"

	"cherry/internal/economy"
	"cherry/internal/store"
)

// PushBadgeInfo carries the eight badge counts inside params.badgeInfo of an LO 0x1B push
// notification (push-noti spec 3.2). All fields are always emitted so NaPushInfo::ParseOnlineInfo
// reads every count directly.
type PushBadgeInfo struct {
	NEWS int `json:"NEWS"`
	CHAT int `json:"CHAT"`
	NFRD int `json:"NFRD"`
	IFRD int `json:"IFRD"`
	GIFT int `json:"GIFT"`
	CSET int `json:"CSET"`
	FACE int `json:"FACE"`
	ROOM int `json:"ROOM"`
}

type pushNotiParams struct {
	PType     string        `json:"pType"`
	BadgeInfo PushBadgeInfo `json:"badgeInfo"`
}

type pushNotiPayload struct {
	Catg          string         `json:"catg"`
	MsgID         string         `json:"msgid"`
	Sender        string         `json:"sender"`
	Content       string         `json:"content"`
	Badge         int            `json:"badge"`
	SID           string         `json:"sid"`
	CustomContent bool           `json:"customContent"`
	Params        pushNotiParams `json:"params"`
}

// PushHeartNoti sends an LO op 0x1B heart-received notification (catg "heart", params.pType "HTR")
// to targetAid's live :10123 session. No-op when targetAid is empty, self-targeted, invalid, or offline.
func PushHeartNoti(targetAid, senderAid, senderName string) bool {
	name := senderName
	if name == "" {
		name = "Someone"
	}
	return sendPushNoti("heart", targetAid, senderAid, fmt.Sprintf("%s sent you a Heart!", name), "HTR", 1, PushBadgeInfo{})
}

// PushFriendNoti sends an LO op 0x1B friend notification (catg "friend", params.pType "FRR" or "FRA")
// to targetAid's live :10123 session. No-op when targetAid is empty, self-targeted, invalid, or offline.
func PushFriendNoti(targetAid, senderAid, senderName, pType string, ifrd, nfrd int) bool {
	name := senderName
	if name == "" {
		name = "Someone"
	}
	var content string
	switch pType {
	case "FRR":
		content = fmt.Sprintf("%s sent you a friend request.", name)
	case "FRA":
		content = fmt.Sprintf("%s accepted your friend request.", name)
	default:
		return false
	}
	ifrd = max(0, ifrd)
	nfrd = max(0, nfrd)
	return sendPushNoti("friend", targetAid, senderAid, content, pType, max(1, ifrd+nfrd), PushBadgeInfo{
		NFRD: nfrd,
		IFRD: ifrd,
	})
}

func sendPushNoti(catg, targetAid, senderAid, content, pType string, badge int, info PushBadgeInfo) bool {
	if targetAid == "" || targetAid == senderAid {
		return false
	}
	target, err := strconv.ParseUint(targetAid, 10, 64)
	if err != nil || target == 0 {
		return false
	}
	payload, err := json.Marshal(pushNotiPayload{
		Catg:          catg,
		MsgID:         strconv.FormatInt(economy.NowFunc().UnixMilli(), 10),
		Sender:        senderAid,
		Content:       content,
		Badge:         badge,
		SID:           "",
		CustomContent: false,
		Params: pushNotiParams{
			PType:     pType,
			BadgeInfo: info,
		},
	})
	if err != nil {
		return false
	}
	sent, err := sessionPush(target, loOpPush, payload)
	if err != nil {
		log.Printf("SESSION push %s pType=%s from=%s to=%s sent=%v err=%v", catg, pType, senderAid, targetAid, sent, err)
		return false
	}
	log.Printf("SESSION push %s pType=%s from=%s to=%s sent=%v", catg, pType, senderAid, targetAid, sent)
	return sent
}

func accountDisplayName(aid string) string {
	if acc, ok := store.AccountByAvatarID(aid); ok {
		return acc.Name
	}
	return ""
}

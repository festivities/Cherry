package lpn

import (
	"log"
	"strconv"
	"time"

	"cherry/internal/economy"
	"cherry/internal/store"
)

// Player reports from the chat gateways: Circle cs_report_chat (msgid 14), Square cr_report_chat (msgid 12) and Room Party
// cr_report_chat (msgid 13). Protobuf fields, the same on all three (the client's report popup): 1 target aid (int64), 2
// target nickname, 3 target country, 4 report policy (a string the popup presets), 5 the reporter's remark (the text the
// reporter typed), 6 chat serial (int64), 7 content code "R<n>" (the reason). The reporter is the connection's verified
// login. The client shows its own confirmation, so nothing is sent back. Nothing here logs the remark or the chat text.

// reportGateway stores one gateway report from the request body data. aid is the verified reporter; channel is the chat
// channel the reporter is in (store.ReportIn.Channel). A report the store refuses as invalid is dropped with one log line.
func reportGateway(logger *log.Logger, source string, aid uint64, data []byte, channel string) {
	target, _ := protobufUintField(data, 1)
	policy, _, _ := pbBytesField(data, 4)
	remark, _, _ := pbBytesField(data, 5)
	serial, hasSerial := protobufUintField(data, 6)
	code, _, _ := pbBytesField(data, 7)
	in := store.ReportIn{
		Source: source, Reporter: strconv.FormatUint(aid, 10), Target: strconv.FormatUint(target, 10),
		Reason: string(code), Message: string(remark), RefID: string(policy), Channel: channel,
	}
	if hasSerial {
		in.RefDetail = strconv.FormatUint(serial, 10)
	}
	stored, err := store.FileReport(in, economy.GameDayStart(time.Now()))
	switch {
	case err != nil:
		logger.Printf("report not stored source=%s: %v", source, err)
	case !stored:
		logger.Printf("report over the daily cap, not stored source=%s", source)
	}
}

// report stores a Circle chat report (cs_report_chat). The channel is the circle the session entered last.
func (s *circleSession) report(data []byte) {
	channel := ""
	if s.circleID != 0 {
		channel = strconv.FormatInt(s.circleID, 10)
	}
	reportGateway(s.logger, store.ReportCircle, s.aid, data, channel)
}

// report stores a Square chat report (cr_report_chat, msgid 12). The channel is the Square's theme id, when the session has
// entered a Square.
func (s *squareSession) report(data []byte) {
	channel := ""
	if s.entered {
		channel = strconv.FormatUint(s.square.Theme, 10)
	}
	reportGateway(s.logger, store.ReportSquare, s.aid, data, channel)
}

// report stores a Room Party chat report (cr_report_chat, msgid 13). The channel is the host of the room the session is in
// (the room it last entered when it has left).
func (s *roomPartySession) report(data []byte) {
	host := s.host
	if s.joined != nil {
		host = s.joined.host
	}
	channel := ""
	if host != 0 {
		channel = strconv.FormatUint(host, 10)
	}
	reportGateway(s.logger, store.ReportRoomParty, s.aid, data, channel)
}

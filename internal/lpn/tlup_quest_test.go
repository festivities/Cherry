package lpn

import (
	"bytes"
	"log"
	"net"
	"testing"
)

// TestGatewayTLUPQuestListReply pins the reply to a TLUP ct_questlist_req (agent 5, msgid 0). The
// client's NaQuestManager::OnReceiveQuestInfo sets NaDataInvoker+316 (the quest-list gate for the
// Garden popup HEART row) for any non-null tc_questlist_res, so the empty protobuf body must keep
// going out with the request's session, agent, packetId and msgid. Wire notes: quests.md 3.2.
func TestGatewayTLUPQuestListReply(t *testing.T) {
	server, client := net.Pipe()
	cleared := make(chan struct{}, 1)
	var output bytes.Buffer
	done := make(chan struct{})
	go func() {
		observeConn(deadlineRecordingConn{server, cleared}, GatewayAddr, log.New(&output, "", 0))
		close(done)
	}()
	defer func() {
		client.Close()
		<-done
	}()
	if banner := readFullDeadline(t, client, len(sessionBanner)); !bytes.Equal(banner, sessionBanner) {
		t.Fatalf("gateway banner = %x", banner)
	}
	// ct_questlist_req body: BNotAssinged #2 = false, BExpired #3 = false.
	body := []byte{0x10, 0x00, 0x18, 0x00}
	frame := []byte{'D', 0, 0, 1, 0, 1, 0, 0x6f, 0, 0, 5, 0, 0, 0, 0x42, 0, 0, 0}
	frame = append(frame, body...)
	wire := append([]byte{0, 0, 0, byte(len(frame))}, frame...)
	if _, err := client.Write(wire); err != nil {
		t.Fatalf("questlist write: %v", err)
	}
	// Reply: length 18 (header only, empty tc_questlist_res), echoed session, agent 5, packetId, msgid 0.
	want := []byte{0, 0, 0, 18, 'D', 0, 0, 1, 0, 1, 0, 0x6f, 0, 0, 5, 0, 0, 0, 0x42, 0, 0, 0}
	if reply := readFullDeadline(t, client, len(want)); !bytes.Equal(reply, want) {
		t.Fatalf("questlist reply = %x, want %x", reply, want)
	}
}

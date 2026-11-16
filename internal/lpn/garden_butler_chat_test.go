package lpn

import (
	"io"
	"log"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"cherry/internal/economy"
	"cherry/internal/store"
	"cherry/internal/testutil"
)

// chatButton is one ButlerDialogPopupButtonInfo of a Chat reply: its slot, label and state.
type chatButton struct {
	index uint64
	text  string
	state uint64
}

// chatButtons decodes a Chat reply's buttonList (f3, butler-robo-spec 10.2).
func chatButtons(t *testing.T, body []byte) []chatButton {
	t.Helper()
	var out []chatButton
	for _, b := range pbAll(t, body, 3) {
		index, _ := protobufUintField(b, 1)
		text, _, _ := pbBytesField(b, 2)
		state, _ := protobufUintField(b, 3)
		out = append(out, chatButton{index, string(text), state})
	}
	return out
}

// TestButlerChatDialogOnTheWire: the Chat popup (ch 48 to hc 57, butler-robo-spec 10.2) for a butler under contract: result 0,
// the greeting from the data, the five buttons with their labels, the gathering slot in the state the Book card shows (idle 0,
// collecting 1, done 2), the butler's ButlerInfo (chief flag, voice record) and her first voice line. A butler without voices
// has no voice line; a butler not under contract and an unknown one are refused with result 1.
func TestButlerChatDialogOnTheWire(t *testing.T) {
	now := time.Date(2026, 11, 17, 3, 0, 0, 0, time.UTC)
	const merry, ophelia, unknown = 1900000006, 1900000004, 1900000050
	logger := log.New(io.Discard, "", 0)
	hire := func(id int64) []store.ButlerHire {
		return []store.ButlerHire{{ID: id, Begin: now.UnixMilli(), End: now.UnixMilli() + 30*86400000}}
	}
	buttons := func(gather chatButton) []chatButton {
		return []chatButton{
			{1, "I'm bored! Play with me!", 0}, gather, {3, "Take me to the butler list!", 0},
			{4, "Make me a special order!", 0}, {5, "Memories", 0},
		}
	}
	gathering := func(from, to time.Duration) []store.ButlerGather {
		return []store.ButlerGather{{ID: merry, Begin: now.Add(from).UnixMilli(), End: now.Add(to).UnixMilli()}}
	}
	cases := []struct {
		name    string
		book    *store.ButlerBook
		buttons []chatButton
	}{
		{"idle", &store.ButlerBook{Leader: merry, Hired: hire(merry)}, buttons(chatButton{2, "Please gather!", 0})},
		{"collecting", &store.ButlerBook{Leader: merry, Hired: hire(merry), Gathering: gathering(-time.Hour, time.Hour)},
			buttons(chatButton{2, "Currently collecting.", 1})},
		{"done", &store.ButlerBook{Leader: merry, Hired: hire(merry), Gathering: gathering(-2*time.Hour, -time.Hour)},
			buttons(chatButton{2, "Collecting complete!", 2})},
	}
	tokens := map[string]*store.Account{}
	for i, c := range cases {
		aid := strconv.Itoa(3600 + i)
		tokens["chat-"+aid] = &store.Account{AccessToken: "chat-" + aid, SessionKey: "sk", Aid: aid, Name: "Chat", Butlers: c.book}
	}
	// Ophelia (no voices) under contract, and Merry with no contract, for the refusals below.
	tokens["chat-3603"] = &store.Account{AccessToken: "chat-3603", SessionKey: "sk", Aid: "3603", Name: "Chat",
		Butlers: &store.ButlerBook{Leader: ophelia, Hired: hire(ophelia)}}
	tokens["chat-3604"] = &store.Account{AccessToken: "chat-3604", SessionKey: "sk", Aid: "3604", Name: "Chat"}
	testutil.InstallAccounts(t, tokens)

	m, _ := economy.ButlerByID(merry)
	greeting := economy.ActiveButlers().ChatGreeting
	if greeting == "" {
		t.Fatal("the data has no chat greeting")
	}
	for i, c := range cases {
		msgs := gardenButlerReply(48, uint64(3600+i), pbVar(nil, 1, merry), now, logger)
		if len(msgs) != 1 || msgs[0].id != 57 {
			t.Fatalf("%s: got %d frames, want one hc 57", c.name, len(msgs))
		}
		body := msgs[0].body
		checkButlerWire(t, "dialog", body)
		if result, _ := protobufUintField(body, 1); result != 0 {
			t.Fatalf("%s: result %d, want 0", c.name, result)
		}
		if line, _, _ := pbBytesField(body, 2); string(line) != greeting {
			t.Fatalf("%s: greeting %q, want %q", c.name, line, greeting)
		}
		if got := chatButtons(t, body); !slices.Equal(got, c.buttons) {
			t.Fatalf("%s: buttons %+v, want %+v", c.name, got, c.buttons)
		}
		info, found, _ := pbBytesField(body, 4)
		if !found {
			t.Fatalf("%s: no ButlerInfo", c.name)
		}
		if id, _ := protobufUintField(info, 1); id != merry {
			t.Fatalf("%s: ButlerInfo id %d, want %d", c.name, id, merry)
		}
		if chief, _ := protobufUintField(info, 4); chief != 1 {
			t.Fatalf("%s: the chief flag is %d, want 1 (Merry is the chief)", c.name, chief)
		}
		if _, employed, _ := pbBytesField(info, 6); !employed {
			t.Fatalf("%s: the contract is missing from ButlerInfo", c.name)
		}
		voice, _, _ := pbBytesField(info, 9)
		record, _, _ := pbBytesField(voice, 2)
		if zip, _, _ := pbBytesField(record, 4); string(zip) != m.Voices.ZipPath() {
			t.Fatalf("%s: voice record zip %q, want %q", c.name, zip, m.Voices.ZipPath())
		}
		if file, found, _ := pbBytesField(body, 5); !found || string(file) != "butler_voice_master/3/voice_001.ogg" {
			t.Fatalf("%s: first voice line %q, want voice_001.ogg", c.name, file)
		}
	}

	// Ophelia has no voices: no voice line and no voice record in her ButlerInfo.
	msgs := gardenButlerReply(48, 3603, pbVar(nil, 1, ophelia), now, logger)
	checkButlerWire(t, "dialog", msgs[0].body)
	if _, found, _ := pbBytesField(msgs[0].body, 5); found {
		t.Fatal("a butler without voices has a voice line in its Chat")
	}
	info, _, _ := pbBytesField(msgs[0].body, 4)
	if _, found, _ := pbBytesField(info, 9); found {
		t.Fatal("a butler without voices carries a voice record")
	}

	// Refusals: a butler not under contract and an unknown butler get result 1 and no greeting.
	for _, c := range []struct {
		aid uint64
		id  int64
	}{{3604, merry}, {3600, unknown}} {
		msgs := gardenButlerReply(48, c.aid, pbVar(nil, 1, uint64(c.id)), now, logger)
		if len(msgs) != 1 || msgs[0].id != 57 {
			t.Fatalf("refused chat %d: got %d frames, want one hc 57", c.id, len(msgs))
		}
		checkButlerWire(t, "result", msgs[0].body)
		if result, _ := protobufUintField(msgs[0].body, 1); result != butlerRefused {
			t.Fatalf("refused chat %d: result %d, want %d", c.id, result, butlerRefused)
		}
	}

	// Talk (ch 49 to hc 58): for a butler under contract, result 0 and the Talk popup's content (the butler, the talk line
	// and one of its voice lines; a reply without it opened an empty popup on the device), and a refusal without one.
	msgs = gardenButlerReply(49, 3600, pbVar(nil, 1, merry), now, logger)
	if len(msgs) != 1 || msgs[0].id != 58 {
		t.Fatalf("talk: got %+v, want one hc 58", msgs)
	}
	checkButlerWire(t, "talk", msgs[0].body)
	popup, _, _ := pbBytesField(msgs[0].body, 2)
	if line, _, _ := pbBytesField(popup, 3); string(line) != economy.ActiveButlers().TalkLine {
		t.Fatalf("talk line %q, want the talk_line of butlers.json", line)
	}
	if _, ok, _ := pbBytesField(popup, 2); !ok {
		t.Fatal("talk: the popup carries no ButlerInfo (the device drew an empty figure)")
	}
	if voice, _, _ := pbBytesField(popup, 4); !strings.HasPrefix(string(voice), "butler_voice_master/") {
		t.Fatalf("talk voice %q, want one of Merry's lines", voice)
	}
	msgs = gardenButlerReply(49, 3604, pbVar(nil, 1, merry), now, logger)
	if len(msgs) != 1 || msgs[0].id != 58 || string(msgs[0].body) != string(pbVar(nil, 1, butlerRefused)) {
		t.Fatalf("talk without a contract: got %+v, want a refusal of 1", msgs)
	}
}

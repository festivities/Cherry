package economy

import (
	"regexp"
	"strings"
	"testing"

	"cherry/data"
	"cherry/internal/opdata"
)

// decodeWith decodes a data file with its own check, the way its loader does.
func decodeWith[T any](check func(*T) error) func([]byte) error {
	return func(b []byte) error {
		_, err := opdata.Decode(b, check)
		return err
	}
}

var (
	originKey = regexp.MustCompile(`"origin":\s*"[^"]*",?`)
	noteValue = regexp.MustCompile(`"note":\s*"(?:[^"\\]|\\.)*"`)
)

// TestOperatorFilesNeedOriginAndNote: every economy data file refuses a document with no origin and one with an
// empty note, so an unmarked file never starts (AGENTS "Cherry-made marking pass").
func TestOperatorFilesNeedOriginAndNote(t *testing.T) {
	for _, f := range []struct {
		name   string
		raw    []byte
		decode func([]byte) error
	}{
		{"atelier.json", data.AtelierJSON, decodeWith((*Atelier).check)},
		{"calendars.json", data.CalendarsJSON, decodeWith(checkCalendars)},
		{"exchange.json", data.ExchangeJSON, decodeWith(checkExchange)},
		{"fishboss.json", data.FishbossJSON, decodeWith(checkFishBoss)},
		{"fishing.json", data.FishingJSON, decodeWith(checkFishing)},
		{"gacha.json", data.GachaJSON, decodeWith(checkGacha)},
		{"gardens.json", data.GardensJSON, decodeWith(checkGardens)},
		{"gathering.json", data.GatheringJSON, decodeWith(checkGathering)},
		{"prices.json", data.PricesJSON, decodeWith(checkPrices)},
		{"racing.json", data.RacingJSON, decodeWith(checkRacing)},
		{"racing_exchange.json", data.RacingExchangeJSON, decodeWith(checkRideShop)},
		{"rewards.json", data.RewardsJSON, decodeWith(checkRewards)},
	} {
		if err := f.decode(f.raw); err != nil {
			t.Fatalf("%s: embedded default refused: %v", f.name, err)
		}
		if err := f.decode([]byte(originKey.ReplaceAllString(string(f.raw), ""))); err == nil || !strings.Contains(err.Error(), "origin") {
			t.Errorf("%s without origin: err = %v", f.name, err)
		}
		if err := f.decode([]byte(noteValue.ReplaceAllString(string(f.raw), `"note": " "`))); err == nil || !strings.Contains(err.Error(), "note") {
			t.Errorf("%s with a blank note: err = %v", f.name, err)
		}
	}
}

// TestFishIDsStayInCherryBlocks: a catch id, a fish shop item id and the boss id outside their Cherry blocks are
// refused at startup (CherryFishFirst/Last, CherryFishShopFirst/Last).
func TestFishIDsStayInCherryBlocks(t *testing.T) {
	fishing := string(data.FishingJSON)
	for _, c := range []struct{ text, want string }{
		{strings.Replace(fishing, `"id": 4101`, `"id": 4100`, 1), "fish 4100: id must be unique and inside the Cherry block 4101-4999"},
		{strings.Replace(fishing, `{"id": 9001, "name": "Square Rod"`, `{"id": 9200, "name": "Square Rod"`, 1), "shop item 9200: id and name must be set"},
		{strings.Replace(string(data.FishbossJSON), `"id": 4901`, `"id": 5000`, 1), "boss id must be inside the Cherry fish block 4101-4999"},
	} {
		var err error
		if strings.Contains(c.text, `"boss"`) {
			_, err = opdata.Decode([]byte(c.text), checkFishBoss)
		} else {
			_, err = opdata.Decode([]byte(c.text), checkFishing)
		}
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("err = %v, want %q", err, c.want)
		}
	}
}

// TestQuestAndDesignIDsStayInCherryBlocks: a daily quest id and an Atelier design id outside their Cherry blocks
// are refused at startup (CherryQuestFirst/Last, CherryDesignFirst/Last).
func TestQuestAndDesignIDsStayInCherryBlocks(t *testing.T) {
	quest := strings.Replace(string(data.RewardsJSON), `"id": 9001, "key": "garden-own"`, `"id": 9900, "key": "garden-own"`, 1)
	if _, err := opdata.Decode([]byte(quest), checkRewards); err == nil || !strings.Contains(err.Error(), "quest 9900: id must be unique and inside the Cherry block 9001-9399") {
		t.Errorf("quest: err = %v", err)
	}
	design := strings.Replace(string(data.AtelierJSON), `"id": 101201,`, `"id": 102001,`, 1)
	if _, err := opdata.Decode([]byte(design), (*Atelier).check); err == nil || !strings.Contains(err.Error(), "design 102001: id must be unique and inside the Cherry block 101201-101399") {
		t.Errorf("design: err = %v", err)
	}
}

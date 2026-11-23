package lpn

import (
	"testing"

	"cherry/internal/economy"
)

// decodeStrokeAnswer reads a petting answer back: the QuestActionCommand Id and its params by name (int values as
// Intvalue, string values as Strvalue; the field numbers are the client's ParamData).
func decodeStrokeAnswer(t *testing.T, body []byte) (uint64, map[string]any) {
	t.Helper()
	var id uint64
	params := map[string]any{}
	ok := pbScan(body, func(f, v uint64, d []byte) {
		if f != 1 || d == nil {
			return
		}
		pbScan(d, func(f, v uint64, d []byte) {
			if f != 1 || d == nil {
				return
			}
			pbScan(d, func(f, v uint64, d []byte) {
				switch {
				case f == 2 && d == nil:
					id = v
				case f == 3 && d != nil:
					var name string
					var val any
					pbScan(d, func(f, v uint64, d []byte) {
						switch {
						case f == 1 && d != nil:
							name = string(d)
						case f == 2 && d == nil:
							val = int64(v)
						case f == 4 && d != nil:
							val = string(d)
						}
					})
					params[name] = val
				}
			})
		})
	})
	if !ok {
		t.Fatalf("answer %x does not parse", body)
	}
	return id, params
}

// TestMiniPetStrokeAnswerParams: the petting answer is QuestActionCommand 112 with every required param; the gift
// params come only with an adoption.
func TestMiniPetStrokeAnswerParams(t *testing.T) {
	plain := decodeStrokeAnswerOf(t, economy.MiniPetStrokeReply{PetID: 1234, Exp: 20, MaxExp: 100, Gained: 10})
	if plain.id != 112 {
		t.Fatalf("command id %d, want 112", plain.id)
	}
	want := map[string]int64{"petId": 1234, "exp": 20, "maxExp": 100, "gainedExp": 10, "paidTouch": 0,
		"petTouchable": 0, "finalLevelYn": 0, "levelUp": 0}
	for name, v := range want {
		if plain.params[name] != v {
			t.Fatalf("param %s = %v, want %d (params %v)", name, plain.params[name], v, plain.params)
		}
	}
	if _, gift := plain.params["giftItemCode"]; gift {
		t.Fatalf("a plain petting carries gift params: %v", plain.params)
	}

	adopted := decodeStrokeAnswerOf(t, economy.MiniPetStrokeReply{PetID: 1234, Exp: 100, MaxExp: 100, Gained: 10, Final: true, LevelUp: true,
		Gift: &economy.MiniPetGift{Item: "CUON004A9", ItemName: "Dress", Grade: "NORMAL", Pet: "PUPE0000G", PetName: "Mixed Breed"}})
	if adopted.params["finalLevelYn"] != int64(1) || adopted.params["levelUp"] != int64(1) {
		t.Fatalf("adoption flags %v", adopted.params)
	}
	gift := map[string]any{"raceId": int64(1), "giftItemCode": "CUON004A9", "giftItemName": "Dress",
		"giftItemGrade": "NORMAL", "petSkinCode": "PUPE0000G", "petName": "Mixed Breed", "level": int64(1)}
	for name, v := range gift {
		if adopted.params[name] != v {
			t.Fatalf("gift param %s = %v, want %v", name, adopted.params[name], v)
		}
	}
}

type strokeAnswer struct {
	id     uint64
	params map[string]any
}

func decodeStrokeAnswerOf(t *testing.T, r economy.MiniPetStrokeReply) strokeAnswer {
	t.Helper()
	id, params := decodeStrokeAnswer(t, tlupPetStrokeBody(r))
	return strokeAnswer{id: id, params: params}
}

// TestMiniPetTargetPetParsed: a ServiceId 101 Eventid 4 event carries its target_pet Intvalue.
func TestMiniPetTargetPetParsed(t *testing.T) {
	ev, ok := tlupEventRequest(tlupEventData(101, 4, tlupParamData("target_pet", 1234)))
	if !ok || ev.serviceID != 101 || ev.eventID != 4 || ev.pet != 1234 {
		t.Fatalf("petting event = %+v ok=%v", ev, ok)
	}
}

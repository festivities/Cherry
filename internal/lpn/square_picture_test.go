package lpn

import (
	"reflect"
	"testing"

	"cherry/internal/store"
	"cherry/internal/testutil"
)

// squarePlayerEntry decodes one _SQPlayerProfileInfo: aid from field 1, and every length-delimited field as a string.
func squarePlayerEntry(t *testing.T, d []byte) (aid uint64, fields map[uint64]string) {
	t.Helper()
	fields = map[uint64]string{}
	ok := pbScan(d, func(f, v uint64, b []byte) {
		if b == nil {
			if f == 1 {
				aid = v
			}
			return
		}
		fields[f] = string(b)
	})
	if !ok {
		t.Fatalf("entry %x is malformed", d)
	}
	return aid, fields
}

// TestSquarePlayerListPictures: rc_current_player_list_res (msg 24) gives each player's picture as obs (6), with profile (4)
// the same path and profileBackground (5) the stored background or profilebg/12.jpg. A player without a picture gets none
// of fields 4 to 6. Fields 1 (aid) and 2 (name) are unchanged.
func TestSquarePlayerListPictures(t *testing.T) {
	testutil.InstallAccounts(t, withLoginAccounts(map[string]*store.Account{
		"sqpic-7101": {AccessToken: "sqpic-7101", SessionKey: testSessionKey, Aid: "7101", Name: "Pic",
			ProfileImage: store.ProfileImage{Image: "/lineplay/pr/7101_1700000001", Bg: "profilebg/3.jpg"}},
		"sqpic-7102": {AccessToken: "sqpic-7102", SessionKey: testSessionKey, Aid: "7102", Name: "Plain"},
		"sqpic-7103": {AccessToken: "sqpic-7103", SessionKey: testSessionKey, Aid: "7103", Name: "NoBg",
			ProfileImage: store.ProfileImage{Image: "/lineplay/pr/7103_1700000002"}},
	}))
	room := &squareRoom{}
	a, b, c := newSqClient(t, room, 1), newSqClient(t, room, 2), newSqClient(t, room, 3)
	a.join(7101)
	b.join(7102)
	a.read() // adduser for 7102
	c.join(7103)
	a.read() // adduser for 7103
	b.read() // adduser for 7103
	a.send(24, nil)
	id, body := a.read()
	if id != 24 {
		t.Fatalf("player list msgid = %d, want 24", id)
	}
	if n, _ := protobufUintField(body, 1); n != 3 {
		t.Fatalf("currentCnt = %d, want 3", n)
	}
	type expect struct{ name, picture, bg string }
	want := map[uint64]expect{
		7101: {"Pic", "/lineplay/pr/7101_1700000001", "profilebg/3.jpg"},
		7102: {"Plain", "", ""},
		7103: {"NoBg", "/lineplay/pr/7103_1700000002", "profilebg/12.jpg"}, // no stored background: the fallback
	}
	var order []uint64
	ok := pbScan(body, func(f, _ uint64, d []byte) {
		if f != 3 || d == nil {
			return
		}
		aid, fields := squarePlayerEntry(t, d)
		exp, known := want[aid]
		if !known {
			t.Fatalf("unexpected entry aid=%d", aid)
		}
		order = append(order, aid)
		wantFields := map[uint64]string{2: exp.name}
		if exp.picture != "" {
			wantFields[4], wantFields[5], wantFields[6] = exp.picture, exp.bg, exp.picture
		}
		if !reflect.DeepEqual(fields, wantFields) {
			t.Fatalf("aid %d fields = %v, want %v", aid, fields, wantFields)
		}
	})
	if !ok || !reflect.DeepEqual(order, []uint64{7101, 7102, 7103}) {
		t.Fatalf("entries ok=%v in order %v, want join order 7101 7102 7103", ok, order)
	}
}

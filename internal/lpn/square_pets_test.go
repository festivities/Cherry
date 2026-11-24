package lpn

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"cherry/internal/itemcode"
	"cherry/internal/store"
	"cherry/internal/testutil"
)

func squarePetFixture(t *testing.T, pets map[string][]string) {
	t.Helper()
	root := t.TempDir()
	for code, names := range pets {
		id, ok := itemcode.ID(code)
		if !ok {
			t.Fatalf("bad pet code %s", code)
		}
		d := filepath.Join(root, "pet", fmt.Sprint(id))
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
		for _, n := range names {
			if err := os.WriteFile(filepath.Join(d, n), []byte("x"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	testutil.SetArchive(t, testutil.ArchiveMounts(root))
}

// TestSquareFollowPetInAvatarInfo verifies that an account's representative pet (Rep == true) is encoded inside
// SQUARE::_AvatarInfo field 23 (PetBaseInfo -> PetFeatureInfo) on Square entry, with catg_cd matching the pet catalog.
func TestSquareFollowPetInAvatarInfo(t *testing.T) {
	squarePetFixture(t, map[string][]string{
		"PUPE0007V": {"skeleton.xml", "dp.png", "ride.aniproj"}, // URIDE
	})
	testutil.InstallAccounts(t, map[string]*store.Account{
		"t1": {
			Aid: "337", Name: "Player", Gender: "FEMALE",
			Pets: []store.PetItem{
				{ID: 33701, Cd: "PUPE00072", Name: "Maru", Rep: false},
				{ID: 33702, Cd: "PUPE0007V", Name: "Odile", Rep: true},
			},
		},
		"t2": {Aid: "338", Name: "NoPet", Gender: "FEMALE"},
	})

	// Account with Rep pet gets _AvatarInfo field 23 with PetFeatureInfo {1:33702, 2:337, 3:"Odile", 4:"PUPE0007V", 5:"URIDE"}.
	av := squareAvatarInfo(337)
	petBase, found, ok := pbBytesField(av, 23)
	if !ok || !found {
		t.Fatalf("_AvatarInfo missing field 23 (pet): %x", av)
	}
	var feat []byte
	var rideState uint64
	if !pbScan(petBase, func(f, v uint64, d []byte) {
		switch f {
		case 1:
			feat = d
		case 2:
			rideState = v
		}
	}) || len(feat) == 0 || rideState != 0 {
		t.Fatalf("PetBaseInfo = feat:%x ride:%d", feat, rideState)
	}
	var gotID, gotOwner uint64
	var gotName, gotSkin, gotCatg string
	pbScan(feat, func(f, v uint64, d []byte) {
		switch f {
		case 1:
			gotID = v
		case 2:
			gotOwner = v
		case 3:
			gotName = string(d)
		case 4:
			gotSkin = string(d)
		case 5:
			gotCatg = string(d)
		}
	})
	if gotID != 33702 || gotOwner != 337 || gotName != "Odile" || gotSkin != "PUPE0007V" || gotCatg != "URIDE" {
		t.Fatalf("PetFeatureInfo = id:%d owner:%d name:%q skin:%q catg:%q", gotID, gotOwner, gotName, gotSkin, gotCatg)
	}

	// Account with no Rep pet omits field 23.
	if _, found, _ := pbBytesField(squareAvatarInfo(338), 23); found {
		t.Fatalf("account without follow pet should not emit field 23")
	}
}

// TestSquarePetRideAndRelays verifies cr_pet_ride_req (92) -> rc_pet_ride_res (120, result 0xF0001) broadcast +
// late-joiner spawn ride state + non-rideable refusal, and pet move/action/interaction relays (40->49, 38->47, 39->48).
func TestSquarePetRideAndRelays(t *testing.T) {
	squarePetFixture(t, map[string][]string{
		"PUPE0007V": {"skeleton.xml", "dp.png", "ride.aniproj"}, // URIDE
		"PUPE0000G": {"skeleton.xml", "dp.png"},                 // UTOY (non-ride)
	})
	testutil.InstallAccounts(t, map[string]*store.Account{
		"t711": {AccessToken: "t711", SessionKey: testSessionKey, Aid: "711", Name: "Rider", Gender: "FEMALE", Pets: []store.PetItem{{ID: 71101, Cd: "PUPE0007V", Name: "Odile", Rep: true}}},
		"t712": {AccessToken: "t712", SessionKey: testSessionKey, Aid: "712", Name: "Watcher", Gender: "FEMALE", Pets: []store.PetItem{{ID: 71201, Cd: "PUPE0000G", Name: "Pup", Rep: true}}},
		"t713": {AccessToken: "t713", SessionKey: testSessionKey, Aid: "713", Name: "Late", Gender: "FEMALE"},
	})

	joinSq := func(c *sqClient, aid uint64) []byte {
		c.send(0, loginBody(aid))
		c.read()
		c.send(2, pbVar(nil, 1, squareThemeID))
		c.read()
		c.send(5, nil)
		id, body := c.read()
		if id != 8 {
			t.Fatalf("aid %d relaystart got msgid %d, want 8", aid, id)
		}
		var rosterCount int
		pbScan(body, func(f, _ uint64, _ []byte) {
			if f == 2 {
				rosterCount++
			}
		})
		if rosterCount > 1 {
			if pid, _ := c.read(); pid != squareRcHighFiveTargetList {
				t.Fatalf("aid %d highfive push got msgid %d, want %d", aid, pid, squareRcHighFiveTargetList)
			}
		}
		return body
	}

	room := &squareRoom{}
	a := newSqClient(t, room, 1)
	b := newSqClient(t, room, 2)
	joinSq(a, 711)
	joinSq(b, 712)
	a.read() // adduser(712)
	a.expect(squareRcHighFiveTargetList, squareHighFiveTargetList([]uint64{712}))

	// Rider mounts URIDE pet 71101 -> both a and b receive rc_pet_ride_res (120) with result=983041, aid=711, pet=71101, state=1.
	a.send(squareCrPetRide, pbVar(pbVar(nil, 1, 71101), 2, 1))
	wantMount := pbVar(pbVar(pbVar(pbVar(nil, 1, squarePetRideOK), 2, 711), 3, 71101), 4, 1)
	a.expect(squareRcPetRide, wantMount)
	b.expect(squareRcPetRide, wantMount)

	// Late joiner c sees Rider's avatar with pet_ride_state=1 in rc_room_relaystart_res.
	c := newSqClient(t, room, 3)
	relayBody := joinSq(c, 713)
	a.read() // adduser(713)
	a.expect(squareRcHighFiveTargetList, squareHighFiveTargetList([]uint64{713}))
	b.read() // adduser(713)
	b.expect(squareRcHighFiveTargetList, squareHighFiveTargetList([]uint64{713}))
	foundMounted := false
	pbScan(relayBody, func(f, _ uint64, d []byte) {
		if f != 2 {
			return
		}
		if aid, _ := protobufUintField(d, 1); aid == 711 {
			av, _, _ := pbBytesField(d, 2)
			pb, _, _ := pbBytesField(av, 23)
			if rs, ok := protobufUintField(pb, 2); ok && rs == 1 {
				foundMounted = true
			}
		}
	})
	if !foundMounted {
		t.Fatalf("late joiner did not see Rider mounted (pet_ride_state=1)")
	}

	// Non-rideable pet mount (Watcher's UTOY pet 71201) -> refused to requester only (result=0).
	b.send(squareCrPetRide, pbVar(pbVar(nil, 1, 71201), 2, 1))
	b.expect(squareRcPetRide, pbVar(pbVar(pbVar(pbVar(nil, 1, 0), 2, 712), 3, 71201), 4, 0))
	a.none()

	// Pet move (40 -> 49 to others only), action (38 -> 47 to others only), interaction (39 -> 48 to all).
	pm := pbVar(nil, 1, 71101)
	a.send(squareCrPetMove, pbLen(nil, 1, pm))
	wantMove := pbLen(pbVar(nil, 1, 711), 2, pm)
	b.expect(squareRcPetMove, wantMove)
	c.expect(squareRcPetMove, wantMove)
	a.none()

	act := pbVar(pbVar(nil, 1, 2), 2, 1)
	a.send(squareCrPetAction, pbLen(pbVar(nil, 1, 71101), 2, act))
	wantAct := pbLen(pbVar(pbVar(nil, 1, 711), 2, 71101), 3, act)
	b.expect(squareRcPetAction, wantAct)
	c.expect(squareRcPetAction, wantAct)
	a.none()

	inter := pbVar(nil, 7, 28)
	b.send(squareCrPetInteraction, pbVar(pbLen(pbVar(nil, 1, 71101), 2, inter), 3, 711))
	wantInter := pbLen(pbVar(pbVar(nil, 1, 711), 2, 71101), 3, inter)
	for _, cl := range []*sqClient{a, b, c} {
		gotID, gotBody := cl.read()
		if gotID != squareRcPetInteraction || !bytes.Equal(gotBody, wantInter) {
			t.Fatalf("interaction = %d/%x, want %d/%x", gotID, gotBody, squareRcPetInteraction, wantInter)
		}
	}
}

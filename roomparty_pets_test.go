package main

import (
	"fmt"
	"strings"
	"testing"
)

// Strict mirror of the native required-field tables (libgame.so 10.1.0.0): a frame that
// fails LPNP::*::IsInitialized is dropped whole by LPNP::roomclient_dispatch @0x252c2cc.
type lpField struct {
	wire     byte // 0 varint, 2 length-delimited
	req, rep bool
	msg      lpMsg // nil: opaque bytes/string
}

type lpMsg map[uint64]lpField

var (
	lpMove   = lpMsg{1: {wire: 0, req: true}, 2: {wire: 0, req: true}, 3: {wire: 0, req: true}, 4: {wire: 0, req: true}, 5: {wire: 0, req: true}, 6: {wire: 0, req: true}} // mask 0x3F @0x262938c
	lpPetFi  = lpMsg{1: {wire: 0, req: true}, 2: {wire: 0, req: true}, 3: {wire: 2, req: true}, 4: {wire: 2, req: true}, 5: {wire: 2, req: true}, 6: {wire: 2}}            // mask 0x37 @0x277e0c4
	lpPetBas = lpMsg{1: {wire: 0, req: true}, 2: {wire: 0, req: true}, 3: {wire: 2, req: true, msg: lpPetFi}, 4: {wire: 2, msg: lpMove}, 7: {wire: 0}, 8: {wire: 0}, 9: {wire: 0}, 10: {wire: 0}, 11: {wire: 0}, 12: {wire: 0}, 13: {wire: 0}}
	lpAvatar = lpMsg{} // opaque: only length-delimited/varint well-formedness is checked
	lpPlayer = lpMsg{1: {wire: 0, req: true}, 2: {wire: 2, req: true, msg: lpAvatar}, 3: {wire: 2, msg: lpMove}}
	lpRelay  = lpMsg{1: {wire: 0, req: true}, 2: {wire: 2, rep: true, msg: lpPlayer}, 3: {wire: 2, rep: true, msg: lpPetBas}, 4: {wire: 0}} // rc_floor_relaystart_res @0x25430b4
	lpAdd    = lpMsg{1: {wire: 2, req: true, msg: lpPlayer}, 2: {wire: 2, rep: true, msg: lpPetBas}}                                        // rc_floor_adduser_all @0x25430fc
)

// lpCheck fails on malformed input, unknown fields, wrong wire types or a missing required field.
func lpCheck(data []byte, m lpMsg, path string) error {
	seen := map[uint64]int{}
	var err error
	ok := pbScan(data, func(f, _ uint64, d []byte) {
		if err != nil {
			return
		}
		spec, known := m[f]
		if !known && len(m) == 0 { // opaque message: accept anything well-formed
			return
		}
		wire := byte(0)
		if d != nil {
			wire = 2
		}
		switch {
		case !known:
			err = fmt.Errorf("%s: unknown field %d", path, f)
		case spec.wire != wire:
			err = fmt.Errorf("%s.%d: wire %d, want %d", path, f, wire, spec.wire)
		default:
			seen[f]++
			if spec.msg != nil {
				err = lpCheck(d, spec.msg, fmt.Sprintf("%s.%d", path, f))
			}
		}
	})
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("%s: malformed", path)
	}
	for f, spec := range m {
		if spec.req && seen[f] == 0 {
			return fmt.Errorf("%s: missing required field %d", path, f)
		}
		if !spec.rep && seen[f] > 1 {
			return fmt.Errorf("%s.%d: repeated", path, f)
		}
	}
	return nil
}

type petView struct {
	owner, id, fOwner, fID uint64
	name, skin, cat, code  string
	level, spawn           uint64
	move                   []byte
}

// lpPets decodes every PetBasicInfo found in list field `field` of body.
func lpPets(t *testing.T, body []byte, field uint64) []petView {
	t.Helper()
	var out []petView
	pbScan(body, func(f, _ uint64, d []byte) {
		if f != field || d == nil {
			return
		}
		var v petView
		pbScan(d, func(f uint64, n uint64, d []byte) {
			switch f {
			case 1:
				v.owner = n
			case 2:
				v.id = n
			case 4:
				v.move = d
			case 11:
				v.level = n
			case 12:
				v.spawn = n
			case 3:
				pbScan(d, func(f uint64, n uint64, d []byte) {
					switch f {
					case 1:
						v.fID = n
					case 2:
						v.fOwner = n
					case 3:
						v.name = string(d)
					case 4:
						v.skin = string(d)
					case 5:
						v.cat = string(d)
					case 6:
						v.code = string(d)
					}
				})
			}
		})
		out = append(out, v)
	})
	return out
}

func TestLPNPStrictCheckerRejectsOldPetShape(t *testing.T) {
	// The reverted attempt: PetFeatureInfo {1,2,3,4,6} without required field 5.
	f := pbLen(pbLen(pbLen(pbVar(pbVar(nil, 1, 1201), 2, 12), 3, []byte("PUPE0000G")), 4, []byte("PUPE0000G")), 6, []byte("Minipet"))
	pet := pbVar(pbLen(pbVar(pbVar(nil, 1, 12), 2, 1201), 3, f), 12, 1)
	body := pbLen(pbVar(nil, 1, 0), 3, pet)
	if err := lpCheck(body, lpRelay, "rc_floor_relaystart_res"); err == nil || !strings.Contains(err.Error(), "required field 5") {
		t.Fatalf("old shape accepted: %v", err)
	}
	// mover without all six fields is rejected too
	if err := lpCheck(pbVar(nil, 1, 1), lpMove, "move"); err == nil {
		t.Fatal("partial _player_move accepted")
	}
	if err := lpCheck(partyPetMove(0), lpMove, "move"); err != nil {
		t.Fatal(err)
	}
}

func TestPartyPetsFrames(t *testing.T) {
	h := partySetup(t)
	a := partyLogin(t, h, 11, 1)
	b := partyLogin(t, h, 12, 2)
	a.enterHost(11)
	a.start(nil)
	id, body := a.read()
	if id != 29 {
		t.Fatalf("host idx %d", id)
	}
	check := func(who string, id int, body []byte, m lpMsg, field uint64, wantPets int) []petView {
		t.Helper()
		if err := lpCheck(body, m, who); err != nil {
			t.Fatalf("%s: %v", who, err)
		}
		pets := lpPets(t, body, field)
		if len(pets) != wantPets {
			t.Fatalf("%s: %d pets, want %d", who, len(pets), wantPets)
		}
		for _, p := range pets {
			if p.id != p.fID || p.owner != p.fOwner || p.spawn != 1 || p.level != 1 || p.cat != "PET" || p.skin == "" || p.code != p.skin || p.name == "" {
				t.Fatalf("%s: pet %+v", who, p)
			}
			if p.owner != 11 && p.owner != 12 {
				t.Fatalf("%s: owner %d", who, p.owner)
			}
		}
		return pets
	}
	// host alone: rep pet 1101 (no position) + arranged 1102/1103 with move info
	pets := check("host idx29", 29, body, lpRelay, 3, 3)
	var arranged int
	for _, p := range pets {
		if p.owner != 11 {
			t.Fatalf("owner %d", p.owner)
		}
		if p.move != nil {
			arranged++
		}
	}
	if pets[0].id != 1101 || pets[0].move != nil || arranged != 2 || pets[1].id != 1102 || pets[2].id != 1103 {
		t.Fatalf("host pets %+v", pets)
	}
	for _, p := range pets[1:] { // arranged pets avoid the hero spawn tiles
		x, y, _, _, _ := partyMoveFields(p.move)
		tx, ty := partyPxTile(x, y)
		for _, s := range partySpawnTiles {
			if s == [2]int{tx, ty} {
				t.Fatalf("pet on spawn tile %v", s)
			}
		}
		if tx < 0 || tx >= 12 || ty < 0 || ty >= 12 {
			t.Fatalf("pet tile (%d,%d)", tx, ty)
		}
	}
	b.enter(11)
	b.read()
	b.start(nil)
	b.read() // room adduser of the host
	id, body = b.read()
	if id != 29 {
		t.Fatalf("guest idx %d", id)
	}
	pets = check("guest idx29", 29, body, lpRelay, 3, 4) // host rep + 2 arranged + guest rep
	if pets[3].owner != 12 || pets[3].id != 1201 || pets[3].move != nil {
		t.Fatalf("guest pet %+v", pets[3])
	}
	// the host receives the joiner's pet through idx30 (room adduser first)
	a.read()
	id, body = a.read()
	if id != 30 {
		t.Fatalf("host got %d", id)
	}
	pets = check("host idx30", 30, body, lpAdd, 2, 1)
	if pets[0].owner != 12 || pets[0].id != 1201 {
		t.Fatalf("idx30 pet %+v", pets[0])
	}
	// floor 2: nobody's arranged pets (only LEVEL_1 has them), rep pet only
	b.send(24, pbVar(nil, 1, 2))
	b.read()
	b.send(25, pbLen(pbVar(nil, 1, 2), 2, nil))
	id, body = b.read()
	if id != 29 {
		t.Fatalf("floor idx %d", id)
	}
	check("floor idx29", 29, body, lpRelay, 3, 1)
	// host moves to floor 2 too: host gets host rep pet only, guest gets idx30 with it
	a.send(24, pbVar(nil, 1, 2))
	a.send(25, pbLen(pbVar(nil, 1, 2), 2, nil))
	var got bool
	for i := 0; i < 8; i++ {
		id, body = b.read()
		if id == 30 {
			p := check("floor idx30", 30, body, lpAdd, 2, 1)
			got = p[0].owner == 11 && p[0].move == nil
			break
		}
	}
	if !got {
		t.Fatal("no idx30 with the host's rep pet on floor 2")
	}
}

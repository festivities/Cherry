// Package itemcode converts between an item code (nine characters, e.g. RUGO007DR) and the numeric item id
// the client derives from it (SbItemTable::GetIntIdx): id = attr*1e8 + type*1e7 + catIdx*1e5 + base36(code[4:9]),
// attr in "?RCPG", type in "MFUA". The category table is the client's room, cloth, pet and landscape letters.
package itemcode

import (
	"strconv"
	"strings"
)

// roomLetters are the room categories, catIdx 3..35 in order.
const roomLetters = "TA CH CL DR BE KI TB BA EL PR BG WA TI WD TD DO WI GO PL FD LA AB GC FU SL PT PI ZA BW BT ZB MP VO"

// clothLetters are the clothing categories (attribute C).
var clothLetters = map[int64]string{38: "HE", 40: "EB", 41: "EY", 42: "NO", 43: "MO", 44: "HA", 50: "TO", 51: "ON", 52: "PA", 53: "SH", 54: "AH", 55: "AE", 56: "FE"}

// Code is the item code of numeric id n; ok is false for an id no category explains.
func Code(n int64) (string, bool) {
	attr, typ, cat, base := n/1e8, n/1e7%10, n/1e5%100, n%1e5
	if attr < 1 || attr > 4 || typ > 3 {
		return "", false
	}
	letters := ""
	switch attr {
	case 1:
		if f := strings.Fields(roomLetters); cat >= 3 && int(cat-3) < len(f) {
			letters = f[cat-3]
		}
	case 2:
		letters = clothLetters[cat]
	case 3:
		if cat == 64 {
			letters = "PE"
		}
	case 4:
		if cat == 84 {
			letters = "LS" // Garden landscapes (economy/landscape.go)
		}
	}
	if letters == "" {
		return "", false
	}
	b := strings.ToUpper(strconv.FormatInt(base, 36))
	return string("?RCPG"[attr]) + string("MFUA"[typ]) + letters + strings.Repeat("0", 5-len(b)) + b, true
}

// ID is the numeric id of item code, the inverse of Code. Only canonical codes parse: upper case, a known
// category for the attribute, and a tail below 1e5 that round-trips.
func ID(code string) (int64, bool) {
	if len(code) != 9 {
		return 0, false
	}
	attr := strings.IndexByte("RCPG", code[0]) + 1
	typ := strings.IndexByte("MFUA", code[1])
	if attr < 1 || typ < 0 {
		return 0, false
	}
	cat, ok := category(attr, code[2:4])
	if !ok {
		return 0, false
	}
	base, err := strconv.ParseInt(code[4:], 36, 64)
	if err != nil {
		return 0, false
	}
	id := int64(attr)*1e8 + int64(typ)*1e7 + cat*1e5 + base
	if back, ok := Code(id); !ok || back != code {
		return 0, false
	}
	return id, true
}

// category is the catIdx of two category letters under attribute attr.
func category(attr int, letters string) (int64, bool) {
	switch attr {
	case 1:
		for i, f := range strings.Fields(roomLetters) {
			if f == letters {
				return int64(i + 3), true
			}
		}
	case 2:
		for cat, f := range clothLetters {
			if f == letters {
				return cat, true
			}
		}
	case 3:
		return 64, letters == "PE"
	case 4:
		return 84, letters == "LS"
	}
	return 0, false
}

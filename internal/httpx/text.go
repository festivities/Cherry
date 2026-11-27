package httpx

import (
	"bytes"
	"encoding/json"
	"errors"
	"unicode"
	"unicode/utf8"
)

// The text shape rule: what every player-typed string must keep before Cherry stores it or shows it to anyone. Chat goes
// through lpn (chattext.go), JSON request bodies through UnmarshalNativeJSON, nicknames through store.ValidNickname.

// MaxMarks is the longest run of combining marks (Unicode Mn and Me) a player-typed text may hold. A long run stacks that
// many marks on one character and crashes the stock client of everyone who draws it: on 2026-11-27 a Thai letter carrying
// 49 stacked marks, sent to a Square every 25 to 45 seconds, was followed within 3 s by 6.2 gateway connection resets on
// average (1.1 in the 9 s before) and runs of 47 by 8 to 10. Ordinary text stays far below the cap: Thai stacks up to 3
// (a vowel and a tone mark), emphasis (one Thai vowel typed five times) and kaomoji reach 6, and of 1.81 million recorded
// chat lines about 20 went past 8, nearly all of them copies of the attack strings (REFERENCE "Text moderation").
const MaxMarks = 8

// ErrUnsafeText is UnmarshalNativeJSON's error for a body holding a string that breaks the text shape rule.
var ErrUnsafeText = errors.New("a string stacks more combining marks than the text shape rule allows")

// SafeText reports whether s keeps the text shape rule: valid UTF-8 with no run of more than MaxMarks combining marks.
// A format character (Unicode Cf: zero-width joiner, word joiner and the like) neither counts nor ends a run, because a
// renderer skips it when it stacks marks on the letter before; any other character ends the run.
func SafeText(s string) bool {
	if !utf8.ValidString(s) {
		return false
	}
	run := 0
	for _, r := range s {
		switch {
		case r < utf8.RuneSelf:
			run = 0
		case unicode.In(r, unicode.Mn, unicode.Me):
			if run++; run > MaxMarks {
				return false
			}
		case unicode.Is(unicode.Cf, r):
		default:
			run = 0
		}
	}
	return true
}

// SafeJSON reports whether every string of the JSON text raw, keys and values with their escapes decoded, keeps the text
// shape rule. ASCII without a \u escape is safe at a glance. Text that is not JSON passes: its decoder refuses it.
func SafeJSON(raw []byte) bool {
	if !bytes.Contains(raw, []byte(`\u`)) && isASCII(raw) {
		return true
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	for {
		tok, err := dec.Token()
		if err != nil {
			return true
		}
		if s, ok := tok.(string); ok && !SafeText(s) {
			return false
		}
	}
}

func isASCII(b []byte) bool {
	for _, c := range b {
		if c >= utf8.RuneSelf {
			return false
		}
	}
	return true
}

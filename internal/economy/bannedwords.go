package economy

import (
	"fmt"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"cherry/data"
	"cherry/internal/opdata"
)

// The banned word list: words the operator keeps out of what players say to each other. LINE PLAY's own list came from
// LINE's server (GET /v4/banned/keyword) and is in neither the client nor the archives, so the list is Cherry-made and
// empty until an operator fills it (owner, 2026-11-27). It works in two places: web serves it to the client for its own
// check (NaDataManager::SetInhibitionWordList, libgame 0x1c0a170, lowercases each word), and the server masks the words
// itself (MaskBannedWords) in chat and in the text it stores, because a modified client skips its own check. The chat log
// keeps what was typed, for staff. data/bannedwords.json is the operator document; it is hot (opdata.NewHot): a publish
// or rollback is live at once, since every reader calls Get and the folded words are part of the value. Strict JSON:
//
//	origin, note   Cherry-made (opdata.Provenance)
//	words          0 to maxBannedWords words, each 1 to maxBannedWordRunes characters, not blank, no control characters,
//	               unique ignoring case. A blank word is refused: it would match every text and loop the client's check.
type BannedWordList struct {
	opdata.Provenance
	Words []string `json:"words"`

	folded [][]rune // Words lowercased rune by rune, filled by checkBannedWords
}

const (
	maxBannedWords     = 1000
	maxBannedWordRunes = 64
)

var bannedWordsFile = opdata.NewHot("bannedwords.json", data.BannedWordsJSON, checkBannedWords, nil)

// BannedWords is the list in use, as GET /v4/banned/keyword serves it (never nil).
func BannedWords() []string { return bannedWordsFile.Get().Words }

func checkBannedWords(l *BannedWordList) error {
	if err := l.CheckOrigin("cherry"); err != nil {
		return err
	}
	if len(l.Words) > maxBannedWords {
		return fmt.Errorf("words must hold at most %d words", maxBannedWords)
	}
	if l.Words == nil {
		l.Words = []string{}
	}
	l.folded = make([][]rune, 0, len(l.Words))
	seen := make(map[string]bool, len(l.Words))
	for i, w := range l.Words {
		f := foldRunes(w)
		switch {
		case strings.TrimSpace(w) == "":
			return fmt.Errorf("words[%d] is blank: a blank word matches every text", i+1)
		case !utf8.ValidString(w) || len(f) > maxBannedWordRunes:
			return fmt.Errorf("words[%d] must be 1 to %d characters", i+1, maxBannedWordRunes)
		case strings.ContainsFunc(w, unicode.IsControl):
			return fmt.Errorf("words[%d] holds a control character", i+1)
		case seen[string(f)]:
			return fmt.Errorf("words[%d] %q is listed twice (case does not matter)", i+1, w)
		}
		seen[string(f)] = true
		l.folded = append(l.folded, f)
	}
	return nil
}

// MaskBannedWords returns s with every occurrence of a banned word, matched rune by rune ignoring case, replaced by one
// '*' per character; overlapping matches are all masked. Without banned words it returns s as it is.
func MaskBannedWords(s string) string {
	words := bannedWordsFile.Get().folded
	if len(words) == 0 || s == "" {
		return s
	}
	text, low := []rune(s), foldRunes(s)
	var hit []bool
	for _, w := range words {
		for i := 0; i+len(w) <= len(low); i++ {
			if slices.Equal(low[i:i+len(w)], w) {
				if hit == nil {
					hit = make([]bool, len(text))
				}
				for j := i; j < i+len(w); j++ {
					hit[j] = true
				}
			}
		}
	}
	if hit == nil {
		return s
	}
	for i := range text {
		if hit[i] {
			text[i] = '*'
		}
	}
	return string(text)
}

// foldRunes is s lowercased one rune at a time, so it keeps one rune per rune of s.
func foldRunes(s string) []rune {
	r := []rune(s)
	for i, c := range r {
		r[i] = unicode.ToLower(c)
	}
	return r
}

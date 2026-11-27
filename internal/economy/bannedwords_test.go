package economy

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"cherry/internal/opdata"
	"cherry/internal/store"
)

// useBannedWords makes words the list in use for the test.
func useBannedWords(t *testing.T, words string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "bannedwords.json")
	doc := `{"origin":"cherry","note":"test list","words":` + words + `}`
	if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(bannedWordsFile.Reset)
	if err := bannedWordsFile.LoadFrom(path); err != nil {
		t.Fatal(err)
	}
}

// TestBannedWordsEmbeddedListIsEmpty: the shipped list is empty (owner, 2026-11-27), served as an empty array, and masks
// nothing.
func TestBannedWordsEmbeddedListIsEmpty(t *testing.T) {
	bannedWordsFile.Reset()
	if w := BannedWords(); w == nil || len(w) != 0 {
		t.Fatalf("BannedWords = %#v, want an empty, non-nil list", w)
	}
	if got := MaskBannedWords("botak tolol"); got != "botak tolol" {
		t.Fatalf("MaskBannedWords = %q, want the text unchanged", got)
	}
}

// TestMaskBannedWords: every occurrence is masked one '*' per character, ignoring case, inside words too, overlapping
// matches together, multi-byte words rune by rune; text without a listed word comes back as it is.
func TestMaskBannedWords(t *testing.T) {
	useBannedWords(t, `["bad", "\u00c4RGER", "ab", "bc", "\u0e42\u0e07\u0e48"]`)
	for _, c := range []struct{ in, want string }{
		{"Bad bAD xbadx", "*** *** x***x"},
		{"so much \u00e4rger", "so much *****"},
		{"abc", "***"},
		{"\u0e42\u0e07\u0e48\u0e21\u0e32\u0e01", "***\u0e21\u0e32\u0e01"},
		{"all fine here", "all fine here"},
		{"", ""},
	} {
		if got := MaskBannedWords(c.in); got != c.want {
			t.Errorf("MaskBannedWords(%q) = %q, want %q", c.in, got, c.want)
		}
	}
	if got := BannedWords(); len(got) != 5 || got[1] != "\u00c4RGER" {
		t.Errorf("BannedWords = %q, want the list as written", got)
	}
}

// TestBannedWordsRefusals: a blank word (it would match every text and loop the client's check), a duplicate ignoring
// case, an over-long word and a control character are each refused with the entry named.
func TestBannedWordsRefusals(t *testing.T) {
	for _, c := range []struct{ words, want string }{
		{`["ok", "  "]`, "words[2] is blank"},
		{`[""]`, "words[1] is blank"},
		{`["Bad", "bAD"]`, "listed twice"},
		{`["` + strings.Repeat("x", maxBannedWordRunes+1) + `"]`, "1 to 64 characters"},
		{`["tab\there"]`, "control character"},
	} {
		doc := `{"origin":"cherry","note":"test list","words":` + c.words + `}`
		err := opdata.Validate("bannedwords.json", []byte(doc))
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want one naming %q", c.words, err, c.want)
		}
	}
}

// TestBannedWordsPublishIsLive: bannedwords.json is hot. A publish masks at once and the result says so; a rollback to
// nothing published masks nothing again, without a restart.
func TestBannedWordsPublishIsLive(t *testing.T) {
	docStore(t)
	t.Cleanup(bannedWordsFile.Reset)
	restart(t)
	v, err := store.SaveDraft("bannedwords.json", []byte(`{"origin":"cherry","note":"test list","words":["tolol"]}`), "tester", "")
	if err != nil {
		t.Fatal(err)
	}
	res, err := store.Publish("bannedwords.json", v.Version, "loopback")
	if err != nil || !res.Live || res.Message != "is live now" {
		t.Fatalf("publish = %+v, %v; want live", res, err)
	}
	if got := MaskBannedWords("botak tolol"); got != "botak *****" {
		t.Fatalf("after publish MaskBannedWords = %q, want the word masked at once", got)
	}
	if d := liveDoc(t, "bannedwords.json"); !d.Hot || d.Pending || d.Source != "database v1" {
		t.Fatalf("live after publish = %+v, want hot database v1, nothing pending", d)
	}
	if back, err := store.Rollback("bannedwords.json", "loopback"); err != nil || !back.Live {
		t.Fatalf("rollback = %+v, %v", back, err)
	}
	if got := MaskBannedWords("botak tolol"); got != "botak tolol" || len(BannedWords()) != 0 {
		t.Fatalf("after rollback MaskBannedWords = %q, words %q; want the empty default", got, BannedWords())
	}
	if d := liveDoc(t, "bannedwords.json"); d.Pending || d.Source != "embedded default" {
		t.Fatalf("live after rollback = %+v", d)
	}
}

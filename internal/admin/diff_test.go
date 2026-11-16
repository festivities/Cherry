package admin

import (
	"fmt"
	"math/rand"
	"strings"
	"testing"
)

// countOps returns the unchanged, removed and added lines of an edit script.
func countOps(ops []diffOp) (same, removed, added int) {
	for _, op := range ops {
		switch op.kind {
		case ' ':
			same++
		case '-':
			removed++
		case '+':
			added++
		}
	}
	return same, removed, added
}

// splitLines is the line list of s ("" is no lines).
func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

// TestLineDiffCases: identical, added, removed and changed lines are counted as the edit script says.
func TestLineDiffCases(t *testing.T) {
	cases := []struct {
		name, a, b    string
		added, remove int
	}{
		{"identical", "x\ny", "x\ny", 0, 0},
		{"added", "x\ny", "x\nz\ny", 1, 0},
		{"removed", "x\nz\ny", "x\ny", 0, 1},
		{"changed", "x\nold\ny", "x\nnew\ny", 1, 1},
		{"all new", "a", "b\nc", 2, 1},
		{"empty live", "", "a\nb", 2, 0},
		{"empty version", "a\nb", "", 0, 2},
	}
	for _, tc := range cases {
		ops, ok := lineDiff(splitLines(tc.a), splitLines(tc.b))
		if !ok {
			t.Fatalf("%s: refused", tc.name)
		}
		_, removed, added := countOps(ops)
		if removed != tc.remove || added != tc.added {
			t.Errorf("%s: -%d +%d, want -%d +%d", tc.name, removed, added, tc.remove, tc.added)
		}
	}
}

// TestLineDiffRebuildsBothSides: on random inputs the script gives back both sides exactly, and its unchanged lines are a
// longest common subsequence (the length a plain dynamic programme finds).
func TestLineDiffRebuildsBothSides(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	alphabet := []string{"a", "b", "c", "d"}
	for round := 0; round < 300; round++ {
		a := randomLines(rng, alphabet, rng.Intn(12))
		b := randomLines(rng, alphabet, rng.Intn(12))
		ops, ok := lineDiff(a, b)
		if !ok {
			t.Fatalf("round %d: refused", round)
		}
		var gotA, gotB []string
		for _, op := range ops {
			if op.kind != '+' {
				gotA = append(gotA, op.text)
			}
			if op.kind != '-' {
				gotB = append(gotB, op.text)
			}
		}
		if strings.Join(gotA, "\n") != strings.Join(a, "\n") || strings.Join(gotB, "\n") != strings.Join(b, "\n") {
			t.Fatalf("round %d: the script does not rebuild its inputs", round)
		}
		same, _, _ := countOps(ops)
		if want := lcsLength(a, b); same != want {
			t.Fatalf("round %d: %d unchanged lines, the longest common subsequence is %d", round, same, want)
		}
	}
}

func randomLines(rng *rand.Rand, alphabet []string, n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = alphabet[rng.Intn(len(alphabet))]
	}
	return out
}

// lcsLength is the plain dynamic programme the diff must match.
func lcsLength(a, b []string) int {
	prev := make([]int, len(b)+1)
	for i := 1; i <= len(a); i++ {
		cur := make([]int, len(b)+1)
		for j := 1; j <= len(b); j++ {
			if a[i-1] == b[j-1] {
				cur[j] = prev[j-1] + 1
			} else {
				cur[j] = max(prev[j], cur[j-1])
			}
		}
		prev = cur
	}
	return prev[len(b)]
}

// TestLineDiffCap: a middle with more cells than diffMaxCells is refused (the page says too large), and so is a document
// past diffMaxLines lines; a small change to a long document is still shown.
func TestLineDiffCap(t *testing.T) {
	var a, b []string
	for i := 0; i < 2100; i++ {
		a = append(a, fmt.Sprintf("old %d", i))
		b = append(b, fmt.Sprintf("new %d", i))
	}
	if _, ok := lineDiff(a, b); ok {
		t.Fatalf("%d x %d changed lines: diffed past the cap of %d cells", len(a), len(b), diffMaxCells)
	}
	long := make([]byte, 0, 1<<20)
	long = append(long, '[')
	for i := 0; i < diffMaxLines; i++ {
		if i > 0 {
			long = append(long, ',')
		}
		long = append(long, '1')
	}
	long = append(long, ']')
	if view := diffDocs("base", long, []byte(`[]`)); !view.TooLarge {
		t.Errorf("%d lines: not refused past diffMaxLines", diffMaxLines)
	}
	small := []byte(`{"tips":[{"title":"a"},{"title":"b"}]}`)
	changed := []byte(`{"tips":[{"title":"a"},{"title":"c"}]}`)
	if view := diffDocs("base", small, changed); view.TooLarge || view.Added != 1 || view.Removed != 1 {
		t.Errorf("small change: too large %t, +%d -%d", view.TooLarge, view.Added, view.Removed)
	}
}

// TestDiffDocsIgnoresLayout: the same JSON with other spacing is no change; a changed value is one line each way.
func TestDiffDocsIgnoresLayout(t *testing.T) {
	live := []byte(`{"a":1,"b":[1,2]}`)
	pretty := []byte("{\n  \"a\": 1,\n  \"b\": [1, 2]\n}")
	if view := diffDocs("base", live, pretty); !view.Same {
		t.Errorf("layout only: not the same (+%d -%d)", view.Added, view.Removed)
	}
	if view := diffDocs("base", live, []byte(`{"a":2,"b":[1,2]}`)); view.Added != 1 || view.Removed != 1 {
		t.Errorf("changed value: +%d -%d", view.Added, view.Removed)
	}
}

// TestDiffDocsKeepsNumbers: a version's numbers reach the page exactly as written, never as a double.
func TestDiffDocsKeepsNumbers(t *testing.T) {
	live := []byte(`{"id":1900000005,"big":12345678901234567890}`)
	version := []byte(`{"id":1900000006,"big":12345678901234567891}`)
	view := diffDocs("base", live, version)
	var text strings.Builder
	for _, row := range view.Rows {
		text.WriteString(row.Text + "\n")
	}
	for _, want := range []string{"1900000006", "12345678901234567891", "1900000005", "12345678901234567890"} {
		if !strings.Contains(text.String(), want) {
			t.Errorf("the diff lost %s:\n%s", want, text.String())
		}
	}
	if strings.Contains(text.String(), "e+") {
		t.Errorf("a number was written in exponent form:\n%s", text.String())
	}
}

// TestContextRowsFoldUnchangedRuns: a change keeps diffContext lines around it; the rest of a long run is one gap row.
func TestContextRowsFoldUnchangedRuns(t *testing.T) {
	var a, b []string
	for i := 0; i < 40; i++ {
		a = append(a, fmt.Sprintf("line %d", i))
		b = append(b, fmt.Sprintf("line %d", i))
	}
	b[20] = "changed"
	ops, _ := lineDiff(a, b)
	rows, added, removed := contextRows(ops)
	if added != 1 || removed != 1 {
		t.Fatalf("+%d -%d, want +1 -1", added, removed)
	}
	var gaps []int
	changed := 0
	for _, r := range rows {
		switch r.Kind {
		case "gap":
			gaps = append(gaps, r.Gap)
		case "add", "del":
			changed++
		}
	}
	// The edit script is 20 unchanged, one removed, one added, then 19 unchanged: 3 lines of context on each side of the change.
	if changed != 2 || len(gaps) != 2 || gaps[0] != 17 || gaps[1] != 16 {
		t.Errorf("rows: %d changed, gaps %v; want 2 changed and gaps [17 16]", changed, gaps)
	}
	if rows[0].Kind != "gap" || rows[len(rows)-1].Kind != "gap" {
		t.Errorf("the run at both ends is not folded: first %s, last %s", rows[0].Kind, rows[len(rows)-1].Kind)
	}
}

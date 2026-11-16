package admin

import (
	"bytes"
	"encoding/json"
	"strings"
)

// The version page compares a version with the live document (PLAN Roadmap 8, slice 8.7). Both sides are pretty-printed
// first, so a change of spacing or line breaks shows nothing; the lines are compared with a longest common subsequence.
// ponytail: the LCS runs on the middle that is left after the common head and tail of the two line lists, and only when
// that middle has at most diffMaxCells cells (a full table of int32). Past the cap the page says the change is too large
// and shows no lines. Upgrade path: Myers (O((N+M)·D)) or a patience diff when a rewrite must be shown line by line.
const (
	diffMaxCells = 1 << 22 // 4M cells: a 16 MB table at most, per request
	diffMaxLines = 50000   // per side, before the table is even considered
	diffContext  = 3       // unchanged lines kept around each change
)

// diffView is what the templates show: the summary, the base it is compared with and the rows (changes with their
// context; a run of unchanged lines beyond the context is one "gap" row).
type diffView struct {
	Base           string
	Added, Removed int
	Same, TooLarge bool
	Rows           []diffRow
}

// diffRow is one line of the diff. Kind is "add", "del", "same" or "gap" (Gap unchanged lines, no numbers).
type diffRow struct {
	Kind     string
	Sign     string
	Old, New int // 1-based line numbers; 0 where the side has no such line
	Text     string
	Gap      int
}

// diffOp is one step of the edit script: ' ' unchanged, '-' only in the live document, '+' only in the version.
type diffOp struct {
	kind byte
	text string
}

// diffDocs compares version with live, both as JSON. base names the live side for the page ("the published v3").
func diffDocs(base string, live, version []byte) *diffView {
	d := &diffView{Base: base}
	a, b := prettyLines(live), prettyLines(version)
	if len(a) > diffMaxLines || len(b) > diffMaxLines {
		d.TooLarge = true
		return d
	}
	ops, ok := lineDiff(a, b)
	if !ok {
		d.TooLarge = true
		return d
	}
	d.Rows, d.Added, d.Removed = contextRows(ops)
	d.Same = d.Added == 0 && d.Removed == 0
	return d
}

// prettyLines is body indented with two spaces, split into lines. A body that is not JSON (never stored: drafts are checked
// for JSON) is compared as it is.
func prettyLines(body []byte) []string {
	var buf bytes.Buffer
	if err := json.Indent(&buf, body, "", "  "); err != nil {
		buf.Reset()
		buf.Write(body)
	}
	return strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
}

// lineDiff is the edit script from a to b. ok is false when the middle is over diffMaxCells.
func lineDiff(a, b []string) ([]diffOp, bool) {
	head := 0
	for head < len(a) && head < len(b) && a[head] == b[head] {
		head++
	}
	tail := 0
	for tail < len(a)-head && tail < len(b)-head && a[len(a)-1-tail] == b[len(b)-1-tail] {
		tail++
	}
	am, bm := a[head:len(a)-tail], b[head:len(b)-tail]
	n, m := len(am), len(bm)
	if (n+1)*(m+1) > diffMaxCells {
		return nil, false
	}
	// lcs[i*w+j] is the length of the LCS of am[i:] and bm[j:].
	w := m + 1
	lcs := make([]int32, (n+1)*w)
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if am[i] == bm[j] {
				lcs[i*w+j] = lcs[(i+1)*w+j+1] + 1
			} else {
				lcs[i*w+j] = max(lcs[(i+1)*w+j], lcs[i*w+j+1])
			}
		}
	}
	ops := make([]diffOp, 0, len(a)+len(b))
	for _, line := range a[:head] {
		ops = append(ops, diffOp{' ', line})
	}
	for i, j := 0, 0; i < n || j < m; {
		switch {
		case i < n && j < m && am[i] == bm[j]:
			ops = append(ops, diffOp{' ', am[i]})
			i, j = i+1, j+1
		case j == m || (i < n && lcs[(i+1)*w+j] >= lcs[i*w+j+1]):
			ops = append(ops, diffOp{'-', am[i]})
			i++
		default:
			ops = append(ops, diffOp{'+', bm[j]})
			j++
		}
	}
	for _, line := range a[len(a)-tail:] {
		ops = append(ops, diffOp{' ', line})
	}
	return ops, true
}

// contextRows keeps the changes of ops with diffContext unchanged lines around each, and folds every longer run of
// unchanged lines into one gap row. It also counts the added and removed lines.
func contextRows(ops []diffOp) (rows []diffRow, added, removed int) {
	keep := make([]bool, len(ops))
	for i, op := range ops {
		if op.kind == ' ' {
			continue
		}
		for j := max(0, i-diffContext); j <= min(len(ops)-1, i+diffContext); j++ {
			keep[j] = true
		}
	}
	oldNo, newNo := 0, 0
	for i := 0; i < len(ops); {
		if !keep[i] {
			j := i
			for j < len(ops) && !keep[j] {
				j++
			}
			for _, op := range ops[i:j] {
				if op.kind != '+' {
					oldNo++
				}
				if op.kind != '-' {
					newNo++
				}
			}
			rows = append(rows, diffRow{Kind: "gap", Gap: j - i})
			i = j
			continue
		}
		op := ops[i]
		row := diffRow{Text: op.text}
		switch op.kind {
		case ' ':
			row.Kind = "same"
			oldNo++
			newNo++
			row.Old, row.New = oldNo, newNo
		case '-':
			row.Kind, row.Sign = "del", "-"
			removed++
			oldNo++
			row.Old = oldNo
		case '+':
			row.Kind, row.Sign = "add", "+"
			added++
			newNo++
			row.New = newNo
		}
		rows = append(rows, row)
		i++
	}
	return rows, added, removed
}

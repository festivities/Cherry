package admin

import (
	"fmt"
	"html"
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"

	"cherry/internal/store"
)

// seedAudit writes one audit row with the given actor, action and target.
func seedAudit(t *testing.T, actor, action, target string) {
	t.Helper()
	if err := store.Audit(actor, action, target, map[string]any{"k": 1}); err != nil {
		t.Fatal(err)
	}
}

// TestAuditFilters: each filter keeps only the rows it names; the substring filters match inside the value; the date range
// is UTC days; the filters combine.
func TestAuditFilters(t *testing.T) {
	fx := newFixture(t, devConfig())
	c := fx.client()
	c.signInDev("owner")
	seedAudit(t, "dev-alpha", "grant", "player:11")
	seedAudit(t, "dev-beta", "publish", "cherrytips.json")
	seedAudit(t, "dev-alpha", "ban", "player:12")
	today := time.Now().UTC().Format(time.DateOnly)

	cases := []struct {
		query   string
		want    []string // must appear
		notWant []string // must not
	}{
		{"actor=alpha", []string{"player:11", "player:12"}, []string{"cherrytips.json"}},
		{"actor=dev-b", []string{"cherrytips.json"}, []string{"player:11"}},
		{"action=publish", []string{"cherrytips.json"}, []string{"player:11", "player:12"}},
		{"target=player:1", []string{"player:11", "player:12"}, []string{"cherrytips.json"}},
		{"actor=alpha&action=ban", []string{"player:12"}, []string{"player:11"}},
		{"from=2000-01-01&to=2000-01-02", []string{"No rows match."}, []string{"player:11", "cherrytips.json"}},
		{"from=" + today + "&to=" + today, []string{"player:11", "cherrytips.json"}, nil},
	}
	for _, tc := range cases {
		r := c.get("/audit?" + tc.query)
		expect(t, r, http.StatusOK)
		for _, want := range tc.want {
			if !strings.Contains(r.body, want) {
				t.Errorf("?%s: page lacks %q", tc.query, want)
			}
		}
		for _, not := range tc.notWant {
			if strings.Contains(r.body, not) {
				t.Errorf("?%s: page holds %q", tc.query, not)
			}
		}
	}

	// The store's listing takes the same filter.
	rows, err := store.AuditSearch(store.AuditFilter{Action: "ban", Actor: "alpha"}, 0, 50)
	if err != nil || len(rows) != 1 || rows[0].Target != "player:12" {
		t.Fatalf("AuditSearch(ban by alpha): %v rows, err %v", rows, err)
	}
}

// TestAuditFilterValidation: a date that is not a day, an action with a space and a filter that is too long are 400, and
// the page keeps none of the bad value.
func TestAuditFilterValidation(t *testing.T) {
	fx := newFixture(t, devConfig())
	c := fx.client()
	c.signInDev("owner")
	for _, query := range []string{
		"from=2026-13-01", "to=yesterday", "action=a%20b", "action=" + strings.Repeat("a", 41),
		"actor=" + strings.Repeat("x", 101), "target=" + strings.Repeat("x", 101),
	} {
		r := c.get("/audit?" + query)
		if r.StatusCode != http.StatusBadRequest {
			t.Errorf("?%s: status %d, want 400", query, r.StatusCode)
		}
	}
	expect(t, c.get("/audit?from=2026-11-17&to=2026-11-17&actor=dev-owner"), http.StatusOK)
}

// olderHref is the href of the "Older rows" link on an audit page, unescaped.
var olderHref = regexp.MustCompile(`href="(/audit\?[^"]*)">Older rows`)

// TestAuditPagesKeepTheFilter: a page of 200 rows links to the next page with the filter kept, and the next page holds the
// rest of the filtered rows.
func TestAuditPagesKeepTheFilter(t *testing.T) {
	fx := newFixture(t, devConfig())
	c := fx.client()
	c.signInDev("owner")
	for i := 0; i < 201; i++ {
		seedAudit(t, "dev-bulk", "bulk", fmt.Sprintf("row:%d", i))
	}
	first := c.get("/audit?action=bulk")
	expect(t, first, http.StatusOK)
	m := olderHref.FindStringSubmatch(first.body)
	if m == nil {
		t.Fatal("the first page has no older-rows link")
	}
	next := html.UnescapeString(m[1])
	if !strings.Contains(next, "action=bulk") || !strings.Contains(next, "before=") {
		t.Fatalf("older link %q drops the filter or the marker", next)
	}
	second := c.get(next)
	expect(t, second, http.StatusOK)
	if n := strings.Count(second.body, "<td>bulk</td>"); n != 1 {
		t.Errorf("the second page holds %d bulk rows, want the 1 that is left", n)
	}
	if strings.Contains(second.body, "Older rows") {
		t.Error("the last page still links to older rows")
	}
	if n := strings.Count(first.body, "<td>bulk</td>"); n != 200 {
		t.Errorf("the first page holds %d bulk rows, want 200", n)
	}
}

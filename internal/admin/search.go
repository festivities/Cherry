package admin

import (
	"net/http"
	"strings"
	"unicode/utf8"

	"cherry/internal/auth"
	"cherry/internal/store"
)

// The global search (PLAN Roadmap 8, staff panel phase 5): GET /search?q= from the header box of every page. It answers four
// groups, each bounded to searchRows: the players (row id, avatar id, identity or name), the chat log's messages (newest first,
// the text as a case-insensitive substring), the audit rows (actor, target or the detail that holds the reason) and the
// documents whose name contains q. A query is at most playerQueryMax characters, the same limit as the player search.

const searchRows = 20 // hits per group

// searchPlayer is a player hit: its page, avatar id, name and identity (masked as the logs mask it).
type searchPlayer struct {
	Href, Aid, Name, Identity string
	ID                        int64
}

// searchAudit is an audit hit with the audit page filtered to its target.
type searchAudit struct {
	Row  store.AuditRow
	Href string
	By   string
}

// searchDoc is a document hit: its name and its page.
type searchDoc struct{ Name, Href string }

// searchPage is the search results: the query and each group, with whether the group was cut at searchRows.
type searchPage struct {
	Query       string
	Players     []searchPlayer
	PlayersMore bool
	Deleted     []deletedRow // deleted avatars by avatar id (exact) or name (deleted.go)
	DeletedMore bool
	Chat        []chatRow
	ChatMore    bool
	Audit       []searchAudit
	AuditMore   bool
	Docs        []searchDoc
}

// handleSearch: GET /search?q=.
func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request, sess session) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if utf8.RuneCountInString(q) > playerQueryMax {
		s.fail(w, http.StatusBadRequest, "The search is at most 100 characters.")
		return
	}
	v := s.base(r, sess, "Search")
	v.SearchQuery = q
	v.Search = searchPage{Query: q}
	if q != "" {
		if err := s.searchInto(&v.Search, q); err != nil {
			s.storeError(w, err)
			return
		}
	}
	s.render(w, http.StatusOK, "search", v)
}

// searchInto fills page with the hits of q. Each group reads at most searchRows+1 rows, so "more" is known without a count.
func (s *Server) searchInto(page *searchPage, q string) error {
	players, more := store.SearchPlayers(q, searchRows)
	page.PlayersMore = more
	for _, p := range players {
		page.Players = append(page.Players, searchPlayer{ID: p.ID, Href: playerPath(p.ID), Aid: p.Aid, Name: p.Name, Identity: auth.MaskIdentity(p.Identity)})
	}
	deleted, deletedMore, err := store.SearchDeletedAvatars(q, searchRows)
	if err != nil {
		return err
	}
	page.DeletedMore = deletedMore
	for _, d := range deleted {
		row := deletedRowOf(d)
		row.Identity = auth.MaskIdentity(row.Identity) // the search masks identities, as the player hits do
		page.Deleted = append(page.Deleted, row)
	}
	msgs, err := store.ChatLogSearch(store.ChatFilter{Text: q}, 0, searchRows)
	if err != nil {
		return err
	}
	page.ChatMore = len(msgs) == searchRows
	page.Chat = chatRows(msgs)
	rows, err := store.AuditFind(q, searchRows)
	if err != nil {
		return err
	}
	page.AuditMore = len(rows) == searchRows
	for _, row := range rows {
		page.Audit = append(page.Audit, searchAudit{Row: row, By: auth.MaskIdentity(row.Actor),
			Href: auditHref(store.AuditFilter{Target: row.Target}, 0)})
	}
	docs, err := store.Live()
	if err != nil {
		return err
	}
	needle := strings.ToLower(q)
	for _, d := range docs {
		if strings.Contains(strings.ToLower(d.Name), needle) {
			page.Docs = append(page.Docs, searchDoc{Name: d.Name, Href: "/docs/" + d.Name})
		}
	}
	return nil
}

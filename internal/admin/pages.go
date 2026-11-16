package admin

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"cherry/internal/auth"
	"cherry/internal/opdata"
	"cherry/internal/store"
)

// link is one navigation entry or breadcrumb: Href (empty for the current page) and Label.
type link struct {
	Href, Label string
	Current     bool
}

// navGroup is one heading of the sidebar and the pages under it. Only pages that exist are listed: Moderation has no page of
// its own yet (the player page holds the removals), so it is not in the sidebar.
type navGroup struct {
	Title string
	Links []link
}

var navGroups = []navGroup{
	{"Overview", []link{{Href: "/", Label: "Dashboard"}}},
	{"Players", []link{{Href: "/players", Label: "Players"}}},
	{"Content", []link{{Href: "/docs", Label: "Documents"}}},
	{"System", []link{{Href: "/audit", Label: "Audit log"}}},
}

// notices are the flash texts a redirect may name with ?notice=<code>. The code picks a fixed text; a code that is not in the
// map shows nothing, so no text from the query ever reaches a page.
var notices = map[string]string{
	"draft-saved":     "The draft is saved as a new version. Validate it, then publish it.",
	"granted":         "The balance is changed. The ledger and the audit trail have the line.",
	"banned":          "The identity is banned. Its sessions and live connections end now.",
	"unbanned":        "The ban is lifted. The player can sign in again.",
	"renamed":         "The nickname is changed. Diary posts and guestbook entries take it too.",
	"post-removed":    "The diary post is removed. The audit trail keeps its number and your reason.",
	"entry-removed":   "The guestbook entry is removed. The audit trail keeps its number and your reason.",
	"comment-removed": "The diary comment is removed. The audit trail keeps its number and your reason.",
	"picture-removed": "The picture is removed. Posts that showed it no longer do.",
}

// check is a validation result shown on a version page.
type check struct {
	Valid      bool
	ArtSkipped bool
	Message    string
}

// view is what the templates read. A page fills the fields it shows; the rest stay empty.
type view struct {
	Title, Server, Who, Role, CSRF string
	Nav                            bool
	NavGroups                      []navGroup
	Crumbs                         []link
	Notice                         string // the flash of a successful action
	Text, Login, Next              string

	// dashboard
	Stats   stats
	Recent  []store.AuditRow
	Signups []store.Signup

	// documents
	Docs     []store.DocLive
	DocName  string
	Doc      store.DocLive
	Versions []store.DocVersion
	Draft    string
	Version  store.DocVersion
	Body     string
	Check    *check

	// audit
	Rows       []store.AuditRow
	Filter     store.AuditFilter
	NewestHref string
	OlderHref  string

	// players
	Query    string
	Players  []store.Player
	More     bool
	Player   store.Player
	Ledger   []store.LedgerLine
	Posts    []playerPost
	Entries  []playerEntry
	Comments []playerComment
	Media    []playerMedia
}

// base is the view of a signed-in page: the sidebar, the identity and role in the header, the session's CSRF token and the
// flash the request names (?notice=, mapped through notices).
func (s *Server) base(r *http.Request, sess session, title string) view {
	role := "Staff"
	if sess.identity == s.owner {
		role = "Owner"
	}
	return view{
		Title: title, Server: s.cfg.ServerName, Who: whoOf(sess), Role: role, CSRF: sess.csrf,
		Nav: true, NavGroups: navFor(r.URL.Path), Notice: notices[r.URL.Query().Get("notice")],
	}
}

// whoOf is the identity the header shows: the display name Discord gave, then the identity masked as the logs mask it.
func whoOf(sess session) string {
	id := auth.MaskIdentity(sess.identity)
	if sess.name != "" && sess.name != sess.identity {
		return sess.name + " (" + id + ")"
	}
	return id
}

// navFor copies the sidebar and marks the entry of the area p is in ("/players/5" is in "/players").
func navFor(p string) []navGroup {
	area := "/" + strings.Split(strings.TrimPrefix(p, "/"), "/")[0]
	out := make([]navGroup, len(navGroups))
	for i, g := range navGroups {
		out[i] = navGroup{Title: g.Title, Links: slices.Clone(g.Links)}
		for j := range out[i].Links {
			out[i].Links[j].Current = out[i].Links[j].Href == area
		}
	}
	return out
}

// fail answers with the error page: a short text, never a stack trace or a store message the caller did not expect.
func (s *Server) fail(w http.ResponseWriter, status int, text string) {
	s.render(w, status, "error", view{Title: "Error", Text: text})
}

// signInPage is the answer to a request without a live session.
func (s *Server) signInPage(w http.ResponseWriter) {
	s.render(w, http.StatusUnauthorized, "error", view{Title: "Sign in", Text: "Sign in with Discord to continue.", Login: loginURL})
}

// refuseRequest refuses a state change whose Origin, session or CSRF token is wrong: one generic page, one log line.
func (s *Server) refuseRequest(w http.ResponseWriter, r *http.Request, reason string) {
	s.logger.Printf("cherry: staff request refused reason=%s path=%s", reason, r.URL.Path)
	s.fail(w, http.StatusForbidden, "This request was refused. Go back, reload the page and try again.")
}

// redirect sends the browser to loc with code (a same-host path only: every caller passes one it built or validated).
func redirect(w http.ResponseWriter, code int, loc string) {
	w.Header().Set("Location", loc)
	w.WriteHeader(code)
}

// storeError maps a store error to its status and a short message. Unknown errors are logged and answered generically.
func (s *Server) storeError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrNoDatabase):
		s.fail(w, http.StatusServiceUnavailable, "The database is not available.")
	case errors.Is(err, store.ErrNotDocument), errors.Is(err, store.ErrNoVersion), errors.Is(err, store.ErrNoPlayer), errors.Is(err, store.ErrNoItem):
		s.fail(w, http.StatusNotFound, err.Error())
	case errors.Is(err, store.ErrNicknameCopies):
		s.fail(w, http.StatusInternalServerError, err.Error())
	case errors.Is(err, store.ErrAlreadyBanned), errors.Is(err, store.ErrNotBanned):
		s.fail(w, http.StatusConflict, err.Error())
	case errors.Is(err, store.ErrInvalidDocument):
		s.fail(w, http.StatusUnprocessableEntity, err.Error())
	case errors.Is(err, store.ErrDocumentState):
		s.fail(w, http.StatusConflict, err.Error())
	case errors.Is(err, store.ErrDocumentTooLarge):
		s.fail(w, http.StatusRequestEntityTooLarge, err.Error())
	case errors.Is(err, store.ErrBadInput):
		s.fail(w, http.StatusBadRequest, err.Error())
	default:
		s.logger.Printf("cherry: staff request failed: %v", err)
		s.fail(w, http.StatusInternalServerError, "Something went wrong. The server log has the detail.")
	}
}

// docArgs reads the {name} and {version} of a document route; ok is false (and the reply written) for a bad version.
func (s *Server) docArgs(w http.ResponseWriter, r *http.Request) (string, int, bool) {
	v, err := strconv.Atoi(r.PathValue("version"))
	if err != nil || v < 1 {
		s.fail(w, http.StatusBadRequest, "The version must be a positive number.")
		return "", 0, false
	}
	return r.PathValue("name"), v, true
}

// docLive is the Live row of one document.
func docLive(name string) (store.DocLive, error) {
	docs, err := store.Live()
	if err != nil {
		return store.DocLive{}, err
	}
	for _, d := range docs {
		if d.Name == name {
			return d, nil
		}
	}
	return store.DocLive{Name: name}, nil
}

// prefill is the body a new draft starts from: the published version, else the default built into the binary.
func prefill(live store.DocLive) (string, error) {
	if live.Published > 0 {
		_, body, err := store.Get(live.Name, live.Published)
		return string(body), err
	}
	def, _ := opdata.Embedded(live.Name)
	return string(def), nil
}

// handleDocsList: the Live table of every document.
func (s *Server) handleDocsList(w http.ResponseWriter, r *http.Request, sess session) {
	docs, err := store.Live()
	if err != nil {
		s.storeError(w, err)
		return
	}
	v := s.base(r, sess, "Documents")
	v.Docs = docs
	s.render(w, http.StatusOK, "docs", v)
}

// handleDocHistory: one document's versions and the draft form, prefilled with what a new draft should start from.
func (s *Server) handleDocHistory(w http.ResponseWriter, r *http.Request, sess session) {
	name := r.PathValue("name")
	versions, err := store.History(name)
	if err != nil {
		s.storeError(w, err)
		return
	}
	live, err := docLive(name)
	if err != nil {
		s.storeError(w, err)
		return
	}
	draft, err := prefill(live)
	if err != nil {
		s.storeError(w, err)
		return
	}
	v := s.base(r, sess, name)
	v.Crumbs = []link{{Href: "/docs", Label: "Documents"}, {Label: name}}
	v.DocName, v.Doc, v.Versions, v.Draft = name, live, versions, draft
	s.render(w, http.StatusOK, "doc", v)
}

// handleDraft: POST /docs/{name}/draft stores the body as the next draft. The audit row names the actor and the hash.
func (s *Server) handleDraft(w http.ResponseWriter, r *http.Request, sess session) {
	name := r.PathValue("name")
	// Browsers submit textarea line breaks as CRLF; drafts are stored with LF so hashes do not depend on the staff machine (user, 2026-11-15).
	body := strings.ReplaceAll(r.PostFormValue("body"), "\r\n", "\n")
	dv, err := store.SaveDraft(name, []byte(body), sess.identity, r.PostFormValue("note"))
	if err != nil {
		s.storeError(w, err)
		return
	}
	redirect(w, http.StatusSeeOther, fmt.Sprintf("/docs/%s/%d?notice=draft-saved", url.PathEscape(name), dv.Version))
}

// handleDocVersion: one stored version, read only, with the validate, publish and roll-back buttons.
func (s *Server) handleDocVersion(w http.ResponseWriter, r *http.Request, sess session) {
	name, ver, ok := s.docArgs(w, r)
	if !ok {
		return
	}
	dv, body, err := store.Get(name, ver)
	if err != nil {
		s.storeError(w, err)
		return
	}
	v := s.base(r, sess, fmt.Sprintf("%s v%d", name, ver))
	v.Crumbs = []link{{Href: "/docs", Label: "Documents"}, {Href: "/docs/" + url.PathEscape(name), Label: name}, {Label: fmt.Sprintf("v%d", ver)}}
	v.DocName, v.Version, v.Body = name, dv, string(body)
	s.render(w, http.StatusOK, "version", v)
}

// handleValidate: POST …/validate runs the validator on the version and changes nothing.
func (s *Server) handleValidate(w http.ResponseWriter, r *http.Request, sess session) {
	name, ver, ok := s.docArgs(w, r)
	if !ok {
		return
	}
	dv, body, err := store.Get(name, ver)
	if err != nil {
		s.storeError(w, err)
		return
	}
	v := s.base(r, sess, fmt.Sprintf("%s v%d", name, ver))
	v.Crumbs = []link{{Href: "/docs", Label: "Documents"}, {Href: "/docs/" + url.PathEscape(name), Label: name}, {Label: fmt.Sprintf("v%d", ver)}}
	v.DocName, v.Version, v.Body = name, dv, string(body)
	status := http.StatusOK
	artSkipped, verr := store.Validate(name, ver)
	switch {
	case errors.Is(verr, store.ErrInvalidDocument):
		v.Check = &check{Valid: false, Message: verr.Error()}
		status = http.StatusUnprocessableEntity
	case verr != nil:
		s.storeError(w, verr)
		return
	default:
		v.Check = &check{Valid: true, ArtSkipped: artSkipped}
	}
	s.render(w, status, "version", v)
}

// handlePublish: POST …/publish (privileged). The result page says the change applies at the next restart.
func (s *Server) handlePublish(w http.ResponseWriter, r *http.Request, sess session) {
	name, ver, ok := s.docArgs(w, r)
	if !ok {
		return
	}
	res, err := store.Publish(name, ver, sess.identity)
	if err != nil {
		s.storeError(w, err)
		return
	}
	v := s.base(r, sess, "Published")
	v.Notice = publishNotice(res)
	s.render(w, http.StatusOK, "result", v)
}

// publishNotice is the line a publish result shows.
func publishNotice(res store.DocResult) string {
	replaced := "it replaced none"
	if res.Replaces > 0 {
		replaced = fmt.Sprintf("it replaced v%d", res.Replaces)
	}
	note := ""
	if res.ArtSkipped {
		note = " The art checks were skipped: this process has no asset store."
	}
	return fmt.Sprintf("%s v%d is published (%s). The change applies at the next restart.%s", res.Name, res.Version, replaced, note)
}

// handleRollback: POST /docs/{name}/rollback (privileged): back to the version the published one replaced, or none.
func (s *Server) handleRollback(w http.ResponseWriter, r *http.Request, sess session) {
	name := r.PathValue("name")
	res, err := store.Rollback(name, sess.identity)
	if err != nil {
		s.storeError(w, err)
		return
	}
	v := s.base(r, sess, "Rolled back")
	if res.Version == 0 {
		v.Notice = fmt.Sprintf("%s has no published version now (v%d was rolled back). The config file or the embedded default applies at the next restart.", name, res.Replaces)
	} else {
		v.Notice = fmt.Sprintf("%s v%d is published again (v%d was rolled back). The change applies at the next restart.", name, res.Version, res.Replaces)
	}
	s.render(w, http.StatusOK, "result", v)
}

// auditFilterOf reads the audit filter of a query. It returns the problem to show when a value is not usable.
func auditFilterOf(q url.Values) (store.AuditFilter, string) {
	f := store.AuditFilter{
		Actor:  strings.TrimSpace(q.Get("actor")),
		Target: strings.TrimSpace(q.Get("target")),
		Action: strings.TrimSpace(q.Get("action")),
		From:   strings.TrimSpace(q.Get("from")),
		To:     strings.TrimSpace(q.Get("to")),
	}
	if utf8.RuneCountInString(f.Actor) > playerQueryMax || utf8.RuneCountInString(f.Target) > playerQueryMax {
		return f, "The actor and target filters are at most 100 characters."
	}
	if len(f.Action) > 40 || !onlyChars(f.Action, "-_.") {
		return f, "The action filter holds letters, digits, dashes, dots and underscores, at most 40 characters."
	}
	for _, day := range []string{f.From, f.To} {
		if _, err := time.Parse(time.DateOnly, day); day != "" && err != nil {
			return f, "The dates are days in the form YYYY-MM-DD."
		}
	}
	return f, ""
}

// auditHref is the audit page with the filter and, when before is set, the page marker.
func auditHref(f store.AuditFilter, before int64) string {
	q := url.Values{}
	for k, v := range map[string]string{"actor": f.Actor, "target": f.Target, "action": f.Action, "from": f.From, "to": f.To} {
		if v != "" {
			q.Set(k, v)
		}
	}
	if before > 0 {
		q.Set("before", strconv.FormatInt(before, 10))
	}
	if len(q) == 0 {
		return "/audit"
	}
	return "/audit?" + q.Encode()
}

// handleAudit: the rows the filter keeps, auditPage a page; ?before=<id> reads the page that follows.
func (s *Server) handleAudit(w http.ResponseWriter, r *http.Request, sess session) {
	q := r.URL.Query()
	var before int64
	if raw := q["before"]; len(raw) > 0 {
		n, err := strconv.ParseInt(raw[0], 10, 64)
		if len(raw) != 1 || err != nil || n < 1 {
			s.fail(w, http.StatusBadRequest, "The page marker is not valid.")
			return
		}
		before = n
	}
	f, problem := auditFilterOf(q)
	if problem != "" {
		s.fail(w, http.StatusBadRequest, problem)
		return
	}
	rows, err := store.AuditSearch(f, before, auditPage)
	if err != nil {
		s.storeError(w, err)
		return
	}
	v := s.base(r, sess, "Audit log")
	v.Rows, v.Filter, v.NewestHref = rows, f, auditHref(f, 0)
	if len(rows) == auditPage {
		v.OlderHref = auditHref(f, rows[len(rows)-1].ID)
	}
	s.render(w, http.StatusOK, "audit", v)
}

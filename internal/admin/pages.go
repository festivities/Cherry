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

// navGroup is one heading of the sidebar and the pages under it. Only pages that exist are listed.
type navGroup struct {
	Title string
	Links []link
}

var navGroups = []navGroup{
	{"Overview", []link{{Href: "/", Label: "Dashboard"}}},
	{"Players", []link{{Href: "/players", Label: "Players"}, {Href: "/players/deleted", Label: "Deleted avatars"}, {Href: "/mail", Label: "Mail"}}},
	{"Moderation", []link{{Href: "/chat", Label: "Chat log"}, {Href: "/photos", Label: "Photos"}}},
	{"Insights", []link{{Href: "/online", Label: "Online now"}, {Href: "/economy", Label: "Economy"}, {Href: "/activity", Label: "Activity"}}},
	{"Content", []link{{Href: "/docs", Label: "Documents"}}},
	{"System", []link{{Href: "/audit", Label: "Audit log"}, {Href: "/settings", Label: "Settings"}}},
}

// notices are the flash texts a redirect may name with ?notice=<code>. The code picks a fixed text; a code that is not in the
// map shows nothing, so no text from the query ever reaches a page.
var notices = map[string]string{
	"draft-saved":       "The draft is saved as a new version. Validate it, then publish it.",
	"granted":           "The change is saved. The audit trail has the line; a currency change has its ledger line too.",
	"mail-sent":         "The mail is sent. The sent mail list has its recipients and the audit trail has the line.",
	"mail-already-sent": "This mail was already sent. Nothing was sent again.",
	"banned":            "The identity is banned. Its sessions and live connections end now.",
	"unbanned":          "The ban is lifted. The player can sign in again.",
	"renamed":           "The nickname is changed. Diary posts and guestbook entries take it too.",
	"post-removed":      "The diary post is removed. The audit trail keeps its number and your reason.",
	"entry-removed":     "The guestbook entry is removed. The audit trail keeps its number and your reason.",
	"comment-removed":   "The diary comment is removed. The audit trail keeps its number and your reason.",
	"picture-removed":   "The picture is removed. Posts that showed it no longer do.",
	"kicked":            "The player's live connections are closed. This is not a ban: the player can sign in again at once.",
	"draft-checked":     "The draft is saved and it passes its checks. Publish it when it is ready.",
	// draft-checked-art-skipped: the art checks could not run in this process (no asset store), so the note says so.
	"draft-checked-art-skipped": "The draft is saved and it passes the checks this process can run. The art checks were skipped: this process has no asset store.",
	"chat-days-saved":           "The chat log keeps messages for the new number of days. The next hourly sweep applies it; lowering the number deletes the older messages then, and they cannot be brought back.",
	"chat-days-reset":           "The chat log uses the server's configured retention again. The next hourly sweep applies it.",
	"square-room-cap-saved":     "The room size is saved. New players go to the first room with space from now on; anyone already inside stays where they are.",
	"square-room-cap-reset":     "Square rooms hold the default number of players again. The next entry uses it.",
	"staff-added":               "The staff member is added. They can sign in at this panel from now on.",
	"staff-removed":             "The staff member is removed. Their open sessions end now.",
}

// check is a validation result shown on a version page, or next to the draft editor when a draft just saved was refused
// (Saved is that draft's version then).
type check struct {
	Valid      bool
	ArtSkipped bool
	Saved      int
	Message    string
}

// view is what the templates read. A page fills the fields it shows; the rest stay empty.
type view struct {
	Title, Server, Who, Role, CSRF string
	SessionLeft                    string // how long the session lasts without a request, shown in the header
	SearchQuery                    string // the header search box's value on the search page
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
	Docs      []store.DocLive
	DocName   string
	Doc       store.DocLive
	Versions  []store.DocVersion
	Draft     string
	DraftNote string
	Version   store.DocVersion
	Body      string
	Check     *check
	Diff      *diffView
	Scripts   []string // the page's own scripts (under /static; the layout adds the tags)

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

	// chat log and photos (moderation.go)
	ChatRows    []chatRow
	ChatFilter  store.ChatFilter
	ChatKinds   []string
	ChatContext string // the context line of a context link
	Keep        int    // days the chat log keeps a message (0 = no limit): the panel's value, else retention.chat-days
	Pictures    []pictureRow
	PicFilter   store.PictureFilter
	PicKinds    []string

	// Insights (insights.go, online.go, economy.go, activity.go)
	Online   onlinePage
	Economy  economyPage
	Activity activityPage
	Summary  insightsSummary // the dashboard's row

	// the player page (playerpage.go), mail (mail.go), search (search.go), settings (settings.go)
	Page     playerPage
	Mail     mailPage
	Search   searchPage
	Settings settingsPage

	// deleted avatars (deleted.go): the list rows and one record
	DeletedRows   []deletedRow
	DeletedAvatar deletedAvatarPage
}

// base is the view of a signed-in page: the sidebar, the identity and role in the header, the session's CSRF token and the
// flash the request names (?notice=, mapped through notices).
func (s *Server) base(r *http.Request, sess session, title string) view {
	role := "Staff"
	if sess.identity == s.owner {
		role = "Owner"
	}
	return view{
		Title: title, Server: s.cfg.ServerName, Who: whoOf(sess), Role: role, CSRF: sess.csrf, SessionLeft: sessionLeft(sess, s.now()),
		Nav: true, NavGroups: navFor(r.URL.Path, s.isOwner(sess)), Notice: notices[r.URL.Query().Get("notice")],
	}
}

// isOwner reports whether the session is the owner's (the only identity that may send mail to every account).
func (s *Server) isOwner(sess session) bool { return sess.identity == s.owner }

// sessionLeft is the time a session lasts from now: to the idle timeout after its last request, or the absolute lifetime,
// whichever is first. Less than a minute is "under a minute".
func sessionLeft(sess session, now time.Time) string {
	end := sess.lastSeen.Add(idleTimeout)
	if abs := sess.created.Add(absoluteTimeout); abs.Before(end) {
		end = abs
	}
	left := end.Sub(now)
	if left < time.Minute {
		return "under a minute"
	}
	return fmt.Sprintf("%d min", int(left/time.Minute))
}

// whoOf is the identity the header shows: the display name Discord gave, then the identity masked as the logs mask it.
func whoOf(sess session) string {
	id := auth.MaskIdentity(sess.identity)
	if sess.name != "" && sess.name != sess.identity {
		return sess.name + " (" + id + ")"
	}
	return id
}

// navFor copies the sidebar and marks the entry of the area p is in ("/players/5" is in "/players"). The Settings entry is the
// owner's only: the copy for anyone else leaves it out.
func navFor(p string, owner bool) []navGroup {
	area := "/" + strings.Split(strings.TrimPrefix(p, "/"), "/")[0]
	out := make([]navGroup, len(navGroups))
	for i, g := range navGroups {
		out[i] = navGroup{Title: g.Title, Links: slices.Clone(g.Links)}
		if !owner {
			out[i].Links = slices.DeleteFunc(out[i].Links, func(l link) bool { return l.Href == "/settings" })
		}
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

// liveBody is the body a document runs on as the staff UI shows it: the published version, else the default built into
// the binary. base names it for the page (the diff's "compared with"). A config file override is not shown here.
func liveBody(live store.DocLive) (base string, body []byte, err error) {
	if live.Published > 0 {
		_, published, err := store.Get(live.Name, live.Published)
		return fmt.Sprintf("the published v%d", live.Published), published, err
	}
	def, _ := opdata.Embedded(live.Name)
	return "the built-in default (nothing is published)", def, nil
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

// docPage renders the document page: the draft form with draft and note as its values, the check of a draft that was just
// saved (chk), and the history. It is the page of GET /docs/{name} and of a refused "save and check".
func (s *Server) docPage(w http.ResponseWriter, r *http.Request, sess session, name, draft, note string, chk *check, status int) {
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
	v := s.base(r, sess, name)
	v.Crumbs = []link{{Href: "/docs", Label: "Documents"}, {Label: name}}
	v.DocName, v.Doc, v.Versions, v.Draft, v.DraftNote, v.Check = name, live, versions, draft, note, chk
	v.Scripts = []string{"editor.js"}
	s.render(w, status, "doc", v)
}

// handleDocHistory: GET /docs/{name}. The draft form starts from the live document; the history is below it.
func (s *Server) handleDocHistory(w http.ResponseWriter, r *http.Request, sess session) {
	name := r.PathValue("name")
	live, err := docLive(name)
	if err != nil {
		s.storeError(w, err)
		return
	}
	_, draft, err := liveBody(live)
	if err != nil {
		s.storeError(w, err)
		return
	}
	s.docPage(w, r, sess, name, string(draft), "", nil, http.StatusOK)
}

// handleDraft: POST /docs/{name}/draft stores the body as the next draft. The audit row names the actor and the hash. With
// then=check the draft is also validated at once: a refused draft stays saved, and the page shows why next to the editor.
func (s *Server) handleDraft(w http.ResponseWriter, r *http.Request, sess session) {
	name := r.PathValue("name")
	// Browsers submit textarea line breaks as CRLF; drafts are stored with LF so hashes do not depend on the staff machine (user, 2026-11-15).
	body := strings.ReplaceAll(r.PostFormValue("body"), "\r\n", "\n")
	note := r.PostFormValue("note")
	dv, err := store.SaveDraft(name, []byte(body), sess.identity, note)
	if err != nil {
		s.storeError(w, err)
		return
	}
	if r.PostFormValue("then") != "check" {
		redirect(w, http.StatusSeeOther, fmt.Sprintf("/docs/%s/%d?notice=draft-saved", url.PathEscape(name), dv.Version))
		return
	}
	artSkipped, verr := store.Validate(name, dv.Version)
	switch {
	case errors.Is(verr, store.ErrInvalidDocument):
		s.docPage(w, r, sess, name, body, note, &check{Valid: false, Saved: dv.Version, Message: verr.Error()}, http.StatusUnprocessableEntity)
	case verr != nil:
		s.storeError(w, verr)
	default:
		notice := "draft-checked"
		if artSkipped {
			notice = "draft-checked-art-skipped"
		}
		redirect(w, http.StatusSeeOther, fmt.Sprintf("/docs/%s/%d?notice=%s", url.PathEscape(name), dv.Version, notice))
	}
}

// versionDiff compares body (a stored version of name) with the live document of name.
func (s *Server) versionDiff(name string, body []byte) (*diffView, error) {
	live, err := docLive(name)
	if err != nil {
		return nil, err
	}
	base, liveDoc, err := liveBody(live)
	if err != nil {
		return nil, err
	}
	return diffDocs(base, liveDoc, body), nil
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
	diff, err := s.versionDiff(name, body)
	if err != nil {
		s.storeError(w, err)
		return
	}
	v := s.base(r, sess, fmt.Sprintf("%s v%d", name, ver))
	v.Crumbs = []link{{Href: "/docs", Label: "Documents"}, {Href: "/docs/" + url.PathEscape(name), Label: name}, {Label: fmt.Sprintf("v%d", ver)}}
	v.DocName, v.Version, v.Body, v.Diff = name, dv, string(body), diff
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
	diff, err := s.versionDiff(name, body)
	if err != nil {
		s.storeError(w, err)
		return
	}
	v := s.base(r, sess, fmt.Sprintf("%s v%d", name, ver))
	v.Crumbs = []link{{Href: "/docs", Label: "Documents"}, {Href: "/docs/" + url.PathEscape(name), Label: name}, {Label: fmt.Sprintf("v%d", ver)}}
	v.DocName, v.Version, v.Body, v.Diff = name, dv, string(body), diff
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
	if !dateOK(f.From, f.To) {
		return f, "The dates are days in the form YYYY-MM-DD."
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

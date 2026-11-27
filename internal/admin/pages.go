package admin

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"cherry/internal/auth"
	"cherry/internal/opdata"
	"cherry/internal/store"
)

// link is one navigation entry or breadcrumb: Href (empty for the current page), Label, and the minimum role that sees it.
type link struct {
	Href, Label string
	Current     bool
	MinRole     staffRole
}

// navGroup is one heading of the sidebar and the pages under it. Only pages that exist are listed.
type navGroup struct {
	Title string
	Links []link
}

var navGroups = []navGroup{
	{"Overview", []link{{Href: "/", Label: "Dashboard", MinRole: roleModerator}}},
	{"Players", []link{
		{Href: "/players", Label: "Players", MinRole: roleModerator},
		{Href: "/players/deleted", Label: "Deleted avatars", MinRole: roleModerator},
		{Href: "/mail", Label: "Mail", MinRole: roleModerator},
		{Href: "/balances", Label: "All balances", MinRole: roleOwner},
		{Href: "/broadcast", Label: "Broadcast", MinRole: roleOwner},
	}},
	{"Moderation", []link{
		{Href: "/reports", Label: "Reports", MinRole: roleModerator},
		{Href: "/chat", Label: "Chat log", MinRole: roleModerator},
		{Href: "/diary", Label: "Diary entries", MinRole: roleModerator},
		{Href: "/circles", Label: "Circles", MinRole: roleModerator},
		{Href: "/photos", Label: "Photos", MinRole: roleModerator},
	}},
	{"Insights", []link{
		{Href: "/online", Label: "Online now", MinRole: roleModerator},
		{Href: "/economy", Label: "Economy", MinRole: roleModerator},
		{Href: "/activity", Label: "Activity", MinRole: roleModerator},
		{Href: "/geography", Label: "Geography", MinRole: roleModerator},
	}},
	{"Content", []link{
		{Href: "/items", Label: "Item names", MinRole: roleHelper},
		{Href: "/garden-buttons", Label: "Garden buttons", MinRole: roleOwner},
		{Href: "/docs", Label: "Documents", MinRole: roleOwner},
	}},
	{"System", []link{
		{Href: "/audit", Label: "Audit log", MinRole: roleModerator},
		{Href: "/settings", Label: "Settings", MinRole: roleOwner},
	}},
}

// notices are the flash texts a redirect may name with ?notice=<code>. The code picks a fixed text; a code that is not in the
// map shows nothing, so no text from the query ever reaches a page.
var notices = map[string]string{
	"draft-saved":       "The draft is saved as a new version. Validate it, then publish it.",
	"draft-discarded":   "The draft is deleted and cannot be restored. The audit trail has the line.",
	"avatar-moved":      "The avatar is moved to the identity. The account it left has no avatar now; every login of the moved avatar has ended, so the device that had it is signed out.",
	"avatar-restored":   "The avatar is restored to the identity. Its posts, guestbook entries, comments, likes and friendships are back where their numbers were free; the audit row lists what was skipped.",
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
	"upload-total-cap-saved":    "The total photo storage cap is saved. New uploads check it from now on; existing pictures are kept.",
	"upload-total-cap-reset":    "The total photo storage cap uses the server's configured value again.",
	"upload-quota-bytes-saved":  "The picture storage per player is saved. New uploads check it from now on; existing pictures are kept.",
	"upload-quota-bytes-reset":  "The picture storage per player uses the server's configured value again.",
	"upload-receive-cap-saved":  "The maximum upload receive size is saved. New uploads check it from now on; existing pictures are kept.",
	"upload-receive-cap-reset":  "The maximum upload receive size uses the server's configured value again.",
	"upload-jpeg-cap-saved":     "The target JPEG filesize is saved. New uploads check it from now on; existing pictures are kept.",
	"upload-jpeg-cap-reset":     "The target JPEG filesize uses the server's configured value again.",
	"upload-png-cap-saved":      "The target transparent PNG filesize is saved. New uploads check it from now on; existing pictures are kept.",
	"upload-png-cap-reset":      "The target transparent PNG filesize uses the server's configured value again.",
	"max-players-saved":         "The concurrent player cap is saved. New logins check it from now on; anyone already online stays connected.",
	"max-players-reset":         "The concurrent player cap uses the server's configured value again.",
	"limit-saved":               "The server limit is saved and applies now. Frame limits reach only connections opened from now on; open connections keep the values they had.",
	"limit-reset":               "The server limit uses the server's configured value again. Frame limits reach only connections opened from now on.",
	"staff-added":               "The staff member is added. They can sign in at this panel from now on.",
	"staff-role-saved":          "The staff member's role is updated. Their open sessions end now so the new role applies at their next sign-in.",
	"staff-removed":             "The staff member is removed. Their open sessions end now.",
	"item-name-saved":           "The item name is saved and live in the game now.",
	"item-name-reset":           "The item name is reset to its default.",
	"maint-whitelist-added":     "The account is added to the maintenance whitelist. It now bypasses maintenance popups.",
	"maint-whitelist-removed":   "The account is removed from the maintenance whitelist.",
	"report-resolved":           "The report is resolved. The audit trail has the line and your note.",
	"report-dismissed":          "The report is dismissed. The audit trail has the line and your note.",
	"balances-already-applied":  "This balance change was already made. Nothing changed again.",
	"circle-edited":             "The circle details are saved. The audit trail has the line and your reason.",
	"circle-member-removed":     "The member is removed from the circle. The audit trail has the line and your reason.",
	"circle-disbanded":          "The circle is disbanded. Its members are removed and the audit trail has the line.",
	"garden-button-added":       "The Garden button is saved. Players see it the next time their Garden loads.",
	"garden-button-removed":     "The Garden button is removed. A player who already saw it keeps it until they restart the app.",
	"garden-picture-added":      "The custom picture is uploaded. Players download it at their next app start; a button can use it now.",
	"garden-picture-removed":    "The custom picture is removed. Players get the updated button file at their next app start.",
	"broadcast-sent":            "The announcement was sent to all currently online players. The audit trail has the line.",
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
	Title, Server, Who, Role, RoleClass, CSRF string
	CanSearch                                 bool   // moderators and the owner get the topbar global search
	SessionLeft                               string // how long the session lasts without a request, shown in the header
	SearchQuery                               string // the header search box's value on the search page
	Nav                                       bool
	NavGroups                                 []navGroup
	Crumbs                                    []link
	Notice                                    string // the flash of a successful action
	Text, Login, Next                         string

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
	ChatRows     []chatRow
	ChatFilter   store.ChatFilter
	ChatKinds    []string
	ChatContext  string // the context line of a context link
	ChatNewestID int64
	ChatOldestID int64
	ChatHasMore  bool
	ChatLive     bool
	ChatQuery    string
	ChatLiveHref string
	Keep         int // days the chat log keeps a message (0 = no limit): the panel's value, else retention.chat-days
	Pictures     []pictureRow
	PicFilter    store.PictureFilter
	PicKinds     []string
	Diary        diaryPage

	// Insights (insights.go, online.go, economy.go, activity.go, geography.go)
	Online    onlinePage
	Economy   economyPage
	Activity  activityPage
	Geography *geographyView
	Summary   insightsSummary // the dashboard's row

	// the player page (playerpage.go), mail (mail.go), balances (balances.go), broadcast (broadcast.go), search (search.go), settings (settings.go)
	Page      playerPage
	Mail      mailPage
	Balances  balancesPage
	Broadcast *broadcastPage
	// Garden buttons (gardenbuttons.go)
	GardenButtons *gardenButtonsPage
	Search        searchPage
	Settings      settingsPage

	// deleted avatars (deleted.go): the list rows and one record
	DeletedRows   []deletedRow
	DeletedAvatar deletedAvatarPage
	Avatar        avatarPage

	// reports (reports.go): the queue, one report and the dashboard's open count
	Reports     reportsPage
	Report      reportDetail
	OpenReports int

	// circles (circles.go): the list and one circle's detail page
	Circles circlesPage
	Circle  circleDetailPage

	// item names (items.go)
	Items itemsPage
}

// handleRoot sends Helpers straight to /items so they land on the item-naming view with zero clutter, and shows the
// Dashboard to Moderators and the Owner.
func (s *Server) handleRoot(w http.ResponseWriter, r *http.Request, sess session) {
	if s.roleOf(sess.identity) == roleHelper {
		redirect(w, http.StatusSeeOther, "/items")
		return
	}
	s.handleDashboard(w, r, sess)
}

// base is the view of a signed-in page: the sidebar, the identity and role in the header, the session's CSRF token and the
// flash the request names (?notice=, mapped through notices).
func (s *Server) base(r *http.Request, sess session, title string) view {
	role := s.roleOf(sess.identity)
	nav := navFor(r.URL.Path, role)
	if role >= roleModerator {
		reportsBadge(nav)
	}
	return view{
		Title: title, Server: s.cfg.ServerName, Who: whoOf(sess), Role: role.Label(), RoleClass: role.Slug(),
		CanSearch: role >= roleModerator, CSRF: sess.csrf, SessionLeft: sessionLeft(sess, s.now()),
		Nav: true, NavGroups: nav, Notice: notices[r.URL.Query().Get("notice")],
	}
}

// reportsBadge labels the Reports entry of the sidebar with the open count. A count that cannot be read leaves the label bare.
func reportsBadge(groups []navGroup) {
	n, err := store.CountOpenReports()
	if err != nil || n == 0 {
		return
	}
	for i := range groups {
		for j := range groups[i].Links {
			if groups[i].Links[j].Href == "/reports" {
				groups[i].Links[j].Label = fmt.Sprintf("Reports (%d)", n)
			}
		}
	}
}

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

// navFor copies the sidebar filtered to what role may access, drops empty groups, and marks the entry of the area p is in
// ("/players/5" is in "/players").
func navFor(p string, role staffRole) []navGroup {
	area := "/" + strings.Split(strings.TrimPrefix(p, "/"), "/")[0]
	out := make([]navGroup, 0, len(navGroups))
	for _, g := range navGroups {
		var links []link
		for _, l := range g.Links {
			if role < l.MinRole {
				continue
			}
			l.Current = l.Href == area
			links = append(links, l)
		}
		if len(links) == 0 {
			continue
		}
		out = append(out, navGroup{Title: g.Title, Links: links})
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
	case errors.Is(err, store.ErrNotDocument), errors.Is(err, store.ErrNoVersion), errors.Is(err, store.ErrNoPlayer), errors.Is(err, store.ErrNoItem), errors.Is(err, store.ErrNoRecord),
		errors.Is(err, store.ErrNoReport):
		s.fail(w, http.StatusNotFound, err.Error())
	case errors.Is(err, store.ErrNicknameCopies):
		s.fail(w, http.StatusInternalServerError, err.Error())
	case store.IsRefused(err):
		s.fail(w, http.StatusConflict, err.Error())
	case errors.Is(err, store.ErrAlreadyBanned), errors.Is(err, store.ErrNotBanned), errors.Is(err, store.ErrReportClosed):
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

// handleDiscard: POST /docs/{name}/{version}/discard deletes a draft version. The store refuses a published or retired one.
// The history page shows the notice.
func (s *Server) handleDiscard(w http.ResponseWriter, r *http.Request, sess session) {
	name, ver, ok := s.docArgs(w, r)
	if !ok {
		return
	}
	if err := store.DiscardDraft(name, ver, sess.identity); err != nil {
		s.storeError(w, err)
		return
	}
	redirect(w, http.StatusSeeOther, fmt.Sprintf("/docs/%s?notice=draft-discarded", url.PathEscape(name)))
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

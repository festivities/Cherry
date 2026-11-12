package admin

import (
	"bytes"
	_ "embed" // staff.css
	"errors"
	"fmt"
	"html/template"
	"net/http"
	"net/url"
	"strconv"

	"cherry/internal/metrics"
	"cherry/internal/opdata"
	"cherry/internal/store"
)

//go:embed static/staff.css
var staffCSS []byte

// link is one entry of the navigation. Part 2 adds players, bans, reports and season tools here.
type link struct{ Href, Label string }

var navLinks = []link{{"/", "Dashboard"}, {"/docs", "Documents"}, {"/audit", "Audit"}}

// check is a validation result shown on a version page.
type check struct {
	Valid      bool
	ArtSkipped bool
	Message    string
}

// view is what the templates read. A page fills the fields it shows; the rest stay empty.
type view struct {
	Title, Server, Who, CSRF  string
	Nav                       bool
	Links                     []link
	Text, Notice, Login, Next string

	// dashboard
	Accounts    int
	Connections int64
	Saves       int64

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
	Rows  []store.AuditRow
	Older int64
}

func serveCSS(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/css; charset=utf-8")
	_, _ = w.Write(staffCSS)
}

// base is the view of a signed-in page: the navigation, who is signed in and the CSRF token of the session.
func (s *Server) base(sess session, title string) view {
	who := sess.identity
	if sess.name != "" && sess.name != sess.identity {
		who = sess.name + " (" + sess.identity + ")"
	}
	return view{Title: title, Server: s.cfg.ServerName, Who: who, CSRF: sess.csrf, Nav: true, Links: navLinks}
}

// render writes one page. The page is built in full first, so a template error leaves no half page.
func (s *Server) render(w http.ResponseWriter, status int, name string, v view) {
	if v.Server == "" {
		v.Server = s.cfg.ServerName
	}
	var buf bytes.Buffer
	if err := pages.ExecuteTemplate(&buf, name, v); err != nil {
		s.logger.Printf("cherry: staff page %s: %v", name, err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(buf.Bytes())
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
	case errors.Is(err, store.ErrNotDocument), errors.Is(err, store.ErrNoVersion):
		s.fail(w, http.StatusNotFound, err.Error())
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

// handleDashboard: who is signed in and the counts of the running process.
func (s *Server) handleDashboard(w http.ResponseWriter, r *http.Request, sess session) {
	v := s.base(sess, "Dashboard")
	v.Accounts = store.AccountCount()
	v.Connections = metrics.SessionConns.Load() + metrics.GatewayConns.Load()
	v.Saves = metrics.Saves()
	s.render(w, http.StatusOK, "dashboard", v)
}

// handleDocsList: the Live table of every document.
func (s *Server) handleDocsList(w http.ResponseWriter, r *http.Request, sess session) {
	docs, err := store.Live()
	if err != nil {
		s.storeError(w, err)
		return
	}
	v := s.base(sess, "Documents")
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
	v := s.base(sess, name)
	v.DocName, v.Doc, v.Versions, v.Draft = name, live, versions, draft
	s.render(w, http.StatusOK, "doc", v)
}

// handleDraft: POST /docs/{name}/draft stores the body as the next draft. The audit row names the actor and the hash.
func (s *Server) handleDraft(w http.ResponseWriter, r *http.Request, sess session) {
	name := r.PathValue("name")
	dv, err := store.SaveDraft(name, []byte(r.PostFormValue("body")), sess.identity, r.PostFormValue("note"))
	if err != nil {
		s.storeError(w, err)
		return
	}
	redirect(w, http.StatusSeeOther, fmt.Sprintf("/docs/%s/%d", url.PathEscape(name), dv.Version))
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
	v := s.base(sess, fmt.Sprintf("%s v%d", name, ver))
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
	v := s.base(sess, fmt.Sprintf("%s v%d", name, ver))
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
	v := s.base(sess, "Published")
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
	v := s.base(sess, "Rolled back")
	if res.Version == 0 {
		v.Notice = fmt.Sprintf("%s has no published version now (v%d was rolled back). The config file or the embedded default applies at the next restart.", name, res.Replaces)
	} else {
		v.Notice = fmt.Sprintf("%s v%d is published again (v%d was rolled back). The change applies at the next restart.", name, res.Version, res.Replaces)
	}
	s.render(w, http.StatusOK, "result", v)
}

// handleAudit: the newest rows, 200 a page; ?before=<id> reads the page that follows.
func (s *Server) handleAudit(w http.ResponseWriter, r *http.Request, sess session) {
	var before int64
	if raw := r.URL.Query()["before"]; len(raw) > 0 {
		n, err := strconv.ParseInt(raw[0], 10, 64)
		if len(raw) != 1 || err != nil || n < 1 {
			s.fail(w, http.StatusBadRequest, "The page marker is not valid.")
			return
		}
		before = n
	}
	rows, err := store.AuditRows(before, auditPage)
	if err != nil {
		s.storeError(w, err)
		return
	}
	v := s.base(sess, "Audit")
	v.Rows = rows
	if len(rows) == auditPage {
		v.Older = rows[len(rows)-1].ID
	}
	s.render(w, http.StatusOK, "audit", v)
}

// pages holds the templates. Every page is "top", its body and "bottom": html/template escapes all data, and no template
// holds a script, a style attribute or template.HTML.
var pages = template.Must(template.New("staff").Parse(pagesHTML))

const pagesHTML = `
{{define "top"}}<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="robots" content="noindex, nofollow">
<title>{{.Title}} | Cherry staff</title>
<link rel="stylesheet" href="/static/staff.css">
</head>
<body>
<header class="bar">
<span class="brand">Cherry staff <span class="server">{{.Server}}</span></span>
{{if .Nav}}<nav>{{range .Links}}<a href="{{.Href}}">{{.Label}}</a>{{end}}</nav>
<span class="who">{{.Who}}</span>
<form method="post" action="/auth/logout" class="inline">
<input type="hidden" name="csrf" value="{{.CSRF}}">
<button type="submit">Sign out</button>
</form>{{end}}
</header>
<main>
{{end}}

{{define "bottom"}}</main>
</body>
</html>
{{end}}

{{define "error"}}{{template "top" .}}
<h1>{{.Title}}</h1>
<p>{{.Text}}</p>
{{if .Login}}<p><a class="button" href="{{.Login}}">Sign in with Discord</a></p>{{end}}
{{template "bottom" .}}{{end}}

{{define "signedin"}}<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="robots" content="noindex, nofollow">
<meta http-equiv="refresh" content="0;url={{.Next}}">
<title>Signed in | Cherry staff</title>
<link rel="stylesheet" href="/static/staff.css">
</head>
<body>
<main>
<h1>Signed in</h1>
<p><a href="{{.Next}}">Continue to the staff pages</a></p>
</main>
</body>
</html>
{{end}}

{{define "result"}}{{template "top" .}}
<h1>{{.Title}}</h1>
<p class="notice">{{.Notice}}</p>
<p><a href="/docs">Back to the documents</a> · <a href="/audit">Audit log</a></p>
{{template "bottom" .}}{{end}}

{{define "dashboard"}}{{template "top" .}}
<h1>Dashboard</h1>
<dl class="facts">
<dt>Signed in as</dt><dd>{{.Who}}</dd>
<dt>Server</dt><dd>{{.Server}}</dd>
<dt>Accounts in memory</dt><dd>{{.Accounts}}</dd>
<dt>Open connections</dt><dd>{{.Connections}}</dd>
<dt>Saves since start</dt><dd>{{.Saves}}</dd>
</dl>
<p class="muted">A published operator document applies at the next restart.</p>
{{template "bottom" .}}{{end}}

{{define "docs"}}{{template "top" .}}
<h1>Documents</h1>
<div class="scroll"><table>
<thead><tr><th>Name</th><th>Runs on</th><th>Published</th><th>Pending</th><th>Rejected at start</th><th>Versions</th></tr></thead>
<tbody>
{{range .Docs}}<tr>
<td><a href="/docs/{{.Name}}">{{.Name}}</a></td>
<td>{{.Source}}</td>
<td>{{if .Published}}v{{.Published}}{{else}}none{{end}}</td>
<td>{{if .Pending}}yes{{else}}no{{end}}</td>
<td>{{with .Rejected}}v{{.Version}}: {{.Error}}{{else}}no{{end}}</td>
<td>{{.Versions}}</td>
</tr>{{else}}<tr><td colspan="6">No documents.</td></tr>{{end}}
</tbody>
</table></div>
{{template "bottom" .}}{{end}}

{{define "doc"}}{{template "top" .}}
<h1>{{.DocName}}</h1>
<p>Runs on: {{.Doc.Source}}. Published: {{if .Doc.Published}}v{{.Doc.Published}}{{else}}none{{end}}.{{if .Doc.Pending}} A change applies at the next restart.{{end}}</p>
{{with .Doc.Rejected}}<p class="warn">Published v{{.Version}} was rejected at start: {{.Error}}</p>{{end}}
<h2>History</h2>
<div class="scroll"><table>
<thead><tr><th>Version</th><th>State</th><th>Author</th><th>Created</th><th>Published</th><th>Replaces</th><th>Note</th></tr></thead>
<tbody>
{{range .Versions}}<tr>
<td><a href="/docs/{{$.DocName}}/{{.Version}}">v{{.Version}}</a></td>
<td>{{.State}}</td>
<td>{{.Author}}</td>
<td>{{.Created}}</td>
<td>{{.Published}}</td>
<td>{{if .Replaces}}v{{.Replaces}}{{end}}</td>
<td>{{.Note}}</td>
</tr>{{else}}<tr><td colspan="7">No stored versions yet.</td></tr>{{end}}
</tbody>
</table></div>
<h2>New draft</h2>
<form method="post" action="/docs/{{.DocName}}/draft">
<input type="hidden" name="csrf" value="{{.CSRF}}">
<label>Note <input type="text" name="note" maxlength="500"></label>
<label>Body (JSON)
<textarea name="body" rows="30" spellcheck="false" required>{{.Draft}}</textarea></label>
<button type="submit">Save draft</button>
</form>
{{template "bottom" .}}{{end}}

{{define "version"}}{{template "top" .}}
<h1>{{.DocName}} v{{.Version.Version}}</h1>
{{with .Notice}}<p class="notice">{{.}}</p>{{end}}
<p>State: {{.Version.State}}. Author: {{.Version.Author}}. Created {{.Version.Created}}{{if .Version.Published}}, published {{.Version.Published}}{{end}}. SHA-256 {{.Version.SHA256}}.</p>
{{if .Version.Note}}<p>Note: {{.Version.Note}}</p>{{end}}
{{with .Check}}{{if .Valid}}<p class="ok">Valid.{{if .ArtSkipped}} The art checks were skipped: this process has no asset store.{{end}}</p>{{else}}<p class="warn">Not valid: {{.Message}}</p>{{end}}{{end}}
<h2>Body (read only)</h2>
<pre class="body">{{.Body}}</pre>
<div class="actions">
<form method="post" action="/docs/{{.DocName}}/{{.Version.Version}}/validate"><input type="hidden" name="csrf" value="{{.CSRF}}"><button type="submit">Validate</button></form>
<form method="post" action="/docs/{{.DocName}}/{{.Version.Version}}/publish"><input type="hidden" name="csrf" value="{{.CSRF}}"><button type="submit">Publish</button></form>
<form method="post" action="/docs/{{.DocName}}/rollback"><input type="hidden" name="csrf" value="{{.CSRF}}"><button type="submit">Roll back</button></form>
</div>
<p class="muted">Publish and roll back need a Discord sign-in in the last five minutes. A published version applies at the next restart.</p>
{{template "bottom" .}}{{end}}

{{define "audit"}}{{template "top" .}}
<h1>Audit log</h1>
<div class="scroll"><table>
<thead><tr><th>Time (UTC)</th><th>Actor</th><th>Action</th><th>Target</th><th>Detail</th></tr></thead>
<tbody>
{{range .Rows}}<tr><td>{{.Time}}</td><td>{{.Actor}}</td><td>{{.Action}}</td><td>{{.Target}}</td><td class="detail">{{.Detail}}</td></tr>
{{else}}<tr><td colspan="5">No rows yet.</td></tr>{{end}}
</tbody>
</table></div>
{{if .Older}}<p><a href="/audit?before={{.Older}}">Older rows</a></p>{{end}}
{{template "bottom" .}}{{end}}
`

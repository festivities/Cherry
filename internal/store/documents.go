package store

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	"cherry/internal/assets"
	"cherry/internal/opdata"
)

// Operator documents (PLAN Roadmap 8, P3; slice 8.5). An operator data file is a document: its versions live in the
// documents table. A draft is stored and checked for JSON and the name only. Publishing checks the full validator
// (refused when it fails, always) and makes one version the one a restart loads from the database; rollback re-publishes
// the version it replaced. Precedence at start (opdata): published database version, config file, embedded default.
// Publishing never changes the running process: the result says the change applies at the next restart.
// ponytail: hot swap per document (PLAN P3) hooks in at Publish: the document's opdata File would take the version too.

// MaxDocumentBytes caps one stored body. docKeep caps the versions kept per name: the newest docKeep plus the published
// version, which is never removed. maxDocNote caps the note.
const (
	MaxDocumentBytes = 2 << 20
	docKeep          = 50
	maxDocNote       = 500
	applyMessage     = "applies at the next restart"
)

// Errors of the document functions. The staff API maps each to a status code with errors.Is.
var (
	ErrNoDatabase       = errors.New("no database attached")
	ErrNotDocument      = errors.New("not a document")
	ErrNoVersion        = errors.New("no such version")
	ErrInvalidDocument  = errors.New("document is invalid")
	ErrDocumentState    = errors.New("document state does not allow this")
	ErrDocumentTooLarge = errors.New("document is too large")
	ErrBadInput         = errors.New("bad input")
)

// DocVersion is one stored version of a document, without its body. State is "draft", "published" or "retired";
// Replaces is the version this one replaced when it was published (0 = none).
type DocVersion struct {
	Name      string `json:"name"`
	Version   int    `json:"version"`
	State     string `json:"state"`
	SHA256    string `json:"sha256"`
	Author    string `json:"author"`
	Note      string `json:"note"`
	Created   string `json:"created"`
	Published string `json:"published"`
	Replaces  int    `json:"replaces"`
}

// DocResult is the reply of Publish and Rollback. Publish: Version is the version published and Replaces the one it
// replaced. Rollback: Version is the version now published (0 = none) and Replaces the version rolled back from.
// ArtSkipped says the art checks did not run (no asset store in this process).
type DocResult struct {
	Name       string `json:"name"`
	Version    int    `json:"version"`
	Replaces   int    `json:"replaces"`
	ArtSkipped bool   `json:"artSkipped"`
	Message    string `json:"message"`
}

// DocLive is one document's state: what it runs on (opdata.Live), its published version in the database, and whether a
// restart would change what runs (Pending).
type DocLive struct {
	opdata.Status
	Published int  `json:"published"`
	Pending   bool `json:"pending"`
	Latest    int  `json:"latest"`
	Versions  int  `json:"versions"`
}

const docCols = "name, version, state, sha256, author, note, created, published, replaces"

// scanDoc reads a row of docCols, plus the body when body is not nil.
func scanDoc(sc interface{ Scan(...any) error }, body *string) (DocVersion, error) {
	var v DocVersion
	dest := []any{&v.Name, &v.Version, &v.State, &v.SHA256, &v.Author, &v.Note, &v.Created, &v.Published, &v.Replaces}
	if body != nil {
		dest = append(dest, body)
	}
	return v, sc.Scan(dest...)
}

// docRow reads one version and its body.
func docRow(q Querier, name string, version int) (DocVersion, string, error) {
	var body string
	v, err := scanDoc(q.QueryRow("SELECT "+docCols+", body FROM documents WHERE name = ? AND version = ?", name, version), &body)
	if errors.Is(err, sql.ErrNoRows) {
		return DocVersion{}, "", fmt.Errorf("%w: %s v%d", ErrNoVersion, name, version)
	}
	return v, body, err
}

// publishedRow reads the published version of name and its body; ok is false when none is published.
func publishedRow(q Querier, name string) (v DocVersion, body string, ok bool, err error) {
	v, err = scanDoc(q.QueryRow("SELECT "+docCols+", body FROM documents WHERE name = ? AND state = 'published'", name), &body)
	if errors.Is(err, sql.ErrNoRows) {
		return DocVersion{}, "", false, nil
	}
	return v, body, err == nil, err
}

// auditTx appends one audit row. The detail is small JSON (versions and hashes), never a body or a secret.
func auditTx(tx *sql.Tx, actor, action, target string, detail map[string]any) error {
	b, err := json.Marshal(detail)
	if err != nil {
		return err
	}
	_, err = tx.Exec("INSERT INTO audit (time, actor, action, target, detail) VALUES (?, ?, ?, ?, ?)", dbNow(), actor, action, target, string(b))
	return err
}

func docDB() (*DB, error) {
	d := database.Load()
	if d == nil {
		return nil, ErrNoDatabase
	}
	return d, nil
}

func checkDocName(name string) error {
	if !opdata.Known(name) {
		return fmt.Errorf("%w: %s (configuration switch files are not stored)", ErrNotDocument, name)
	}
	return nil
}

// validateBody runs the document's validator on body: the checks a startup load runs. artSkipped reports that the
// document has art checks and this process has no asset store to run them against (they skip themselves).
func validateBody(name string, body []byte) (artSkipped bool, err error) {
	if err := opdata.Validate(name, body); err != nil {
		return false, fmt.Errorf("%w: %v", ErrInvalidDocument, err)
	}
	return opdata.HasArtChecks(name) && assets.Archive == nil, nil
}

// SaveDraft stores body as the next draft version of document name. The body must be JSON; the full validator runs when
// the version is published, so a draft may hold work in progress. The newest docKeep versions are kept and the published
// one never removed. The audit row records the version and the hash, never the body.
func SaveDraft(name string, body []byte, actor, note string) (DocVersion, error) {
	d, err := docDB()
	if err != nil {
		return DocVersion{}, err
	}
	if err := checkDocName(name); err != nil {
		return DocVersion{}, err
	}
	if len(body) > MaxDocumentBytes {
		return DocVersion{}, fmt.Errorf("%w: the cap is %d bytes", ErrDocumentTooLarge, MaxDocumentBytes)
	}
	if !json.Valid(body) {
		return DocVersion{}, fmt.Errorf("%w: the body is not JSON", ErrBadInput)
	}
	if len(note) > maxDocNote {
		return DocVersion{}, fmt.Errorf("%w: the note is longer than %d bytes", ErrBadInput, maxDocNote)
	}
	sum := sha256.Sum256(body)
	hash := hex.EncodeToString(sum[:])
	var v DocVersion
	err = d.Update(func(tx *sql.Tx) error {
		var next int
		if err := tx.QueryRow("SELECT COALESCE(MAX(version), 0) + 1 FROM documents WHERE name = ?", name).Scan(&next); err != nil {
			return err
		}
		if _, err := tx.Exec("INSERT INTO documents (name, version, state, body, sha256, author, note, created) VALUES (?, ?, 'draft', ?, ?, ?, ?, ?)",
			name, next, string(body), hash, actor, note, dbNow()); err != nil {
			return err
		}
		if _, err := tx.Exec(`DELETE FROM documents WHERE name = ? AND state <> 'published'
			AND version NOT IN (SELECT version FROM documents WHERE name = ? ORDER BY version DESC LIMIT ?)`, name, name, docKeep); err != nil {
			return err
		}
		if err := auditTx(tx, actor, "draft", name, map[string]any{"version": next, "sha256": hash}); err != nil {
			return err
		}
		row, _, err := docRow(tx, name, next)
		v = row
		return err
	})
	return v, err
}

// Validate runs the document's validator on a stored version and changes nothing.
func Validate(name string, version int) (artSkipped bool, err error) {
	d, err := docDB()
	if err != nil {
		return false, err
	}
	if err := checkDocName(name); err != nil {
		return false, err
	}
	var body string
	if err := d.View(func(q Querier) error {
		var err error
		_, body, err = docRow(q, name, version)
		return err
	}); err != nil {
		return false, err
	}
	return validateBody(name, []byte(body))
}

// Publish makes version the published one: the version a restart loads from the database. A version that fails the
// validator is refused. In one transaction the previous published version is retired, this one is published (it
// remembers the version it replaced, which Rollback returns to) and the audit row is written. Nothing running changes.
func Publish(name string, version int, actor string) (DocResult, error) {
	d, err := docDB()
	if err != nil {
		return DocResult{}, err
	}
	if err := checkDocName(name); err != nil {
		return DocResult{}, err
	}
	var body string
	if err := d.View(func(q Querier) error {
		var err error
		_, body, err = docRow(q, name, version)
		return err
	}); err != nil {
		return DocResult{}, err
	}
	artSkipped, err := validateBody(name, []byte(body))
	if err != nil {
		return DocResult{}, err
	}
	var res DocResult
	err = d.Update(func(tx *sql.Tx) error {
		cur, _, ok, err := publishedRow(tx, name)
		if err != nil {
			return err
		}
		if ok && cur.Version == version {
			return fmt.Errorf("%w: %s v%d is already published", ErrDocumentState, name, version)
		}
		replaces := 0
		if ok {
			replaces = cur.Version
			if _, err := tx.Exec("UPDATE documents SET state = 'retired' WHERE name = ? AND version = ?", name, replaces); err != nil {
				return err
			}
		}
		if _, err := tx.Exec("UPDATE documents SET state = 'published', published = ?, replaces = ? WHERE name = ? AND version = ?",
			dbNow(), replaces, name, version); err != nil {
			return err
		}
		res = DocResult{Name: name, Version: version, Replaces: replaces, ArtSkipped: artSkipped, Message: applyMessage}
		return auditTx(tx, actor, "publish", name, map[string]any{"version": version, "replaces": replaces, "artSkipped": artSkipped})
	})
	return res, err
}

// Rollback re-publishes the version the published one replaced, or un-publishes the document when it replaced none (the
// config file or the embedded default applies at the next start). The version to return to is validated first and
// refused if it fails now. One transaction retires the current version, publishes the target and writes the audit row.
func Rollback(name, actor string) (DocResult, error) {
	d, err := docDB()
	if err != nil {
		return DocResult{}, err
	}
	if err := checkDocName(name); err != nil {
		return DocResult{}, err
	}
	var cur DocVersion
	var target string
	if err := d.View(func(q Querier) error {
		c, _, ok, err := publishedRow(q, name)
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("%w: %s has no published version", ErrDocumentState, name)
		}
		cur = c
		if c.Replaces == 0 {
			return nil
		}
		_, target, err = docRow(q, name, c.Replaces)
		if errors.Is(err, ErrNoVersion) {
			return fmt.Errorf("%w: v%d, which v%d replaced, is no longer kept", ErrDocumentState, c.Replaces, c.Version)
		}
		return err
	}); err != nil {
		return DocResult{}, err
	}
	var artSkipped bool
	if cur.Replaces != 0 {
		if artSkipped, err = validateBody(name, []byte(target)); err != nil {
			return DocResult{}, err
		}
	}
	var res DocResult
	err = d.Update(func(tx *sql.Tx) error {
		now, _, ok, err := publishedRow(tx, name)
		if err != nil {
			return err
		}
		if !ok || now.Version != cur.Version {
			return fmt.Errorf("%w: %s changed while rolling back, try again", ErrDocumentState, name)
		}
		if _, err := tx.Exec("UPDATE documents SET state = 'retired' WHERE name = ? AND version = ?", name, cur.Version); err != nil {
			return err
		}
		if cur.Replaces != 0 {
			if _, err := tx.Exec("UPDATE documents SET state = 'published', published = ? WHERE name = ? AND version = ?",
				dbNow(), name, cur.Replaces); err != nil {
				return err
			}
		}
		res = DocResult{Name: name, Version: cur.Replaces, Replaces: cur.Version, ArtSkipped: artSkipped, Message: applyMessage}
		return auditTx(tx, actor, "rollback", name, map[string]any{"from": cur.Version, "to": cur.Replaces, "artSkipped": artSkipped})
	})
	return res, err
}

// History lists the stored versions of document name, newest first, without bodies.
func History(name string) ([]DocVersion, error) {
	d, err := docDB()
	if err != nil {
		return nil, err
	}
	if err := checkDocName(name); err != nil {
		return nil, err
	}
	out := []DocVersion{} // an empty history is [] in the JSON, not null
	err = d.View(func(q Querier) error {
		rows, err := q.Query("SELECT "+docCols+" FROM documents WHERE name = ? ORDER BY version DESC", name)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			v, err := scanDoc(rows, nil)
			if err != nil {
				return err
			}
			out = append(out, v)
		}
		return rows.Err()
	})
	return out, err
}

// Get returns one stored version of document name and its body (the JSON as submitted).
func Get(name string, version int) (DocVersion, []byte, error) {
	d, err := docDB()
	if err != nil {
		return DocVersion{}, nil, err
	}
	if err := checkDocName(name); err != nil {
		return DocVersion{}, nil, err
	}
	var v DocVersion
	var body string
	err = d.View(func(q Querier) error {
		var err error
		v, body, err = docRow(q, name, version)
		return err
	})
	return v, []byte(body), err
}

// Live reports every document: what it runs on now, the database's published version and whether a restart would change
// what runs. Documents are listed in name order, then rejected names the build does not know.
func Live() ([]DocLive, error) {
	type tally struct{ published, latest, versions int }
	tallies := map[string]*tally{}
	if d := database.Load(); d != nil {
		err := d.View(func(q Querier) error {
			rows, err := q.Query("SELECT name, version, state FROM documents")
			if err != nil {
				return err
			}
			defer rows.Close()
			for rows.Next() {
				var name, state string
				var version int
				if err := rows.Scan(&name, &version, &state); err != nil {
					return err
				}
				t := tallies[name]
				if t == nil {
					t = &tally{}
					tallies[name] = t
				}
				t.versions++
				t.latest = max(t.latest, version)
				if state == "published" {
					t.published = version
				}
			}
			return rows.Err()
		})
		if err != nil {
			return nil, err
		}
	}
	var out []DocLive
	for _, st := range opdata.Live() {
		l := DocLive{Status: st}
		if t := tallies[st.Name]; t != nil {
			l.Published, l.Latest, l.Versions = t.published, t.latest, t.versions
		}
		// A rejected published version stays rejected until its document is published again: a restart cannot load it.
		rejectedNow := st.Rejected != nil && st.Rejected.Version == l.Published
		l.Pending = !rejectedNow && l.Published != st.Version
		out = append(out, l)
	}
	return out, nil
}

// OfferPublishedDocuments hands every published version of the attached database to opdata. The startup load then uses
// it, per document, before the config file and the embedded default (opdata.File.Load). Without a database nothing is
// offered and every document loads as before.
func OfferPublishedDocuments() error {
	d := database.Load()
	if d == nil {
		return nil
	}
	type published struct {
		name    string
		version int
		body    string
	}
	var rows []published
	err := d.View(func(q Querier) error {
		rs, err := q.Query("SELECT name, version, body FROM documents WHERE state = 'published' ORDER BY name")
		if err != nil {
			return err
		}
		defer rs.Close()
		for rs.Next() {
			var p published
			if err := rs.Scan(&p.name, &p.version, &p.body); err != nil {
				return err
			}
			rows = append(rows, p)
		}
		return rs.Err()
	})
	if err != nil {
		return err
	}
	for _, p := range rows {
		opdata.Offer(p.name, p.version, []byte(p.body))
	}
	return nil
}

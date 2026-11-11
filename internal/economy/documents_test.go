package economy

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"cherry/data"
	"cherry/internal/assets"
	"cherry/internal/opdata"
	"cherry/internal/store"

	_ "modernc.org/sqlite" // the raw connection that corrupts one stored version (a database changed outside Cherry)
)

// docStore points the operator folder at a temp dir and opens a fresh database as the record of the store. It returns
// the config folder and the database file; the cleanup closes the store and drops the document state.
func docStore(t *testing.T) (configDir, dbPath string) {
	t.Helper()
	configDir = operatorDir(t)
	dbPath = filepath.Join(filepath.Dir(configDir), store.DBFileName)
	if err := store.OpenStore(dbPath); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		store.CloseStore()
		opdata.ResetOffers()
		cherryTipsFile.Reset()
		calendarsFile.Reset()
	})
	return configDir, dbPath
}

// loadRestart is a restart: the published versions are offered again and every operator file loads, in the order
// cmd/cherry uses.
func loadRestart() error {
	opdata.ResetOffers()
	cherryTipsFile.Reset()
	calendarsFile.Reset()
	if err := store.OfferPublishedDocuments(); err != nil {
		return err
	}
	return LoadOperatorData()
}

func restart(t *testing.T) {
	t.Helper()
	if err := loadRestart(); err != nil {
		t.Fatal(err)
	}
}

func liveDoc(t *testing.T, name string) store.DocLive {
	t.Helper()
	docs, err := store.Live()
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range docs {
		if d.Name == name {
			return d
		}
	}
	t.Fatalf("no live row for %s", name)
	return store.DocLive{}
}

func tipTitle() string { return ActiveCherryTips().Tips[0].Title }

// TestDocumentPrecedence: a published database version wins over the config file, which wins over the embedded default;
// publishing never changes the running process; rollback to nothing published hands the document back to the config file.
func TestDocumentPrecedence(t *testing.T) {
	configDir, _ := docStore(t)
	const embedded = "Daily quests"
	restart(t)
	if got := tipTitle(); got != embedded {
		t.Fatalf("no document and no config file: title %q", got)
	}
	if d := liveDoc(t, "cherrytips.json"); d.Source != "embedded default" || d.Published != 0 || d.Pending {
		t.Fatalf("live before any publish = %+v", d)
	}

	draft := []byte(mutateDefault(t, data.CherryTipsJSON, `"title": "Daily quests"`, `"title": "Database tip"`))
	v, err := store.SaveDraft("cherrytips.json", draft, "tester", "precedence")
	if err != nil {
		t.Fatal(err)
	}
	if got := tipTitle(); got != embedded {
		t.Fatalf("a draft changed the value in use: %q", got)
	}
	if artSkipped, err := store.Validate("cherrytips.json", v.Version); err != nil || artSkipped {
		t.Fatalf("validate = %v, %v", artSkipped, err)
	}
	res, err := store.Publish("cherrytips.json", v.Version, "loopback")
	if err != nil || res.Message != "applies at the next restart" || res.ArtSkipped {
		t.Fatalf("publish = %+v, %v", res, err)
	}
	if got := tipTitle(); got != embedded {
		t.Fatalf("publish changed the running process: %q", got)
	}
	if d := liveDoc(t, "cherrytips.json"); d.Published != 1 || !d.Pending {
		t.Fatalf("live after publish = %+v, want published 1 and pending", d)
	}

	restart(t)
	if got := tipTitle(); got != "Database tip" {
		t.Fatalf("after restart the published version is not live: %q", got)
	}
	if d := liveDoc(t, "cherrytips.json"); d.Source != "database v1" || d.Version != 1 || d.Pending || d.Rejected != nil {
		t.Fatalf("live after restart = %+v, want database v1", d)
	}

	cfg := filepath.Join(configDir, "cherrytips.json")
	if err := os.WriteFile(cfg, []byte(mutateDefault(t, data.CherryTipsJSON, `"title": "Daily quests"`, `"title": "Config tip"`)), 0o600); err != nil {
		t.Fatal(err)
	}
	restart(t)
	if got := tipTitle(); got != "Database tip" {
		t.Fatalf("the config file beat the published version: %q", got)
	}

	if back, err := store.Rollback("cherrytips.json", "loopback"); err != nil || back.Version != 0 {
		t.Fatalf("rollback to nothing = %+v, %v", back, err)
	}
	if d := liveDoc(t, "cherrytips.json"); !d.Pending {
		t.Fatalf("un-publish is not pending: %+v", d)
	}
	restart(t)
	if got := tipTitle(); got != "Config tip" {
		t.Fatalf("with nothing published the config file should apply: %q", got)
	}
	if d := liveDoc(t, "cherrytips.json"); d.Source != "config file" {
		t.Fatalf("live = %+v, want the config file", d)
	}

	if err := os.Remove(cfg); err != nil {
		t.Fatal(err)
	}
	restart(t)
	if got := tipTitle(); got != embedded {
		t.Fatalf("no config file again: %q", got)
	}
}

// TestRejectedDatabaseVersionFallsBackPerDocument: a published version that fails its checks at start falls back to the
// config file, then the embedded default, for that document only. A config file that fails its checks still stops the
// start.
func TestRejectedDatabaseVersionFallsBackPerDocument(t *testing.T) {
	configDir, dbPath := docStore(t)
	restart(t)
	if _, err := store.SaveDraft("cherrytips.json", []byte(mutateDefault(t, data.CherryTipsJSON, `"title": "Daily quests"`, `"title": "Database tip"`)), "tester", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Publish("cherrytips.json", 1, "loopback"); err != nil {
		t.Fatal(err)
	}

	// The stored version stops passing its checks (for example after an upgrade added a required field).
	raw, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`UPDATE documents SET body = '{"origin":"cherry","note":"x","tips":[]}' WHERE name = 'cherrytips.json' AND version = 1`); err != nil {
		t.Fatal(err)
	}
	raw.Close()

	restart(t)
	if got := tipTitle(); got != "Daily quests" {
		t.Fatalf("a rejected version is live: %q", got)
	}
	d := liveDoc(t, "cherrytips.json")
	if d.Source != "embedded default" || d.Rejected == nil || d.Rejected.Version != 1 || d.Pending || !strings.Contains(d.Rejected.Error, "database v1") {
		t.Fatalf("live after a rejected version = %+v", d)
	}

	cfg := filepath.Join(configDir, "cherrytips.json")
	if err := os.WriteFile(cfg, []byte(mutateDefault(t, data.CherryTipsJSON, `"title": "Daily quests"`, `"title": "Config tip"`)), 0o600); err != nil {
		t.Fatal(err)
	}
	restart(t)
	if got := tipTitle(); got != "Config tip" {
		t.Fatalf("the fallback skipped the config file: %q", got)
	}
	if d := liveDoc(t, "cherrytips.json"); d.Source != "config file" || d.Rejected == nil {
		t.Fatalf("live = %+v, want the config file with the rejection still listed", d)
	}

	// A config file is not soft: an operator who edits it sees the error at once.
	if err := os.WriteFile(cfg, []byte(mutateDefault(t, data.CherryTipsJSON, `"origin": "cherry"`, `"origin": "mixed"`)), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := loadRestart(); err == nil || !strings.Contains(err.Error(), cfg) {
		t.Fatalf("an invalid config file must refuse the start and name the file: %v", err)
	}
}

// TestDocumentArtChecksReportSkip: a document with art checks reports when they were skipped (no asset store in this
// process), and the publish still goes through.
func TestDocumentArtChecksReportSkip(t *testing.T) {
	docStore(t)
	restart(t)
	v, err := store.SaveDraft("calendars.json", data.CalendarsJSON, "tester", "")
	if err != nil {
		t.Fatal(err)
	}
	res, err := store.Publish("calendars.json", v.Version, "loopback")
	if err != nil {
		t.Fatalf("publish of a valid calendar: %v", err)
	}
	if res.ArtSkipped != (assets.Archive == nil) {
		t.Fatalf("artSkipped = %v with the asset store nil = %v", res.ArtSkipped, assets.Archive == nil)
	}
}

// TestDocumentAdminAPI: the staff routes are loopback only and reply with the documents' states and the validator's
// messages; a publish shows as pending until the restart.
func TestDocumentAdminAPI(t *testing.T) {
	docStore(t)
	restart(t)
	mux := NewAdminMux()
	do := func(method, target, body, remote string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, target, strings.NewReader(body))
		req.RemoteAddr = remote
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec
	}
	const lan, loop = "192.0.2.7:4000", "127.0.0.1:4000"
	good := mutateDefault(t, data.CherryTipsJSON, `"title": "Daily quests"`, `"title": "API tip"`)
	bad := mutateDefault(t, data.CherryTipsJSON, `"origin": "cherry"`, `"origin": "mixed"`)

	for _, c := range []struct{ method, target, body string }{
		{"GET", "/admin/docs", ""},
		{"GET", "/admin/docs/cherrytips.json", ""},
		{"POST", "/admin/docs/cherrytips.json", good},
		{"POST", "/admin/docs/cherrytips.json/1/publish", ""},
	} {
		if rec := do(c.method, c.target, c.body, lan); rec.Code != 404 {
			t.Fatalf("%s %s from a LAN address: %d, want 404", c.method, c.target, rec.Code)
		}
	}
	if versions, err := store.History("cherrytips.json"); err != nil || len(versions) != 0 {
		t.Fatalf("a LAN request changed the documents: %v %v", versions, err)
	}

	rec := do("POST", "/admin/docs/cherrytips.json?note=api", good, loop)
	var draft store.DocVersion
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &draft) != nil || draft.Version != 1 || draft.State != "draft" || draft.Author != "loopback" || draft.Note != "api" {
		t.Fatalf("draft = %d %s", rec.Code, rec.Body.String())
	}
	if rec := do("POST", "/admin/docs/cherrytips.json/1/validate", "", loop); rec.Code != 200 || !strings.Contains(rec.Body.String(), `"valid":true`) {
		t.Fatalf("validate = %d %s", rec.Code, rec.Body.String())
	}
	if rec := do("POST", "/admin/docs/cherrytips.json", bad, loop); rec.Code != 200 {
		t.Fatalf("second draft = %d %s", rec.Code, rec.Body.String())
	}
	if rec := do("POST", "/admin/docs/cherrytips.json/2/validate", "", loop); rec.Code != 422 || !strings.Contains(rec.Body.String(), `origin must be`) {
		t.Fatalf("validate of an invalid draft = %d %s", rec.Code, rec.Body.String())
	}
	if rec := do("POST", "/admin/docs/cherrytips.json/2/publish", "", loop); rec.Code != 422 {
		t.Fatalf("publish of an invalid draft = %d %s", rec.Code, rec.Body.String())
	}
	if rec := do("POST", "/admin/docs/cherrytips.json/1/publish", "", loop); rec.Code != 200 || !strings.Contains(rec.Body.String(), "applies at the next restart") {
		t.Fatalf("publish = %d %s", rec.Code, rec.Body.String())
	}

	rec = do("GET", "/admin/docs", "", loop)
	var list struct {
		Documents []store.DocLive `json:"documents"`
	}
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &list) != nil {
		t.Fatalf("list = %d %s", rec.Code, rec.Body.String())
	}
	found := false
	for _, d := range list.Documents {
		if d.Name == "cherrytips.json" {
			found = d.Published == 1 && d.Pending && d.Source == "embedded default"
		}
	}
	if !found {
		t.Fatalf("cherrytips.json is not pending in the list: %s", rec.Body.String())
	}
	if rec := do("GET", "/admin/docs/cherrytips.json", "", loop); rec.Code != 200 || strings.Count(rec.Body.String(), `"version"`) != 2 {
		t.Fatalf("history = %d %s", rec.Code, rec.Body.String())
	}
	rec = do("GET", "/admin/docs/cherrytips.json/1", "", loop)
	var got struct {
		Body json.RawMessage `json:"body"`
	}
	var wantCompact, gotCompact bytes.Buffer
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &got) != nil ||
		json.Compact(&wantCompact, []byte(good)) != nil || json.Compact(&gotCompact, got.Body) != nil || wantCompact.String() != gotCompact.String() {
		t.Fatalf("body of v1 = %d %s", rec.Code, rec.Body.String())
	}
	if rec := do("GET", "/admin/docs/cherrytips.json/x", "", loop); rec.Code != 400 {
		t.Fatalf("a bad version = %d", rec.Code)
	}
	if rec := do("POST", "/admin/docs/streaming.json", `{"enabled":true}`, loop); rec.Code != 404 {
		t.Fatalf("a switch file = %d %s", rec.Code, rec.Body.String())
	}
	if rec := do("POST", "/admin/docs/cherrytips.json/rollback", "", loop); rec.Code != 200 || !strings.Contains(rec.Body.String(), `"version":0`) {
		t.Fatalf("rollback = %d %s", rec.Code, rec.Body.String())
	}
	restart(t)
	if tipTitle() != "Daily quests" {
		t.Fatalf("after the rollback and a restart the embedded default should apply: %q", tipTitle())
	}
}

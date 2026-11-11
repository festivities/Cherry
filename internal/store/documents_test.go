package store

import (
	"bytes"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"cherry/data"
	"cherry/internal/opdata"
)

// docStore points the config folder at a temp dir, opens a fresh database as the record of the store and resets the
// document state when the test ends. profiles.json is the only document this package declares, so it stands in for the
// others here (the economy tests cover the real files).
func docStore(t *testing.T) {
	t.Helper()
	base := t.TempDir()
	t.Setenv("LOCALAPPDATA", base)
	t.Setenv("XDG_CACHE_HOME", base)
	if err := OpenStore(filepath.Join(base, DBFileName)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		CloseStore()
		opdata.ResetOffers()
		profilesFile.Reset()
	})
}

// profilesWith returns the embedded profiles.json with one old text replaced by new (the old text must be there).
func profilesWith(t *testing.T, old, new string) []byte {
	t.Helper()
	if !bytes.Contains(data.ProfilesJSON, []byte(old)) {
		t.Fatalf("profiles.json has no %q", old)
	}
	return bytes.Replace(data.ProfilesJSON, []byte(old), []byte(new), 1)
}

// restartLoad is a restart: the offers are read again and every document loads (the startup order of cmd/cherry).
func restartLoad(t *testing.T) {
	t.Helper()
	opdata.ResetOffers()
	profilesFile.Reset()
	if err := OfferPublishedDocuments(); err != nil {
		t.Fatal(err)
	}
	if err := LoadOperatorData(); err != nil {
		t.Fatal(err)
	}
}

func liveOf(t *testing.T, name string) DocLive {
	t.Helper()
	docs, err := Live()
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range docs {
		if d.Name == name {
			return d
		}
	}
	t.Fatalf("no live row for %s", name)
	return DocLive{}
}

// TestMigrationV2ToV3KeepsRows: a database at schema v2 with rows migrates to v3 with every row in place, and the
// documents and audit tables exist.
func TestMigrationV2ToV3KeepsRows(t *testing.T) {
	path := filepath.Join(t.TempDir(), DBFileName)
	raw, err := sql.Open("sqlite", path+"?"+dbPragmas)
	if err != nil {
		t.Fatal(err)
	}
	if err := migrateTo(raw, 2); err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec("INSERT INTO meta (key, value) VALUES ('probe', 'kept')"); err != nil {
		t.Fatal(err)
	}
	raw.Close()

	d, err := OpenDB(path)
	if err != nil {
		t.Fatalf("migrate v2 to v3: %v", err)
	}
	defer d.Close()
	var version int
	var kept string
	if err := d.write.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != 3 {
		t.Fatalf("user_version = %d (err %v), want 3", version, err)
	}
	if err := d.write.QueryRow("SELECT value FROM meta WHERE key = 'probe'").Scan(&kept); err != nil || kept != "kept" {
		t.Fatalf("v2 row after migration = %q (err %v)", kept, err)
	}
	if _, err := d.write.Exec("SELECT COUNT(*) FROM documents"); err != nil {
		t.Fatalf("documents table: %v", err)
	}
	if _, err := d.write.Exec("SELECT COUNT(*) FROM audit"); err != nil {
		t.Fatalf("audit table: %v", err)
	}
}

// TestDocumentLifecycle: drafts are stored and checked for JSON and the name only; invalid versions never publish; one
// version is published per name; publish and rollback write audit rows; rollback returns to the version replaced and
// finally un-publishes. Nothing running changes until a restart.
func TestDocumentLifecycle(t *testing.T) {
	docStore(t)
	if err := LoadOperatorData(); err != nil {
		t.Fatal(err)
	}
	if h, err := History("profiles.json"); err != nil || h == nil || len(h) != 0 {
		t.Fatalf("an empty history = %#v, %v; want an empty, non-nil list", h, err)
	}
	live := ActiveProfiles().FriendName

	good1 := profilesWith(t, `"friend_name": "Friend"`, `"friend_name": "Pal"`)
	good2 := profilesWith(t, `"friend_name": "Friend"`, `"friend_name": "Kin"`)
	bad := profilesWith(t, `"origin": "mixed"`, `"origin": "cherry"`)

	if _, err := SaveDraft("streaming.json", []byte(`{"enabled":true}`), "x", ""); !errors.Is(err, ErrNotDocument) {
		t.Fatalf("a switch file was stored: %v", err)
	}
	if _, err := SaveDraft("profiles.json", []byte("not json"), "x", ""); !errors.Is(err, ErrBadInput) {
		t.Fatalf("a non-JSON draft: %v", err)
	}
	v1, err := SaveDraft("profiles.json", good1, "tester", "first")
	if err != nil || v1.Version != 1 || v1.State != "draft" || v1.Author != "tester" || v1.Note != "first" {
		t.Fatalf("draft 1 = %+v, %v", v1, err)
	}
	if _, body, err := Get("profiles.json", 1); err != nil || !bytes.Equal(body, good1) {
		t.Fatalf("Get returned a different body: %v", err)
	}
	if artSkipped, err := Validate("profiles.json", 1); err != nil || artSkipped {
		t.Fatalf("validate v1 = %v, %v", artSkipped, err)
	}
	if ActiveProfiles().FriendName != live {
		t.Fatal("a draft changed the value in use")
	}

	res, err := Publish("profiles.json", 1, "loopback")
	if err != nil || res.Version != 1 || res.Replaces != 0 || res.Message != "applies at the next restart" {
		t.Fatalf("publish v1 = %+v, %v", res, err)
	}
	if ActiveProfiles().FriendName != live {
		t.Fatal("publish changed the running process")
	}
	if d := liveOf(t, "profiles.json"); d.Published != 1 || !d.Pending || d.Source != "embedded default" {
		t.Fatalf("live after publish = %+v, want published 1 and pending", d)
	}

	if _, err := SaveDraft("profiles.json", good2, "tester", ""); err != nil {
		t.Fatal(err)
	}
	if res, err := Publish("profiles.json", 2, "loopback"); err != nil || res.Replaces != 1 {
		t.Fatalf("publish v2 = %+v, %v", res, err)
	}
	if _, err := Publish("profiles.json", 2, "loopback"); !errors.Is(err, ErrDocumentState) {
		t.Fatalf("publishing the published version again: %v", err)
	}

	if _, err := SaveDraft("profiles.json", bad, "tester", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := Validate("profiles.json", 3); !errors.Is(err, ErrInvalidDocument) {
		t.Fatalf("validate of an invalid draft: %v", err)
	}
	if _, err := Publish("profiles.json", 3, "loopback"); !errors.Is(err, ErrInvalidDocument) {
		t.Fatalf("an invalid version was published: %v", err)
	}
	if d := liveOf(t, "profiles.json"); d.Published != 2 {
		t.Fatalf("a refused publish changed the published version: %d", d.Published)
	}

	history, err := History("profiles.json")
	if err != nil || len(history) != 3 || history[0].Version != 3 || history[1].State != "published" || history[2].State != "retired" {
		t.Fatalf("history = %+v, %v", history, err)
	}

	// One published row per name, enforced by the database itself.
	d, _ := docDB()
	if _, err := d.write.Exec("UPDATE documents SET state = 'published' WHERE version = 1"); err == nil {
		t.Fatal("two published rows for one name were accepted")
	}

	back, err := Rollback("profiles.json", "loopback")
	if err != nil || back.Version != 1 || back.Replaces != 2 {
		t.Fatalf("rollback = %+v, %v", back, err)
	}
	if back, err := Rollback("profiles.json", "loopback"); err != nil || back.Version != 0 || back.Replaces != 1 {
		t.Fatalf("rollback to none = %+v, %v", back, err)
	}
	if _, err := Rollback("profiles.json", "loopback"); !errors.Is(err, ErrDocumentState) {
		t.Fatalf("rollback with nothing published: %v", err)
	}
	if d := liveOf(t, "profiles.json"); d.Published != 0 || d.Pending {
		t.Fatalf("live after un-publish = %+v, want nothing published and nothing pending", d)
	}

	// Refused calls write no audit row; each accepted one writes exactly one, with ids and hashes in the detail only.
	var audited []string
	rows, err := d.write.Query("SELECT action, detail FROM audit ORDER BY id")
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var action, detail string
		if err := rows.Scan(&action, &detail); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(detail, "friend_name") || strings.Contains(detail, "Kin") {
			t.Fatalf("audit detail carries document content: %s", detail)
		}
		audited = append(audited, action)
	}
	rows.Close()
	if got := strings.Join(audited, ","); got != "draft,publish,draft,publish,draft,rollback,rollback" {
		t.Fatalf("audit actions = %s", got)
	}
}

// TestDocumentsNeedADatabase: without an attached database nothing is stored or published, and the status still
// reads (every document shows its source, nothing published).
func TestDocumentsNeedADatabase(t *testing.T) {
	docStore(t)
	CloseStore()
	if _, err := SaveDraft("profiles.json", []byte("{}"), "x", ""); !errors.Is(err, ErrNoDatabase) {
		t.Fatalf("a draft with no database: %v", err)
	}
	if _, err := Publish("profiles.json", 1, "x"); !errors.Is(err, ErrNoDatabase) {
		t.Fatalf("a publish with no database: %v", err)
	}
	docs, err := Live()
	if err != nil || len(docs) == 0 {
		t.Fatalf("live without a database = %v, %v", docs, err)
	}
	for _, d := range docs {
		if d.Published != 0 || d.Pending {
			t.Fatalf("live without a database = %+v", d)
		}
	}
}

// TestDocumentVersionCap: at most docKeep newest versions are kept, plus the published one, which is never removed.
func TestDocumentVersionCap(t *testing.T) {
	docStore(t)
	if err := LoadOperatorData(); err != nil {
		t.Fatal(err)
	}
	if _, err := SaveDraft("profiles.json", profilesWith(t, `"friend_name": "Friend"`, `"friend_name": "Pal"`), "x", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := Publish("profiles.json", 1, "loopback"); err != nil {
		t.Fatal(err)
	}
	for i := 2; i <= docKeep+11; i++ {
		body := profilesWith(t, `"friend_name": "Friend"`, fmt.Sprintf(`"friend_name": "Pal %d"`, i))
		if _, err := SaveDraft("profiles.json", body, "x", ""); err != nil {
			t.Fatal(err)
		}
	}
	history, err := History("profiles.json")
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != docKeep+1 {
		t.Fatalf("%d versions kept, want %d (the newest %d and the published v1)", len(history), docKeep+1, docKeep)
	}
	if got := history[len(history)-1]; got.Version != 1 || got.State != "published" {
		t.Fatalf("oldest kept = %+v, want the published v1", got)
	}
	if _, _, err := Get("profiles.json", 2); !errors.Is(err, ErrNoVersion) {
		t.Fatalf("v2 should be gone: %v", err)
	}
	if _, _, err := Get("profiles.json", docKeep+11); err != nil {
		t.Fatalf("the newest version is gone: %v", err)
	}
}

// TestPublishedVersionFallsBackAtStart: a published version that fails its checks after an upgrade does not stop the
// load. The embedded default is used for that one document, and the rejection is listed. A valid published version is
// used after a restart, and nothing published means the default again.
func TestPublishedVersionFallsBackAtStart(t *testing.T) {
	docStore(t)
	if err := LoadOperatorData(); err != nil {
		t.Fatal(err)
	}
	if _, err := SaveDraft("profiles.json", profilesWith(t, `"friend_name": "Friend"`, `"friend_name": "Pal"`), "x", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := Publish("profiles.json", 1, "loopback"); err != nil {
		t.Fatal(err)
	}
	restartLoad(t)
	if got := ActiveProfiles().FriendName; got != "Pal" {
		t.Fatalf("after restart the database version is not live: %q", got)
	}
	if d := liveOf(t, "profiles.json"); d.Source != "database v1" || d.Version != 1 || d.Pending || d.Rejected != nil {
		t.Fatalf("live = %+v, want database v1 in use", d)
	}

	// Simulate an upgrade that makes the stored version invalid (the database does not re-check it).
	d, _ := docDB()
	if _, err := d.write.Exec(`UPDATE documents SET body = '{"origin":"mixed","note":"x"}' WHERE name = 'profiles.json' AND version = 1`); err != nil {
		t.Fatal(err)
	}
	restartLoad(t)
	if got := ActiveProfiles().FriendName; got != "Friend" {
		t.Fatalf("a rejected version is live: %q, want the embedded default", got)
	}
	dl := liveOf(t, "profiles.json")
	if dl.Source != "embedded default" || dl.Rejected == nil || dl.Rejected.Version != 1 || dl.Pending {
		t.Fatalf("live after a rejected version = %+v", dl)
	}
	if !strings.Contains(dl.Rejected.Error, "database v1") {
		t.Fatalf("rejection does not name the version: %q", dl.Rejected.Error)
	}

	// Publishing a fixed version clears the rejection at the next restart.
	if _, err := SaveDraft("profiles.json", profilesWith(t, `"friend_name": "Friend"`, `"friend_name": "Kin"`), "x", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := Publish("profiles.json", 2, "loopback"); err != nil {
		t.Fatal(err)
	}
	if d := liveOf(t, "profiles.json"); d.Published != 2 || !d.Pending {
		t.Fatalf("live after publishing a fix = %+v, want pending", d)
	}
	restartLoad(t)
	if got := ActiveProfiles().FriendName; got != "Kin" {
		t.Fatalf("fixed version not live: %q", got)
	}
}

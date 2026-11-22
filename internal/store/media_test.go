package store

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"cherry/internal/config"
)

// mediaDataDir points the data directory at a temporary folder, so the file side of a removal stays out of the lab's data.
func mediaDataDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	config.Activate(config.Config{DataDir: dir, CacheDir: dir})
	t.Cleanup(func() { config.Activate(config.Config{}) })
	return dir
}

// storeMediaFile writes a picture of the key tuple into kind's folder and records its row under owner, as an upload does.
func storeMediaFile(t *testing.T, kind, owner, userID, ctime, oid string) MediaFile {
	t.Helper()
	dir, err := MediaDir(kind)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	file := MediaFileName(userID, ctime, oid)
	data := []byte("picture " + file)
	if err := os.WriteFile(filepath.Join(dir, file), data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := AddMediaFile(MediaFile{Kind: kind, Owner: owner, File: file, Size: int64(len(data))}); err != nil {
		t.Fatal(err)
	}
	row, found, err := MediaRowByFile(file)
	if err != nil || !found {
		t.Fatalf("row of the new picture: found=%t err=%v", found, err)
	}
	return row
}

// onDisk reports whether the picture's file is in its folder.
func onDisk(t *testing.T, f MediaFile) bool {
	t.Helper()
	dir, err := MediaDir(f.Kind)
	if err != nil {
		t.Fatal(err)
	}
	_, err = os.Stat(filepath.Join(dir, f.File))
	return err == nil
}

// TestMediaRefFileForms: the picture a URL names is the same whatever its form (relative, absolute, any tid, the profile path
// with or without the root prefix); a URL that names no picture, or names it ambiguously, is not one.
func TestMediaRefFileForms(t *testing.T) {
	want := MediaFileName("1001", "1760000000", "1001_1760000000")
	for _, raw := range []string{
		"/lineplay/d/download.nhn?oid=1001_1760000000&ctime=1760000000&userid=1001&tid=640x960",
		"https://obs.line-apps.com/lineplay/d/download.nhn?oid=1001_1760000000&ctime=1760000000&userid=1001",
		"lineplay/d/download.nhn?oid=1001_1760000000&ctime=1760000000&userid=1001",
		"/lineplay/pr/1001_1760000000",
		"/r/lineplay/pr/1001_1760000000",
	} {
		if got, ok := MediaRefFile(raw); !ok || got != want {
			t.Errorf("%q = %q, %t; want %q", raw, got, ok, want)
		}
	}
	for _, raw := range []string{
		"",
		"https://fapi.play.naver.jp/v4/x",
		"/lineplay/d/download.nhn?oid=1001_1760000000&ctime=1760000000",
		"/lineplay/d/download.nhn?oid=a&oid=b&ctime=1760000000&userid=1001",
		"/lineplay/d/download.nhn?oid=x&ctime=abc&userid=1001",
		"/lineplay/pr/1001",
		"/lineplay/d/upload.nhn?oid=1001_1760000000&ctime=1760000000&userid=1001",
	} {
		if got, ok := MediaRefFile(raw); ok {
			t.Errorf("%q accepted as %q", raw, got)
		}
	}
	if mediaFileOK("../../x.img") || mediaFileOK(strings.Repeat("A", 64)+".img") || !mediaFileOK(want) {
		t.Error("a folder path or a non-digest name passed as a stored file name")
	}
}

// TestMediaUsageSumsOwnerAllKinds: a quota counts the bytes of one owner's pictures of every kind together; another owner's do not count.
func TestMediaUsageSumsOwnerAllKinds(t *testing.T) {
	attachTestDB(t)
	mediaDataDir(t)
	diary1 := storeMediaFile(t, MediaDiary, "1001", "1001", "1", "1001_1")
	diary2 := storeMediaFile(t, MediaDiary, "1001", "1001", "2", "1001_2")
	room := storeMediaFile(t, MediaRoom, "1001", "1001", "3", "o3")
	other := storeMediaFile(t, MediaDiary, "1002", "1002", "1", "1002_1")
	if got, err := MediaUsage("1001"); err != nil || got != diary1.Size+diary2.Size+room.Size {
		t.Fatalf("usage of 1001 = %d bytes, err %v; want %d (every kind)", got, err, diary1.Size+diary2.Size+room.Size)
	}
	if got, _ := MediaUsage("1002"); got != other.Size {
		t.Fatalf("usage of 1002 = %d bytes, want %d", got, other.Size)
	}
	if got, _ := MediaUsage("1003"); got != 0 {
		t.Fatalf("usage of an account with no pictures = %d bytes, want 0", got)
	}
}

// TestMediaTotalsSumsEachKind: one query gives the count and the bytes of every kind that has rows; a kind with none is absent.
func TestMediaTotalsSumsEachKind(t *testing.T) {
	attachTestDB(t)
	mediaDataDir(t)
	if totals, err := MediaTotals(); err != nil || len(totals) != 0 {
		t.Fatalf("totals of an empty database = %v, err %v", totals, err)
	}
	a := storeMediaFile(t, MediaDiary, "1001", "1001", "1", "1001_1")
	b := storeMediaFile(t, MediaDiary, "1002", "1002", "2", "1002_2")
	c := storeMediaFile(t, MediaRoom, "1002", "1002", "3", "o3")
	totals, err := MediaTotals()
	if err != nil {
		t.Fatal(err)
	}
	if d := totals[MediaDiary]; d.Files != 2 || d.Bytes != a.Size+b.Size {
		t.Fatalf("diary totals = %+v, want 2 files and %d bytes", d, a.Size+b.Size)
	}
	if r := totals[MediaRoom]; r.Files != 1 || r.Bytes != c.Size {
		t.Fatalf("room totals = %+v, want 1 file and %d bytes", r, c.Size)
	}
	if _, ok := totals[MediaProfile]; ok {
		t.Fatal("a kind with no rows is in the totals")
	}
}

// TestRemovePlayerMediaClearsEveryReference: one removal clears the picture from the post that holds it (the post stays with its
// other picture), from the profile photos, the profile picture and a room preset; deletes its row and file; and writes one audit
// row that names the number and the reason, never the picture. The other picture is untouched.
func TestRemovePlayerMediaClearsEveryReference(t *testing.T) {
	attachTestDB(t)
	ResetSocial()
	t.Cleanup(ResetSocial)
	mediaDataDir(t)
	acc, id := staffAccount(t, "dev-pm", "1001", "Pic", 0)
	shot := storeMediaFile(t, MediaDiary, "1001", "1001", "1760000000", "1001_1760000000")
	keep := storeMediaFile(t, MediaDiary, "1001", "1001", "1760000001", "1001_1760000001")
	shotURL := "/lineplay/d/download.nhn?oid=1001_1760000000&ctime=1760000000&userid=1001"
	keepURL := "/lineplay/d/download.nhn?oid=1001_1760000001&ctime=1760000001&userid=1001"
	AccountsMu.Lock()
	acc.DiaryProfile = &DiaryProfile{Photos: []ProfilePhoto{{ImageID: "a", ImageURL: shotURL}, {ImageID: "b", ImageURL: keepURL}}}
	acc.ProfileImage = ProfileImage{Image: "/lineplay/pr/1001_1760000000", WholeBody: "/lineplay/pr/1001_1760000001"}
	acc.Presets = map[string]RoomPreset{"1": {Image: shotURL}}
	AccountsMu.Unlock()
	if err := SaveLocked(nil, acc); err != nil {
		t.Fatal(err)
	}
	SocialMu.Lock()
	DiaryPosts = []DiaryPost{{DiaryNo: "5", AvatarID: "1001", OpenState: "public", ImageTypeKind: "obs", ImageLocationType: "obs",
		Images: []DiaryImage{{ImageURL: shotURL}, {ImageURL: keepURL}}}}
	SocialMu.Unlock()
	if err := SaveSocialLocked(SocialChange{Posts: []string{"5"}, Scalars: true}); err != nil {
		t.Fatal(err)
	}

	if err := RemovePlayerMedia(id, shot.ID, "  stolen photo ", "discord:9"); err != nil {
		t.Fatalf("remove: %v", err)
	}
	SocialMu.Lock()
	post := DiaryPosts[0]
	SocialMu.Unlock()
	if len(post.Images) != 1 || post.Images[0].ImageURL != keepURL || post.ImageLocationType != "obs" {
		t.Fatalf("post after removal: %d images, location %q", len(post.Images), post.ImageLocationType)
	}
	AccountsMu.Lock()
	photos, image, preset := acc.DiaryProfile.Photos, acc.ProfileImage, acc.Presets["1"]
	AccountsMu.Unlock()
	if len(photos) != 1 || photos[0].ImageID != "b" {
		t.Fatalf("profile photos after removal = %+v", photos)
	}
	if image.Image != "" || image.WholeBody != "/lineplay/pr/1001_1760000001" {
		t.Fatalf("profile picture after removal = %+v", image)
	}
	if preset.Image != "" {
		t.Fatalf("room thumbnail after removal = %q", preset.Image)
	}
	if _, found, _ := MediaRowByFile(shot.File); found || onDisk(t, shot) {
		t.Fatalf("removed picture still has a row (%t) or a file (%t)", found, onDisk(t, shot))
	}
	if _, found, _ := MediaRowByFile(keep.File); !found || !onDisk(t, keep) {
		t.Fatal("the other picture lost its row or its file")
	}
	rows, err := AuditRows(0, 50)
	if err != nil {
		t.Fatal(err)
	}
	var audited bool
	for _, r := range rows {
		if r.Action == "remove-media" && r.Target == playerTarget(id) && r.Actor == "discord:9" {
			audited = strings.Contains(r.Detail, "stolen photo") && !strings.Contains(r.Detail, "download.nhn") && !strings.Contains(r.Detail, "picture ")
		}
	}
	if !audited {
		t.Fatalf("no clean remove-media audit row in %+v", rows)
	}
}

// TestRemovePlayerMediaRollsBackWithItsAudit: when the audit row cannot be written, the transaction fails as a whole: the
// database keeps the row and the post, and memory keeps the post's picture and the file stays on disk.
func TestRemovePlayerMediaRollsBackWithItsAudit(t *testing.T) {
	d := attachTestDB(t)
	ResetSocial()
	t.Cleanup(ResetSocial)
	mediaDataDir(t)
	_, id := staffAccount(t, "dev-rb", "1001", "Roll", 0)
	shot := storeMediaFile(t, MediaDiary, "1001", "1001", "1760000002", "1001_1760000002")
	shotURL := "/lineplay/d/download.nhn?oid=1001_1760000002&ctime=1760000002&userid=1001"
	SocialMu.Lock()
	DiaryPosts = []DiaryPost{{DiaryNo: "6", AvatarID: "1001", OpenState: "public", ImageTypeKind: "obs", ImageLocationType: "obs",
		Images: []DiaryImage{{ImageURL: shotURL}}}}
	SocialMu.Unlock()
	if err := SaveSocialLocked(SocialChange{Posts: []string{"6"}, Scalars: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := d.write.Exec(`CREATE TRIGGER refuse_audit BEFORE INSERT ON audit BEGIN SELECT RAISE(ABORT, 'audit refused'); END`); err != nil {
		t.Fatal(err)
	}
	if err := RemovePlayerMedia(id, shot.ID, "reason", "discord:1"); err == nil {
		t.Fatal("removal succeeded without its audit row")
	}
	SocialMu.Lock()
	kept := len(DiaryPosts[0].Images)
	SocialMu.Unlock()
	if kept != 1 {
		t.Fatalf("memory lost the post's picture: %d images", kept)
	}
	if _, found, _ := MediaRowByFile(shot.File); !found || !onDisk(t, shot) {
		t.Fatal("a failed removal lost the row or the file")
	}
}

// TestErasePostKeepsPicturesStillNamed: an owner's delete of a post removes the pictures only that post named (row and file),
// keeps a picture another post still names, and a staff removal of a post does the same and writes its audit row.
func TestErasePostKeepsPicturesStillNamed(t *testing.T) {
	attachTestDB(t)
	ResetSocial()
	t.Cleanup(ResetSocial)
	mediaDataDir(t)
	_, id := staffAccount(t, "dev-ep", "1001", "Erase", 0)
	shared := storeMediaFile(t, MediaDiary, "1001", "1001", "1760000003", "1001_1760000003")
	only := storeMediaFile(t, MediaDiary, "1001", "1001", "1760000004", "1001_1760000004")
	sharedURL := "/lineplay/d/download.nhn?oid=1001_1760000003&ctime=1760000003&userid=1001"
	onlyURL := "/lineplay/d/download.nhn?oid=1001_1760000004&ctime=1760000004&userid=1001"
	// Posts are added newest first, as the in-game write does: each new row is at the front of the list when it is saved.
	for _, p := range []DiaryPost{
		{DiaryNo: "7", AvatarID: "1001", OpenState: "public", ImageTypeKind: "obs", ImageLocationType: "obs", Images: []DiaryImage{{ImageURL: onlyURL}}},
		{DiaryNo: "8", AvatarID: "1001", OpenState: "public", ImageTypeKind: "obs", ImageLocationType: "obs", Images: []DiaryImage{{ImageURL: sharedURL}}},
		{DiaryNo: "9", AvatarID: "1001", OpenState: "public", ImageTypeKind: "obs", ImageLocationType: "obs", Images: []DiaryImage{{ImageURL: sharedURL}}},
	} {
		SocialMu.Lock()
		DiaryPosts = append([]DiaryPost{p}, DiaryPosts...)
		SocialMu.Unlock()
		if err := SaveSocialLocked(SocialChange{Posts: []string{p.DiaryNo}, Scalars: true}); err != nil {
			t.Fatal(err)
		}
	}

	if err := EraseDiaryPost("1001", "8"); err != nil {
		t.Fatalf("owner erase: %v", err)
	}
	if _, found, _ := MediaRowByFile(shared.File); !found || !onDisk(t, shared) {
		t.Fatal("a picture post 9 still names was removed with post 8")
	}
	if err := EraseDiaryPost("1002", "7"); err != nil {
		t.Fatalf("erase of another's post: %v", err)
	}
	if _, found, _ := MediaRowByFile(only.File); !found {
		t.Fatal("a post that is not the caller's removed a picture")
	}
	if err := RemovePlayerPost(id, "7", "spam", "discord:2"); err != nil {
		t.Fatalf("staff remove: %v", err)
	}
	if _, found, _ := MediaRowByFile(only.File); found || onDisk(t, only) {
		t.Fatal("staff removal of post 7 kept its only picture")
	}
	rows, err := AuditRows(0, 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) == 0 || rows[0].Action != "remove-post" || rows[0].Target != playerTarget(id) {
		t.Fatalf("newest audit row = %+v, want remove-post", rows)
	}
}

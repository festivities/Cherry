package store

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func openMediaPruneStore(t *testing.T) (string, *Account) {
	t.Helper()
	dir := mediaDataDir(t)
	if err := OpenStore(filepath.Join(dir, DBFileName)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = CloseStore() })
	ResetSocial()
	t.Cleanup(ResetSocial)
	acc, _ := staffAccount(t, "dev-mp", "7101", "Tester", 0)
	return dir, acc
}

func TestSaveProfileImagePrunesOldAndAbandoned(t *testing.T) {
	_, acc := openMediaPruneStore(t)

	oldHead := storeMediaFile(t, MediaProfile, "7101", "7101", "1760000001", "7101_1760000001")
	oldWB := storeMediaFile(t, MediaProfile, "7101", "7101", "1760000002", "7101_1760000002")
	abandoned := storeMediaFile(t, MediaProfile, "7101", "7101", "1760000003", "7101_1760000003")
	newHead := storeMediaFile(t, MediaProfile, "7101", "7101", "1760000004", "7101_1760000004")
	newWB := storeMediaFile(t, MediaProfile, "7101", "7101", "1760000005", "7101_1760000005")
	// An unrelated room thumbnail of the same account must stay untouched.
	roomThumb := storeMediaFile(t, MediaRoom, "7101", "7101", "1760000006", "7101_r1")

	AccountsMu.Lock()
	acc.ProfileImage = ProfileImage{Image: "/lineplay/pr/7101_1760000001", WholeBody: "/lineplay/pr/7101_1760000002", Bg: "profilebg/12.jpg"}
	if err := SaveLocked(nil, acc); err != nil {
		AccountsMu.Unlock()
		t.Fatal(err)
	}
	err := SaveProfileImageLocked(acc, ProfileImage{Image: "/lineplay/pr/7101_1760000004", WholeBody: "/lineplay/pr/7101_1760000005", Bg: "profilebg/15.jpg"})
	AccountsMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}

	for _, gone := range []MediaFile{oldHead, oldWB, abandoned} {
		if _, found, err := MediaRowByFile(gone.File); err != nil || found {
			t.Errorf("stale profile row %s still present: found=%v err=%v", gone.File, found, err)
		}
		if onDisk(t, gone) {
			t.Errorf("stale profile file %s still on disk", gone.File)
		}
	}
	for _, kept := range []MediaFile{newHead, newWB, roomThumb} {
		if _, found, err := MediaRowByFile(kept.File); err != nil || !found {
			t.Errorf("live media row %s missing: found=%v err=%v", kept.File, found, err)
		}
		if !onDisk(t, kept) {
			t.Errorf("live media file %s missing on disk", kept.File)
		}
	}
	usage, err := MediaUsage("7101")
	if err != nil {
		t.Fatal(err)
	}
	wantUsage := newHead.Size + newWB.Size + roomThumb.Size
	if usage != wantUsage {
		t.Fatalf("MediaUsage = %d, want %d", usage, wantUsage)
	}
}

func TestSaveRoomPresetsPrunesSupersededAndRemoved(t *testing.T) {
	_, acc := openMediaPruneStore(t)

	thumb1 := storeMediaFile(t, MediaRoom, "7101", "7101", "1760000010", "7101_r10")
	thumb2 := storeMediaFile(t, MediaRoom, "7101", "7101", "1760000011", "7101_r11")
	url1 := "/lineplay/r/download.nhn?userid=7101&ctime=1760000010&oid=7101_r10"
	url2 := "/lineplay/r/download.nhn?userid=7101&ctime=1760000011&oid=7101_r11"

	// Put thumb1 on both slot 1 and slot 2: overwriting slot 1 must keep thumb1 because slot 2 still uses it.
	AccountsMu.Lock()
	if err := SaveRoomPresetsLocked(acc, map[string]RoomPreset{
		"1": {Image: url1},
		"2": {Image: url1},
	}); err != nil {
		AccountsMu.Unlock()
		t.Fatal(err)
	}
	if err := SaveRoomPresetsLocked(acc, map[string]RoomPreset{
		"1": {Image: url2},
		"2": {Image: url1},
	}, url1); err != nil {
		AccountsMu.Unlock()
		t.Fatal(err)
	}
	AccountsMu.Unlock()
	if !onDisk(t, thumb1) || !onDisk(t, thumb2) {
		t.Fatalf("shared thumb1 or new thumb2 unexpectedly removed")
	}

	// Removing slot 2 drops the last reference to thumb1: thumb1 is deleted immediately.
	AccountsMu.Lock()
	if err := SaveRoomPresetsLocked(acc, map[string]RoomPreset{
		"1": {Image: url2},
	}, url1); err != nil {
		AccountsMu.Unlock()
		t.Fatal(err)
	}
	AccountsMu.Unlock()
	if _, found, _ := MediaRowByFile(thumb1.File); found || onDisk(t, thumb1) {
		t.Fatalf("superseded thumb1 still present after last slot removed")
	}
	if _, found, _ := MediaRowByFile(thumb2.File); !found || !onDisk(t, thumb2) {
		t.Fatalf("live thumb2 missing")
	}
}

func TestSaveProfilePhotosAndModifyDiaryPostPruneUnused(t *testing.T) {
	_, acc := openMediaPruneStore(t)

	phOld := storeMediaFile(t, MediaDiary, "7101", "7101", "1760000020", "7101_1760000020")
	phKeep := storeMediaFile(t, MediaDiary, "7101", "7101", "1760000021", "7101_1760000021")
	phOldURL := "/lineplay/d/download.nhn?userid=7101&ctime=1760000020&oid=7101_1760000020"
	phKeepURL := "/lineplay/d/download.nhn?userid=7101&ctime=1760000021&oid=7101_1760000021"

	AccountsMu.Lock()
	if err := SaveProfilePhotosLocked(acc, &DiaryProfile{Photos: []ProfilePhoto{{ImageURL: phOldURL}, {ImageURL: phKeepURL}}}); err != nil {
		AccountsMu.Unlock()
		t.Fatal(err)
	}
	if err := SaveProfilePhotosLocked(acc, &DiaryProfile{Photos: []ProfilePhoto{{ImageURL: phKeepURL}}}, phOldURL, phKeepURL); err != nil {
		AccountsMu.Unlock()
		t.Fatal(err)
	}
	AccountsMu.Unlock()
	if _, found, _ := MediaRowByFile(phOld.File); found || onDisk(t, phOld) {
		t.Fatalf("removed profile photo still present")
	}
	if _, found, _ := MediaRowByFile(phKeep.File); !found || !onDisk(t, phKeep) {
		t.Fatalf("kept profile photo missing")
	}

	// Diary modify: drop one image, keep the other.
	dOld := storeMediaFile(t, MediaDiary, "7101", "7101", "1760000022", "7101_1760000022")
	dKeep := storeMediaFile(t, MediaDiary, "7101", "7101", "1760000023", "7101_1760000023")
	dOldURL := "lineplay/d/download.nhn?userid=7101&ctime=1760000022&oid=7101_1760000022"
	dKeepURL := "lineplay/d/download.nhn?userid=7101&ctime=1760000023&oid=7101_1760000023"

	SocialMu.Lock()
	DiaryPosts = []DiaryPost{{
		DiaryNo: "101", AvatarID: "7101", OpenState: "public",
		Images: []DiaryImage{{ImageURL: dOldURL}, {ImageURL: dKeepURL}},
	}}
	if err := SaveSocialLocked(SocialChange{Posts: []string{"101"}}); err != nil {
		SocialMu.Unlock()
		t.Fatal(err)
	}
	SocialMu.Unlock()

	_, found, err := ModifyDiaryPost("7101", "101", func(p *DiaryPost) {
		p.Images = []DiaryImage{{ImageURL: dKeepURL}}
	})
	if err != nil || !found {
		t.Fatalf("ModifyDiaryPost: found=%v err=%v", found, err)
	}
	if _, ok, _ := MediaRowByFile(dOld.File); ok || onDisk(t, dOld) {
		t.Fatalf("removed diary post image still present")
	}
	if _, ok, _ := MediaRowByFile(dKeep.File); !ok || !onDisk(t, dKeep) {
		t.Fatalf("kept diary post image missing")
	}
}

func TestPruneStaleMediaPreservesLiveFreshAndDeletedAvatars(t *testing.T) {
	dir, acc := openMediaPruneStore(t)

	livePr := storeMediaFile(t, MediaProfile, "7101", "7101", "1760000030", "7101_1760000030")
	staleOld := storeMediaFile(t, MediaProfile, "7101", "7101", "1760000031", "7101_1760000031")
	staleFresh := storeMediaFile(t, MediaRoom, "7101", "7101", "1760000032", "7101_r32")
	deletedPic := storeMediaFile(t, MediaDiary, "7999", "7999", "1760000033", "7999_1760000033")

	AccountsMu.Lock()
	acc.ProfileImage = ProfileImage{Image: "/lineplay/pr/7101_1760000030", Bg: "profilebg/12.jpg"}
	if err := SaveLocked(nil, acc); err != nil {
		AccountsMu.Unlock()
		t.Fatal(err)
	}
	AccountsMu.Unlock()

	// Record an unrestored deleted avatar owning deletedPic, and backdate staleOld + deletedPic to 3h ago.
	raw, err := sql.Open("sqlite", filepath.Join(dir, DBFileName))
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	threeHoursAgo := time.Now().UTC().Add(-3 * time.Hour).Format(time.RFC3339)
	if _, err := raw.Exec(`UPDATE media_files SET created = ? WHERE id IN (?, ?)`, threeHoursAgo, staleOld.ID, deletedPic.ID); err != nil {
		t.Fatal(err)
	}
	recBytes, _ := json.Marshal(AvatarRecord{Account: StoredAccount{Aid: "7999"}, Media: []MediaFile{deletedPic}})
	if _, err := raw.Exec(`INSERT INTO deleted_avatars (aid, account_id, identity, name, deleted_at, data, restored_at) VALUES ('7999', NULL, 'id-7999', 'Gone', ?, ?, '')`, threeHoursAgo, string(recBytes)); err != nil {
		t.Fatal(err)
	}

	// Add an orphan .img file on disk (old) and a non-.img file (ignored) in profile-media.
	prDir, err := MediaDir(MediaProfile)
	if err != nil {
		t.Fatal(err)
	}
	orphanName := MediaFileName("7101", "1760000099", "7101_1760000099")
	orphanPath := filepath.Join(prDir, orphanName)
	if err := os.WriteFile(orphanPath, []byte("orphan"), 0600); err != nil {
		t.Fatal(err)
	}
	oldTime := time.Now().Add(-3 * time.Hour)
	if err := os.Chtimes(orphanPath, oldTime, oldTime); err != nil {
		t.Fatal(err)
	}
	keepNotePath := filepath.Join(prDir, "README.txt")
	if err := os.WriteFile(keepNotePath, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(keepNotePath, oldTime, oldTime); err != nil {
		t.Fatal(err)
	}

	rowsDeleted, bytesFreed, orphans, err := PruneStaleMedia(time.Now().UTC(), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if rowsDeleted != 1 || bytesFreed != staleOld.Size || orphans != 1 {
		t.Fatalf("PruneStaleMedia = (%d, %d, %d), want (1, %d, 1)", rowsDeleted, bytesFreed, orphans, staleOld.Size)
	}
	if _, found, _ := MediaRowByFile(staleOld.File); found || onDisk(t, staleOld) {
		t.Errorf("staleOld still present")
	}
	for _, kept := range []MediaFile{livePr, staleFresh, deletedPic} {
		if _, found, _ := MediaRowByFile(kept.File); !found || !onDisk(t, kept) {
			t.Errorf("protected file %s was pruned", kept.File)
		}
	}
	if _, err := os.Stat(orphanPath); !os.IsNotExist(err) {
		t.Errorf("orphan .img file still on disk")
	}
	if _, err := os.Stat(keepNotePath); err != nil {
		t.Errorf("non-.img file was removed: %v", err)
	}
}

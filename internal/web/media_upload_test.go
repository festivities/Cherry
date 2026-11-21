package web

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"cherry/internal/config"
	"cherry/internal/store"
	"cherry/internal/testutil"
)

// Picture uploads in identity modes (slice 8.6b part 3): the owner is the credential's account, a quota per account, the
// operator switch, and a post, profile or room thumbnail may name only pictures its owner uploaded.

// uploadConfig activates c for one test and restores the zero configuration afterwards.
func uploadConfig(t *testing.T, c config.Config) {
	t.Helper()
	config.Activate(c)
	t.Cleanup(func() { config.Activate(config.Config{}) })
}

// uploadAs is one diary upload of data under the key tuple (userID, ctime), with token as the AV_AUTH cookie (empty: none).
func uploadAs(t *testing.T, dir, token, userID, ctime string, data []byte) *httptest.ResponseRecorder {
	t.Helper()
	oid := userID + "_" + ctime
	req := diaryUploadRequest(t, diaryParamsJSON(t, userID, ctime, oid), data, "image/png")
	if token != "" {
		req.Header.Set("Cookie", `AV_AUTH="`+token+`"`)
	}
	rec := httptest.NewRecorder()
	handleDiaryImageUploadAt(rec, req, dir)
	return rec
}

// twoAccounts installs the accounts 1001 (token tok1) and 1002 (token tok2) and attaches a database.
func twoAccounts(t *testing.T) {
	t.Helper()
	installSocialTestAccounts(t, map[string]*store.Account{
		"tok1": {AccessToken: "tok1", Aid: "1001", Name: "One"},
		"tok2": {AccessToken: "tok2", Aid: "1002", Name: "Two"},
	})
	testutil.UseDatabase(t)
}

// TestUploadCredentialRules: without a credential an identity-mode upload is refused and stores nothing; with one the owner is
// the credential's account, and a diary form userid that is not that account is a 400. A My Room thumbnail carries the
// credential as the AV_AUTH header.
func TestUploadCredentialRules(t *testing.T) {
	twoAccounts(t)
	uploadConfig(t, config.Config{AuthMode: config.AuthDevFake})
	dir := filepath.Join(t.TempDir(), "diary-media")
	data := diaryTestImage(t, "png", 3, 2, 11)

	if rec := uploadAs(t, dir, "", "1001", "1760000200", data); rec.Code != http.StatusForbidden {
		t.Fatalf("unsigned diary upload = %d %s, want 403", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("an unsigned upload created the folder: %v", err)
	}
	if rec := uploadAs(t, dir, "tok1", "1002", "1760000201", data); rec.Code != http.StatusBadRequest {
		t.Fatalf("diary upload naming another account = %d, want 400", rec.Code)
	}
	if rec := uploadAs(t, dir, "tok1", "1001", "1760000202", data); rec.Code != http.StatusOK {
		t.Fatalf("signed diary upload = %d %s", rec.Code, rec.Body.String())
	}
	if row, found, err := store.MediaRowByFile(store.MediaFileName("1001", "1760000202", "1001_1760000202")); err != nil || !found || row.Owner != "1001" || row.Kind != store.MediaDiary {
		t.Fatalf("row of the signed upload = %+v found=%t err=%v", row, found, err)
	}

	roomDir := filepath.Join(t.TempDir(), "room-media")
	const oid = "thumb-1"
	params, _ := json.Marshal(diaryImageUploadParams{Version: "1.0", Type: "image", Name: oid, UserID: "1002", OID: oid, CTime: "1760000203"})
	req := diaryUploadRequest(t, params, data, "image/png")
	req.Header.Set("AV_AUTH", "tok2")
	rec := httptest.NewRecorder()
	handleMediaUpload(rec, req, roomDir, maxDiaryMediaStorage, mediaRoom)
	if rec.Code != http.StatusOK {
		t.Fatalf("room thumbnail with the AV_AUTH header = %d %s", rec.Code, rec.Body.String())
	}
	if row, found, _ := store.MediaRowByFile(store.MediaFileName("1002", "1760000203", oid)); !found || row.Owner != "1002" || row.Kind != store.MediaRoom {
		t.Fatalf("room row = %+v found=%t", row, found)
	}

	profile := diaryUploadRequest(t, diaryParamsJSON(t, "1001", "1760000204", "1001_1760000204"), data, "image/png")
	rec = httptest.NewRecorder()
	handleProfileImageUpload(rec, profile)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("profile upload without a credential = %d, want 400", rec.Code)
	}
}

// TestUploadQuotaPerAccount: an account holds at most its file quota of a kind; the next upload of that account is 507 with the
// quota text, and another account's uploads are not affected.
func TestUploadQuotaPerAccount(t *testing.T) {
	twoAccounts(t)
	uploadConfig(t, config.Config{AuthMode: config.AuthDevFake, UploadQuotaFiles: 2})
	dir := filepath.Join(t.TempDir(), "diary-media")
	data := diaryTestImage(t, "png", 3, 2, 12)
	for _, ctime := range []string{"1760000300", "1760000301"} {
		if rec := uploadAs(t, dir, "tok1", "1001", ctime, data); rec.Code != http.StatusOK {
			t.Fatalf("upload %s within the quota = %d", ctime, rec.Code)
		}
	}
	if rec := uploadAs(t, dir, "tok1", "1001", "1760000302", data); rec.Code != http.StatusInsufficientStorage || rec.Body.String() != diaryMediaQuotaBody {
		t.Fatalf("third upload = %d %s, want the account quota", rec.Code, rec.Body.String())
	}
	if rec := uploadAs(t, dir, "tok2", "1002", "1760000303", data); rec.Code != http.StatusOK {
		t.Fatalf("another account's upload = %d, want 200", rec.Code)
	}
	if entries, err := os.ReadDir(dir); err != nil || len(entries) != 3 {
		t.Fatalf("stored files = %d (err %v), want 3", len(entries), err)
	}
}

// TestUploadQuotaBytes: an account's bytes of a kind are capped as well; a picture that would pass the cap is refused whole.
func TestUploadQuotaBytes(t *testing.T) {
	twoAccounts(t)
	uploadConfig(t, config.Config{AuthMode: config.AuthDevFake, UploadQuotaBytes: 1})
	dir := filepath.Join(t.TempDir(), "diary-media")
	if rec := uploadAs(t, dir, "tok1", "1001", "1760000310", diaryTestImage(t, "png", 3, 2, 13)); rec.Code != http.StatusInsufficientStorage || rec.Body.String() != diaryMediaQuotaBody {
		t.Fatalf("upload over the byte quota = %d %s", rec.Code, rec.Body.String())
	}
	if files, _, _ := store.MediaUsage(store.MediaDiary, "1001"); files != 0 {
		t.Fatalf("a refused upload left %d rows", files)
	}
}

// TestUploadsSwitchOff: with uploads.enabled false a new upload is 503 and stores nothing, while a picture stored before still
// downloads.
func TestUploadsSwitchOff(t *testing.T) {
	twoAccounts(t)
	uploadConfig(t, config.Config{AuthMode: config.AuthDevFake})
	dir := filepath.Join(t.TempDir(), "diary-media")
	data := diaryTestImage(t, "png", 3, 2, 14)
	if rec := uploadAs(t, dir, "tok1", "1001", "1760000400", data); rec.Code != http.StatusOK {
		t.Fatalf("upload with the switch on = %d", rec.Code)
	}
	config.Activate(config.Config{AuthMode: config.AuthDevFake, UploadsOff: true})
	if rec := uploadAs(t, dir, "tok1", "1001", "1760000401", data); rec.Code != http.StatusServiceUnavailable || rec.Body.String() != diaryMediaOffBody {
		t.Fatalf("upload with the switch off = %d %s", rec.Code, rec.Body.String())
	}
	if files, _, _ := store.MediaUsage(store.MediaDiary, "1001"); files != 1 {
		t.Fatalf("switched-off upload changed the rows: %d files, want 1", files)
	}
	get := httptest.NewRequest(http.MethodGet, diaryDownloadTarget(store.DiaryDownloadPath, "1001", "1760000400", "1001_1760000400", ""), nil)
	got := httptest.NewRecorder()
	handleDiaryImageDownloadAt(got, get, dir)
	if got.Code != http.StatusOK || len(got.Body.Bytes()) == 0 {
		t.Fatalf("download with the switch off = %d", got.Code)
	}
}

// TestDiaryPostRefusesForeignPicture: a post may name a picture its poster uploaded and nothing else.
func TestDiaryPostRefusesForeignPicture(t *testing.T) {
	twoAccounts(t)
	store.ResetSocial()
	t.Cleanup(store.ResetSocial)
	recordPicture(t, store.MediaDiary, "1002", "1002", "1760000500", "1002_1760000500")
	recordPicture(t, store.MediaDiary, "1001", "1001", "1760000501", "1001_1760000501")
	post := func(token, aid, picture string) *httptest.ResponseRecorder {
		body := fmt.Sprintf(`{"avatarId":%q,"title":"photo","content":"caption","openState":"public","imageTypeKind":"photo","images":[{"imageUrl":%q}]}`, aid, picture)
		return socialTestRequestAs(t, token, http.MethodPost, "/v4/diary2/ext/write", body)
	}
	foreign := "/lineplay/d/download.nhn?oid=1002_1760000500&ctime=1760000500&userid=1002"
	own := "/lineplay/d/download.nhn?oid=1001_1760000501&ctime=1760000501&userid=1001"
	if rec := post("tok1", "1001", foreign); rec.Code != http.StatusBadRequest {
		t.Fatalf("post naming another account's picture = %d %s, want 400", rec.Code, rec.Body.String())
	}
	if rec := post("tok1", "1001", own); rec.Code != http.StatusOK {
		t.Fatalf("post naming its own picture = %d %s", rec.Code, rec.Body.String())
	}
}

// TestRoomPresetRefusesForeignThumbnail: a room preset keeps only a thumbnail its owner uploaded; a foreign one is not saved.
func TestRoomPresetRefusesForeignThumbnail(t *testing.T) {
	testutil.UseDatabase(t)
	acc := decorSetup(t)
	recordPicture(t, store.MediaRoom, "1002", "1002", "1", "thumb")
	body := `{"tileSize":12,"representImagePath":"/lineplay/r/download.nhn?oid=thumb&ctime=1&userid=1002","groundLevel":"LEVEL_1","additionItems":[]}`
	if rec := decorReq(t, "dtok", http.MethodPost, "/v4/room/preset/save/1", body); rec.Body.String() != `{"result":false}` {
		t.Fatalf("foreign thumbnail save = %d %s", rec.Code, rec.Body.String())
	}
	store.AccountsMu.Lock()
	n := len(acc.Presets)
	store.AccountsMu.Unlock()
	if n != 0 {
		t.Fatalf("a refused preset was kept: %d", n)
	}
}

// TestDiaryEraseRemovesItsPictureFile: the owner's in-game delete of a photo post deletes the picture's row and file.
func TestDiaryEraseRemovesItsPictureFile(t *testing.T) {
	twoAccounts(t)
	store.ResetSocial()
	t.Cleanup(store.ResetSocial)
	dataDir := t.TempDir()
	uploadConfig(t, config.Config{AuthMode: config.AuthDevFake, DataDir: dataDir, CacheDir: dataDir})
	dir := filepath.Join(dataDir, "diary-media")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	file := store.MediaFileName("1001", "1760000600", "1001_1760000600")
	if err := os.WriteFile(filepath.Join(dir, file), diaryTestImage(t, "png", 2, 2, 15), 0600); err != nil {
		t.Fatal(err)
	}
	recordPicture(t, store.MediaDiary, "1001", "1001", "1760000600", "1001_1760000600")
	body := `{"avatarId":"1001","title":"photo","content":"c","openState":"public","imageTypeKind":"photo","images":[{"imageUrl":"/lineplay/d/download.nhn?oid=1001_1760000600&ctime=1760000600&userid=1001"}]}`
	wrote := socialTestRequestAs(t, "tok1", http.MethodPost, "/v4/diary2/ext/write", body)
	var out struct {
		Result store.DiaryPost `json:"result"`
	}
	if err := json.Unmarshal(wrote.Body.Bytes(), &out); wrote.Code != http.StatusOK || err != nil || out.Result.DiaryNo == "" {
		t.Fatalf("photo write = %d %s", wrote.Code, wrote.Body.String())
	}
	if rec := socialTestRequestAs(t, "tok1", http.MethodPost, "/v4/diary2/erase/"+out.Result.DiaryNo, ""); rec.Code != http.StatusOK {
		t.Fatalf("erase = %d %s", rec.Code, rec.Body.String())
	}
	if _, found, _ := store.MediaRowByFile(file); found {
		t.Fatal("the erased photo's row remains")
	}
	if _, err := os.Stat(filepath.Join(dir, file)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the erased photo's file remains: %v", err)
	}
}

func noisyPNG(t *testing.T, w, h int, transparent bool) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	state := uint32(0x12345678)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			state = state*1664525 + 1013904223
			a := uint8(255)
			if transparent && (x+y)%3 == 0 {
				a = uint8(100 + (state>>24)%100)
			}
			img.SetNRGBA(x, y, color.NRGBA{
				R: uint8(state >> 24),
				G: uint8(state >> 16),
				B: uint8(state >> 8),
				A: a,
			})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestUploadMaxFileBytesConfigurable(t *testing.T) {
	twoAccounts(t)
	uploadConfig(t, config.Config{AuthMode: config.AuthDevFake, UploadMaxFileBytes: 100 << 10})
	dir := filepath.Join(t.TempDir(), "diary-media")

	// 1. Opaque image larger than maxFile (100 KiB) is compressed to fit and saved as <= maxFile.
	opaque := noisyPNG(t, 260, 260, false)
	if int64(len(opaque)) <= 100<<10 {
		t.Fatalf("test setup: opaque PNG len = %d, want > 100 KiB", len(opaque))
	}
	if rec := uploadAs(t, dir, "tok1", "1001", "1760000700", opaque); rec.Code != http.StatusOK {
		t.Fatalf("opaque upload > maxFile = %d %s, want 200", rec.Code, rec.Body.String())
	}
	savedOpaque := filepath.Join(dir, store.MediaFileName("1001", "1760000700", "1001_1760000700"))
	info, err := os.Stat(savedOpaque)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() > 100<<10 {
		t.Fatalf("compressed opaque size = %d, want <= %d", info.Size(), 100<<10)
	}
	f, err := os.Open(savedOpaque)
	if err != nil {
		t.Fatal(err)
	}
	_, format, err := image.Decode(f)
	_ = f.Close()
	if err != nil || format != "jpeg" {
		t.Fatalf("saved opaque decode = (%q, %v), want jpeg", format, err)
	}

	// 2. Transparent PNG larger than maxFile is compressed while preserving PNG format and alpha.
	trans := noisyPNG(t, 240, 240, true)
	if int64(len(trans)) <= 100<<10 {
		t.Fatalf("test setup: transparent PNG len = %d, want > 100 KiB", len(trans))
	}
	if rec := uploadAs(t, dir, "tok1", "1001", "1760000701", trans); rec.Code != http.StatusOK {
		t.Fatalf("transparent PNG > maxFile = %d %s, want 200", rec.Code, rec.Body.String())
	}
	savedTrans := filepath.Join(dir, store.MediaFileName("1001", "1760000701", "1001_1760000701"))
	info, err = os.Stat(savedTrans)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() > 100<<10 {
		t.Fatalf("compressed transparent PNG size = %d, want <= %d", info.Size(), 100<<10)
	}
	f, err = os.Open(savedTrans)
	if err != nil {
		t.Fatal(err)
	}
	decoded, format, err := image.Decode(f)
	_ = f.Close()
	if err != nil || format != "png" {
		t.Fatalf("saved transparent decode = (%q, %v), want png", format, err)
	}
	if !hasTransparency(decoded) {
		t.Fatal("compressed transparent PNG lost alpha transparency")
	}

	// 3. Upload larger than hard receive ceiling (> 20 MiB) still returns 413.
	overReceive := bytes.Repeat([]byte{'x'}, int(maxDiaryImageSize+1))
	req := diaryUploadRequest(t, diaryParamsJSON(t, "1001", "1760000702", "1001_1760000702"), overReceive, "image/png")
	req.Header.Set("Cookie", `AV_AUTH="tok1"`)
	req.ContentLength = -1
	rec := httptest.NewRecorder()
	handleDiaryImageUploadAt(rec, req, dir)
	if rec.Code != http.StatusRequestEntityTooLarge || rec.Body.String() != diaryMediaTooLargeBody {
		t.Fatalf("over hard ceiling = %d %s, want 413", rec.Code, rec.Body.String())
	}
}

func TestUploadMaxTotalBytesConfigurable(t *testing.T) {
	twoAccounts(t)
	dataDir := t.TempDir()
	uploadConfig(t, config.Config{
		AuthMode:            config.AuthDevFake,
		DataDir:             dataDir,
		CacheDir:            dataDir,
		UploadMaxTotalBytes: 1 << 20,
	})
	small := diaryTestImage(t, "png", 3, 2, 17)

	// Missing directories don't block an upload under the shared cap.
	diaryReq := diaryUploadRequest(t, diaryParamsJSON(t, "1001", "1760000800", "1001_1760000800"), small, "image/png")
	diaryReq.Header.Set("AV_AUTH", "tok1")
	diaryRec := httptest.NewRecorder()
	handleDiaryImageUpload(diaryRec, diaryReq)
	if diaryRec.Code != http.StatusOK {
		t.Fatalf("initial diary upload = %d %s, want 200", diaryRec.Code, diaryRec.Body.String())
	}

	// Pre-fill diary (600 KiB) + room (424 KiB) so combined usage across dirs reaches the 1 MiB global cap.
	diaryDir, err := store.MediaDir(store.MediaDiary)
	if err != nil {
		t.Fatal(err)
	}
	roomDir, err := store.MediaDir(store.MediaRoom)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(roomDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(diaryDir, "seed_diary.img"), bytes.Repeat([]byte{0}, 600<<10), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(roomDir, "seed_room.img"), bytes.Repeat([]byte{0}, 424<<10), 0600); err != nil {
		t.Fatal(err)
	}

	// All three endpoints (diary, room, profile) share the same global cap and return 507.
	for _, tc := range []struct {
		name  string
		kind  string
		serve func(*httptest.ResponseRecorder, *http.Request)
		oid   string
	}{
		{
			name:  "diary",
			kind:  store.MediaDiary,
			serve: func(rec *httptest.ResponseRecorder, req *http.Request) { handleDiaryImageUpload(rec, req) },
			oid:   "1001_1760000801",
		},
		{
			name:  "room",
			kind:  store.MediaRoom,
			serve: func(rec *httptest.ResponseRecorder, req *http.Request) { handleRoomImageUpload(rec, req) },
			oid:   "room_thumb_1",
		},
		{
			name:  "profile",
			kind:  store.MediaProfile,
			serve: func(rec *httptest.ResponseRecorder, req *http.Request) { handleProfileImageUpload(rec, req) },
			oid:   "1001_1760000801",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var params []byte
			if tc.kind == store.MediaRoom {
				params, _ = json.Marshal(diaryImageUploadParams{Version: "1.0", Type: "image", Name: tc.oid, UserID: "1001", OID: tc.oid, CTime: "1760000801"})
			} else {
				params = diaryParamsJSON(t, "1001", "1760000801", tc.oid)
			}
			req := diaryUploadRequest(t, params, small, "image/png")
			req.Header.Set("AV_AUTH", "tok1")
			rec := httptest.NewRecorder()
			tc.serve(rec, req)
			if rec.Code != http.StatusInsufficientStorage || rec.Body.String() != diaryMediaFullBody {
				t.Fatalf("%s upload over 1 MiB shared cap = %d %s, want 507", tc.name, rec.Code, rec.Body.String())
			}
		})
	}

	// Raising the global cap live in the database unblocks uploads across kinds without restart.
	if err := store.SetUploadMaxTotalBytes(2<<20, "dev-owner"); err != nil {
		t.Fatal(err)
	}
	profReq := diaryUploadRequest(t, diaryParamsJSON(t, "1001", "1760000802", "1001_1760000802"), small, "image/png")
	profReq.Header.Set("AV_AUTH", "tok1")
	profRec := httptest.NewRecorder()
	handleProfileImageUpload(profRec, profReq)
	if profRec.Code != http.StatusOK {
		t.Fatalf("profile upload after raising global cap = %d %s, want 200", profRec.Code, profRec.Body.String())
	}
}

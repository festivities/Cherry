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
	return uploadKindAs(t, dir, mediaDiary, token, userID, ctime, data)
}

// uploadKindAs is uploadAs for any picture kind (diary, room or profile) stored in dir.
func uploadKindAs(t *testing.T, dir string, kind mediaKind, token, userID, ctime string, data []byte) *httptest.ResponseRecorder {
	t.Helper()
	oid := userID + "_" + ctime
	req := diaryUploadRequest(t, diaryParamsJSON(t, userID, ctime, oid), data, "image/png")
	if token != "" {
		req.Header.Set("Cookie", `AV_AUTH="`+token+`"`)
	}
	rec := httptest.NewRecorder()
	handleMediaUploadIn(rec, req, dir, store.UploadMaxTotalBytes(config.UploadMaxTotalBytes()), kind)
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

// TestUploadQuotaHasNoCountLimit: the quota is bytes only, so an account may keep many small pictures while they stay under it;
// another account's uploads are not affected by them.
func TestUploadQuotaHasNoCountLimit(t *testing.T) {
	twoAccounts(t)
	uploadConfig(t, config.Config{AuthMode: config.AuthDevFake, UploadQuotaBytes: 1 << 20})
	dir := filepath.Join(t.TempDir(), "diary-media")
	data := diaryTestImage(t, "png", 3, 2, 12)
	for i := range 60 {
		if rec := uploadAs(t, dir, "tok1", "1001", fmt.Sprint(1760000300+i), data); rec.Code != http.StatusOK {
			t.Fatalf("upload %d under the byte quota = %d %s", i, rec.Code, rec.Body.String())
		}
	}
	if rec := uploadAs(t, dir, "tok2", "1002", "1760000400", data); rec.Code != http.StatusOK {
		t.Fatalf("another account's upload = %d, want 200", rec.Code)
	}
	if entries, err := os.ReadDir(dir); err != nil || len(entries) != 61 {
		t.Fatalf("stored files = %d (err %v), want 61", len(entries), err)
	}
}

// TestUploadQuotaCombinesKinds: one byte total covers diary, room and profile pictures. With a quota of 2.5 pictures, an account
// holding a diary and a profile picture is refused a room picture (the sum would pass the quota) while another account's room
// picture is accepted, and the refused upload leaves nothing.
func TestUploadQuotaCombinesKinds(t *testing.T) {
	twoAccounts(t)
	uploadConfig(t, config.Config{AuthMode: config.AuthDevFake})
	base := t.TempDir()
	data := diaryTestImage(t, "png", 3, 2, 16)
	if rec := uploadKindAs(t, filepath.Join(base, "diary"), mediaDiary, "tok1", "1001", "1760000600", data); rec.Code != http.StatusOK {
		t.Fatalf("diary upload = %d", rec.Code)
	}
	one, err := store.MediaUsage("1001")
	if err != nil || one <= 0 {
		t.Fatalf("one picture holds %d bytes (err %v)", one, err)
	}
	config.Activate(config.Config{AuthMode: config.AuthDevFake, UploadQuotaBytes: one * 5 / 2})
	if rec := uploadKindAs(t, filepath.Join(base, "profile"), mediaProfile, "tok1", "1001", "1760000601", data); rec.Code != http.StatusOK {
		t.Fatalf("profile upload within the quota = %d %s", rec.Code, rec.Body.String())
	}
	if rec := uploadKindAs(t, filepath.Join(base, "room"), mediaRoom, "tok1", "1001", "1760000602", data); rec.Code != http.StatusInsufficientStorage || rec.Body.String() != diaryMediaQuotaBody {
		t.Fatalf("room upload past the combined quota = %d %s, want the quota text", rec.Code, rec.Body.String())
	}
	if rec := uploadKindAs(t, filepath.Join(base, "room"), mediaRoom, "tok2", "1002", "1760000603", data); rec.Code != http.StatusOK {
		t.Fatalf("another account's room upload = %d, want 200", rec.Code)
	}
	if got, _ := store.MediaUsage("1001"); got != 2*one {
		t.Fatalf("account 1001 holds %d bytes after a refused upload, want %d", got, 2*one)
	}
}

// TestUploadQuotaBytes: an account's bytes are capped as well; a picture that would pass the cap is refused whole.
func TestUploadQuotaBytes(t *testing.T) {
	twoAccounts(t)
	uploadConfig(t, config.Config{AuthMode: config.AuthDevFake, UploadQuotaBytes: 1})
	dir := filepath.Join(t.TempDir(), "diary-media")
	if rec := uploadAs(t, dir, "tok1", "1001", "1760000310", diaryTestImage(t, "png", 3, 2, 13)); rec.Code != http.StatusInsufficientStorage || rec.Body.String() != diaryMediaQuotaBody {
		t.Fatalf("upload over the byte quota = %d %s", rec.Code, rec.Body.String())
	}
	if got, _ := store.MediaUsage("1001"); got != 0 {
		t.Fatalf("a refused upload left %d bytes", got)
	}
}

// TestUploadPlayerLimitFromPanel: the Owner Settings byte quota of one player replaces the configured one at once; an upload past
// the panel's total is 507 with the quota text, and a reset restores the configured value.
func TestUploadPlayerLimitFromPanel(t *testing.T) {
	twoAccounts(t)
	uploadConfig(t, config.Config{AuthMode: config.AuthDevFake})
	dir := filepath.Join(t.TempDir(), "diary-media")
	data := diaryTestImage(t, "png", 3, 2, 15)

	// 1002 holds 1 MiB minus one byte of diary pictures: the small picture fits the configured 50 MiB but not the panel's 1 MiB.
	if err := store.AddMediaFile(store.MediaFile{Kind: store.MediaDiary, Owner: "1002", File: "seed-1002.img", Size: 1<<20 - 1}); err != nil {
		t.Fatal(err)
	}
	if err := store.SetUploadQuotaBytes(1<<20, "dev-owner"); err != nil {
		t.Fatal(err)
	}
	if rec := uploadAs(t, dir, "tok2", "1002", "1760000502", data); rec.Code != http.StatusInsufficientStorage || rec.Body.String() != diaryMediaQuotaBody {
		t.Fatalf("upload past the panel's byte quota = %d %s, want the quota text", rec.Code, rec.Body.String())
	}
	if err := store.ClearUploadQuotaBytes("dev-owner"); err != nil {
		t.Fatal(err)
	}
	if rec := uploadAs(t, dir, "tok2", "1002", "1760000503", data); rec.Code != http.StatusOK {
		t.Fatalf("upload after the reset = %d %s, want 200", rec.Code, rec.Body.String())
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
	stored, _ := store.MediaUsage("1001")
	config.Activate(config.Config{AuthMode: config.AuthDevFake, UploadsOff: true})
	if rec := uploadAs(t, dir, "tok1", "1001", "1760000401", data); rec.Code != http.StatusServiceUnavailable || rec.Body.String() != diaryMediaOffBody {
		t.Fatalf("upload with the switch off = %d %s", rec.Code, rec.Body.String())
	}
	if got, _ := store.MediaUsage("1001"); got != stored {
		t.Fatalf("switched-off upload changed the rows: %d bytes, want %d", got, stored)
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
	if transparent && w >= 64 && h >= 32 {
		// Solid anchor patches so transparent (alpha=0) and opaque (alpha=255) survival can be verified even after downscaling.
		for y := 0; y < 32; y++ {
			for x := 0; x < 32; x++ {
				img.SetNRGBA(x, y, color.NRGBA{R: 0, G: 0, B: 0, A: 0})
			}
			for x := 32; x < 64; x++ {
				img.SetNRGBA(x, y, color.NRGBA{R: 220, G: 40, B: 60, A: 255})
			}
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestUploadCompressionAndCaps(t *testing.T) {
	twoAccounts(t)
	uploadConfig(t, config.Config{AuthMode: config.AuthDevFake})
	dir := filepath.Join(t.TempDir(), "diary-media")

	// 1. Default JPEG cap (150 KiB): large noisy opaque image (> 150 KiB) compresses to <= 150 KiB as valid JPEG.
	opaque := noisyPNG(t, 360, 360, false)
	if int64(len(opaque)) <= config.DefaultUploadMaxJPEGBytes {
		t.Fatalf("test setup: opaque PNG len = %d, want > %d", len(opaque), config.DefaultUploadMaxJPEGBytes)
	}
	if rec := uploadAs(t, dir, "tok1", "1001", "1760000700", opaque); rec.Code != http.StatusOK {
		t.Fatalf("opaque upload > 150 KiB = %d %s, want 200", rec.Code, rec.Body.String())
	}
	savedOpaque := filepath.Join(dir, store.MediaFileName("1001", "1760000700", "1001_1760000700"))
	info, err := os.Stat(savedOpaque)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() > config.DefaultUploadMaxJPEGBytes {
		t.Fatalf("compressed opaque size = %d, want <= %d", info.Size(), config.DefaultUploadMaxJPEGBytes)
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

	// 2. Default PNG cap (150 KiB): large transparent PNG (> 150 KiB) compresses to <= 150 KiB as valid PNG preserving alpha and opaque pixels.
	trans := noisyPNG(t, 520, 520, true)
	if int64(len(trans)) <= config.DefaultUploadMaxPNGBytes {
		t.Fatalf("test setup: transparent PNG len = %d, want > %d", len(trans), config.DefaultUploadMaxPNGBytes)
	}
	if rec := uploadAs(t, dir, "tok1", "1001", "1760000701", trans); rec.Code != http.StatusOK {
		t.Fatalf("transparent PNG > 150 KiB = %d %s, want 200", rec.Code, rec.Body.String())
	}
	savedTrans := filepath.Join(dir, store.MediaFileName("1001", "1760000701", "1001_1760000701"))
	info, err = os.Stat(savedTrans)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() > config.DefaultUploadMaxPNGBytes {
		t.Fatalf("compressed transparent PNG size = %d, want <= %d", info.Size(), config.DefaultUploadMaxPNGBytes)
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
	b := decoded.Bounds()
	// Sample inside the top-left 32x32 transparent patch and the adjacent 32x32 opaque patch (scaled proportionately).
	scaleX := float64(b.Dx()) / 520.0
	scaleY := float64(b.Dy()) / 520.0
	tx := b.Min.X + int(8*scaleX)
	ty := b.Min.Y + int(8*scaleY)
	ox := b.Min.X + int(48*scaleX)
	oy := b.Min.Y + int(8*scaleY)
	if _, _, _, a := decoded.At(tx, ty).RGBA(); a != 0 {
		t.Fatalf("transparent patch pixel alpha = %d, want 0", a)
	}
	if _, _, _, a := decoded.At(ox, oy).RGBA(); a != 0xffff {
		t.Fatalf("opaque patch pixel alpha = %d, want 0xffff", a)
	}

	// 3. Separate JPEG vs PNG targets are respected independently (JPEG 80 KiB, PNG 250 KiB).
	uploadConfig(t, config.Config{
		AuthMode:              config.AuthDevFake,
		UploadMaxReceiveBytes: 2 << 20,
		UploadMaxJPEGBytes:    80 << 10,
		UploadMaxPNGBytes:     250 << 10,
	})
	mediumTrans := noisyPNG(t, 240, 240, true)
	if int64(len(mediumTrans)) <= 80<<10 || int64(len(mediumTrans)) > 250<<10 {
		t.Fatalf("test setup: medium transparent PNG len = %d, want in (80 KiB, 250 KiB]", len(mediumTrans))
	}
	if rec := uploadAs(t, dir, "tok1", "1001", "1760000702", mediumTrans); rec.Code != http.StatusOK {
		t.Fatalf("transparent PNG under 250 KiB PNG cap = %d %s, want 200", rec.Code, rec.Body.String())
	}
	savedFastPNG, err := os.ReadFile(filepath.Join(dir, store.MediaFileName("1001", "1760000702", "1001_1760000702")))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(savedFastPNG, mediumTrans) {
		t.Fatal("transparent PNG <= maxPNG did not take the untouched fast path")
	}

	mediumOpaque := noisyPNG(t, 260, 260, false)
	if int64(len(mediumOpaque)) <= 80<<10 || int64(len(mediumOpaque)) > 250<<10 {
		t.Fatalf("test setup: medium opaque PNG len = %d, want in (80 KiB, 250 KiB]", len(mediumOpaque))
	}
	if rec := uploadAs(t, dir, "tok1", "1001", "1760000703", mediumOpaque); rec.Code != http.StatusOK {
		t.Fatalf("opaque upload > 80 KiB JPEG cap = %d %s, want 200", rec.Code, rec.Body.String())
	}
	savedJPEG := filepath.Join(dir, store.MediaFileName("1001", "1760000703", "1001_1760000703"))
	info, err = os.Stat(savedJPEG)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() > 80<<10 {
		t.Fatalf("compressed JPEG size = %d, want <= %d", info.Size(), 80<<10)
	}
	f, err = os.Open(savedJPEG)
	if err != nil {
		t.Fatal(err)
	}
	_, format, err = image.Decode(f)
	_ = f.Close()
	if err != nil || format != "jpeg" {
		t.Fatalf("saved JPEG decode = (%q, %v), want jpeg", format, err)
	}

	// 4. Uploads exceeding uploads.max-receive-bytes return 413 before decoding/compression.
	uploadConfig(t, config.Config{
		AuthMode:              config.AuthDevFake,
		UploadMaxReceiveBytes: 1 << 20,
		UploadMaxJPEGBytes:    150 << 10,
		UploadMaxPNGBytes:     150 << 10,
	})
	overReceive := bytes.Repeat([]byte{'x'}, (1<<20)+1)
	req := diaryUploadRequest(t, diaryParamsJSON(t, "1001", "1760000704", "1001_1760000704"), overReceive, "image/png")
	req.Header.Set("Cookie", `AV_AUTH="tok1"`)
	req.ContentLength = -1
	rec := httptest.NewRecorder()
	handleDiaryImageUploadAt(rec, req, dir)
	if rec.Code != http.StatusRequestEntityTooLarge || rec.Body.String() != diaryMediaTooLargeBody {
		t.Fatalf("over max-receive-bytes = %d %s, want 413", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(filepath.Join(dir, store.MediaFileName("1001", "1760000704", "1001_1760000704"))); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("rejected > max-receive-bytes upload created file: %v", err)
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

	// Pre-fill diary (600 KiB) + room (424 KiB) rows, held by another player, so their bytes reach the 1 MiB global cap.
	if err := store.AddMediaFile(store.MediaFile{Kind: store.MediaDiary, Owner: "1009", File: "seed_diary.img", Size: 600 << 10}); err != nil {
		t.Fatal(err)
	}
	if err := store.AddMediaFile(store.MediaFile{Kind: store.MediaRoom, Owner: "1009", File: "seed_room.img", Size: 424 << 10}); err != nil {
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

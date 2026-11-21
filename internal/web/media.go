package web

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"image"
	"image/draw"
	"image/jpeg"
	"image/png"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"cherry/internal/config"
	"cherry/internal/store"
)

const (
	maxDiaryImageSize      int64 = 20 << 20
	maxDiaryUploadSize     int64 = maxDiaryImageSize + (1 << 20)
	diaryMultipartMemLimit       = 1 << 20
	diaryImageMaxDimension       = 16_000
	diaryImageMaxPixels          = 24_000_000
	maxDiaryMediaStorage   int64 = 1 << 30
	roomDownloadPath             = "/lineplay/r/download.nhn"
)

const (
	diaryMediaBadRequestBody = `{"errorCode":"400","errorMessage":"cherry: bad request"}`
	diaryMediaNotFoundBody   = `{"errorCode":"404","errorMessage":"cherry: unknown route"}`
	diaryMediaTooLargeBody   = `{"errorCode":"413","errorMessage":"cherry: request too large"}`
	diaryMediaConflictBody   = `{"errorCode":"409","errorMessage":"cherry: media conflict"}`
	diaryMediaFullBody       = `{"errorCode":"507","errorMessage":"cherry: media storage full"}`
	diaryMediaSaveFailedBody = `{"errorCode":"500","errorMessage":"cherry: save failed"}`
	diaryMediaQuotaBody      = `{"errorCode":"507","errorMessage":"cherry: upload quota reached"}`
	diaryMediaOffBody        = `{"errorCode":"503","errorMessage":"cherry: picture uploads are off"}`
	diaryMediaUnsignedBody   = `{"errorCode":"403","errorMessage":"cherry: sign in to upload"}`
)

var diaryMediaSaveMu sync.Mutex

type mediaKind int

const (
	mediaDiary mediaKind = iota
	mediaRoom
	mediaProfile
)

type diaryImageUploadParams struct {
	Version string `json:"ver"`
	Type    string `json:"type"`
	Name    string `json:"name"`
	UserID  string `json:"userid"`
	OID     string `json:"oid"`
	CTime   string `json:"ctime"`
}

func handleDiaryImageUpload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeDiaryMediaJSON(w, http.StatusNotFound, diaryMediaNotFoundBody)
		return
	}
	dir, err := defaultDiaryMediaDir()
	if err != nil {
		writeDiaryMediaJSON(w, http.StatusInternalServerError, diaryMediaSaveFailedBody)
		return
	}
	handleDiaryImageUploadAt(w, r, dir)
}

func handleDiaryImageDownload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		writeDiaryMediaJSON(w, http.StatusNotFound, diaryMediaNotFoundBody)
		return
	}
	dir, err := defaultDiaryMediaDir()
	if err != nil {
		writeDiaryMediaJSON(w, http.StatusInternalServerError, diaryMediaSaveFailedBody)
		return
	}
	handleDiaryImageDownloadAt(w, r, dir)
}

func handleRoomImageUpload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeDiaryMediaJSON(w, http.StatusNotFound, diaryMediaNotFoundBody)
		return
	}
	dir, err := defaultRoomMediaDir()
	if err != nil {
		writeDiaryMediaJSON(w, http.StatusInternalServerError, diaryMediaSaveFailedBody)
		return
	}
	handleMediaUpload(w, r, dir, maxDiaryMediaStorage, mediaRoom)
}

func handleRoomImageDownload(w http.ResponseWriter, r *http.Request) {
	dir, err := defaultRoomMediaDir()
	if err != nil {
		writeDiaryMediaJSON(w, http.StatusInternalServerError, diaryMediaSaveFailedBody)
		return
	}
	handleMediaDownload(w, r, dir, true)
}

// handleProfileImageUpload serves POST /lineplay/pr/upload.nhn (ReqObsProfileUploadURL
// @0x1bbb1c8: params ver/type/name/userid/oid/ctime with oid = name = userid_ctime). The
// reply body is ignored by ResObsProfileUploadURL @0x1a78bac, so any 200 succeeds.
func handleProfileImageUpload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeDiaryMediaJSON(w, http.StatusNotFound, diaryMediaNotFoundBody)
		return
	}
	dir, err := defaultProfileMediaDir()
	if err != nil {
		writeDiaryMediaJSON(w, http.StatusInternalServerError, diaryMediaSaveFailedBody)
		return
	}
	handleMediaUpload(w, r, dir, maxDiaryMediaStorage, mediaProfile)
}

// handleProfileImageDownload serves GET <obs>/lineplay/pr/<oid> and <obs-cdn>/r/lineplay/pr/<oid>
// (ReqObsProfileAsyncImageDownload @0x1b872e0 and ReqObsOrObsCdnDownload @0x1b7ff80). The client
// caches the bytes as profiles/<oid>.png, so the stored image is served unchanged.
func handleProfileImageDownload(w http.ResponseWriter, r *http.Request) {
	dir, err := defaultProfileMediaDir()
	if err != nil {
		writeDiaryMediaJSON(w, http.StatusInternalServerError, diaryMediaSaveFailedBody)
		return
	}
	handleProfileImageDownloadAt(w, r, dir)
}

func handleProfileImageDownloadAt(w http.ResponseWriter, r *http.Request, dir string) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		writeDiaryMediaJSON(w, http.StatusNotFound, diaryMediaNotFoundBody)
		return
	}
	oid := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
	i := strings.LastIndex(oid, "_")
	if i < 0 || !store.ValidDiaryMediaTuple(oid[:i], oid[i+1:], oid) {
		writeDiaryMediaJSON(w, http.StatusNotFound, diaryMediaNotFoundBody)
		return
	}
	serveMediaFile(w, r, diaryMediaHashPath(dir, oid[:i], oid[i+1:], oid), true)
}

func defaultProfileMediaDir() (string, error) { return store.MediaDir(store.MediaProfile) }

func defaultRoomMediaDir() (string, error) { return store.MediaDir(store.MediaRoom) }

func defaultDiaryMediaDir() (string, error) { return store.MediaDir(store.MediaDiary) }

// The At handlers take an explicit directory so tests can use disposable storage.
func handleDiaryImageUploadAt(w http.ResponseWriter, r *http.Request, dir string) {
	handleDiaryImageUploadAtWithLimit(w, r, dir, maxDiaryMediaStorage)
}

func handleDiaryImageUploadAtWithLimit(w http.ResponseWriter, r *http.Request, dir string, storageLimit int64) {
	handleMediaUpload(w, r, dir, storageLimit, mediaDiary)
}

// handleMediaUpload serves the OBS upload for diary photos, My Room preset thumbnails (mediaRoom) and profile pictures
// (mediaProfile, diary naming, re-uploadable). The owner is the account of the AV_AUTH credential (the AV_AUTH header or
// cookie), never the form's userid: an upload without one is refused in identity modes, and lab-guest keeps the form's
// userid as the owner when no credential is sent. A signed diary or profile upload must name its own aid as userid
// (anti-squatting of keys); the room client's userid is not checked (its form is unverified).
// ponytail: the room client's oid format is unverified, so room mode accepts any printable oid equal to the form name
// instead of requiring userid_ctime.
func handleMediaUpload(w http.ResponseWriter, r *http.Request, dir string, storageLimit int64, kind mediaKind) {
	room := kind == mediaRoom
	if r.Method != http.MethodPost {
		writeDiaryMediaJSON(w, http.StatusNotFound, diaryMediaNotFoundBody)
		return
	}
	if !config.UploadsOn() {
		writeDiaryMediaJSON(w, http.StatusServiceUnavailable, diaryMediaOffBody)
		return
	}
	if dir == "" {
		writeDiaryMediaJSON(w, http.StatusInternalServerError, diaryMediaSaveFailedBody)
		return
	}
	if r.ContentLength > maxDiaryUploadSize {
		writeDiaryMediaJSON(w, http.StatusRequestEntityTooLarge, diaryMediaTooLargeBody)
		return
	}
	signer, signed := store.AccountForUpload(r)
	if !signed {
		if kind == mediaProfile {
			writeDiaryMediaJSON(w, http.StatusBadRequest, diaryMediaBadRequestBody)
			return
		}
		if config.IdentityMode() {
			log.Printf("cherry: picture upload refused kind=%s: no AV_AUTH credential", kindName(kind))
			writeDiaryMediaJSON(w, http.StatusForbidden, diaryMediaUnsignedBody)
			return
		}
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxDiaryUploadSize)
	defer func() {
		if r.MultipartForm != nil {
			_ = r.MultipartForm.RemoveAll()
		}
	}()
	if err := r.ParseMultipartForm(diaryMultipartMemLimit); err != nil {
		if diaryMediaTooLarge(err) {
			writeDiaryMediaJSON(w, http.StatusRequestEntityTooLarge, diaryMediaTooLargeBody)
		} else {
			writeDiaryMediaJSON(w, http.StatusBadRequest, diaryMediaBadRequestBody)
		}
		return
	}
	form := r.MultipartForm
	if form == nil || len(form.Value) != 1 || len(form.Value["params"]) != 1 {
		writeDiaryMediaJSON(w, http.StatusBadRequest, diaryMediaBadRequestBody)
		return
	}
	paramsRaw := form.Value["params"][0]
	if len(paramsRaw) == 0 || len(paramsRaw) > 4096 {
		writeDiaryMediaJSON(w, http.StatusBadRequest, diaryMediaBadRequestBody)
		return
	}
	var params diaryImageUploadParams
	if json.Unmarshal([]byte(paramsRaw), &params) != nil || params.Version != "1.0" || params.Type != "image" || !mediaUploadNameOK(params, room) {
		writeDiaryMediaJSON(w, http.StatusBadRequest, diaryMediaBadRequestBody)
		return
	}
	owner := params.UserID
	if signed {
		if !room && params.UserID != signer.Aid {
			log.Printf("cherry: picture upload refused kind=%s: userid is not the signed account", kindName(kind))
			writeDiaryMediaJSON(w, http.StatusBadRequest, diaryMediaBadRequestBody)
			return
		}
		owner = signer.Aid
	}
	fileHeader, ok := diaryMediaFileHeader(form)
	if !ok {
		writeDiaryMediaJSON(w, http.StatusBadRequest, diaryMediaBadRequestBody)
		return
	}
	if fileHeader.Filename == "" || fileHeader.Size <= 0 {
		writeDiaryMediaJSON(w, http.StatusBadRequest, diaryMediaBadRequestBody)
		return
	}
	if fileHeader.Size > maxDiaryImageSize {
		writeDiaryMediaJSON(w, http.StatusRequestEntityTooLarge, diaryMediaTooLargeBody)
		return
	}
	imageFile, err := fileHeader.Open()
	if err != nil {
		writeDiaryMediaJSON(w, http.StatusBadRequest, diaryMediaBadRequestBody)
		return
	}
	defer imageFile.Close()
	// The image must decode as JPEG or PNG within the dimension and pixel limits: the decoder registry is the magic-byte check.
	if _, _, err := validateDiaryImage(imageFile); err != nil {
		writeDiaryMediaJSON(w, http.StatusBadRequest, diaryMediaBadRequestBody)
		return
	}
	if _, err := imageFile.Seek(0, io.SeekStart); err != nil {
		writeDiaryMediaJSON(w, http.StatusBadRequest, diaryMediaBadRequestBody)
		return
	}
	if _, _, err := image.Decode(imageFile); err != nil {
		writeDiaryMediaJSON(w, http.StatusBadRequest, diaryMediaBadRequestBody)
		return
	}
	if _, err := imageFile.Seek(0, io.SeekStart); err != nil {
		writeDiaryMediaJSON(w, http.StatusBadRequest, diaryMediaBadRequestBody)
		return
	}

	file := store.MediaFileName(params.UserID, params.CTime, params.OID)
	path := filepath.Join(dir, file)
	if err := os.MkdirAll(dir, 0700); err != nil {
		writeDiaryMediaJSON(w, http.StatusInternalServerError, diaryMediaSaveFailedBody)
		return
	}
	if err := os.Chmod(dir, 0700); err != nil {
		writeDiaryMediaJSON(w, http.StatusInternalServerError, diaryMediaSaveFailedBody)
		return
	}
	tmp, err := os.CreateTemp(dir, ".diary-*.tmp")
	if err != nil {
		writeDiaryMediaJSON(w, http.StatusInternalServerError, diaryMediaSaveFailedBody)
		return
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	defer tmp.Close()
	hash := sha256.New()
	written, err := io.Copy(io.MultiWriter(tmp, hash), io.LimitReader(imageFile, maxDiaryImageSize+1))
	if err != nil {
		writeDiaryMediaJSON(w, http.StatusBadRequest, diaryMediaBadRequestBody)
		return
	}
	if written > maxDiaryImageSize {
		writeDiaryMediaJSON(w, http.StatusRequestEntityTooLarge, diaryMediaTooLargeBody)
		return
	}
	if written != fileHeader.Size {
		writeDiaryMediaJSON(w, http.StatusBadRequest, diaryMediaBadRequestBody)
		return
	}
	if err := tmp.Sync(); err != nil {
		writeDiaryMediaJSON(w, http.StatusInternalServerError, diaryMediaSaveFailedBody)
		return
	}
	if err := tmp.Close(); err != nil {
		writeDiaryMediaJSON(w, http.StatusInternalServerError, diaryMediaSaveFailedBody)
		return
	}
	var contentHash [sha256.Size]byte
	copy(contentHash[:], hash.Sum(nil))
	if kind == mediaProfile {
		// The client sends the head image, then the whole-body image, each named userid_<unix
		// seconds> (ReqObsProfileUploadURL @0x1bbb1c8; time(NULL) per call), and saves the first as
		// imageUrl. Both usually land in one second, so the first upload (the head, which every
		// profile screen shows) wins and a same-oid repeat is acknowledged without a write.
		if _, err := os.Stat(path); err == nil {
			writeDiaryMediaJSON(w, http.StatusOK, `{"result":true}`)
			return
		}
	}
	row := store.MediaFile{Kind: kindName(kind), Owner: owner, File: file, Size: written}
	if err := storeDiaryMedia(path, tmpPath, row, contentHash, storageLimit); err != nil {
		var conflict diaryMediaConflictError
		var quota diaryMediaQuotaError
		switch {
		case errors.As(err, &conflict):
			writeDiaryMediaJSON(w, http.StatusConflict, diaryMediaConflictBody)
		case errors.As(err, &quota):
			writeDiaryMediaJSON(w, http.StatusInsufficientStorage, diaryMediaQuotaBody)
		case errors.Is(err, diaryMediaDiskFull):
			writeDiaryMediaJSON(w, http.StatusInsufficientStorage, diaryMediaFullBody)
		default:
			writeDiaryMediaJSON(w, http.StatusInternalServerError, diaryMediaSaveFailedBody)
		}
		return
	}
	if room {
		// ResSaveMyRoomImgToOBS @0x1b08220 scans the reply for "x-obs-oid" and uses the
		// next token as the oid of the thumbnail URL it sends with room/preset/save.
		w.Header()["x-obs-oid"] = []string{params.OID} // literal lowercase key: Set would canonicalize to X-Obs-Oid
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = io.WriteString(w, "x-obs-oid: "+params.OID+"\r\n")
		return
	}
	writeDiaryMediaJSON(w, http.StatusOK, `{"result":true}`)
}

// kindName is the store's name of a picture kind.
func kindName(kind mediaKind) string {
	switch kind {
	case mediaRoom:
		return store.MediaRoom
	case mediaProfile:
		return store.MediaProfile
	}
	return store.MediaDiary
}

// handleRoomImageDelete serves POST /lineplay/r/delete.nhn?oid=..., sent before a
// filled quick-pick slot is overwritten. ponytail: the file is kept (the request
// carries only the oid, not the userid/ctime that key the stored file); the media
// quota bounds growth.
func handleRoomImageDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeDiaryMediaJSON(w, http.StatusNotFound, diaryMediaNotFoundBody)
		return
	}
	writeDiaryMediaJSON(w, http.StatusOK, `{"result":true}`)
}

func mediaUploadNameOK(p diaryImageUploadParams, room bool) bool {
	if room {
		return p.Name == p.OID && store.ValidDiaryMediaUserID(p.OID) && store.ValidDiaryMediaUserID(p.UserID) && store.ValidDiaryMediaCTime(p.CTime)
	}
	return p.Name == p.UserID+"_"+p.CTime && store.ValidDiaryMediaTuple(p.UserID, p.CTime, p.OID)
}

func diaryMediaFileHeader(form *multipart.Form) (*multipart.FileHeader, bool) {
	if form == nil || len(form.File) != 1 {
		return nil, false
	}
	files, ok := form.File["filedata"]
	if !ok {
		files, ok = form.File["Filedata"]
	}
	if !ok || len(files) != 1 {
		return nil, false
	}
	return files[0], true
}

func handleDiaryImageDownloadAt(w http.ResponseWriter, r *http.Request, dir string) {
	handleMediaDownload(w, r, dir, false)
}

func handleMediaDownload(w http.ResponseWriter, r *http.Request, dir string, room bool) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		writeDiaryMediaJSON(w, http.StatusNotFound, diaryMediaNotFoundBody)
		return
	}
	if dir == "" {
		writeDiaryMediaJSON(w, http.StatusInternalServerError, diaryMediaSaveFailedBody)
		return
	}
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		writeDiaryMediaJSON(w, http.StatusBadRequest, diaryMediaBadRequestBody)
		return
	}
	userID, userOK := store.OneDiaryMediaQueryValue(query, "userid")
	ctime, ctimeOK := store.OneDiaryMediaQueryValue(query, "ctime")
	oid, oidOK := store.OneDiaryMediaQueryValue(query, "oid")
	tid, tidOK := store.OptionalDiaryMediaQueryValue(query, "tid")
	if !userOK || !ctimeOK || !oidOK || !tidOK || !mediaUploadNameOK(diaryImageUploadParams{Name: oid, UserID: userID, OID: oid, CTime: ctime}, room) {
		writeDiaryMediaJSON(w, http.StatusBadRequest, diaryMediaBadRequestBody)
		return
	}
	if room {
		if r.URL.Path != roomDownloadPath || tid != "" && !store.ValidDiaryMediaTID(tid) {
			writeDiaryMediaJSON(w, http.StatusNotFound, diaryMediaNotFoundBody)
			return
		}
	} else if !store.ValidDiaryMediaVariant(r.URL.Path, tid) {
		writeDiaryMediaJSON(w, http.StatusNotFound, diaryMediaNotFoundBody)
		return
	}
	serveMediaFile(w, r, diaryMediaHashPath(dir, userID, ctime, oid), room)
}

// serveMediaFile validates and serves one stored image (diary PNGs are flattened to JPEG).
func serveMediaFile(w http.ResponseWriter, r *http.Request, path string, keepFormat bool) {
	// A picture of a deleted avatar is not served to the game (2026-11-21: the staff panel keeps it; the game answered 404 for it
	// before the records were kept, and still does).
	if store.PictureOfDeletedAvatar(filepath.Base(path)) {
		writeDiaryMediaJSON(w, http.StatusNotFound, diaryMediaNotFoundBody)
		return
	}
	file, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			writeDiaryMediaJSON(w, http.StatusNotFound, diaryMediaNotFoundBody)
		} else {
			writeDiaryMediaJSON(w, http.StatusInternalServerError, diaryMediaSaveFailedBody)
		}
		return
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		writeDiaryMediaJSON(w, http.StatusInternalServerError, diaryMediaSaveFailedBody)
		return
	}
	if !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > maxDiaryImageSize {
		writeDiaryMediaJSON(w, http.StatusNotFound, diaryMediaNotFoundBody)
		return
	}
	contentType, _, err := validateDiaryImage(file)
	if err != nil {
		writeDiaryMediaJSON(w, http.StatusNotFound, diaryMediaNotFoundBody)
		return
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		writeDiaryMediaJSON(w, http.StatusInternalServerError, diaryMediaSaveFailedBody)
		return
	}
	if !keepFormat && contentType == "image/png" {
		// The client caches diary downloads as <oid>.jpg and cocos2d-x picks the decoder from
		// the extension, so PNG bytes (e.g. the comic photozone render) never display.
		img, err := png.Decode(file)
		if err != nil {
			writeDiaryMediaJSON(w, http.StatusNotFound, diaryMediaNotFoundBody)
			return
		}
		flat := image.NewRGBA(img.Bounds())
		draw.Draw(flat, flat.Bounds(), image.White, image.Point{}, draw.Src)
		draw.Draw(flat, flat.Bounds(), img, img.Bounds().Min, draw.Over)
		var buf bytes.Buffer
		if err := jpeg.Encode(&buf, flat, &jpeg.Options{Quality: 90}); err != nil {
			writeDiaryMediaJSON(w, http.StatusInternalServerError, diaryMediaSaveFailedBody)
			return
		}
		w.Header().Set("Content-Type", "image/jpeg")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		http.ServeContent(w, r, "", info.ModTime(), bytes.NewReader(buf.Bytes()))
		return
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	http.ServeContent(w, r, "", info.ModTime(), file)
}

func diaryMediaPath(dir, userID, ctime, oid string) (string, bool) {
	if !store.ValidDiaryMediaTuple(userID, ctime, oid) {
		return "", false
	}
	return diaryMediaHashPath(dir, userID, ctime, oid), true
}

func diaryMediaHashPath(dir, userID, ctime, oid string) string {
	return filepath.Join(dir, store.MediaFileName(userID, ctime, oid))
}

func validateDiaryImage(src io.Reader) (string, image.Config, error) {
	config, format, err := image.DecodeConfig(src)
	if err != nil || config.Width <= 0 || config.Height <= 0 || config.Width > diaryImageMaxDimension || config.Height > diaryImageMaxDimension || config.Width > diaryImageMaxPixels/config.Height {
		return "", image.Config{}, errors.New("invalid diary image")
	}
	switch format {
	case "jpeg":
		return "image/jpeg", config, nil
	case "png":
		return "image/png", config, nil
	default:
		return "", image.Config{}, errors.New("unsupported diary image format")
	}
}

func diaryMediaTooLarge(err error) bool {
	var maxErr *http.MaxBytesError
	return errors.As(err, &maxErr) || errors.Is(err, multipart.ErrMessageTooLarge)
}

// diaryMediaDiskFull is the folder's disk cap (1 GiB per kind, all owners); an owner's quota is diaryMediaQuotaError.
var diaryMediaDiskFull = errors.New("media folder full")

// storeDiaryMedia moves the uploaded file to path and records it under its owner. Writes are serialised. A same-content repeat
// of a stored key is acknowledged (a file with no row is adopted by this owner); different content is a conflict, and so is a
// key stored under another owner. A new file must fit the owner's quota (files and bytes of its kind) and the folder's cap.
func storeDiaryMedia(path, tmpPath string, row store.MediaFile, contentHash [sha256.Size]byte, storageLimit int64) error {
	diaryMediaSaveMu.Lock()
	defer diaryMediaSaveMu.Unlock()
	info, err := os.Lstat(path)
	if err == nil {
		if info.Mode().IsRegular() && info.Size() == row.Size {
			same, hashErr := diaryMediaFilesEqual(path, row.Size, contentHash)
			if hashErr != nil {
				return hashErr
			}
			if same {
				stored, found, err := store.MediaRowByFile(row.File)
				if err != nil {
					return err
				}
				if !found {
					return store.AddMediaFile(row)
				}
				if stored.Owner != row.Owner {
					return diaryMediaConflictError{}
				}
				return nil
			}
		}
		return diaryMediaConflictError{}
	}
	if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	files, bytes, err := store.MediaUsage(row.Kind, row.Owner)
	if err != nil {
		return err
	}
	maxBytes, maxFiles := config.UploadLimits()
	if files+1 > maxFiles || bytes+row.Size > maxBytes {
		return diaryMediaQuotaError{}
	}
	tooLarge, err := diaryMediaStorageWouldExceed(filepath.Dir(path), row.Size, storageLimit)
	if err != nil {
		return err
	}
	if tooLarge {
		return diaryMediaDiskFull
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return err
	}
	if err := store.AddMediaFile(row); err != nil {
		os.Remove(path)
		return err
	}
	return nil
}

type diaryMediaConflictError struct{}

func (diaryMediaConflictError) Error() string {
	return "diary media already exists with different content"
}

// diaryMediaQuotaError is an owner's quota: its files or its bytes of the kind; diaryMediaFullBody is the folder's disk cap.
type diaryMediaQuotaError struct{}

func (diaryMediaQuotaError) Error() string { return "picture upload quota reached" }

// ponytail: quota accounting scans O(file count); add indexed accounting if uploads make the scan slow.
func diaryMediaStorageWouldExceed(dir string, incoming, limit int64) (bool, error) {
	if incoming < 0 || limit < 0 || incoming > limit {
		return true, nil
	}
	remaining := limit - incoming
	dirFile, err := os.Open(dir)
	if err != nil {
		return false, err
	}
	defer dirFile.Close()
	for {
		entries, readErr := dirFile.ReadDir(256)
		for _, entry := range entries {
			if filepath.Ext(entry.Name()) != ".img" {
				continue
			}
			info, err := entry.Info()
			if err != nil {
				return false, err
			}
			if !info.Mode().IsRegular() {
				continue
			}
			if info.Size() > remaining {
				return true, nil
			}
			remaining -= info.Size()
		}
		if errors.Is(readErr, io.EOF) {
			return false, nil
		}
		if readErr != nil {
			return false, readErr
		}
	}
}

func diaryMediaFilesEqual(path string, size int64, want [sha256.Size]byte) (bool, error) {
	file, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer file.Close()
	hash := sha256.New()
	written, err := io.Copy(hash, io.LimitReader(file, maxDiaryImageSize+1))
	if err != nil {
		return false, err
	}
	var got [sha256.Size]byte
	copy(got[:], hash.Sum(nil))
	return written == size && got == want, nil
}

func writeDiaryMediaJSON(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, body)
}

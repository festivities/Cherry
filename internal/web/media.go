package web

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	"image/png"
	"io"
	"log"
	"math"
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
	diaryMultipartMemLimit       = 1 << 20
	diaryImageMaxDimension       = 16_000
	diaryImageMaxPixels          = 24_000_000
	maxDiaryMediaStorage   int64 = config.DefaultUploadMaxTotalBytes
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

var (
	diaryMediaSaveMu sync.Mutex
	mediaCompressSem = make(chan struct{}, 2)
)

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
	dirs, err := allMediaDirs()
	if err != nil {
		writeDiaryMediaJSON(w, http.StatusInternalServerError, diaryMediaSaveFailedBody)
		return
	}
	handleMediaUploadIn(w, r, dir, dirs, store.UploadMaxTotalBytes(config.UploadMaxTotalBytes()), mediaDiary)
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
	dirs, err := allMediaDirs()
	if err != nil {
		writeDiaryMediaJSON(w, http.StatusInternalServerError, diaryMediaSaveFailedBody)
		return
	}
	handleMediaUploadIn(w, r, dir, dirs, store.UploadMaxTotalBytes(config.UploadMaxTotalBytes()), mediaRoom)
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
	dirs, err := allMediaDirs()
	if err != nil {
		writeDiaryMediaJSON(w, http.StatusInternalServerError, diaryMediaSaveFailedBody)
		return
	}
	handleMediaUploadIn(w, r, dir, dirs, store.UploadMaxTotalBytes(config.UploadMaxTotalBytes()), mediaProfile)
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

func allMediaDirs() ([]string, error) {
	diary, err := defaultDiaryMediaDir()
	if err != nil {
		return nil, err
	}
	room, err := defaultRoomMediaDir()
	if err != nil {
		return nil, err
	}
	profile, err := defaultProfileMediaDir()
	if err != nil {
		return nil, err
	}
	return []string{diary, room, profile}, nil
}

// The At handlers take an explicit directory so tests can use disposable storage.
func handleDiaryImageUploadAt(w http.ResponseWriter, r *http.Request, dir string) {
	handleDiaryImageUploadAtWithLimit(w, r, dir, store.UploadMaxTotalBytes(config.UploadMaxTotalBytes()))
}

func handleDiaryImageUploadAtWithLimit(w http.ResponseWriter, r *http.Request, dir string, storageLimit int64) {
	handleMediaUpload(w, r, dir, storageLimit, mediaDiary)
}

func handleMediaUpload(w http.ResponseWriter, r *http.Request, dir string, storageLimit int64, kind mediaKind) {
	handleMediaUploadIn(w, r, dir, []string{dir}, storageLimit, kind)
}

// handleMediaUploadIn serves the OBS upload for diary photos, My Room preset thumbnails (mediaRoom) and profile pictures
// (mediaProfile, diary naming, re-uploadable), checking storageLimit across storageDirs. The owner is the account of the AV_AUTH credential (the AV_AUTH header or
// cookie), never the form's userid: an upload without one is refused in identity modes, and lab-guest keeps the form's
// userid as the owner when no credential is sent. A signed diary or profile upload must name its own aid as userid
// (anti-squatting of keys); the room client's userid is not checked (its form is unverified).
// ponytail: the room client's oid format is unverified, so room mode accepts any printable oid equal to the form name
// instead of requiring userid_ctime.
func handleMediaUploadIn(w http.ResponseWriter, r *http.Request, dir string, storageDirs []string, storageLimit int64, kind mediaKind) {
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
	maxFile := store.UploadMaxFileBytes(config.UploadMaxFileBytes())
	maxReceive := max(maxDiaryImageSize, maxFile)
	maxUpload := maxReceive + (1 << 20)
	if r.ContentLength > maxUpload {
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
	r.Body = http.MaxBytesReader(w, r.Body, maxUpload)
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
	if fileHeader.Size > maxReceive {
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
	img, format, err := image.Decode(imageFile)
	if err != nil {
		writeDiaryMediaJSON(w, http.StatusBadRequest, diaryMediaBadRequestBody)
		return
	}
	var compressed []byte
	if fileHeader.Size > maxFile {
		var fit bool
		compressed, fit = compressMediaImageGuarded(img, format, maxFile)
		if !fit {
			writeDiaryMediaJSON(w, http.StatusRequestEntityTooLarge, diaryMediaTooLargeBody)
			return
		}
	} else if _, err := imageFile.Seek(0, io.SeekStart); err != nil {
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
	var written int64
	if compressed != nil {
		written, err = io.Copy(io.MultiWriter(tmp, hash), bytes.NewReader(compressed))
		if err != nil {
			writeDiaryMediaJSON(w, http.StatusInternalServerError, diaryMediaSaveFailedBody)
			return
		}
	} else {
		written, err = io.Copy(io.MultiWriter(tmp, hash), io.LimitReader(imageFile, maxReceive+1))
		if err != nil {
			writeDiaryMediaJSON(w, http.StatusBadRequest, diaryMediaBadRequestBody)
			return
		}
		if written > maxReceive || written > maxFile {
			writeDiaryMediaJSON(w, http.StatusRequestEntityTooLarge, diaryMediaTooLargeBody)
			return
		}
		if written != fileHeader.Size {
			writeDiaryMediaJSON(w, http.StatusBadRequest, diaryMediaBadRequestBody)
			return
		}
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
	if err := storeDiaryMedia(path, tmpPath, row, contentHash, storageDirs, storageLimit); err != nil {
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
	if !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > store.UploadMaxFileBytesMax {
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

// diaryMediaDiskFull is the server's global photo storage cap across all kinds (or a single directory in tests); an owner's quota is diaryMediaQuotaError.
var diaryMediaDiskFull = errors.New("media folder full")

// storeDiaryMedia moves the uploaded file to path and records it under its owner. Writes are serialised. A same-content repeat
// of a stored key is acknowledged (a file with no row is adopted by this owner); different content is a conflict, and so is a
// key stored under another owner. A new file must fit the owner's quota (files and bytes of its kind) and the storage cap across storageDirs.
func storeDiaryMedia(path, tmpPath string, row store.MediaFile, contentHash [sha256.Size]byte, storageDirs []string, storageLimit int64) error {
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
	tooLarge, err := diaryMediaStorageWouldExceed(storageDirs, row.Size, storageLimit)
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
func diaryMediaStorageWouldExceed(dirs []string, incoming, limit int64) (bool, error) {
	if incoming < 0 || limit < 0 || incoming > limit {
		return true, nil
	}
	remaining := limit - incoming
	for _, dir := range dirs {
		next, exceed, err := scanMediaDirRemaining(dir, remaining)
		if err != nil || exceed {
			return exceed, err
		}
		remaining = next
	}
	return false, nil
}

func scanMediaDirRemaining(dir string, remaining int64) (int64, bool, error) {
	dirFile, err := os.Open(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return remaining, false, nil
		}
		return 0, false, err
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
				return 0, false, err
			}
			if !info.Mode().IsRegular() {
				continue
			}
			if info.Size() > remaining {
				return 0, true, nil
			}
			remaining -= info.Size()
		}
		if errors.Is(readErr, io.EOF) {
			return remaining, false, nil
		}
		if readErr != nil {
			return 0, false, readErr
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
	written, err := io.Copy(hash, io.LimitReader(file, size+1))
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

func compressMediaImageGuarded(img image.Image, format string, maxBytes int64) ([]byte, bool) {
	mediaCompressSem <- struct{}{}
	defer func() { <-mediaCompressSem }()
	return compressMediaImage(img, format, maxBytes)
}

// compressMediaImage re-encodes (and adaptively downscales if needed) img to fit within maxBytes.
// Opaque images encode as JPEG; PNGs with any non-opaque alpha pixel preserve PNG transparency.
func compressMediaImage(img image.Image, format string, maxBytes int64) ([]byte, bool) {
	adaptiveDims := func(cur image.Image, lastLen int, safety float64) (int, int, bool) {
		if lastLen <= 0 {
			return 0, 0, false
		}
		scale := math.Sqrt(float64(maxBytes)/float64(lastLen)) * safety
		if scale < 0.5 {
			scale = 0.5
		} else if scale > 0.85 {
			scale = 0.85
		}
		b := cur.Bounds()
		nw := int(math.Round(float64(b.Dx()) * scale))
		nh := int(math.Round(float64(b.Dy()) * scale))
		if nw < 64 || nh < 64 {
			return 0, 0, false
		}
		return nw, nh, true
	}

	cur := img
	if format == "png" && hasTransparency(img) {
		enc := png.Encoder{CompressionLevel: png.BestCompression}
		encodePNG := func(src image.Image) ([]byte, int, bool) {
			var buf bytes.Buffer
			if err := enc.Encode(&buf, src); err != nil {
				return nil, 0, false
			}
			return buf.Bytes(), buf.Len(), int64(buf.Len()) <= maxBytes
		}
		out, lastLen, ok := encodePNG(cur)
		if ok {
			return out, true
		}
		for round := 0; round < 5; round++ {
			nw, nh, canScale := adaptiveDims(cur, lastLen, 0.90)
			if !canScale {
				break
			}
			cur = downscaleBilinear(cur, nw, nh)
			out, nextLen, ok := encodePNG(cur)
			if ok {
				return out, true
			}
			if nextLen == 0 {
				break
			}
			lastLen = nextLen
		}
		return nil, false
	}

	encodeJPEG := func(src image.Image, q int) ([]byte, int, bool) {
		var buf bytes.Buffer
		if err := jpeg.Encode(&buf, src, &jpeg.Options{Quality: q}); err != nil {
			return nil, 0, false
		}
		return buf.Bytes(), buf.Len(), int64(buf.Len()) <= maxBytes
	}
	tryJPEG := func(src image.Image, qualities ...int) ([]byte, int, bool) {
		var lastLen int
		for _, q := range qualities {
			out, n, ok := encodeJPEG(src, q)
			if ok {
				return out, n, true
			}
			if n > 0 {
				lastLen = n
			}
		}
		return nil, lastLen, false
	}

	out, lastLen, ok := tryJPEG(cur, 85, 75)
	if ok {
		return out, true
	}
	for round := 0; round < 4; round++ {
		nw, nh, canScale := adaptiveDims(cur, lastLen, 0.92)
		if !canScale {
			break
		}
		cur = downscaleBilinear(cur, nw, nh)
		out, nextLen, ok := tryJPEG(cur, 82, 72)
		if ok {
			return out, true
		}
		if nextLen == 0 {
			break
		}
		lastLen = nextLen
	}
	if out, _, ok := tryJPEG(cur, 60, 45); ok {
		return out, true
	}
	b := cur.Bounds()
	if nw, nh := (b.Dx()*3)/4, (b.Dy()*3)/4; nw >= 64 && nh >= 64 {
		cur = downscaleBilinear(cur, nw, nh)
		if out, _, ok := tryJPEG(cur, 45); ok {
			return out, true
		}
	}
	return nil, false
}

func hasTransparency(img image.Image) bool {
	if op, ok := img.(interface{ Opaque() bool }); ok && op.Opaque() {
		return false
	}
	b := img.Bounds()
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			_, _, _, a := img.At(x, y).RGBA()
			if a != 0xffff {
				return true
			}
		}
	}
	return false
}

func downscaleBilinear(src image.Image, dstW, dstH int) image.Image {
	b := src.Bounds()
	srcW, srcH := b.Dx(), b.Dy()
	dst := image.NewNRGBA(image.Rect(0, 0, dstW, dstH))
	for y := 0; y < dstH; y++ {
		sy := (float64(y)+0.5)*float64(srcH)/float64(dstH) - 0.5
		if sy < 0 {
			sy = 0
		} else if sy > float64(srcH-1) {
			sy = float64(srcH - 1)
		}
		y0 := int(math.Floor(sy))
		y1 := y0 + 1
		if y1 >= srcH {
			y1 = srcH - 1
		}
		fy := sy - float64(y0)
		for x := 0; x < dstW; x++ {
			sx := (float64(x)+0.5)*float64(srcW)/float64(dstW) - 0.5
			if sx < 0 {
				sx = 0
			} else if sx > float64(srcW-1) {
				sx = float64(srcW - 1)
			}
			x0 := int(math.Floor(sx))
			x1 := x0 + 1
			if x1 >= srcW {
				x1 = srcW - 1
			}
			fx := sx - float64(x0)

			r00, g00, b00, a00 := src.At(b.Min.X+x0, b.Min.Y+y0).RGBA()
			r10, g10, b10, a10 := src.At(b.Min.X+x1, b.Min.Y+y0).RGBA()
			r01, g01, b01, a01 := src.At(b.Min.X+x0, b.Min.Y+y1).RGBA()
			r11, g11, b11, a11 := src.At(b.Min.X+x1, b.Min.Y+y1).RGBA()

			w00 := (1 - fx) * (1 - fy)
			w10 := fx * (1 - fy)
			w01 := (1 - fx) * fy
			w11 := fx * fy

			rf := float64(r00)*w00 + float64(r10)*w10 + float64(r01)*w01 + float64(r11)*w11
			gf := float64(g00)*w00 + float64(g10)*w10 + float64(g01)*w01 + float64(g11)*w11
			bf := float64(b00)*w00 + float64(b10)*w10 + float64(b01)*w01 + float64(b11)*w11
			af := float64(a00)*w00 + float64(a10)*w10 + float64(a01)*w01 + float64(a11)*w11

			if af < 1 {
				dst.SetNRGBA(x, y, color.NRGBA{})
				continue
			}
			a8 := uint8(math.Min(255, math.Round(af*255/65535)))
			r8 := uint8(math.Min(255, math.Round(rf*255/af)))
			g8 := uint8(math.Min(255, math.Round(gf*255/af)))
			b8 := uint8(math.Min(255, math.Round(bf*255/af)))
			dst.SetNRGBA(x, y, color.NRGBA{R: r8, G: g8, B: b8, A: a8})
		}
	}
	return dst
}

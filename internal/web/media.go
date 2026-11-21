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
	"slices"
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
	maxReceiveCfg := store.UploadMaxReceiveBytes(config.UploadMaxReceiveBytes())
	maxJPEG := store.UploadMaxJPEGBytes(config.UploadMaxJPEGBytes())
	maxPNG := store.UploadMaxPNGBytes(config.UploadMaxPNGBytes())
	maxReceive := max(maxReceiveCfg, maxJPEG, maxPNG)
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
	isTransparentPNG := format == "png" && hasTransparency(img)
	targetBytes := maxJPEG
	if isTransparentPNG {
		targetBytes = maxPNG
	}
	var compressed []byte
	if fileHeader.Size > targetBytes {
		var fit bool
		compressed, fit = compressMediaImageGuarded(img, isTransparentPNG, targetBytes)
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
		if written > maxReceive || written > targetBytes {
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
	if !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > store.UploadMaxReceiveBytesMax {
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

func compressMediaImageGuarded(img image.Image, transparent bool, targetBytes int64) ([]byte, bool) {
	mediaCompressSem <- struct{}{}
	defer func() { <-mediaCompressSem }()
	return compressMediaImage(img, transparent, targetBytes)
}

// compressMediaImage re-encodes (and adaptively downscales if needed) img to fit within targetBytes.
// Opaque images encode as JPEG; transparent PNGs preserve PNG transparency using lossless RGBA, PNG-8 median-cut quantization, and area downscaling.
func compressMediaImage(img image.Image, transparent bool, targetBytes int64) ([]byte, bool) {
	adaptiveDims := func(cur image.Image, lastLen int, safety, minScale, maxScale float64) (int, int, bool) {
		if lastLen <= 0 {
			return 0, 0, false
		}
		scale := math.Sqrt(float64(targetBytes)/float64(lastLen)) * safety
		if scale < minScale {
			scale = minScale
		} else if scale > maxScale {
			scale = maxScale
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
	if transparent {
		enc := png.Encoder{CompressionLevel: png.BestCompression}
		encodePNG := func(src image.Image) ([]byte, int, bool) {
			var buf bytes.Buffer
			if err := enc.Encode(&buf, src); err != nil {
				return nil, 0, false
			}
			return buf.Bytes(), buf.Len(), int64(buf.Len()) <= targetBytes
		}
		// 1. Native resolution lossless PNG.
		out, lastLen, ok := encodePNG(cur)
		if ok {
			return out, true
		}
		// 2. Native resolution PNG-8 with alpha.
		if out, palLen, ok := encodePNG(quantizeMedianCut(cur, 256)); ok {
			return out, true
		} else if palLen > 0 {
			lastLen = palLen
		}
		// 3. Adaptive area downscale + PNG-8.
		for round := 0; round < 5; round++ {
			nw, nh, canScale := adaptiveDims(cur, lastLen, 0.92, 0.5, 0.85)
			if !canScale {
				break
			}
			cur = downscaleArea(cur, nw, nh)
			out, nextLen, ok := encodePNG(quantizeMedianCut(cur, 256))
			if ok {
				return out, true
			}
			if nextLen == 0 {
				break
			}
			lastLen = nextLen
		}
		// 4. Last resort: 128-color PNG-8 on current scaled image.
		if out, _, ok := encodePNG(quantizeMedianCut(cur, 128)); ok {
			return out, true
		}
		return nil, false
	}

	encodeJPEG := func(src image.Image, q int) ([]byte, int, bool) {
		var buf bytes.Buffer
		if err := jpeg.Encode(&buf, src, &jpeg.Options{Quality: q}); err != nil {
			return nil, 0, false
		}
		return buf.Bytes(), buf.Len(), int64(buf.Len()) <= targetBytes
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

	// 1. Native resolution, high quality.
	out, lastLen, ok := tryJPEG(cur, 85, 78)
	if ok {
		return out, true
	}
	// 2. Adaptive area downscale + highest quality in [65, 85] that fits.
	for round := 0; round < 5; round++ {
		nw, nh, canScale := adaptiveDims(cur, lastLen, 0.94, 0.45, 0.85)
		if !canScale {
			break
		}
		cur = downscaleArea(cur, nw, nh)
		if out80, len80, ok80 := encodeJPEG(cur, 80); ok80 {
			if out85, _, ok85 := encodeJPEG(cur, 85); ok85 {
				return out85, true
			}
			return out80, true
		} else {
			out72, len72, ok72 := encodeJPEG(cur, 72)
			if ok72 {
				return out72, true
			}
			out65, len65, ok65 := encodeJPEG(cur, 65)
			if ok65 {
				return out65, true
			}
			switch {
			case len65 > 0:
				lastLen = len65
			case len72 > 0:
				lastLen = len72
			case len80 > 0:
				lastLen = len80
			default:
				return nil, false
			}
		}
	}
	// 3. Last resort on current scaled image + one optional 0.75x downscale at Q=45.
	if out, _, ok := tryJPEG(cur, 55, 45, 35); ok {
		return out, true
	}
	b := cur.Bounds()
	if nw, nh := (b.Dx()*3)/4, (b.Dy()*3)/4; nw >= 64 && nh >= 64 {
		cur = downscaleArea(cur, nw, nh)
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

// rgbaPixelReader returns a fast, allocation-free 16-bit premultiplied RGBA reader over 0-based (x, y) offsets into src.
func rgbaPixelReader(src image.Image) func(x, y int) (r, g, b, a uint32) {
	bounds := src.Bounds()
	ox, oy := bounds.Min.X, bounds.Min.Y
	switch s := src.(type) {
	case *image.NRGBA:
		return func(x, y int) (uint32, uint32, uint32, uint32) {
			off := s.PixOffset(ox+x, oy+y)
			a8 := uint32(s.Pix[off+3])
			if a8 == 0 {
				return 0, 0, 0, 0
			}
			a := a8 * 0x101
			if a8 == 255 {
				return uint32(s.Pix[off]) * 0x101, uint32(s.Pix[off+1]) * 0x101, uint32(s.Pix[off+2]) * 0x101, a
			}
			r := (uint32(s.Pix[off]) * 0x101 * a8) / 255
			g := (uint32(s.Pix[off+1]) * 0x101 * a8) / 255
			b := (uint32(s.Pix[off+2]) * 0x101 * a8) / 255
			return r, g, b, a
		}
	case *image.RGBA:
		return func(x, y int) (uint32, uint32, uint32, uint32) {
			off := s.PixOffset(ox+x, oy+y)
			return uint32(s.Pix[off]) * 0x101, uint32(s.Pix[off+1]) * 0x101, uint32(s.Pix[off+2]) * 0x101, uint32(s.Pix[off+3]) * 0x101
		}
	case *image.YCbCr:
		return func(x, y int) (uint32, uint32, uint32, uint32) {
			yy := s.Y[s.YOffset(ox+x, oy+y)]
			ci := s.COffset(ox+x, oy+y)
			r, g, b := color.YCbCrToRGB(yy, s.Cb[ci], s.Cr[ci])
			return uint32(r) * 0x101, uint32(g) * 0x101, uint32(b) * 0x101, 0xffff
		}
	default:
		return func(x, y int) (uint32, uint32, uint32, uint32) {
			return src.At(ox+x, oy+y).RGBA()
		}
	}
}

// downscaleArea resamples src to dstW x dstH using area-average (box) filtering in premultiplied RGBA space.
func downscaleArea(src image.Image, dstW, dstH int) image.Image {
	b := src.Bounds()
	srcW, srcH := b.Dx(), b.Dy()
	if dstW <= 0 {
		dstW = 1
	}
	if dstH <= 0 {
		dstH = 1
	}
	dst := image.NewNRGBA(image.Rect(0, 0, dstW, dstH))
	if srcW <= 0 || srcH <= 0 {
		return dst
	}
	readAt := rgbaPixelReader(src)

	type span struct {
		i0, i1 int
		s0, s1 float64
	}
	xSpans := make([]span, dstW)
	for dx := 0; dx < dstW; dx++ {
		sx0 := float64(dx) * float64(srcW) / float64(dstW)
		sx1 := float64(dx+1) * float64(srcW) / float64(dstW)
		ix0 := int(math.Floor(sx0))
		if ix0 < 0 {
			ix0 = 0
		}
		ix1 := int(math.Ceil(sx1)) - 1
		if ix1 < ix0 {
			ix1 = ix0
		}
		if ix1 >= srcW {
			ix1 = srcW - 1
		}
		xSpans[dx] = span{i0: ix0, i1: ix1, s0: sx0, s1: sx1}
	}

	for dy := 0; dy < dstH; dy++ {
		sy0 := float64(dy) * float64(srcH) / float64(dstH)
		sy1 := float64(dy+1) * float64(srcH) / float64(dstH)
		iy0 := int(math.Floor(sy0))
		if iy0 < 0 {
			iy0 = 0
		}
		iy1 := int(math.Ceil(sy1)) - 1
		if iy1 < iy0 {
			iy1 = iy0
		}
		if iy1 >= srcH {
			iy1 = srcH - 1
		}
		for dx := 0; dx < dstW; dx++ {
			xs := xSpans[dx]
			var rSum, gSum, bSum, aSum, wSum float64
			for iy := iy0; iy <= iy1; iy++ {
				wy := math.Min(float64(iy+1), sy1) - math.Max(float64(iy), sy0)
				if wy <= 0 {
					continue
				}
				for ix := xs.i0; ix <= xs.i1; ix++ {
					wx := math.Min(float64(ix+1), xs.s1) - math.Max(float64(ix), xs.s0)
					if wx <= 0 {
						continue
					}
					w := wx * wy
					r, g, b, a := readAt(ix, iy)
					rSum += float64(r) * w
					gSum += float64(g) * w
					bSum += float64(b) * w
					aSum += float64(a) * w
					wSum += w
				}
			}
			if wSum <= 0 {
				continue
			}
			aAvg := aSum / wSum
			if aAvg < 1 {
				dst.SetNRGBA(dx, dy, color.NRGBA{})
				continue
			}
			a8 := uint8(math.Min(255, math.Round(aAvg*255/65535)))
			if a8 == 0 {
				dst.SetNRGBA(dx, dy, color.NRGBA{})
				continue
			}
			rAvg := rSum / wSum
			gAvg := gSum / wSum
			bAvg := bSum / wSum
			r8 := uint8(math.Min(255, math.Round(rAvg*255/aAvg)))
			g8 := uint8(math.Min(255, math.Round(gAvg*255/aAvg)))
			b8 := uint8(math.Min(255, math.Round(bAvg*255/aAvg)))
			dst.SetNRGBA(dx, dy, color.NRGBA{R: r8, G: g8, B: b8, A: a8})
		}
	}
	return dst
}

type mcBucket struct {
	r, g, b, a uint8
	count      int
}

type mcAccum struct {
	sumR, sumG, sumB, sumA uint64
	count                  int
}

func nrgbaPixels(img image.Image) ([]color.NRGBA, int, int, int, int) {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	pixels := make([]color.NRGBA, w*h)
	var nOpaque, nSemi int
	if src, ok := img.(*image.NRGBA); ok {
		for y := 0; y < h; y++ {
			rowOff := src.PixOffset(b.Min.X, b.Min.Y+y)
			dstRow := y * w
			for x := 0; x < w; x++ {
				off := rowOff + x*4
				c := color.NRGBA{R: src.Pix[off], G: src.Pix[off+1], B: src.Pix[off+2], A: src.Pix[off+3]}
				pixels[dstRow+x] = c
				if c.A == 255 {
					nOpaque++
				} else if c.A > 0 {
					nSemi++
				}
			}
		}
		return pixels, w, h, nOpaque, nSemi
	}
	for y := 0; y < h; y++ {
		dstRow := y * w
		for x := 0; x < w; x++ {
			c := color.NRGBAModel.Convert(img.At(b.Min.X+x, b.Min.Y+y)).(color.NRGBA)
			pixels[dstRow+x] = c
			if c.A == 255 {
				nOpaque++
			} else if c.A > 0 {
				nSemi++
			}
		}
	}
	return pixels, w, h, nOpaque, nSemi
}

func medianCutBuckets(buckets []mcBucket, maxBoxes int, forceAlpha255 bool) []color.NRGBA {
	if len(buckets) == 0 || maxBoxes <= 0 {
		return nil
	}
	type box struct {
		items        []mcBucket
		total        int
		maxRange     int
		splitChannel int // 0=R, 1=G, 2=B, 3=A
	}
	measure := func(items []mcBucket) box {
		minR, maxR := 255, 0
		minG, maxG := 255, 0
		minB, maxB := 255, 0
		minA, maxA := 255, 0
		total := 0
		for _, it := range items {
			total += it.count
			if int(it.r) < minR {
				minR = int(it.r)
			}
			if int(it.r) > maxR {
				maxR = int(it.r)
			}
			if int(it.g) < minG {
				minG = int(it.g)
			}
			if int(it.g) > maxG {
				maxG = int(it.g)
			}
			if int(it.b) < minB {
				minB = int(it.b)
			}
			if int(it.b) > maxB {
				maxB = int(it.b)
			}
			if int(it.a) < minA {
				minA = int(it.a)
			}
			if int(it.a) > maxA {
				maxA = int(it.a)
			}
		}
		rR, rG, rB, rA := maxR-minR, maxG-minG, maxB-minB, maxA-minA
		bestRange, ch := rR, 0
		if rG > bestRange {
			bestRange, ch = rG, 1
		}
		if rB > bestRange {
			bestRange, ch = rB, 2
		}
		if rA > bestRange {
			bestRange, ch = rA, 3
		}
		return box{items: items, total: total, maxRange: bestRange, splitChannel: ch}
	}

	boxes := []box{measure(buckets)}
	for len(boxes) < maxBoxes {
		bestIdx := -1
		for i := range boxes {
			if len(boxes[i].items) <= 1 || boxes[i].maxRange <= 0 {
				continue
			}
			if bestIdx < 0 || boxes[i].maxRange > boxes[bestIdx].maxRange ||
				(boxes[i].maxRange == boxes[bestIdx].maxRange && boxes[i].total > boxes[bestIdx].total) {
				bestIdx = i
			}
		}
		if bestIdx < 0 {
			break
		}
		b := boxes[bestIdx]
		slices.SortFunc(b.items, func(x, y mcBucket) int {
			switch b.splitChannel {
			case 0:
				return int(x.r) - int(y.r)
			case 1:
				return int(x.g) - int(y.g)
			case 2:
				return int(x.b) - int(y.b)
			default:
				return int(x.a) - int(y.a)
			}
		})
		half := b.total / 2
		acc := 0
		mid := 1
		for i := 0; i < len(b.items)-1; i++ {
			acc += b.items[i].count
			if acc >= half {
				mid = i + 1
				break
			}
		}
		left := measure(b.items[:mid])
		right := measure(b.items[mid:])
		boxes[bestIdx] = left
		boxes = append(boxes, right)
	}

	out := make([]color.NRGBA, 0, len(boxes))
	for _, b := range boxes {
		var sr, sg, sb, sa, cnt uint64
		for _, it := range b.items {
			c := uint64(it.count)
			sr += uint64(it.r) * c
			sg += uint64(it.g) * c
			sb += uint64(it.b) * c
			sa += uint64(it.a) * c
			cnt += c
		}
		if cnt == 0 {
			continue
		}
		a8 := uint8((sa + cnt/2) / cnt)
		if forceAlpha255 {
			a8 = 255
		} else if a8 == 0 {
			a8 = 1
		}
		out = append(out, color.NRGBA{
			R: uint8((sr + cnt/2) / cnt),
			G: uint8((sg + cnt/2) / cnt),
			B: uint8((sb + cnt/2) / cnt),
			A: a8,
		})
	}
	return out
}

// quantizeMedianCut builds a PNG-8 palette of up to maxColors entries (slot 0 reserved for transparent {0,0,0,0})
// using median-cut quantization over opaque and semi-transparent pixels separately, then maps pixels by nearest color.
func quantizeMedianCut(img image.Image, maxColors int) *image.Paletted {
	if maxColors < 2 {
		maxColors = 2
	}
	pixels, w, h, nOpaque, nSemi := nrgbaPixels(img)
	budget := maxColors - 1
	var kOpaque, kSemi int
	switch {
	case nOpaque > 0 && nSemi > 0:
		if budget >= 2 {
			kSemi = max(1, (budget*nSemi)/(nOpaque+nSemi))
			if kSemi >= budget {
				kSemi = budget - 1
			}
			kOpaque = budget - kSemi
		} else if nOpaque >= nSemi {
			kOpaque = 1
		} else {
			kSemi = 1
		}
	case nOpaque > 0:
		kOpaque = budget
	case nSemi > 0:
		kSemi = budget
	}

	palette := make(color.Palette, 1, maxColors)
	palette[0] = color.NRGBA{}

	var opaqueStart, opaqueEnd, semiStart, semiEnd int
	if kOpaque > 0 {
		grid := make([]mcAccum, 32*32*32)
		for _, p := range pixels {
			if p.A != 255 {
				continue
			}
			idx := (int(p.R>>3) << 10) | (int(p.G>>3) << 5) | int(p.B>>3)
			g := &grid[idx]
			g.sumR += uint64(p.R)
			g.sumG += uint64(p.G)
			g.sumB += uint64(p.B)
			g.count++
		}
		buckets := make([]mcBucket, 0, 1024)
		for i := range grid {
			g := &grid[i]
			if g.count == 0 {
				continue
			}
			c := uint64(g.count)
			buckets = append(buckets, mcBucket{
				r:     uint8((g.sumR + c/2) / c),
				g:     uint8((g.sumG + c/2) / c),
				b:     uint8((g.sumB + c/2) / c),
				a:     255,
				count: g.count,
			})
		}
		opaqueStart = len(palette)
		for _, c := range medianCutBuckets(buckets, kOpaque, true) {
			palette = append(palette, c)
		}
		opaqueEnd = len(palette)
	}

	if kSemi > 0 {
		grid := make([]mcAccum, 16*16*16*16)
		for _, p := range pixels {
			if p.A == 0 || p.A == 255 {
				continue
			}
			idx := (int(p.R>>4) << 12) | (int(p.G>>4) << 8) | (int(p.B>>4) << 4) | int(p.A>>4)
			g := &grid[idx]
			g.sumR += uint64(p.R)
			g.sumG += uint64(p.G)
			g.sumB += uint64(p.B)
			g.sumA += uint64(p.A)
			g.count++
		}
		buckets := make([]mcBucket, 0, 1024)
		for i := range grid {
			g := &grid[i]
			if g.count == 0 {
				continue
			}
			c := uint64(g.count)
			a8 := uint8((g.sumA + c/2) / c)
			if a8 == 0 {
				a8 = 1
			}
			buckets = append(buckets, mcBucket{
				r:     uint8((g.sumR + c/2) / c),
				g:     uint8((g.sumG + c/2) / c),
				b:     uint8((g.sumB + c/2) / c),
				a:     a8,
				count: g.count,
			})
		}
		semiStart = len(palette)
		for _, c := range medianCutBuckets(buckets, kSemi, false) {
			palette = append(palette, c)
		}
		semiEnd = len(palette)
	}

	palNRGBA := make([]color.NRGBA, len(palette))
	for i, c := range palette {
		palNRGBA[i] = c.(color.NRGBA)
	}
	nearestIn := func(p color.NRGBA, start, end int) uint8 {
		bestIdx := uint8(start)
		bestDist := int32(math.MaxInt32)
		pr, pg, pb, pa := int32(p.R), int32(p.G), int32(p.B), int32(p.A)
		for i := start; i < end; i++ {
			c := palNRGBA[i]
			dr := pr - int32(c.R)
			dg := pg - int32(c.G)
			db := pb - int32(c.B)
			da := pa - int32(c.A)
			d := dr*dr + dg*dg + db*db + da*da
			if d < bestDist {
				bestDist = d
				bestIdx = uint8(i)
				if d == 0 {
					break
				}
			}
		}
		return bestIdx
	}

	out := image.NewPaletted(image.Rect(0, 0, w, h), palette)
	type cacheEntry struct {
		key uint32
		idx uint8
	}
	cache := make([]cacheEntry, 65536)
	for i, p := range pixels {
		if p.A == 0 || len(palette) <= 1 {
			out.Pix[i] = 0
			continue
		}
		key := (uint32(p.R) << 24) | (uint32(p.G) << 16) | (uint32(p.B) << 8) | uint32(p.A)
		slot := ((key * 0x9e3779b1) >> 16) & 0xffff
		if e := cache[slot]; e.key == key {
			out.Pix[i] = e.idx
			continue
		}
		var start, end int
		switch {
		case p.A == 255 && opaqueEnd > opaqueStart:
			start, end = opaqueStart, opaqueEnd
		case p.A < 255 && semiEnd > semiStart:
			start, end = semiStart, semiEnd
		default:
			start, end = 1, len(palette)
		}
		idx := nearestIn(p, start, end)
		cache[slot] = cacheEntry{key: key, idx: idx}
		out.Pix[i] = idx
	}
	return out
}

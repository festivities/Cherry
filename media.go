package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"image"
	"image/draw"
	"image/jpeg"
	"image/png"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"sync"
)

const (
	maxDiaryImageSize      int64 = 20 << 20
	maxDiaryUploadSize     int64 = maxDiaryImageSize + (1 << 20)
	diaryMultipartMemLimit       = 1 << 20
	diaryImageMaxDimension       = 16_000
	diaryImageMaxPixels          = 24_000_000
	maxDiaryMediaStorage   int64 = 1 << 30
	diaryDownloadPath            = "/lineplay/d/download.nhn"
	roomDownloadPath             = "/lineplay/r/download.nhn"
)

const (
	diaryMediaBadRequestBody = `{"errorCode":"400","errorMessage":"cherry: bad request"}`
	diaryMediaNotFoundBody   = `{"errorCode":"404","errorMessage":"cherry: unknown route"}`
	diaryMediaTooLargeBody   = `{"errorCode":"413","errorMessage":"cherry: request too large"}`
	diaryMediaConflictBody   = `{"errorCode":"409","errorMessage":"cherry: media conflict"}`
	diaryMediaFullBody       = `{"errorCode":"507","errorMessage":"cherry: media storage full"}`
	diaryMediaSaveFailedBody = `{"errorCode":"500","errorMessage":"cherry: save failed"}`
)

var diaryMediaSaveMu sync.Mutex

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
	handleMediaUpload(w, r, dir, maxDiaryMediaStorage, true)
}

func handleRoomImageDownload(w http.ResponseWriter, r *http.Request) {
	dir, err := defaultRoomMediaDir()
	if err != nil {
		writeDiaryMediaJSON(w, http.StatusInternalServerError, diaryMediaSaveFailedBody)
		return
	}
	handleMediaDownload(w, r, dir, true)
}

func defaultRoomMediaDir() (string, error) {
	d, err := defaultDiaryMediaDir()
	return filepath.Join(filepath.Dir(d), "room-media"), err
}

func defaultDiaryMediaDir() (string, error) {
	cacheDir, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	if cacheDir == "" {
		return "", errors.New("empty user cache directory")
	}
	return filepath.Join(cacheDir, "Cherry", "diary-media"), nil
}

// The At handlers take an explicit directory so tests can use disposable storage.
func handleDiaryImageUploadAt(w http.ResponseWriter, r *http.Request, dir string) {
	handleDiaryImageUploadAtWithLimit(w, r, dir, maxDiaryMediaStorage)
}

func handleDiaryImageUploadAtWithLimit(w http.ResponseWriter, r *http.Request, dir string, storageLimit int64) {
	handleMediaUpload(w, r, dir, storageLimit, false)
}

// handleMediaUpload serves the OBS upload for diary photos (room=false) and My
// Room preset thumbnails (room=true). ponytail: the room client's oid format is
// unverified, so room mode accepts any printable oid equal to the form name
// instead of requiring userid_ctime.
func handleMediaUpload(w http.ResponseWriter, r *http.Request, dir string, storageLimit int64, room bool) {
	if r.Method != http.MethodPost {
		writeDiaryMediaJSON(w, http.StatusNotFound, diaryMediaNotFoundBody)
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

	path := diaryMediaHashPath(dir, params.UserID, params.CTime, params.OID)
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
	if err := storeDiaryMedia(path, tmpPath, written, contentHash, storageLimit); err != nil {
		var conflict diaryMediaConflictError
		if errors.As(err, &conflict) {
			writeDiaryMediaJSON(w, http.StatusConflict, diaryMediaConflictBody)
		} else {
			var full diaryMediaQuotaError
			if errors.As(err, &full) {
				writeDiaryMediaJSON(w, http.StatusInsufficientStorage, diaryMediaFullBody)
			} else {
				writeDiaryMediaJSON(w, http.StatusInternalServerError, diaryMediaSaveFailedBody)
			}
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
		return p.Name == p.OID && validDiaryMediaUserID(p.OID) && validDiaryMediaUserID(p.UserID) && validDiaryMediaCTime(p.CTime)
	}
	return p.Name == p.UserID+"_"+p.CTime && validDiaryMediaTuple(p.UserID, p.CTime, p.OID)
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
	userID, userOK := oneDiaryMediaQueryValue(query, "userid")
	ctime, ctimeOK := oneDiaryMediaQueryValue(query, "ctime")
	oid, oidOK := oneDiaryMediaQueryValue(query, "oid")
	tid, tidOK := optionalDiaryMediaQueryValue(query, "tid")
	if !userOK || !ctimeOK || !oidOK || !tidOK || !mediaUploadNameOK(diaryImageUploadParams{Name: oid, UserID: userID, OID: oid, CTime: ctime}, room) {
		writeDiaryMediaJSON(w, http.StatusBadRequest, diaryMediaBadRequestBody)
		return
	}
	if room {
		if r.URL.Path != roomDownloadPath || tid != "" && !validDiaryMediaTID(tid) {
			writeDiaryMediaJSON(w, http.StatusNotFound, diaryMediaNotFoundBody)
			return
		}
	} else if !validDiaryMediaVariant(r.URL.Path, tid) {
		writeDiaryMediaJSON(w, http.StatusNotFound, diaryMediaNotFoundBody)
		return
	}
	path := diaryMediaHashPath(dir, userID, ctime, oid)
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
	if !room && contentType == "image/png" {
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
	if !validDiaryMediaTuple(userID, ctime, oid) {
		return "", false
	}
	return diaryMediaHashPath(dir, userID, ctime, oid), true
}

func diaryMediaHashPath(dir, userID, ctime, oid string) string {
	sum := sha256.Sum256([]byte(userID + "\x00" + ctime + "\x00" + oid))
	return filepath.Join(dir, hex.EncodeToString(sum[:])+".img")
}

func validDiaryMediaTuple(userID, ctime, oid string) bool {
	if !validDiaryMediaUserID(userID) || !validDiaryMediaCTime(ctime) {
		return false
	}
	return oid == userID+"_"+ctime
}

func validDiaryMediaUserID(userID string) bool {
	if len(userID) == 0 || len(userID) > 128 {
		return false
	}
	for i := 0; i < len(userID); i++ {
		c := userID[i]
		if c < 0x21 || c > 0x7e || c == '/' || c == '\\' {
			return false
		}
	}
	return true
}

func validDiaryMediaCTime(ctime string) bool {
	if ctime == "" || len(ctime) > 19 {
		return false
	}
	for i := 0; i < len(ctime); i++ {
		if ctime[i] < '0' || ctime[i] > '9' {
			return false
		}
	}
	seconds, err := strconv.ParseInt(ctime, 10, 64)
	return err == nil && seconds >= 0 && strconv.FormatInt(seconds, 10) == ctime
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

func oneDiaryMediaQueryValue(query url.Values, key string) (string, bool) {
	values, ok := query[key]
	if ok && len(values) == 1 {
		return values[0], true
	}
	return "", false
}

func optionalDiaryMediaQueryValue(query url.Values, key string) (string, bool) {
	values, ok := query[key]
	if !ok {
		return "", true
	}
	if len(values) != 1 {
		return "", false
	}
	if values[0] == "" {
		return "", false
	}
	return values[0], true
}

func validDiaryMediaVariant(path, tid string) bool {
	if path != diaryDownloadPath {
		return false
	}
	return tid == "" || validDiaryMediaTID(tid)
}

func validDiaryMediaTID(tid string) bool {
	switch tid {
	case "306x0.r", "460x0.r", "612x0.r", "790x0.r", "320x480", "480x720", "640x960", "800x1200":
		return true
	default:
		return false
	}
}

func storeDiaryMedia(path, tmpPath string, size int64, contentHash [sha256.Size]byte, storageLimit int64) error {
	diaryMediaSaveMu.Lock()
	defer diaryMediaSaveMu.Unlock()
	info, err := os.Lstat(path)
	if err == nil {
		if info.Mode().IsRegular() && info.Size() == size {
			same, hashErr := diaryMediaFilesEqual(path, size, contentHash)
			if hashErr != nil {
				return hashErr
			}
			if same {
				return nil
			}
		}
		return diaryMediaConflictError{}
	}
	if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	tooLarge, err := diaryMediaStorageWouldExceed(filepath.Dir(path), size, storageLimit)
	if err != nil {
		return err
	}
	if tooLarge {
		return diaryMediaQuotaError{}
	}
	return os.Rename(tmpPath, path)
}

type diaryMediaConflictError struct{}

func (diaryMediaConflictError) Error() string {
	return "diary media already exists with different content"
}

type diaryMediaQuotaError struct{}

func (diaryMediaQuotaError) Error() string { return "diary media storage limit exceeded" }

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

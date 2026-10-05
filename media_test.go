package main

import (
	"bytes"
	"encoding/json"
	"image"
	"image/jpeg"
	"image/png"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDiaryImageUploadDownloadRoundTrip(t *testing.T) {
	tidVariants := []string{"306x0.r", "460x0.r", "612x0.r", "790x0.r", "320x480", "480x720", "640x960", "800x1200"}
	for _, tc := range []struct {
		format, uploadType, fileField string
	}{
		{"jpeg", "image/jpeg", "filedata"},
		{"png", "image/jpeg", "Filedata"}, // Native upload MIME may say JPEG while the bytes are PNG.
	} {
		t.Run(tc.format, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "diary-media")
			const userID, ctime = "session-user_42", "1760000000"
			oid := userID + "_" + ctime
			original := diaryTestImage(t, tc.format, 3, 2, 73)
			req := diaryUploadRequestWithFields(t, diaryParamsJSON(t, userID, ctime, oid), original, tc.uploadType, tc.fileField)
			rec := httptest.NewRecorder()
			handleDiaryImageUploadAt(rec, req, dir)
			if rec.Code != http.StatusOK || rec.Body.String() != `{"result":true}` {
				t.Fatalf("upload = %d %q", rec.Code, rec.Body.String())
			}
			if got := rec.Header().Get("Content-Type"); got != "application/json; charset=utf-8" {
				t.Fatalf("upload Content-Type = %q", got)
			}

			entries, err := os.ReadDir(dir)
			if err != nil || len(entries) != 1 || strings.Contains(entries[0].Name(), userID) {
				t.Fatalf("stored files = %v, err = %v", entries, err)
			}
			wantType := "image/" + tc.format
			if tc.format == "jpeg" {
				wantType = "image/jpeg"
			}
			for _, tid := range tidVariants {
				// Calling the download handler again against the same directory models a fresh handler/process lookup.
				get := httptest.NewRequest(http.MethodGet, diaryDownloadTarget(diaryDownloadPath, userID, ctime, oid, tid), nil)
				got := httptest.NewRecorder()
				handleDiaryImageDownloadAt(got, get, dir)
				if got.Code != http.StatusOK || got.Header().Get("Content-Type") != wantType || !bytes.Equal(got.Body.Bytes(), original) {
					t.Fatalf("download tid=%s: status=%d type=%q bytes_match=%t", tid, got.Code, got.Header().Get("Content-Type"), bytes.Equal(got.Body.Bytes(), original))
				}
			}
		})
	}
}

func TestDiaryImageUploadRejectsMultipleFileParts(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "diary-media")
	const userID, ctime = "aid1", "1760000006"
	data := diaryTestImage(t, "png", 2, 2, 20)
	req := diaryUploadRequestWithFields(t, diaryParamsJSON(t, userID, ctime, userID+"_"+ctime), data, "image/png", "filedata", "Filedata")
	rec := httptest.NewRecorder()
	handleDiaryImageUploadAt(rec, req, dir)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("two file parts status = %d, want 400", rec.Code)
	}
}

func TestDiaryImageDuplicateTuple(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "diary-media")
	const userID, ctime = "aid1", "1760000001"
	oid := userID + "_" + ctime
	first := diaryTestImage(t, "png", 2, 2, 20)
	second := diaryTestImage(t, "png", 2, 2, 200)
	for i, data := range [][]byte{first, first, second} {
		req := diaryUploadRequest(t, diaryParamsJSON(t, userID, ctime, oid), data, "image/png")
		rec := httptest.NewRecorder()
		handleDiaryImageUploadAt(rec, req, dir)
		want := http.StatusOK
		if i == 2 {
			want = http.StatusConflict
		}
		if rec.Code != want {
			t.Fatalf("upload %d status = %d, want %d: %s", i, rec.Code, want, rec.Body.String())
		}
	}
	get := httptest.NewRequest(http.MethodGet, diaryDownloadTarget(diaryDownloadPath, userID, ctime, oid, ""), nil)
	rec := httptest.NewRecorder()
	handleDiaryImageDownloadAt(rec, get, dir)
	if rec.Code != http.StatusOK || !bytes.Equal(rec.Body.Bytes(), first) {
		t.Fatalf("download after conflict = %d, original preserved=%t", rec.Code, bytes.Equal(rec.Body.Bytes(), first))
	}
}

func TestDiaryMediaStorageQuota(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "diary-media")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	const userID, ctime = "aid-quota", "1760000008"
	oid := userID + "_" + ctime
	existing := diaryTestImage(t, "png", 2, 2, 20)
	existingPath, _ := diaryMediaPath(dir, userID, ctime, oid)
	if err := os.WriteFile(existingPath, existing, 0600); err != nil {
		t.Fatal(err)
	}
	limit := int64(len(existing))
	changed := diaryTestImage(t, "png", 2, 2, 200)
	upload := func(userID, ctime, oid string, data []byte) *httptest.ResponseRecorder {
		t.Helper()
		req := diaryUploadRequest(t, diaryParamsJSON(t, userID, ctime, oid), data, "image/png")
		rec := httptest.NewRecorder()
		handleDiaryImageUploadAtWithLimit(rec, req, dir, limit)
		return rec
	}
	if rec := upload(userID, ctime, oid, existing); rec.Code != http.StatusOK {
		t.Fatalf("identical duplicate at full quota = %d %s", rec.Code, rec.Body.String())
	}
	if rec := upload(userID, ctime, oid, changed); rec.Code != http.StatusConflict {
		t.Fatalf("conflicting duplicate at full quota = %d %s", rec.Code, rec.Body.String())
	}
	newCTime := "1760000009"
	newOID := userID + "_" + newCTime
	if rec := upload(userID, newCTime, newOID, changed); rec.Code != http.StatusInsufficientStorage {
		t.Fatalf("new upload at quota = %d %s, want 507", rec.Code, rec.Body.String())
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 || entries[0].Name() != filepath.Base(existingPath) {
		t.Fatalf("quota/conflict left unexpected files: %v, err=%v", entries, err)
	}
	if got, err := os.ReadFile(existingPath); err != nil || !bytes.Equal(got, existing) {
		t.Fatalf("existing image changed: read err=%v, bytes_match=%t", err, bytes.Equal(got, existing))
	}
}

func TestDiaryImageUploadRejectsMalformedAndUnsafeRequests(t *testing.T) {
	const userID, ctime = "member-7", "1760000002"
	oid := userID + "_" + ctime
	imageBytes := diaryTestImage(t, "jpeg", 2, 2, 90)
	missingCTime, _ := json.Marshal(map[string]string{
		"ver": "1.0", "type": "image", "name": userID + "_", "userid": userID, "oid": userID + "_",
	})
	traversalUser := "../outside"
	traversalOID := traversalUser + "_" + ctime
	cases := []struct {
		name, params, fileType string
		file                   []byte
		omitParams             bool
	}{
		{name: "malformed params JSON", params: "{"},
		{name: "path traversal user ID", params: string(diaryParamsJSON(t, traversalUser, ctime, traversalOID))},
		{name: "inconsistent oid", params: string(diaryParamsJSON(t, userID, ctime, "../outside"))},
		{name: "missing ctime", params: string(missingCTime)},
		{name: "missing params field", omitParams: true},
		{name: "non-image file", params: string(diaryParamsJSON(t, userID, ctime, oid)), file: []byte("not an image"), fileType: "image/jpeg"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "diary-media")
			data := tc.file
			if data == nil {
				data = imageBytes
			}
			var params []byte
			if !tc.omitParams {
				params = []byte(tc.params)
			}
			contentType := tc.fileType
			if contentType == "" {
				contentType = "image/jpeg"
			}
			req := diaryUploadRequest(t, params, data, contentType)
			rec := httptest.NewRecorder()
			handleDiaryImageUploadAt(rec, req, dir)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400: %s", rec.Code, rec.Body.String())
			}
			if _, err := os.Stat(dir); !os.IsNotExist(err) {
				t.Fatalf("invalid upload created storage dir: stat err=%v", err)
			}
		})
	}
}

func TestDiaryImageUploadRejectsHeaderOnlyPNG(t *testing.T) {
	const userID, ctime = "aid-header", "1760000007"
	dir := filepath.Join(t.TempDir(), "diary-media")
	valid := diaryTestImage(t, "png", 2, 2, 30)
	truncated := valid[:33] // PNG signature and complete IHDR chunk, but no IDAT data.
	if _, _, err := image.DecodeConfig(bytes.NewReader(truncated)); err != nil {
		t.Fatalf("truncated PNG DecodeConfig: %v", err)
	}
	if _, _, err := image.Decode(bytes.NewReader(truncated)); err == nil {
		t.Fatal("full PNG decode unexpectedly accepted missing IDAT data")
	}
	req := diaryUploadRequest(t, diaryParamsJSON(t, userID, ctime, userID+"_"+ctime), truncated, "image/png")
	rec := httptest.NewRecorder()
	handleDiaryImageUploadAt(rec, req, dir)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("header-only PNG upload = %d, want 400", rec.Code)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("rejected PNG created storage dir: stat err=%v", err)
	}
}

func TestDiaryImageUploadLimits(t *testing.T) {
	t.Run("file size", func(t *testing.T) {
		const userID, ctime = "aid2", "1760000003"
		dir := filepath.Join(t.TempDir(), "diary-media")
		large := bytes.Repeat([]byte{'x'}, int(maxDiaryImageSize+1))
		req := diaryUploadRequest(t, diaryParamsJSON(t, userID, ctime, userID+"_"+ctime), large, "image/jpeg")
		req.ContentLength = -1
		rec := httptest.NewRecorder()
		handleDiaryImageUploadAt(rec, req, dir)
		if rec.Code != http.StatusRequestEntityTooLarge {
			t.Fatalf("oversized upload = %d, want 413: %s", rec.Code, rec.Body.String())
		}
		if _, err := os.Stat(dir); !os.IsNotExist(err) {
			t.Fatalf("oversized upload created storage dir: stat err=%v", err)
		}
	})

	for _, tc := range []struct {
		name          string
		width, height int
	}{
		{"dimension", 16_001, 1},
		{"pixel count", 4_900, 4_900},
	} {
		t.Run(tc.name, func(t *testing.T) {
			const userID, ctime = "aid3", "1760000004"
			dir := filepath.Join(t.TempDir(), "diary-media")
			data := diaryTestImage(t, "png", tc.width, tc.height, 0)
			req := diaryUploadRequest(t, diaryParamsJSON(t, userID, ctime, userID+"_"+ctime), data, "image/png")
			rec := httptest.NewRecorder()
			handleDiaryImageUploadAt(rec, req, dir)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("oversized dimensions upload = %d, want 400", rec.Code)
			}
		})
	}
}

func TestDiaryImageDownloadRejectsMalformedQueries(t *testing.T) {
	dir := t.TempDir()
	for _, tc := range []struct {
		target string
		status int
	}{
		{diaryDownloadPath, http.StatusBadRequest},
		{diaryDownloadTarget(diaryDownloadPath, "../outside", "1760000005", "../outside_1760000005", ""), http.StatusBadRequest},
		{diaryDownloadTarget(diaryDownloadPath, "aid4", "1760000005", "wrong", ""), http.StatusBadRequest},
		{diaryDownloadTarget(diaryDownloadPath, "aid4", "1760000005", "aid4_1760000005", "../../outside"), http.StatusNotFound},
		{diaryDownloadTarget(diaryDownloadPath+"/640x960", "aid4", "1760000005", "aid4_1760000005", ""), http.StatusNotFound},
		{diaryDownloadTarget(diaryDownloadPath, "aid4/640x960", "1760000005", "aid4/640x960_1760000005", ""), http.StatusBadRequest},
		{diaryDownloadTarget(diaryDownloadPath, "aid4", "1760000005", "aid4_1760000005", "") + "&ctime=1", http.StatusBadRequest},
	} {
		req := httptest.NewRequest(http.MethodGet, tc.target, nil)
		rec := httptest.NewRecorder()
		handleDiaryImageDownloadAt(rec, req, dir)
		if rec.Code != tc.status {
			t.Errorf("GET %s status = %d, want %d", tc.target, rec.Code, tc.status)
		}
	}
}

func diaryTestImage(t *testing.T, format string, width, height int, shade uint8) []byte {
	t.Helper()
	img := image.NewGray(image.Rect(0, 0, width, height))
	if len(img.Pix) != 0 {
		img.Pix[0] = shade
	}
	var buf bytes.Buffer
	var err error
	switch format {
	case "jpeg":
		err = jpeg.Encode(&buf, img, &jpeg.Options{Quality: 90})
	case "png":
		err = png.Encode(&buf, img)
	default:
		t.Fatalf("unknown image format %q", format)
	}
	if err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func diaryParamsJSON(t *testing.T, userID, ctime, oid string) []byte {
	t.Helper()
	params := diaryImageUploadParams{
		Version: "1.0", Type: "image", Name: userID + "_" + ctime,
		UserID: userID, OID: oid, CTime: ctime,
	}
	data, err := json.Marshal(params)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func diaryUploadRequest(t *testing.T, params, data []byte, fileType string) *http.Request {
	t.Helper()
	return diaryUploadRequestWithFields(t, params, data, fileType, "filedata")
}

func diaryUploadRequestWithFields(t *testing.T, params, data []byte, fileType string, fields ...string) *http.Request {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if params != nil {
		header := make(textproto.MIMEHeader)
		header.Set("Content-Disposition", `form-data; name="params"`)
		header.Set("Content-Type", "text/plain")
		part, err := writer.CreatePart(header)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := part.Write(params); err != nil {
			t.Fatal(err)
		}
	}
	for _, field := range fields {
		header := make(textproto.MIMEHeader)
		header.Set("Content-Disposition", `form-data; name="`+field+`"; filename="sample.jpg"`)
		header.Set("Content-Type", fileType)
		part, err := writer.CreatePart(header)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := part.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/lineplay/d/upload.nhn", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	return req
}

func diaryDownloadTarget(path, userID, ctime, oid, tid string) string {
	query := make(url.Values)
	query.Set("oid", oid)
	query.Set("ctime", ctime)
	query.Set("userid", userID)
	if tid != "" {
		query.Set("tid", tid)
	}
	return path + "?" + query.Encode()
}

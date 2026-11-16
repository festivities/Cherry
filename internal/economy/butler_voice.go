package economy

import (
	"archive/zip"
	"bytes"
	"fmt"
	"io/fs"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Butler voices (Garden LPN agent 10, ch 63 -> hc 74; butler-robo-spec section 1). A voice pack is one zip the client
// downloads from the static base at <pack>/cherry_voice_v<revision>.zip. The zip is Cherry-made packaging of the
// original lines: flat entries voice_NNN.ogg (the client's unzip writes each entry beside the zip and skips a name with a
// folder), read from the store and built once.

// butlerVoiceMTime is the modification time of every zip entry: a fixed instant, so the bytes never change.
var butlerVoiceMTime = time.Date(2026, 11, 16, 0, 0, 0, 0, time.UTC)

// ZipPath is the pack's zip relative to the static base and to cache/butler/: "<pack>/cherry_voice_v<revision>.zip".
func (v ButlerVoices) ZipPath() string {
	return fmt.Sprintf("%s/cherry_voice_v%d.zip", v.Pack, v.Revision)
}

// FilePath is line n's file relative to cache/butler/ (the client's scriptList path): "<pack>/voice_NNN.ogg".
func (v ButlerVoices) FilePath(n int64) string { return fmt.Sprintf("%s/voice_%03d.ogg", v.Pack, n) }

// storeFile is line n's file in the store: the device files/ tree, cache/butler/<FilePath>.
func (v ButlerVoices) storeFile(n int64) string { return "cache/butler/" + v.FilePath(n) }

// butlerVoiceZips holds the built packs by ZipPath, so each is built once per process.
var butlerVoiceZips = struct {
	sync.Mutex
	files map[string][]byte
}{files: map[string][]byte{}}

// ButlerVoiceZip returns the voice zip of butler d: one Store entry per line, named voice_NNN.ogg, the original audio read
// from the store. ok is false when d has no voices or a line's file is missing (nothing is cached then).
func ButlerVoiceZip(d ButlerDef) ([]byte, bool) {
	if d.Voices == nil {
		return nil, false
	}
	v := *d.Voices
	butlerVoiceZips.Lock()
	defer butlerVoiceZips.Unlock()
	if b, ok := butlerVoiceZips.files[v.ZipPath()]; ok {
		return b, true
	}
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, n := range v.Lines {
		data, err := fs.ReadFile(butlerSkinFS, v.storeFile(n))
		if err != nil {
			return nil, false
		}
		w, err := zw.CreateHeader(&zip.FileHeader{Name: fmt.Sprintf("voice_%03d.ogg", n), Method: zip.Store, Modified: butlerVoiceMTime})
		if err != nil {
			return nil, false
		}
		if _, err := w.Write(data); err != nil {
			return nil, false
		}
	}
	if err := zw.Close(); err != nil {
		return nil, false
	}
	butlerVoiceZips.files[v.ZipPath()] = buf.Bytes()
	return buf.Bytes(), true
}

// HandleButlerVoiceZip serves GET /<pack>/cherry_voice_v<revision>.zip, the voice pack of the roster butler that names it
// (the URL of ButlerVoiceInfo f2 record f4). Any other path under the folder is a 404.
func HandleButlerVoiceZip(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, "/")
	for _, d := range ActiveButlers().Butlers {
		if d.Voices == nil || d.Voices.ZipPath() != name {
			continue
		}
		b, ok := ButlerVoiceZip(d)
		if !ok || r.Method != http.MethodGet {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/zip")
		_, _ = w.Write(b)
		return
	}
	http.NotFound(w, r)
}

package thumbs

import (
	"path/filepath"
	"testing"

	"cherry/internal/testutil"
)

// santiFiles is the santi device backup's files/ tree: the archive tests read item folders from it.
var santiFiles = filepath.FromSlash(`D:/Dev/projects/Cherry/.opencode/line-play-artifacts/santi-backup-20260921/jp.naver.lineplay.android/files`)

// useArchive makes the device backups the archive for the test, and skips it without them.
func useArchive(t *testing.T) {
	testutil.UseArchive(t)
}

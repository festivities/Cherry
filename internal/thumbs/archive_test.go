package thumbs

import (
	"testing"

	"cherry/internal/testutil"
)

// santiFiles is the santi device backup's files/ tree: the archive tests read item folders from it.
var santiFiles = testutil.SantiFiles()

// useArchive makes the device backups the archive for the test, and skips it without them.
func useArchive(t *testing.T) {
	testutil.UseArchive(t)
}

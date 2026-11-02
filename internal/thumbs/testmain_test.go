package thumbs

import (
	"os"
	"testing"

	"cherry/internal/testutil"
)

// TestMain reads the device archive by default, as the item tests always did; a test with a fixture sets its own.
func TestMain(m *testing.M) {
	testutil.InstallArchive()
	os.Exit(m.Run())
}

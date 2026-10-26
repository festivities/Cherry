package web

import (
	"os"
	"testing"

	"cherry/internal/assets"
)

// TestMain points the converted items at an empty tree: the catalog these tests check is the device archive's, and
// this machine's generated tree (%LOCALAPPDATA%\Cherry\generated\item) must not change it. Tests of converted
// items set their own assets.GeneratedItemRoot.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "cherry-generated-empty-")
	if err != nil {
		panic(err)
	}
	assets.GeneratedItemRoot = dir
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

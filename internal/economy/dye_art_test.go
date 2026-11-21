package economy

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"cherry/data"
	"cherry/internal/assets"
	"cherry/internal/testutil"
)

// TestDyeTubesAreOriginals: every dye's tube is the original file of the robo iOS cache (or one of lpbackup's two dye
// tubes), and Cherry's picture route serves that file byte for byte.
func TestDyeTubesAreOriginals(t *testing.T) {
	testutil.RequireArchive(t)
	for _, it := range ActiveDyes().Dyes {
		if it.Tube == "" {
			t.Errorf("dye %d has no tube", it.ItemNo)
			continue
		}
		want, err := os.ReadFile(filepath.Join(testutil.RoboCaches(), "cache", "dye", it.Tube))
		if err != nil {
			want, err = os.ReadFile(filepath.Join(testutil.LPBackupFiles(), "cache", "dye", it.Tube))
		}
		if err != nil {
			t.Errorf("dye %d: tube %s is in neither donation: %v", it.ItemNo, it.Tube, err)
			continue
		}
		rec := httptest.NewRecorder()
		HandleDyeImage(rec, httptest.NewRequest(http.MethodGet, fmt.Sprintf("/cherry/dye/%d.png", it.ItemNo), nil))
		if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "image/png" || !bytes.Equal(rec.Body.Bytes(), want) {
			t.Errorf("dye %d: status %d, want the original %s byte for byte", it.ItemNo, rec.Code, it.Tube)
		}
	}
}

// TestDyeWithoutTubeIsDrawn: a dye with no tube is drawn in its colour (the fallback), and a catalog with a tube removed
// still starts.
func TestDyeWithoutTubeIsDrawn(t *testing.T) {
	raw := strings.Replace(string(data.DyesJSON), `"tube": "dye_56_1.png", `, ``, 1)
	path := filepath.Join(t.TempDir(), "dyes.json")
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := dyesFile.LoadFrom(path); err != nil {
		t.Fatalf("catalog without a tube refused: %v", err)
	}
	t.Cleanup(dyesFile.Reset)
	rec := httptest.NewRecorder()
	HandleDyeImage(rec, httptest.NewRequest(http.MethodGet, "/cherry/dye/9501.png", nil))
	want, err := dyeIconPNG([]string{"#E53935"})
	if err != nil || rec.Code != http.StatusOK || !bytes.Equal(rec.Body.Bytes(), want) {
		t.Fatalf("status %d, want the drawn red drop (err %v)", rec.Code, err)
	}
}

// TestDyeTubeMissingRefusedOnlyWithAnArchive: a tube the store does not hold stops the start when the store can be asked
// (verifyDyes); without an asset store the check is skipped, as for the other art checks.
func TestDyeTubeMissingRefusedOnlyWithAnArchive(t *testing.T) {
	cat := *ActiveDyes()
	cat.Dyes = slices.Clone(cat.Dyes)
	cat.Dyes[0].Tube = "dye_999_1.png"
	testutil.SetArchive(t, map[string][]string{})
	if err := verifyDyes(&cat); err == nil || !strings.Contains(err.Error(), "dye_999_1.png") {
		t.Fatalf("missing tube: err = %v", err)
	}
	old := assets.Archive
	assets.Archive = nil
	t.Cleanup(func() { assets.Archive = old })
	if err := verifyDyes(&cat); err != nil {
		t.Fatalf("no asset store: err = %v, want nil", err)
	}
}

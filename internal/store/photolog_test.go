package store

import (
	"errors"
	"path/filepath"
	"testing"
)

// TestMediaSearchFiltersAndPages: the photo page's rows are the pictures the kind and player filters keep, newest first, a page
// at a time; each row names its uploader's account row when there is one; a kind no picture has is refused.
func TestMediaSearchFiltersAndPages(t *testing.T) {
	docStore(t)
	mediaDataDir(t)
	first := storeMediaFile(t, MediaDiary, "9031", "9031", "1760000001", "9031_1760000001")
	second := storeMediaFile(t, MediaRoom, "9031", "9031", "1760000002", "9031_1760000002")
	third := storeMediaFile(t, MediaProfile, "9032", "9032", "1760000003", "9032_1760000003")
	ids := func(rows []PictureRow) []int64 {
		var out []int64
		for _, r := range rows {
			out = append(out, r.ID)
		}
		return out
	}
	all, err := MediaSearch(PictureFilter{}, 0, 10)
	if err != nil || !equalIDs(ids(all), []int64{third.ID, second.ID, first.ID}) {
		t.Fatalf("all pictures = %v (err %v), want the newest first", ids(all), err)
	}
	diary, err := MediaSearch(PictureFilter{Kind: MediaDiary}, 0, 10)
	if err != nil || !equalIDs(ids(diary), []int64{first.ID}) {
		t.Fatalf("kind diary = %v (err %v)", ids(diary), err)
	}
	byAid, err := MediaSearch(PictureFilter{Player: "9031"}, 0, 10)
	if err != nil || !equalIDs(ids(byAid), []int64{second.ID, first.ID}) {
		t.Fatalf("player 9031 = %v (err %v)", ids(byAid), err)
	}
	if byAid[0].PlayerID != 0 || byAid[0].Owner != "9031" {
		t.Fatalf("an uploader with no account row: player id %d, owner %q", byAid[0].PlayerID, byAid[0].Owner)
	}
	page, err := MediaSearch(PictureFilter{}, 0, 2)
	if err != nil || !equalIDs(ids(page), []int64{third.ID, second.ID}) {
		t.Fatalf("first page = %v (err %v)", ids(page), err)
	}
	rest, err := MediaSearch(PictureFilter{}, page[1].ID, 2)
	if err != nil || !equalIDs(ids(rest), []int64{first.ID}) {
		t.Fatalf("second page = %v (err %v)", ids(rest), err)
	}
	if _, err := MediaSearch(PictureFilter{Kind: "avatar"}, 0, 10); !errors.Is(err, ErrBadInput) {
		t.Fatalf("kind avatar: err %v, want ErrBadInput", err)
	}
	if _, err := MediaSearch(PictureFilter{}, 0, 0); !errors.Is(err, ErrBadInput) {
		t.Fatalf("limit 0: err %v, want ErrBadInput", err)
	}
}

// TestMediaPathStaysInItsFolder: a picture's path is its kind's folder and the stored name; a row whose name is not the one
// MediaFileName makes has no path.
func TestMediaPathStaysInItsFolder(t *testing.T) {
	docStore(t)
	dir := mediaDataDir(t)
	row := storeMediaFile(t, MediaDiary, "9031", "9031", "1760000004", "9031_1760000004")
	p, err := MediaPath(row)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(dir, "diary-media", row.File); p != want {
		t.Fatalf("path = %s, want %s", p, want)
	}
	for _, bad := range []string{"../escape.img", "diary-media/x.img", "short.img", ""} {
		if _, err := MediaPath(MediaFile{Kind: MediaDiary, File: bad}); err == nil {
			t.Errorf("file %q has a path", bad)
		}
	}
	if _, err := MediaPath(MediaFile{Kind: "avatar", File: row.File}); err == nil {
		t.Error("an unknown kind has a path")
	}
}

// TestMediaRowByIDFindsOnlyTheRow: a row is found by its id, and an unknown id is not found, without an error.
func TestMediaRowByIDFindsOnlyTheRow(t *testing.T) {
	docStore(t)
	mediaDataDir(t)
	row := storeMediaFile(t, MediaRoom, "9041", "9041", "1760000005", "9041_1760000005")
	got, found, err := MediaRowByID(row.ID)
	if err != nil || !found || got.File != row.File || got.Owner != "9041" {
		t.Fatalf("row %d: found=%t err=%v got=%+v", row.ID, found, err, got)
	}
	if _, found, err := MediaRowByID(row.ID + 999); err != nil || found {
		t.Fatalf("an unknown id: found=%t err=%v", found, err)
	}
}

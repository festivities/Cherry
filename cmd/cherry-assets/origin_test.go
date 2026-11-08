package main

import (
	"path/filepath"
	"testing"
)

// checkOrig lists the two default diary skin files of the santi backup by their exact paths and refuses any other file
// under DiarySkin.
func TestCheckOrigListsOnlyTheDiarySkinFiles(t *testing.T) {
	folder := filepath.Join(defaultDataset, "DiarySkin")
	for _, rel := range []string{"common/UIImage_hd/avatar_photo_bg.png", "00001/UIImage_hd/skin_info.json"} {
		o := &object{key: "static/DiarySkin/" + rel, path: filepath.Join(folder, filepath.FromSlash(rel)), source: "DiarySkin", rel: rel}
		if err := checkOrig(o); err != nil {
			t.Errorf("%s refused: %v", o.key, err)
		}
	}
	other := &object{key: "static/DiarySkin/00001/UIImage_hd/avatar_photo_bg.png", path: filepath.Join(folder, "00001", "UIImage_hd", "avatar_photo_bg.png"), source: "DiarySkin", rel: "00001/UIImage_hd/avatar_photo_bg.png"}
	if err := checkOrig(other); err == nil {
		t.Errorf("unlisted diary skin file accepted: %s", other.key)
	}
}

// checkOrig accepts a DailyBonus original under its folder and refuses Cherry-made names, a foreign key and a path
// outside the listed folder.
func TestCheckOrigRefusesCherryMadeAndForeignFiles(t *testing.T) {
	folder := filepath.Join(extraDataset, "DailyBonus")
	ok := &object{key: "static/DailyBonus/UIImage_hd/alpha1px.png", path: filepath.Join(folder, "UIImage_hd", "alpha1px.png"), size: 1, source: "DailyBonus", rel: "UIImage_hd/alpha1px.png"}
	if err := checkOrig(ok); err != nil {
		t.Fatalf("original refused: %v", err)
	}
	cases := map[string]*object{
		"cherry_day tile": {key: "static/DailyBonus/UIImage_hd/cherry_day01.png", path: filepath.Join(folder, "UIImage_hd", "cherry_day01.png"), source: "DailyBonus", rel: "UIImage_hd/cherry_day01.png"},
		"-cherry folder":  {key: "static/DailyBonus/-cherry/x.png", path: filepath.Join(folder, "-cherry", "x.png"), source: "DailyBonus", rel: "-cherry/x.png"},
		"foreign key":     {key: "static/DailyBonus/other.png", path: filepath.Join(folder, "UIImage_hd", "alpha1px.png"), source: "DailyBonus", rel: "UIImage_hd/alpha1px.png"},
		"outside folder":  {key: "static/DailyBonus/x.png", path: filepath.Join(extraDataset, "x.png"), source: "DailyBonus", rel: "x.png"},
		"fish other file": {key: "static/android-4.0.0.0-2016/animation/fish/other.png", path: filepath.Join(originalAssetsRoot, "android-4.0.0.0-2016", "animation", "fish", "other.png"), source: "android-4.0.0.0-2016", rel: "animation/fish/other.png"},
	}
	for name, o := range cases {
		if err := checkOrig(o); err == nil {
			t.Errorf("%s accepted: %s", name, o.key)
		}
	}
}

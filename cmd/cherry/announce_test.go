package main

import (
	"testing"
	"unicode/utf8"

	"cherry/internal/lpn"
)

// TestRestartNoticeFitsTheBanner: the restart banner stays within the banner's one line on every phone (lpn.BannerMaxRunes).
func TestRestartNoticeFitsTheBanner(t *testing.T) {
	if n := utf8.RuneCountInString(restartNotice); n > lpn.BannerMaxRunes {
		t.Fatalf("restartNotice has %d characters, the banner shows at most %d", n, lpn.BannerMaxRunes)
	}
}

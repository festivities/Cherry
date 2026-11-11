package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"cherry/internal/assets"
	"cherry/internal/config"
	"cherry/internal/lpn"
	"cherry/internal/opdata"
	"cherry/internal/store"
	"cherry/internal/thumbs"
)

// Two different folders put every state and cache path under them, and nothing under the lab folder.
func TestPathsFollowTwoFolders(t *testing.T) {
	data, cache := t.TempDir(), t.TempDir()
	oldDp, oldRoot, oldGateway := thumbs.DpCacheDir, assets.GeneratedItemRoot, lpn.GatewayAddr
	t.Cleanup(func() {
		config.Activate(config.Config{})
		thumbs.DpCacheDir, assets.GeneratedItemRoot, lpn.GatewayAddr = oldDp, oldRoot, oldGateway
	})
	activate(config.Config{DataDir: data, CacheDir: cache, Gateway: ":20000"})

	for name, load := range map[string]func() error{
		"accounts":  store.LoadAccounts,
		"social":    store.LoadSocial,
		"circles":   store.LoadCircles,
		"treasure":  store.LoadTreasure,
		"chatrooms": func() error { _, err := store.LoadChatRooms(); return err },
	} {
		if err := load(); err != nil {
			t.Fatalf("load %s: %v", name, err)
		}
	}
	state := map[string]string{
		"accounts.json":  store.AccountStorePath,
		"social.json":    store.SocialStorePath,
		"circles.json":   store.CirclesStorePath,
		"treasure.json":  store.TreasureStorePath,
		"chatrooms.json": store.ChatRoomsStorePath,
		"config":         opdata.Dir(),
		"generated/item": assets.GeneratedItemRoot,
	}
	for name, p := range state {
		if !under(p, data) {
			t.Errorf("%s = %q, want it under the data folder %q", name, p, data)
		}
	}
	if !under(thumbs.DpCacheDir, cache) {
		t.Errorf("dp-cache = %q, want it under the cache folder %q", thumbs.DpCacheDir, cache)
	}
	if lpn.GatewayAddr != ":20000" {
		t.Errorf("gateway = %q, want :20000", lpn.GatewayAddr)
	}
}

// under reports whether p is a path inside dir (both made absolute and cleaned first).
func under(p, dir string) bool {
	if p == "" {
		return false
	}
	rel, err := filepath.Rel(dir, p)
	return err == nil && rel != "." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator)) && rel != ".."
}

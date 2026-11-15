package web

import (
	"bytes"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"cherry/internal/store"
)

// ScrubDeletedBackups applies the retention of deleted-accounts.jsonl (PLAN 8.6b part 4, config retention.deleted-days):
// a backup line older than keepDays keeps only its time, its aid and a scrubbed flag. The name, the posts, the guestbook
// text, the friendships, the comments and the token hashes go. Lines newer than the retention, and every line when
// keepDays is below 1, stay as they are. It rewrites the file through a temporary file and reports how many lines it
// scrubbed. Records are appended under AccountsMu, so the rewrite takes the same lock.
func ScrubDeletedBackups(now time.Time, keepDays int) (int, error) {
	if keepDays < 1 {
		return 0, nil
	}
	store.AccountsMu.Lock()
	defer store.AccountsMu.Unlock()
	dir := store.StoreDirLocked()
	if dir == "" { // no database attached (tests run in memory): nothing is written
		return 0, nil
	}
	return scrubFile(filepath.Join(dir, "deleted-accounts.jsonl"), now, keepDays)
}

// scrubFile is ScrubDeletedBackups for one file (the caller holds whatever lock the file needs).
func scrubFile(path string, now time.Time, keepDays int) (int, error) {
	if keepDays < 1 {
		return 0, nil
	}
	raw, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	cutoff := now.AddDate(0, 0, -keepDays)
	var out bytes.Buffer
	scrubbed := 0
	for _, line := range bytes.Split(raw, []byte("\n")) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var b deletedBackup
		if err := json.Unmarshal(line, &b); err == nil {
			if at, perr := time.Parse(time.RFC3339, b.Time); perr == nil && !at.Before(cutoff) {
				out.Write(line)
				out.WriteByte('\n')
				continue
			}
		}
		// Old, or unreadable (which holds nothing the operator can use either): the aid and the time are all that remain.
		kept := map[string]any{"time": b.Time, "account": map[string]string{"aid": b.Account.Aid}, "scrubbed": true}
		enc, err := json.Marshal(kept)
		if err != nil {
			return scrubbed, err
		}
		out.Write(enc)
		out.WriteByte('\n')
		scrubbed++
	}
	if scrubbed == 0 {
		return 0, nil
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, out.Bytes(), 0o600); err != nil {
		return 0, err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return 0, err
	}
	return scrubbed, nil
}

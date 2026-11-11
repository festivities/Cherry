package auth

import (
	"errors"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"
	"time"
)

// allowlist is auth.allowlist-file: the identities that may make an account for the first time (auth.signup allowlist).
// It is read at start and again when the file's modification time or size changes (one stat per check, no watcher). A
// file that cannot be read or parsed later admits no new identity (fail closed) and is logged once per change; an identity
// that already has an account always signs in (MintIdentityCode).
type allowlist struct {
	path   string
	logger *log.Logger

	mu   sync.Mutex
	mod  time.Time
	size int64
	ids  map[string]bool // "discord:<id>"
}

// loadAllowlist reads the file once; an error refuses the start.
func loadAllowlist(path string, logger *log.Logger) (*allowlist, error) {
	if path == "" {
		return nil, errors.New("auth: auth.signup allowlist needs auth.allowlist-file")
	}
	a := &allowlist{path: path, logger: logger}
	if err := a.reload(); err != nil {
		return nil, err
	}
	return a, nil
}

// has reports whether identity may make its first account now.
func (a *allowlist) has(identity string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if info, err := os.Stat(a.path); err == nil && info.ModTime().Equal(a.mod) && info.Size() == a.size {
		return a.ids[identity]
	}
	if err := a.reload(); err != nil {
		a.logger.Printf("cherry: auth allowlist: %v; no new identity is admitted until it is fixed", err)
	}
	return a.ids[identity]
}

// reload reads the file now and keeps its stat, so the same change is not read again. On an error no identity is admitted.
// Caller holds a.mu (or is loadAllowlist).
func (a *allowlist) reload() error {
	info, err := os.Stat(a.path)
	if err != nil {
		a.ids = nil
		return fmt.Errorf("allowlist file: %w", err)
	}
	a.mod, a.size = info.ModTime(), info.Size()
	raw, err := os.ReadFile(a.path)
	if err != nil {
		a.ids = nil
		return fmt.Errorf("allowlist file: %w", err)
	}
	ids, err := parseAllowlist(raw)
	a.ids = ids
	if err != nil {
		return fmt.Errorf("allowlist file: %w", err)
	}
	return nil
}

// parseAllowlist reads one identity per line: "discord:<id>" or a bare numeric id (stored as discord:<id>); "#" starts a
// comment. An error names the line number, never the line.
func parseAllowlist(raw []byte) (map[string]bool, error) {
	ids := map[string]bool{}
	for n, line := range strings.Split(string(raw), "\n") {
		if i := strings.IndexByte(line, '#'); i >= 0 {
			line = line[:i]
		}
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		id, ok := strings.CutPrefix(line, "discord:")
		if !ok {
			id = line
		}
		if !isSnowflake(id) {
			return nil, fmt.Errorf("line %d is not discord:<id> or a numeric id", n+1)
		}
		ids["discord:"+id] = true
	}
	return ids, nil
}

// isSnowflake: a Discord id is 1 to 20 decimal digits.
func isSnowflake(s string) bool {
	if len(s) < 1 || len(s) > 20 {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

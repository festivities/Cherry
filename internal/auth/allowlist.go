package auth

import (
	"errors"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"
	"time"

	"cherry/internal/config"
)

// Allowlist is an allowlist file of identities: auth.allowlist-file (the identities that may make an account for the first
// time, auth.signup allowlist) and staff.allowlist-file (the other staff of the staff UI). It is read at start and again
// when the file's modification time or size changes (one stat per check, no watcher). A file that cannot be read or
// parsed later admits no identity (fail closed) and is logged once per change; a player whose identity already has an
// account always signs in (MintIdentityCode).
type Allowlist struct {
	path     string
	devNames bool // dev-<name> lines are accepted: the dev-fake provider, staff file only
	logger   *log.Logger

	mu   sync.Mutex
	mod  time.Time
	size int64
	ids  map[string]bool // "discord:<id>" or "dev-<name>"
}

// LoadAllowlist reads the file once; an error refuses the start. devNames accepts dev-<name> lines (dev-fake only, so the
// caller decides). logger gets one line per refused re-read and never a value.
func LoadAllowlist(path string, devNames bool, logger *log.Logger) (*Allowlist, error) {
	if path == "" {
		return nil, errors.New("auth: allowlist file is not set")
	}
	if logger == nil {
		logger = log.Default()
	}
	a := &Allowlist{path: path, devNames: devNames, logger: logger}
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := a.reload(); err != nil {
		return nil, err
	}
	return a, nil
}

// Has reports whether identity is on the list now. A nil list holds nobody.
func (a *Allowlist) Has(identity string) bool {
	if a == nil {
		return false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if info, err := os.Stat(a.path); err == nil && info.ModTime().Equal(a.mod) && info.Size() == a.size {
		return a.ids[identity]
	}
	if err := a.reload(); err != nil {
		a.logger.Printf("cherry: allowlist: %v; no new identity is admitted until it is fixed", err)
	}
	return a.ids[identity]
}

// reload reads the file now and keeps its stat, so the same change is not read again. On an error no identity is admitted.
// Caller holds a.mu (or is LoadAllowlist before the value is shared).
func (a *Allowlist) reload() error {
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
	ids, err := parseAllowlistFor(raw, a.devNames)
	a.ids = ids
	if err != nil {
		return fmt.Errorf("allowlist file: %w", err)
	}
	return nil
}

// parseAllowlist reads the players' form: one identity per line.
func parseAllowlist(raw []byte) (map[string]bool, error) { return parseAllowlistFor(raw, false) }

// parseAllowlistFor reads one identity per line: "discord:<id>" or a bare numeric id (stored as discord:<id>), and with
// devNames "dev-<name>"; "#" starts a comment. An error names the line number, never the line.
func parseAllowlistFor(raw []byte, devNames bool) (map[string]bool, error) {
	ids := map[string]bool{}
	for n, line := range strings.Split(string(raw), "\n") {
		if i := strings.IndexByte(line, '#'); i >= 0 {
			line = line[:i]
		}
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if devNames && strings.HasPrefix(line, "dev-") {
			if !config.ValidDevName(strings.TrimPrefix(line, "dev-")) {
				return nil, fmt.Errorf("line %d is not discord:<id>, a numeric id or dev-<name>", n+1)
			}
			ids[line] = true
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

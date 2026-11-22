package admin

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"cherry/internal/store"
	"cherry/internal/web"
)

// The Server limits table (owner only): the connection caps and the request and frame buckets, changed while Cherry runs.
// A save or a reset writes the audit row (store.SetLimit, store.ClearLimit), then web.ApplyLimits makes the value in effect
// live. Startup applies the same values (cmd/cherry), so a stored value survives a restart.

// limitRow is one row of the table: the configured value, the panel's value (when one is stored) and the value in effect.
type limitRow struct {
	Key, Label, Help  string
	Min, Max          int
	Configured, Panel int
	Effective         int
	FromPanel         bool
}

// limitTexts are the plain-language names of the limits, by config key.
var limitTexts = map[string]struct{ Label, Help string }{
	"limits.conns-per-address": {"Connections per address", "Open connections one address may hold on the game ports. Carrier networks put many players behind one address."},
	"limits.conns-total":       {"Connections in total", "Open connections across every game port. Keep it below the process's file limit (16,384)."},
	"limits.api-rate":          {"Game requests a second, per address", "Each address earns this many game requests a second. 0 turns the limit off."},
	"limits.api-burst":         {"Game request burst, per address", "Requests an address may send at once. At least 1 while the rate is above 0."},
	"limits.account-rate":      {"Game requests a second, per account", "Each signed-in account earns this many game requests a second. 0 turns the limit off."},
	"limits.account-burst":     {"Game request burst, per account", "Requests an account may send at once. At least 1 while the rate is above 0."},
	"limits.guest-per-minute":  {"Guest sign-ups a minute, per address", "New guest accounts an address may create a minute. 0 turns the limit off."},
	"limits.static-rate":       {"Picture and asset requests a second, per address", "Static files (pictures, item art) an address earns a second. 0 turns the limit off."},
	"limits.static-burst":      {"Picture and asset burst, per address", "Static files an address may request at once. At least 1 while the rate is above 0."},
	"limits.frame-rate":        {"Frames a second, per connection", "Frames a game connection may send a second; frames over it are dropped. The chat's session socket gets four times this. 0 turns the limit off."},
	"limits.frame-burst":       {"Frame burst, per connection", "Frames a connection may send at once. At least 1 while the rate is above 0."},
}

// limitRows builds the table from the configured values, the stored panel values and the values in effect.
func (s *Server) limitRows() ([]limitRow, error) {
	overrides, err := store.LimitOverrides()
	if err != nil {
		return nil, err
	}
	configured := store.ConfiguredLimits(s.cfg)
	effective := store.EffectiveLimits(s.cfg)
	rows := make([]limitRow, 0, len(store.LimitKeys))
	for _, k := range store.LimitKeys {
		panel, set := overrides[k.Key]
		rows = append(rows, limitRow{
			Key: k.Key, Label: limitTexts[k.Key].Label, Help: limitTexts[k.Key].Help, Min: k.Min, Max: k.Max,
			Configured: configured.Value(k.Key), Panel: panel, Effective: effective.Value(k.Key), FromPanel: set,
		})
	}
	return rows, nil
}

// handleLimit: POST /settings/limit (privileged, owner). Form: key (a store.LimitKeys name), value (a whole number in its range).
func (s *Server) handleLimit(w http.ResponseWriter, r *http.Request, sess session) {
	if !s.settingsOwner(w, sess) {
		return
	}
	key := r.PostFormValue("key")
	k, ok := limitKeyOf(key)
	if !ok {
		s.fail(w, http.StatusBadRequest, "That is not one of the server limits.")
		return
	}
	n, err := strconv.Atoi(strings.TrimSpace(r.PostFormValue("value")))
	if err != nil || n < k.Min || n > k.Max {
		s.fail(w, http.StatusBadRequest, fmt.Sprintf("%s must be a whole number from %d to %d.", limitTexts[key].Label, k.Min, k.Max))
		return
	}
	if err := store.SetLimit(s.cfg, key, n, sess.identity); err != nil {
		s.storeError(w, err)
		return
	}
	web.ApplyLimits(store.EffectiveLimits(s.cfg))
	redirect(w, http.StatusSeeOther, "/settings?notice=limit-saved")
}

// handleLimitReset: POST /settings/limit/reset (privileged, owner). Form: key. The configured value applies again.
func (s *Server) handleLimitReset(w http.ResponseWriter, r *http.Request, sess session) {
	if !s.settingsOwner(w, sess) {
		return
	}
	if err := store.ClearLimit(s.cfg, r.PostFormValue("key"), sess.identity); err != nil {
		s.storeError(w, err)
		return
	}
	web.ApplyLimits(store.EffectiveLimits(s.cfg))
	redirect(w, http.StatusSeeOther, "/settings?notice=limit-reset")
}

// limitKeyOf returns the table entry for key.
func limitKeyOf(key string) (store.LimitKey, bool) {
	for _, k := range store.LimitKeys {
		if k.Key == key {
			return k, true
		}
	}
	return store.LimitKey{}, false
}

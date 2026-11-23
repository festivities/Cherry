package store

import (
	"database/sql"
	"fmt"
	"strconv"

	"cherry/internal/config"
)

// The server limits the staff panel changes while Cherry runs (Settings, "Server limits"). Each one is a meta row under its
// config key; the value in effect is the row when one is stored and in range, else the server's configured value. Every
// reader takes that answer from EffectiveLimits, so the config file, the panel and the apply step never disagree.

// LimitKey is one limit the panel may change: its config key and the range the panel takes (0 = off for a rate).
type LimitKey struct {
	Key      string
	Min, Max int
}

// LimitKeys are the panel's limits, in the order the page shows them.
var LimitKeys = []LimitKey{
	{"limits.conns-per-address", 1, 10000},
	{"limits.conns-total", 1, 50000}, // the unit raises LimitNOFILE to 65536 at deploy: headroom for the stores and the assets
	{"limits.api-rate", 0, 100000},
	{"limits.api-burst", 1, 1000000},
	{"limits.account-rate", 0, 100000},
	{"limits.account-burst", 1, 1000000},
	{"limits.guest-per-minute", 0, 10000},
	{"limits.static-rate", 0, 100000},
	{"limits.static-burst", 1, 1000000},
	{"limits.frame-rate", 0, 100000},
	{"limits.frame-burst", 1, 1000000},
}

// Limits holds one value per LimitKeys entry, named by its config field.
type Limits struct {
	ConnsPerAddress, ConnsTotal                    int
	APIRate, APIBurst, AccountRate, AccountBurst   int
	GuestPerMinute                                 int
	StaticRate, StaticBurst, FrameRate, FrameBurst int
}

// field returns the field of l that key names, nil for a key the panel does not change.
func (l *Limits) field(key string) *int {
	switch key {
	case "limits.conns-per-address":
		return &l.ConnsPerAddress
	case "limits.conns-total":
		return &l.ConnsTotal
	case "limits.api-rate":
		return &l.APIRate
	case "limits.api-burst":
		return &l.APIBurst
	case "limits.account-rate":
		return &l.AccountRate
	case "limits.account-burst":
		return &l.AccountBurst
	case "limits.guest-per-minute":
		return &l.GuestPerMinute
	case "limits.static-rate":
		return &l.StaticRate
	case "limits.static-burst":
		return &l.StaticBurst
	case "limits.frame-rate":
		return &l.FrameRate
	case "limits.frame-burst":
		return &l.FrameBurst
	}
	return nil
}

// Value returns the limit that key names (0 for a key the panel does not change).
func (l Limits) Value(key string) int {
	if p := l.field(key); p != nil {
		return *p
	}
	return 0
}

// limitKey returns the LimitKeys entry for key.
func limitKey(key string) (LimitKey, bool) {
	for _, k := range LimitKeys {
		if k.Key == key {
			return k, true
		}
	}
	return LimitKey{}, false
}

// ConfiguredLimits is the server's configured value of every panel limit.
func ConfiguredLimits(c config.Config) Limits {
	return Limits{
		ConnsPerAddress: c.ConnsPerAddress, ConnsTotal: c.ConnsTotal,
		APIRate: c.APIRate, APIBurst: c.APIBurst, AccountRate: c.AccountRate, AccountBurst: c.AccountBurst,
		GuestPerMinute: c.GuestPerMinute,
		StaticRate:     c.StaticRate, StaticBurst: c.StaticBurst, FrameRate: c.FrameRate, FrameBurst: c.FrameBurst,
	}
}

// limitOverrides reads the stored panel values by key: only the keys with a valid stored value are in the map.
func limitOverrides(q Querier) (map[string]int, error) {
	out := map[string]int{}
	for _, k := range LimitKeys {
		n, set, err := int64MetaOverride(q, k.Key, int64(k.Min), int64(k.Max))
		if err != nil {
			return nil, err
		}
		if set {
			out[k.Key] = int(n)
		}
	}
	return out, nil
}

// EffectiveLimits is the set of limits in effect: the panel's stored value per key, else c's configured value. Without a
// database, or when the read fails, it is the configured set.
func EffectiveLimits(c config.Config) Limits {
	l := ConfiguredLimits(c)
	d := attachedDB()
	if d == nil {
		return l
	}
	var ov map[string]int
	if err := d.View(func(q Querier) error {
		var err error
		ov, err = limitOverrides(q)
		return err
	}); err != nil {
		return l
	}
	for key, n := range ov {
		*l.field(key) = n
	}
	return l
}

// LimitOverrides returns the panel's stored values by key (only the keys that have one). ErrNoDatabase without a database.
func LimitOverrides() (map[string]int, error) {
	d, err := docDB()
	if err != nil {
		return nil, err
	}
	var ov map[string]int
	err = d.View(func(q Querier) error {
		var err error
		ov, err = limitOverrides(q)
		return err
	})
	return ov, err
}

// SetLimit stores n as the panel's value for key and writes its audit row in one transaction. key must be one of LimitKeys
// and n in its range. The stored set, with c's configured values for the keys without one, must still hold together
// (checkLimitPairs); otherwise nothing changes. Refusals are ErrBadInput.
func SetLimit(c config.Config, key string, n int, actor string) error {
	k, ok := limitKey(key)
	if !ok {
		return fmt.Errorf("%w: %s is not a server limit", ErrBadInput, key)
	}
	if n < k.Min || n > k.Max {
		return fmt.Errorf("%w: %s must be %d to %d", ErrBadInput, key, k.Min, k.Max)
	}
	return changeLimit(c, key, strconv.Itoa(n), actor)
}

// ClearLimit clears the panel's value for key, so the configured value applies again, and writes its audit row. The same
// pair check as SetLimit applies to the result.
func ClearLimit(c config.Config, key, actor string) error {
	if _, ok := limitKey(key); !ok {
		return fmt.Errorf("%w: %s is not a server limit", ErrBadInput, key)
	}
	return changeLimit(c, key, "", actor)
}

// changeLimit writes val (or removes the row when val is "") and the audit row, after the pair check passes.
func changeLimit(c config.Config, key, val, actor string) error {
	d, err := docDB()
	if err != nil {
		return err
	}
	return d.Update(func(tx *sql.Tx) error {
		old, err := metaBefore(tx, key)
		if err != nil {
			return err
		}
		if val == "" {
			_, err = tx.Exec(`DELETE FROM meta WHERE key = ?`, key)
		} else {
			err = setMetaTx(tx, key, val)
		}
		if err != nil {
			return err
		}
		ov, err := limitOverrides(tx)
		if err != nil {
			return err
		}
		eff := ConfiguredLimits(c)
		for k, n := range ov {
			*eff.field(k) = n
		}
		if err := checkLimitPairs(eff); err != nil {
			return err
		}
		newVal := val
		if newVal == "" {
			newVal = "default"
		}
		return auditTx(tx, actor, "setting", key, map[string]any{"old": old, "new": newVal})
	})
}

// checkLimitPairs refuses a rate above zero with a burst below one: the bucket would refuse every request.
func checkLimitPairs(l Limits) error {
	for _, p := range []struct {
		name        string
		rate, burst int
	}{{"api", l.APIRate, l.APIBurst}, {"account", l.AccountRate, l.AccountBurst}, {"static", l.StaticRate, l.StaticBurst}, {"frame", l.FrameRate, l.FrameBurst}} {
		if p.rate > 0 && p.burst < 1 {
			return fmt.Errorf("%w: limits.%s-rate needs limits.%s-burst of at least 1", ErrBadInput, p.name, p.name)
		}
	}
	return nil
}

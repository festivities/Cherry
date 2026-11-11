package store

import (
	"os"

	"cherry/internal/metrics"
)

// The two values only the store can read are published for the admin listener (metrics): the accounts in memory and the
// size of the database's WAL file (cherry.db-wal; 0 without a database).
func init() {
	metrics.Gauge("accounts", func() int64 {
		AccountsMu.Lock()
		defer AccountsMu.Unlock()
		return int64(AccountCountLocked())
	})
	metrics.Gauge("wal_bytes", func() int64 {
		d := attachedDB()
		if d == nil {
			return 0
		}
		fi, err := os.Stat(d.Path() + "-wal")
		if err != nil {
			return 0
		}
		return fi.Size()
	})
}

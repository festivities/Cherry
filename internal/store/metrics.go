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
		return fileSize(d.Path() + "-wal")
	})
}

// fileSize is the size of a file, 0 when it cannot be read.
func fileSize(path string) int64 {
	fi, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return fi.Size()
}

// DBBytes is the size of the attached database file and of its WAL file (zeros without a database).
func DBBytes() (db, wal int64) {
	d := attachedDB()
	if d == nil {
		return 0, 0
	}
	return fileSize(d.Path()), fileSize(d.Path() + "-wal")
}

// AccountCount is the number of accounts in memory (the staff dashboard). It takes AccountsMu itself.
func AccountCount() int {
	AccountsMu.Lock()
	defer AccountsMu.Unlock()
	return AccountCountLocked()
}

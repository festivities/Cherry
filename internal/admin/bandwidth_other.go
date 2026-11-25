//go:build !linux

package admin

// readHostTxBytes returns unavailable on non-Linux hosts where /proc/net counters do not exist.
func readHostTxBytes() (uint64, string, bool) {
	return 0, "", false
}

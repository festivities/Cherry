//go:build linux

package admin

import "os"

// readHostTxBytes returns the cumulative outbound byte counter and name of the host's active default-route interface.
func readHostTxBytes() (uint64, string, bool) {
	routeData, err := os.ReadFile("/proc/net/route")
	if err != nil {
		return 0, "", false
	}
	iface := parseDefaultRoute(routeData)
	if iface == "" {
		return 0, "", false
	}
	devData, err := os.ReadFile("/proc/net/dev")
	if err != nil {
		return 0, "", false
	}
	tx, ok := parseProcNetDevTx(devData, iface)
	if !ok {
		return 0, "", false
	}
	return tx, iface, true
}

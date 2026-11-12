package config

import (
	"strings"
	"testing"
)

// TestDevFakeStaffBindsLoopback: the dev-fake test provider signs anyone in, so its staff UI never listens on an address
// other than loopback.
func TestDevFakeStaffBindsLoopback(t *testing.T) {
	for _, addr := range []string{":18444", "0.0.0.0:18444", "192.168.1.7:18444"} {
		_, err := loadWith(t, map[string]string{"CHERRY_AUTH_MODE": "dev-fake", "CHERRY_LISTEN_STAFF": addr})
		if err == nil || !strings.Contains(err.Error(), "loopback") {
			t.Errorf("dev-fake on %s: %v", addr, err)
		}
	}
	for _, addr := range []string{"127.0.0.1:18444", "localhost:18444", "[::1]:18444"} {
		if _, err := loadWith(t, map[string]string{"CHERRY_AUTH_MODE": "dev-fake", "CHERRY_LISTEN_STAFF": addr}); err != nil {
			t.Errorf("dev-fake on %s: %v", addr, err)
		}
	}
}

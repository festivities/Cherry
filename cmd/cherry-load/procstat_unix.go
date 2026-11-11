//go:build !windows

package main

import (
	"bytes"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// procStats reads the resident set (bytes) and the CPU time (user + system, seconds) of pid from /proc (Linux).
// The clock tick rate is taken as 100 Hz, the Linux default.
func procStats(pid int) (rss int64, cpuSeconds float64, err error) {
	status, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", pid))
	if err != nil {
		return 0, 0, err
	}
	for _, line := range strings.Split(string(status), "\n") {
		if f := strings.Fields(line); len(f) >= 2 && f[0] == "VmRSS:" {
			kb, _ := strconv.ParseInt(f[1], 10, 64)
			rss = kb * 1024
		}
	}
	stat, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return rss, 0, err
	}
	// The command name sits in parentheses and may hold spaces; the fields after it start at field 3 (state).
	after := stat[bytes.LastIndexByte(stat, ')')+2:]
	f := strings.Fields(string(after))
	if len(f) < 13 {
		return rss, 0, fmt.Errorf("short /proc/%d/stat", pid)
	}
	utime, _ := strconv.ParseFloat(f[11], 64) // field 14
	stime, _ := strconv.ParseFloat(f[12], 64) // field 15
	return rss, (utime + stime) / 100, nil
}

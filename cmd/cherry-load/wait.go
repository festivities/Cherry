package main

import (
	"flag"
	"fmt"
	"os"
	"strings"
	"time"
)

// runWait polls the server's log until the line -for appears (the server's "https listening" by default) and prints how
// long that took from the call, with the process's RSS and CPU at that moment. The caller starts it right after the launch.
func runWait(args []string) error {
	fs := flag.NewFlagSet("wait", flag.ContinueOnError)
	logPath := fs.String("log", "", "server log file")
	want := fs.String("for", "https listening", "log text to wait for")
	pid := fs.Int("pid", 0, "server PID (RSS and CPU are printed when given)")
	timeout := fs.Duration("timeout", 2*time.Minute, "give up after this long")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *logPath == "" {
		return fmt.Errorf("wait: -log is required")
	}
	t0 := time.Now()
	for {
		raw, _ := os.ReadFile(*logPath)
		if strings.Contains(string(raw), *want) {
			break
		}
		if time.Since(t0) > *timeout {
			return fmt.Errorf("%q not in %s after %s", *want, *logPath, *timeout)
		}
		time.Sleep(20 * time.Millisecond)
	}
	elapsed := time.Since(t0)
	fmt.Printf("ready after %s: %q\n", elapsed.Round(time.Millisecond), *want)
	if *pid > 0 {
		rss, cpu, err := procStats(*pid)
		if err != nil {
			return err
		}
		fmt.Printf("pid=%d rss_bytes=%d cpu_seconds=%.2f\n", *pid, rss, cpu)
	}
	return nil
}

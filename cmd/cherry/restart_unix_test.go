//go:build unix

package main

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

// TestReexecStartsAgain: reexec starts the same binary again with the same arguments and environment (the test binary
// here, run as a child: its first start re-executes, its second prints and exits).
func TestReexecStartsAgain(t *testing.T) {
	switch os.Getenv("CHERRY_REEXEC_TEST") {
	case "first":
		os.Setenv("CHERRY_REEXEC_TEST", "second")
		t.Fatalf("reexec returned: %v", reexec())
	case "second":
		os.Stdout.WriteString("started again\n")
		os.Exit(0)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestReexecStartsAgain$")
	cmd.Env = append(os.Environ(), "CHERRY_REEXEC_TEST=first")
	out, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(out), "started again") {
		t.Fatalf("child: %v\n%s", err, out)
	}
}

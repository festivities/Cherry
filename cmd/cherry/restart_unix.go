//go:build unix

package main

import (
	"os"
	"syscall"
)

// canReexec: the staff UI's Restart button is offered (admin.OnRestart).
const canReexec = true

// reexec replaces the process with a fresh start of its binary, same arguments and environment. Every descriptor Go opens
// is close-on-exec, so the listeners are free for the new start. It returns only on failure.
func reexec() error {
	exe, err := os.Executable() // on Linux the binary's path, also after a deploy replaced the file
	if err != nil {
		return err
	}
	return syscall.Exec(exe, os.Args, os.Environ())
}

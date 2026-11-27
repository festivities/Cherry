//go:build !unix

package main

import "errors"

// canReexec: no exec here (the Windows lab), so the staff UI's Restart button is hidden.
const canReexec = false

func reexec() error { return errors.New("restart needs a Unix host") }

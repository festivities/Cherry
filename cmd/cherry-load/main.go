// Command cherry-load is the lab's load tool for Cherry (PLAN P9, slice 8.4): it seeds a database of synthetic accounts
// through the store's own write path, drives synthetic players against an isolated server, probes the WAL cost of a save,
// and waits for a server's start. It is a lab tool: nothing here ships with the server, and its data is synthetic.
//
//	cherry-load seed -data-dir <dir> [-accounts 10000] [-force]
//	cherry-load run -out <json> [-players 300] [-duration 5m] [-server-pid N -server-log <file>] ...
//	cherry-load probe -data-dir <copy of a seeded dir> -out <json> [-saves 200]
//	cherry-load wait -log <file> [-for "https listening"] [-pid N] [-timeout 2m]
package main

import (
	"fmt"
	"os"

	"cherry/internal/store"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: cherry-load seed|run|probe|wait [flags]")
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "seed":
		err = runSeed(os.Args[2:])
	case "run":
		err = runLoad(os.Args[2:])
	case "probe":
		err = runProbe(os.Args[2:])
	case "wait":
		err = runWait(os.Args[2:])
	default:
		err = fmt.Errorf("unknown command %q", os.Args[1])
	}
	if err != nil {
		store.CloseStore()
		fmt.Fprintln(os.Stderr, "cherry-load:", err)
		os.Exit(1)
	}
}

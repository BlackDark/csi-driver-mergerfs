// Command csi-driver-mergerfs runs the mergerfs CSI driver: the node plugin or the
// controller for Dynamic Volumes.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stderr))
}

// run returns the process exit code: 0 on success or help, 1 on failure, 2 on usage errors.
func run(args []string, stderr io.Writer) int {
	if len(args) == 0 || (args[0] != "node" && args[0] != "controller") {
		usage(stderr)
		if len(args) > 0 && (args[0] == "-h" || args[0] == "--help") {
			return 0
		}
		return 2
	}
	var err error
	if args[0] == "node" {
		err = runNode(args[1:], stderr)
	} else {
		err = runController(args[1:], stderr)
	}
	switch {
	case err == nil, errors.Is(err, flag.ErrHelp):
		return 0
	case errors.Is(err, errUsage):
		fmt.Fprintln(stderr, err)
		return 2
	default:
		fmt.Fprintln(stderr, err)
		return 1
	}
}

var errUsage = errors.New("usage")

func usage(w io.Writer) {
	fmt.Fprintln(w, "Usage: csi-driver-mergerfs <node|controller> [flags]")
}

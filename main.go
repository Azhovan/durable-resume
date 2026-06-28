package main

import (
	"fmt"
	"os"

	"github.com/azhovan/durable-resume/cmd"
)

// These are set via -ldflags at build time. Defaults are used for `go run`.
var (
	Version  = "dev"
	Revision = "none"
	Date     = "unknown"
)

func main() {
	if err := cmd.NewRootCmd(Version, Revision, Date).Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "dr:", err)
		os.Exit(1)
	}
}

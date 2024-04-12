package main

import (
	"os"

	"github.com/azhovan/durable-resume/cmd"
)

func main() {
	if err := cmd.Execute(); err != nil {
		os.Exit(1)
	}
}

package main

import (
	"os"

	"github.com/arwos/isobox/internal/cli"
	"github.com/arwos/isobox/internal/preflight"
)

func main() {
	if code, handled := preflight.RunProbeHelper(os.Args[1:]); handled {
		os.Exit(code)
	}
	os.Exit(cli.Execute(os.Args[1:], os.Stdout, os.Stderr))
}

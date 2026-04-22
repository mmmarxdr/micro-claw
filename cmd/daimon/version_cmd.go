package main

import (
	"flag"
	"fmt"
	"io"
	"os"
)

// runVersionCommand prints version metadata and exits.
// Equivalent to `daimon --version`, exposed as a subcommand for discoverability.
func runVersionCommand(args []string) error {
	fs := flag.NewFlagSet("version", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	short := fs.Bool("short", false, "print only the version string")
	if err := fs.Parse(args); err != nil {
		fmt.Fprintln(os.Stderr, "Usage: daimon version [--short]")
		return err
	}

	if *short {
		fmt.Println(version)
		return nil
	}
	fmt.Printf("daimon %s (%s, %s)\n", version, commit, date)
	return nil
}

// isDevBuild reports whether the binary was built without release ldflags
// (i.e. `go build` or `go install` without goreleaser). Used by `update` to
// refuse self-replacement of unversioned builds.
func isDevBuild() bool {
	return version == "dev" || version == "" || commit == "none"
}

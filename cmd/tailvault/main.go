package main

import (
	"fmt"
	"os"

	"github.com/Ibtesam-Mahmood/tailvault/internal/cli"
	"github.com/Ibtesam-Mahmood/tailvault/internal/tserr"
)

func main() {
	err := cli.Execute()
	if err != nil {
		// Print the legible "TV-…: cause (fix: …)" line (or the raw error for
		// untyped failures) before exiting with the bucketed code.
		fmt.Fprintln(os.Stderr, err)
	}
	os.Exit(tserr.ExitCodeFor(err))
}

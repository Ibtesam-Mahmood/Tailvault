package main

import (
	"os"

	"github.com/Ibtesam-Mahmood/tailvault/internal/cli"
)

func main() {
	os.Exit(cli.Execute())
}

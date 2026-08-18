package main

import (
	"os"

	"github.com/pedrogpaulino/manu/internal/cli"
)

func main() {
	if code := cli.Run(os.Args[1:], os.Stdout, os.Stderr); code != 0 {
		os.Exit(code)
	}
}

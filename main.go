// Command hue manages Philips Hue lights from the command line.
//
// Its reason for existing is `hue sweep`: smart home apps fire off/on commands
// without verifying them, so lights get left on. The sweep looks for lights
// that are clearly stragglers and switches them off.
package main

import (
	"errors"
	"fmt"
	"os"

	"github.com/brybry192/hue/internal/cli"
)

func main() {
	if err := cli.Run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		if !errors.Is(err, cli.ErrUsage) {
			fmt.Fprintf(os.Stderr, "hue: %v\n", err)
		}
		os.Exit(1)
	}
}

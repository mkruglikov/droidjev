// Command droidjev is a fast jev-powered Android emulator clicker, text-first:
// it dumps the view hierarchy and taps the element that advances the goal.
// Ported from the src/cli.js header.
package main

import (
	"fmt"
	"os"
)

// version is the build version, overridden at release time with
// -ldflags "-X main.version=vX.Y.Z".
var version = "dev"

func main() {
	fmt.Fprintln(os.Stderr, "droidjev: not yet implemented")
	os.Exit(3)
}

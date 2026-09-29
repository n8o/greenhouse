// Command greenhouse is the factory's scheduler (see docs/DESIGN.md).
// Bootstrapping: the subcommands arrive with plan steps gr-boot-1..6.
package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Fprintln(os.Stderr, "greenhouse: not built yet — see .beads/plans/gr-boot.jsonl")
	os.Exit(2)
}

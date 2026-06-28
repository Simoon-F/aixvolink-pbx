// Command aixvolinkpbx is the future service entry point.
//
// Phase 0 intentionally exposes version information only. Protocol validation
// programs live under spikes and are not production service components.
package main

import (
	"flag"
	"fmt"
	"os"
)

const version = "0.0.0-phase0"

func main() {
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Println(version)
		return
	}

	fmt.Fprintln(os.Stderr, "AixvoLinkPBX service is intentionally unavailable during Phase 0; use -version or a spike command")
	os.Exit(2)
}

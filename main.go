// code-speed shows how fast your coding agents write, what they use and
// cost, and how much of their quota is left. See README.md.
package main

import (
	"os"

	"github.com/daemonx666/code-speed/internal/cli"
	_ "github.com/daemonx666/code-speed/internal/sources"
)

// version is set at build time: -ldflags "-X main.version=v1.2.3".
var version = "dev"

func main() {
	os.Exit(cli.Run(os.Args[1:], version, desktop()))
}

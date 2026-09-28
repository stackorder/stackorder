// Command stackorder is the runner side CLI. It scans repositories, resolves
// affected stacks, runs plans and applies, and reports results to the server.
package main

import (
	"os"

	"github.com/stackorder/stackorder/internal/cli"
)

func main() {
	os.Exit(cli.ExitCode(cli.Execute()))
}

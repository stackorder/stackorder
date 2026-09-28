// Command stackorder is the runner side CLI. It scans repositories, resolves
// affected stacks, runs plans and applies, and reports results to the server.
package main

import (
	"fmt"
	"os"

	"github.com/stackorder/stackorder/internal/version"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "version" {
		fmt.Println("stackorder", version.String())
		return
	}
	fmt.Fprintln(os.Stderr, "stackorder: commands are not implemented yet; see internal/cli")
	os.Exit(1)
}

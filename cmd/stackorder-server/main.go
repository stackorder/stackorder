// Command stackorder-server is the control plane: webhooks, the runner API,
// the human API and the embedded web UI in one binary.
package main

import (
	"fmt"
	"os"

	"github.com/stackorder/stackorder/internal/version"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "version" {
		fmt.Println("stackorder-server", version.String())
		return
	}
	fmt.Fprintln(os.Stderr, "stackorder-server: not implemented yet; see internal/server")
	os.Exit(1)
}

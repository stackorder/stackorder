// Command faketf is the binary internal/testutil/faketf builds as terraform
// and tofu.
package main

import (
	"os"

	"github.com/stackorder/stackorder/internal/testutil/faketf"
)

func main() {
	os.Exit(faketf.Main())
}

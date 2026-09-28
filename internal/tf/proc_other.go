//go:build !unix

package tf

import (
	"os/exec"
	"time"
)

func configureInterrupt(cmd *exec.Cmd, timeout time.Duration) {
	cmd.WaitDelay = timeout
}

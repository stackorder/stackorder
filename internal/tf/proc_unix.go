//go:build unix

package tf

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
	"time"
)

func configureInterrupt(cmd *exec.Cmd, timeout time.Duration) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		err := cmd.Process.Signal(os.Interrupt)
		if errors.Is(err, os.ErrProcessDone) {
			return err
		}
		if err != nil {
			return cmd.Process.Kill()
		}
		return nil
	}
	cmd.WaitDelay = timeout
}

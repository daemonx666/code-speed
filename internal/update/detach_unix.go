//go:build !windows

package update

import (
	"os/exec"
	"syscall"
)

// detached runs cmd in its own process group, so it outlives the app.
func detached(cmd *exec.Cmd) *exec.Cmd {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	return cmd
}

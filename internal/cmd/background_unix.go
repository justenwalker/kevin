//go:build linux || darwin

package cmd

import (
	"os/exec"
	"syscall"
)

// detach sets cmd to start its own session (Setsid), so it survives this
// process's own session ending - a closed terminal, a logged-out shell.
func detach(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}

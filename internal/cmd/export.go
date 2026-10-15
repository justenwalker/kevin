package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"
)

// execWith execs target in place of the kevin process, inheriting its
// terminal.
func execWith(target []string) error {
	bin, err := exec.LookPath(target[0])
	if err != nil {
		return fmt.Errorf("do: %w", err)
	}
	if err := syscall.Exec(bin, target, os.Environ()); err != nil { //nolint:gosec // the command is what the user asked to run
		return fmt.Errorf("do: exec %s: %w", bin, err)
	}
	return nil
}

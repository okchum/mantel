//go:build unix

package collect

import (
	"os/exec"
	"syscall"
)

// killGroup starts the command in a process group of its own and makes
// cancellation (timeout, shutdown) kill the whole group: a "sh -c" script's
// background jobs and long sleeps die with it instead of outliving Mantel.
func killGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		// The group id is the child's pid (Setpgid with Pgid 0).
		if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil {
			return cmd.Process.Kill()
		}
		return nil
	}
}

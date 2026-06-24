//go:build !windows

package daemonside

import (
	"context"
	"os/exec"
	"syscall"
)

func shellCommand(ctx context.Context, script string) *exec.Cmd {
	return exec.CommandContext(ctx, "/bin/sh", "-c", script)
}

// configureProcess puts the child in its own process group so we can signal the
// whole tree (the script may spawn a dev server with children).
func configureProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// killProcessGroup sends SIGKILL to the child's process group.
func killProcessGroup(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	// Negative pid targets the whole group (set up via Setpgid above).
	return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
}

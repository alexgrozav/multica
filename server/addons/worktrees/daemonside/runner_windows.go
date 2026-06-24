//go:build windows

package daemonside

import (
	"context"
	"os/exec"
)

func shellCommand(ctx context.Context, script string) *exec.Cmd {
	return exec.CommandContext(ctx, "cmd", "/c", script)
}

// configureProcess is a no-op on Windows; CommandContext + Cancel handle teardown.
func configureProcess(cmd *exec.Cmd) {}

// killProcessGroup kills the child process. Child grandchildren are not reaped
// as reliably as on unix; acceptable for the add-on's Run/Stop use case.
func killProcessGroup(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	return cmd.Process.Kill()
}

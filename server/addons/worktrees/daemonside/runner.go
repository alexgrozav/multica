package daemonside

import (
	"bufio"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"sync"
	"time"

	"github.com/multica-ai/multica/server/addons/worktrees/shared"
)

// runScript executes a shell script in dir, streaming each stdout/stderr line to
// onLine, and returns the process exit code. A cancelled ctx kills the whole
// process group (see the per-OS helpers). exitCode is -1 on a spawn/IO failure.
func runScript(ctx context.Context, dir, script string, extraEnv []string, onLine func(stream, text string)) (int, error) {
	cmd := shellCommand(ctx, script)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), extraEnv...)
	configureProcess(cmd)
	cmd.Cancel = func() error { return killProcessGroup(cmd) }
	cmd.WaitDelay = 10 * time.Second

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return -1, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return -1, err
	}
	if err := cmd.Start(); err != nil {
		return -1, err
	}

	var wg sync.WaitGroup
	wg.Add(2)
	scan := func(r io.Reader, stream string) {
		defer wg.Done()
		sc := bufio.NewScanner(r)
		sc.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)
		for sc.Scan() {
			onLine(stream, sc.Text())
		}
	}
	go scan(stdout, shared.StreamStdout)
	go scan(stderr, shared.StreamStderr)
	wg.Wait()

	if err := cmd.Wait(); err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return ee.ExitCode(), nil
		}
		return -1, err
	}
	return 0, nil
}

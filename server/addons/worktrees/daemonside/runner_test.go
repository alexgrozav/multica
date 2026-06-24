//go:build !windows

package daemonside

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestRunScriptCapturesOutputAndExitCode(t *testing.T) {
	var mu sync.Mutex
	var stdout, stderr []string
	code, err := runScript(context.Background(), t.TempDir(),
		"echo hello; echo oops 1>&2; exit 3", nil,
		func(stream, text string) {
			mu.Lock()
			defer mu.Unlock()
			if stream == "stdout" {
				stdout = append(stdout, text)
			} else {
				stderr = append(stderr, text)
			}
		})
	if err != nil {
		t.Fatalf("runScript error: %v", err)
	}
	if code != 3 {
		t.Errorf("exit code = %d, want 3", code)
	}
	if strings.Join(stdout, "\n") != "hello" {
		t.Errorf("stdout = %v, want [hello]", stdout)
	}
	if strings.Join(stderr, "\n") != "oops" {
		t.Errorf("stderr = %v, want [oops]", stderr)
	}
}

func TestRunScriptCancelKillsProcess(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		// A long sleep that would hang the test if cancel didn't kill it.
		_, _ = runScript(ctx, t.TempDir(), "sleep 30", nil, func(string, string) {})
		close(done)
	}()
	time.Sleep(200 * time.Millisecond)
	cancel()
	select {
	case <-done:
		// returned promptly after cancel — process group was killed
	case <-time.After(5 * time.Second):
		t.Fatal("runScript did not return within 5s after cancel")
	}
}

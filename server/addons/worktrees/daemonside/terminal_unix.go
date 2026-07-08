//go:build !windows

package daemonside

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/creack/pty"
	"golang.org/x/sys/unix"
)

// termPTY is one live shell PTY. Read/Write operate on the master side; the
// child (the shell) is a session leader on the slave side, so its pid doubles
// as the process-group id for signalling and the foreground-group check.
type termPTY struct {
	master *os.File
	cmd    *exec.Cmd
	waitCh chan int // receives the exit code exactly once
}

// startTerminalPTY spawns the user's login shell in dir on a PTY of the given
// size.
func startTerminalPTY(dir string, cols, rows int, env []string) (*termPTY, error) {
	shell := loginShell()
	cmd := exec.Command(shell)
	if runtime.GOOS == "darwin" {
		// macOS convention (Terminal.app, VS Code): login shells, so the user's
		// profile (PATH, prompt) applies.
		cmd.Args = append(cmd.Args, "-l")
	}
	cmd.Dir = dir
	cmd.Env = env
	master, err := pty.StartWithSize(cmd, ptyWinsize(cols, rows))
	if err != nil {
		return nil, err
	}
	p := &termPTY{master: master, cmd: cmd, waitCh: make(chan int, 1)}
	go func() {
		err := cmd.Wait()
		code := 0
		if err != nil {
			code = -1
			if cmd.ProcessState != nil {
				code = cmd.ProcessState.ExitCode()
			}
		}
		p.waitCh <- code
	}()
	return p, nil
}

func ptyWinsize(cols, rows int) *pty.Winsize {
	if cols <= 0 {
		cols = 80
	}
	if rows <= 0 {
		rows = 24
	}
	return &pty.Winsize{Cols: uint16(cols), Rows: uint16(rows)}
}

func (p *termPTY) Read(b []byte) (int, error)  { return p.master.Read(b) }
func (p *termPTY) Write(b []byte) (int, error) { return p.master.Write(b) }

// Resize applies a viewer resize to the PTY (the kernel raises SIGWINCH).
func (p *termPTY) Resize(cols, rows int) {
	if cols <= 0 || rows <= 0 {
		return
	}
	_ = pty.Setsize(p.master, ptyWinsize(cols, rows))
}

// ForegroundProcess names the process group currently owning the terminal, or
// "" when the shell itself is in the foreground (idle prompt).
func (p *termPTY) ForegroundProcess() string {
	pgrp, err := unix.IoctlGetInt(int(p.master.Fd()), unix.TIOCGPGRP)
	if err != nil || pgrp <= 0 {
		return ""
	}
	if p.cmd.Process == nil || pgrp == p.cmd.Process.Pid {
		return ""
	}
	return processComm(pgrp)
}

// processComm resolves a pid to a short executable name, best-effort.
func processComm(pid int) string {
	if runtime.GOOS == "linux" {
		if b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/comm"); err == nil {
			return strings.TrimSpace(string(b))
		}
		return ""
	}
	out, err := exec.Command("ps", "-o", "comm=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return ""
	}
	name := strings.TrimSpace(string(out))
	if i := strings.LastIndexByte(name, '/'); i >= 0 {
		name = name[i+1:]
	}
	return strings.TrimPrefix(name, "-") // login shells report as e.g. "-zsh"
}

// Terminate ends the shell (if still alive) and reaps it, returning the exit
// code. Closing the master hangs up the line (SIGHUP to the foreground group);
// anything that survives the grace period gets the whole group SIGKILLed.
func (p *termPTY) Terminate() int {
	_ = p.master.Close()
	select {
	case code := <-p.waitCh:
		return code
	case <-time.After(3 * time.Second):
	}
	if p.cmd.Process != nil {
		_ = syscall.Kill(-p.cmd.Process.Pid, syscall.SIGKILL)
	}
	select {
	case code := <-p.waitCh:
		return code
	case <-time.After(2 * time.Second):
		return -1
	}
}

// loginShell picks the user's shell, falling back to something that exists.
func loginShell() string {
	if s := strings.TrimSpace(os.Getenv("SHELL")); s != "" && filepath.IsAbs(s) {
		if _, err := os.Stat(s); err == nil {
			return s
		}
	}
	for _, s := range []string{"/bin/zsh", "/bin/bash", "/bin/sh"} {
		if _, err := os.Stat(s); err == nil {
			return s
		}
	}
	return "/bin/sh"
}

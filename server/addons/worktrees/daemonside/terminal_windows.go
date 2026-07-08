//go:build windows

package daemonside

import "errors"

// Interactive terminals are unix-only for now (creack/pty has no ConPTY
// support). A Windows daemon reports the error through the normal exit path so
// the tab shows why instead of hanging on "connecting".
type termPTY struct{}

func startTerminalPTY(dir string, cols, rows int, env []string) (*termPTY, error) {
	return nil, errors.New("interactive terminals are not supported on Windows daemons yet")
}

func (p *termPTY) Read(b []byte) (int, error)  { return 0, errors.New("unsupported") }
func (p *termPTY) Write(b []byte) (int, error) { return 0, errors.New("unsupported") }
func (p *termPTY) Resize(cols, rows int)       {}
func (p *termPTY) ForegroundProcess() string   { return "" }
func (p *termPTY) Terminate() int              { return -1 }

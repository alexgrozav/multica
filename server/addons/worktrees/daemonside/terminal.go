package daemonside

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"github.com/multica-ai/multica/server/addons/worktrees/shared"
)

// The daemon half of the interactive-terminal relay: a pending session arrives
// on the poll, the daemon spawns the user's shell in a PTY at the issue's
// workspace root and dials the server's terminal WebSocket; from then on binary
// frames are PTY bytes (server→PTY: keystrokes, PTY→server: output) and text
// frames are TermCtl JSON (resize in, title/exit out). The PTY-spawning
// primitives are per-OS (terminal_unix.go / terminal_windows.go).

const (
	termDialTimeout = 10 * time.Second
	termWriteWait   = 10 * time.Second
	// termReadIdle bounds a read with no traffic at all. The server pings every
	// ~54s, so a healthy but idle session refreshes well inside this.
	termReadIdle = 90 * time.Second
	// termTitleEvery is how often the foreground process is re-checked.
	termTitleEvery = 2 * time.Second
)

// termHandle tracks one live session for duplicate-claim suppression and
// cancel-by-issue (cleanup kills the shells whose cwd it is about to remove).
type termHandle struct {
	issueID string
	cancel  context.CancelFunc
}

// handleTerminals starts one session goroutine per pending terminal. The
// server re-arms a claim that never attaches, so deliveries can repeat — the
// in-flight map makes that idempotent.
func (m *Module) handleTerminals(ctx context.Context, opens []shared.TerminalOpen) {
	for _, job := range opens {
		job := job
		m.mu.Lock()
		if m.terms == nil {
			m.terms = map[string]termHandle{}
		}
		if _, busy := m.terms[job.ID]; busy {
			m.mu.Unlock()
			continue
		}
		termCtx, cancel := context.WithCancel(ctx)
		m.terms[job.ID] = termHandle{issueID: job.IssueID, cancel: cancel}
		m.mu.Unlock()
		go func() {
			defer func() {
				m.mu.Lock()
				delete(m.terms, job.ID)
				m.mu.Unlock()
				cancel()
			}()
			m.runTerminal(termCtx, job)
		}()
	}
}

// killTerminalsFor ends every live terminal of an issue and waits (bounded)
// for the shells to die — called before cleanup tears the worktree out from
// under them.
func (m *Module) killTerminalsFor(issueID string) {
	m.mu.Lock()
	for _, h := range m.terms {
		if h.issueID == issueID {
			h.cancel()
		}
	}
	m.mu.Unlock()
	for i := 0; i < 50; i++ {
		m.mu.Lock()
		active := false
		for _, h := range m.terms {
			if h.issueID == issueID {
				active = true
				break
			}
		}
		m.mu.Unlock()
		if !active {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// termSock serializes writes on the daemon-side relay socket (PTY pump, title
// ticker and the final exit frame write concurrently).
type termSock struct {
	c  *websocket.Conn
	mu sync.Mutex
}

func (s *termSock) write(msgType int, data []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_ = s.c.SetWriteDeadline(time.Now().Add(termWriteWait))
	return s.c.WriteMessage(msgType, data)
}

func (s *termSock) writeCtl(ctl shared.TermCtl) error {
	b, err := json.Marshal(ctl)
	if err != nil {
		return err
	}
	return s.write(websocket.TextMessage, b)
}

// runTerminal owns one session end-to-end: dial the relay, spawn the shell,
// pump both directions, and report the exit. Ends when the shell exits, the
// server closes the session (tab closed / issue cleaned up), or ctx cancels
// (daemon shutdown / killTerminalsFor).
func (m *Module) runTerminal(ctx context.Context, job shared.TerminalOpen) {
	conn, err := m.dialTerminalWS(ctx, job)
	if err != nil {
		// The server re-arms the claim after a timeout, so a later poll retries.
		m.log().Warn("worktrees: terminal dial failed", "terminal", job.ID, "error", err)
		return
	}
	sock := &termSock{c: conn}
	defer conn.Close()

	dir := IssueWorktreeParent(m.deps.WorkspacesRoot, job.WorkspaceID, job.IssueID)
	if st, serr := os.Stat(dir); serr != nil || !st.IsDir() {
		_ = sock.writeCtl(shared.TermCtl{Type: shared.TermCtlExit, Error: "issue workspace directory not found"})
		return
	}

	pt, err := startTerminalPTY(dir, job.Cols, job.Rows, m.terminalEnv(job))
	if err != nil {
		_ = sock.writeCtl(shared.TermCtl{Type: shared.TermCtlExit, Error: err.Error()})
		return
	}
	m.log().Info("worktrees: terminal opened", "terminal", job.ID, "issue", job.IssueID, "dir", dir)

	sockDone := make(chan struct{}) // server side went away (or read failed)
	ptyDone := make(chan struct{})  // shell output ended (process exited)

	// Server → PTY: keystrokes + resizes.
	go func() {
		defer close(sockDone)
		conn.SetReadLimit(1 << 20)
		refresh := func() { _ = conn.SetReadDeadline(time.Now().Add(termReadIdle)) }
		refresh()
		conn.SetPingHandler(func(msg string) error {
			refresh()
			return conn.WriteControl(websocket.PongMessage, []byte(msg), time.Now().Add(termWriteWait))
		})
		for {
			msgType, data, rerr := conn.ReadMessage()
			if rerr != nil {
				return
			}
			refresh()
			switch msgType {
			case websocket.BinaryMessage:
				if _, werr := pt.Write(data); werr != nil {
					return
				}
			case websocket.TextMessage:
				var ctl shared.TermCtl
				if json.Unmarshal(data, &ctl) == nil && ctl.Type == shared.TermCtlResize {
					pt.Resize(ctl.Cols, ctl.Rows)
				}
			}
		}
	}()

	// PTY → server: raw output.
	go func() {
		defer close(ptyDone)
		buf := make([]byte, 16<<10)
		for {
			n, rerr := pt.Read(buf)
			if n > 0 {
				if werr := sock.write(websocket.BinaryMessage, buf[:n]); werr != nil {
					return
				}
			}
			if rerr != nil {
				return
			}
		}
	}()

	// Foreground-process watcher → tab title ("Terminal 1 (make)").
	titleStop := make(chan struct{})
	go func() {
		t := time.NewTicker(termTitleEvery)
		defer t.Stop()
		last := ""
		for {
			select {
			case <-titleStop:
				return
			case <-t.C:
				if title := pt.ForegroundProcess(); title != last {
					last = title
					_ = sock.writeCtl(shared.TermCtl{Type: shared.TermCtlTitle, Title: title})
				}
			}
		}
	}()

	select {
	case <-ctx.Done():
	case <-sockDone:
	case <-ptyDone:
	}
	close(titleStop)

	code := pt.Terminate()
	_ = sock.writeCtl(shared.TermCtl{Type: shared.TermCtlExit, ExitCode: &code})
	m.log().Info("worktrees: terminal closed", "terminal", job.ID, "issue", job.IssueID, "exit_code", code)
}

// dialTerminalWS opens the daemon side of the relay, authenticating exactly
// like the HTTP client (bearer token + daemon_id/workspace_id params).
func (m *Module) dialTerminalWS(ctx context.Context, job shared.TerminalOpen) (*websocket.Conn, error) {
	base := m.deps.ServerBaseURL
	switch {
	case strings.HasPrefix(base, "https://"):
		base = "wss://" + strings.TrimPrefix(base, "https://")
	case strings.HasPrefix(base, "http://"):
		base = "ws://" + strings.TrimPrefix(base, "http://")
	}
	q := url.Values{"daemon_id": {m.deps.DaemonID}, "workspace_id": {job.WorkspaceID}}
	u := base + "/api/daemon/worktree/terminals/" + job.ID + "/ws?" + q.Encode()

	h := http.Header{}
	if tok := m.cl.authToken(); tok != "" {
		h.Set("Authorization", "Bearer "+tok)
	}
	d := &websocket.Dialer{HandshakeTimeout: termDialTimeout}
	conn, resp, err := d.DialContext(ctx, u, h)
	if err != nil && resp != nil && resp.Body != nil {
		resp.Body.Close()
	}
	return conn, err
}

// terminalEnv is the shell's full environment: the daemon's own env plus
// terminal basics and the same MULTICA_* context the scripts get.
func (m *Module) terminalEnv(job shared.TerminalOpen) []string {
	return append(os.Environ(),
		"TERM=xterm-256color",
		"COLORTERM=truecolor",
		"MULTICA_ISSUE_ID="+job.IssueID,
		"MULTICA_ISSUE_IDENTIFIER="+job.Identifier,
		"MULTICA_WORKSPACE_ID="+job.WorkspaceID,
	)
}

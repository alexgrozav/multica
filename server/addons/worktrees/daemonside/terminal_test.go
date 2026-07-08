//go:build !windows

package daemonside

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/multica-ai/multica/server/addons/worktrees/shared"
)

// TestStartTerminalPTY spawns a real shell on a PTY, round-trips a command,
// and reaps it.
func TestStartTerminalPTY(t *testing.T) {
	t.Setenv("SHELL", "/bin/sh")
	dir := t.TempDir()
	pt, err := startTerminalPTY(dir, 80, 24, append(os.Environ(), "PS1=$ ", "TERM=dumb"))
	if err != nil {
		t.Fatalf("startTerminalPTY: %v", err)
	}

	// A PTY master read has no reliable deadline support; accumulate output on
	// a goroutine and assert with timeouts instead.
	outCh := make(chan []byte, 64)
	go func() {
		buf := make([]byte, 4096)
		for {
			n, rerr := pt.Read(buf)
			if n > 0 {
				b := make([]byte, n)
				copy(b, buf[:n])
				outCh <- b
			}
			if rerr != nil {
				close(outCh)
				return
			}
		}
	}()
	waitFor := func(marker string) {
		t.Helper()
		var out strings.Builder
		deadline := time.After(5 * time.Second)
		for !strings.Contains(out.String(), marker) {
			select {
			case b, ok := <-outCh:
				if !ok {
					t.Fatalf("pty closed before %q; got %q", marker, out.String())
				}
				out.Write(b)
			case <-deadline:
				t.Fatalf("never saw %q; got %q", marker, out.String())
			}
		}
	}

	if _, err := pt.Write([]byte("echo marco-$((40+2))\n")); err != nil {
		t.Fatalf("write: %v", err)
	}
	waitFor("marco-42")

	if _, err := pt.Write([]byte("exit\n")); err != nil {
		t.Fatalf("write exit: %v", err)
	}
	// Wait for the shell to actually exit (the master read errors out) before
	// reaping, so Terminate doesn't hang up a shell mid-command.
	drainDeadline := time.After(5 * time.Second)
	for open := true; open; {
		select {
		case _, ok := <-outCh:
			open = ok
		case <-drainDeadline:
			t.Fatal("shell never exited after `exit`")
		}
	}
	if code := pt.Terminate(); code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
}

// TestTerminateKillsStuckShell proves Terminate ends a shell that ignores the
// hangup (SIGKILL fallback) instead of leaking it.
func TestTerminateKillsStuckShell(t *testing.T) {
	t.Setenv("SHELL", "/bin/sh")
	pt, err := startTerminalPTY(t.TempDir(), 80, 24, os.Environ())
	if err != nil {
		t.Fatalf("startTerminalPTY: %v", err)
	}
	// A foreground child that traps nothing but also never reads the PTY.
	if _, err := pt.Write([]byte("trap '' HUP; sleep 300\n")); err != nil {
		t.Fatalf("write: %v", err)
	}
	time.Sleep(300 * time.Millisecond)

	done := make(chan int, 1)
	go func() { done <- pt.Terminate() }()
	select {
	case <-done:
		// Any exit code is fine — the point is that it returns promptly.
	case <-time.After(10 * time.Second):
		t.Fatal("Terminate hung on a HUP-ignoring shell")
	}
}

// fakeRelayServer accepts the daemon's terminal dial-back and scripts the
// server side of the relay for one session.
type fakeRelayServer struct {
	t        *testing.T
	upgrader websocket.Upgrader

	mu   sync.Mutex
	conn *websocket.Conn
	got  chan wsFrame
}

type wsFrame struct {
	msgType int
	data    []byte
}

func newFakeRelayServer(t *testing.T) (*fakeRelayServer, *httptest.Server) {
	f := &fakeRelayServer{t: t, got: make(chan wsFrame, 256)}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "/terminals/") || !strings.HasSuffix(r.URL.Path, "/ws") {
			http.NotFound(w, r)
			return
		}
		if r.URL.Query().Get("daemon_id") == "" || r.URL.Query().Get("workspace_id") == "" {
			http.Error(w, "missing identity", http.StatusBadRequest)
			return
		}
		conn, err := f.upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		f.mu.Lock()
		f.conn = conn
		f.mu.Unlock()
		for {
			mt, data, err := conn.ReadMessage()
			if err != nil {
				close(f.got)
				return
			}
			f.got <- wsFrame{mt, data}
		}
	}))
	return f, srv
}

func (f *fakeRelayServer) send(t *testing.T, msgType int, data []byte) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		f.mu.Lock()
		conn := f.conn
		f.mu.Unlock()
		if conn != nil {
			if err := conn.WriteMessage(msgType, data); err != nil {
				t.Fatalf("relay send: %v", err)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("daemon never dialed the relay")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// waitFrame reads scripted frames until match returns true, failing on timeout
// or channel close.
func (f *fakeRelayServer) waitFrame(t *testing.T, what string, match func(wsFrame) bool) wsFrame {
	t.Helper()
	deadline := time.After(10 * time.Second)
	for {
		select {
		case fr, ok := <-f.got:
			if !ok {
				t.Fatalf("relay socket closed before %s", what)
			}
			if match(fr) {
				return fr
			}
		case <-deadline:
			t.Fatalf("timed out waiting for %s", what)
		}
	}
}

// TestRunTerminalE2E runs the daemon half for real: a TerminalOpen job spawns
// a shell in the issue workspace dir, output flows to the relay, input flows
// back, and typing `exit` ends the session with an exit control frame.
func TestRunTerminalE2E(t *testing.T) {
	t.Setenv("SHELL", "/bin/sh")
	f, srv := newFakeRelayServer(t)
	defer srv.Close()

	root := t.TempDir()
	wsID, issueID := "ws-term", "11111111-2222-3333-4444-555555555555"
	dir := IssueWorktreeParent(root, wsID, issueID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}

	m := New(Deps{
		ServerBaseURL:  srv.URL,
		WorkspacesRoot: root,
		DaemonID:       "daemon-e2e",
		TokenProvider:  func() string { return "tok" },
	})
	ctx := t.Context()
	m.handleTerminals(ctx, []shared.TerminalOpen{{
		ID: "term-1", IssueID: issueID, WorkspaceID: wsID, Identifier: "WAT-9", Cols: 80, Rows: 24,
	}})
	// Duplicate delivery while in flight is a no-op.
	m.handleTerminals(ctx, []shared.TerminalOpen{{
		ID: "term-1", IssueID: issueID, WorkspaceID: wsID, Cols: 80, Rows: 24,
	}})

	// The shell's cwd is the issue workspace dir.
	f.send(t, websocket.BinaryMessage, []byte("pwd\n"))
	f.waitFrame(t, "pwd output", func(fr wsFrame) bool {
		return fr.msgType == websocket.BinaryMessage && strings.Contains(string(fr.data), dir)
	})

	// `exit` ends the shell → an exit control frame with code 0 arrives.
	f.send(t, websocket.BinaryMessage, []byte("exit\n"))
	fr := f.waitFrame(t, "exit control frame", func(fr wsFrame) bool {
		if fr.msgType != websocket.TextMessage {
			return false
		}
		var ctl shared.TermCtl
		return json.Unmarshal(fr.data, &ctl) == nil && ctl.Type == shared.TermCtlExit
	})
	var ctl shared.TermCtl
	_ = json.Unmarshal(fr.data, &ctl)
	if ctl.ExitCode == nil || *ctl.ExitCode != 0 {
		t.Fatalf("exit ctl = %+v, want code 0", ctl)
	}

	// The in-flight entry drains so the id could be served again.
	deadline := time.Now().Add(5 * time.Second)
	for {
		m.mu.Lock()
		_, busy := m.terms["term-1"]
		m.mu.Unlock()
		if !busy {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("terminal handle never released")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// TestKillTerminalsFor ends a live session by issue id (the cleanup path) and
// waits for the shell to die.
func TestKillTerminalsFor(t *testing.T) {
	t.Setenv("SHELL", "/bin/sh")
	f, srv := newFakeRelayServer(t)
	defer srv.Close()

	root := t.TempDir()
	wsID, issueID := "ws-kill", "99999999-8888-7777-6666-555555555555"
	if err := os.MkdirAll(IssueWorktreeParent(root, wsID, issueID), 0o755); err != nil {
		t.Fatal(err)
	}
	m := New(Deps{ServerBaseURL: srv.URL, WorkspacesRoot: root, DaemonID: "daemon-kill", TokenProvider: func() string { return "" }})
	m.handleTerminals(t.Context(), []shared.TerminalOpen{{
		ID: "term-k", IssueID: issueID, WorkspaceID: wsID, Cols: 80, Rows: 24,
	}})

	// Wait for the shell to be live (any output/prompt or a title frame).
	f.send(t, websocket.BinaryMessage, []byte("echo ready-marker\n"))
	f.waitFrame(t, "shell liveness", func(fr wsFrame) bool {
		return fr.msgType == websocket.BinaryMessage && strings.Contains(string(fr.data), "ready-marker")
	})

	done := make(chan struct{})
	go func() {
		m.killTerminalsFor(issueID)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("killTerminalsFor hung")
	}
	m.mu.Lock()
	_, busy := m.terms["term-k"]
	m.mu.Unlock()
	if busy {
		t.Fatal("terminal still tracked after killTerminalsFor")
	}
}

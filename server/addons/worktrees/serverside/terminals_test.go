package serverside

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/gorilla/websocket"

	"github.com/multica-ai/multica/server/addons/worktrees/shared"
)

// --- registry unit tests (no HTTP) ---

type termEventSink struct {
	eventSink
}

func (s *termEventSink) notify(t shared.Terminal, removed bool) {
	s.publish(t.WorkspaceID, shared.EventWorktreeTerminal, shared.TerminalEvent{IssueID: t.IssueID, Terminal: t, Removed: removed})
}

func TestTerminalRegistryLifecycle(t *testing.T) {
	sink := &termEventSink{}
	r := newTerminalRegistry(sink.notify)

	t1, err := r.create("issue-1", "ws-1", "daemon-1", "WAT-1", 80, 24)
	if err != nil || t1.Index != 1 || t1.Status != shared.TermPending {
		t.Fatalf("create #1 = %+v err=%v, want pending index 1", t1, err)
	}
	t2, _ := r.create("issue-1", "ws-1", "daemon-1", "WAT-1", 80, 24)
	if t2.Index != 2 {
		t.Fatalf("create #2 index = %d, want 2", t2.Index)
	}

	// Claims are per (daemon, workspace) and delivered once.
	if got := r.pendingFor("other-daemon", "ws-1"); len(got) != 0 {
		t.Fatalf("foreign daemon claimed %d, want 0", len(got))
	}
	got := r.pendingFor("daemon-1", "ws-1")
	if len(got) != 2 || got[0].Cols != 80 || got[0].Rows != 24 {
		t.Fatalf("claim = %+v, want both sessions with size", got)
	}
	if again := r.pendingFor("daemon-1", "ws-1"); len(again) != 0 {
		t.Fatalf("second claim re-delivered %d, want 0", len(again))
	}

	// Live-session cap.
	for i := 0; i < maxTerminalsPerIssue-2; i++ {
		if _, err := r.create("issue-1", "ws-1", "daemon-1", "WAT-1", 80, 24); err != nil {
			t.Fatalf("create under cap failed: %v", err)
		}
	}
	if _, err := r.create("issue-1", "ws-1", "daemon-1", "WAT-1", 80, 24); err == nil {
		t.Fatal("create over cap succeeded, want ErrTooManyTerminals")
	}

	// Exited sessions don't count against the cap.
	s1, _ := r.get(t1.ID)
	r.exit(s1, nil, "")
	if _, err := r.create("issue-1", "ws-1", "daemon-1", "WAT-1", 80, 24); err != nil {
		t.Fatalf("create after an exit failed: %v", err)
	}

	// Removing the last session of an issue resets numbering.
	other, _ := r.create("issue-2", "ws-1", "daemon-1", "WAT-2", 80, 24)
	r.remove(other.ID)
	fresh, _ := r.create("issue-2", "ws-1", "daemon-1", "WAT-2", 80, 24)
	if fresh.Index != 1 {
		t.Fatalf("index after full close = %d, want numbering reset to 1", fresh.Index)
	}

	if n := sink.count(shared.EventWorktreeTerminal); n == 0 {
		t.Fatal("no terminal events published")
	}
}

func TestTerminalTickets(t *testing.T) {
	r := newTerminalRegistry(nil)
	sess, _ := r.create("issue-t", "ws-1", "daemon-1", "WAT-3", 80, 24)

	tk, err := r.issueTicket(sess.ID)
	if err != nil || tk == "" {
		t.Fatalf("issueTicket = %q err=%v", tk, err)
	}
	got, ok := r.redeemTicket(tk)
	if !ok || got.info.ID != sess.ID {
		t.Fatalf("redeem = %v ok=%v, want the session", got, ok)
	}
	if _, ok := r.redeemTicket(tk); ok {
		t.Fatal("ticket redeemed twice")
	}
	if _, ok := r.redeemTicket("bogus"); ok {
		t.Fatal("bogus ticket redeemed")
	}

	// Expired tickets are refused.
	tk2, _ := r.issueTicket(sess.ID)
	r.mu.Lock()
	r.tickets[tk2] = termTicket{terminalID: sess.ID, expires: time.Now().Add(-time.Second)}
	r.mu.Unlock()
	if _, ok := r.redeemTicket(tk2); ok {
		t.Fatal("expired ticket redeemed")
	}
}

func TestTerminalSweep(t *testing.T) {
	r := newTerminalRegistry(nil)

	// Unclaimed pending session times out entirely.
	tp, _ := r.create("issue-s", "ws-1", "daemon-1", "WAT-4", 80, 24)
	r.sweep(time.Now().Add(termPendingTimeout + time.Second))
	if got, _ := r.get(tp.ID); got.snapshot().Status != shared.TermExited {
		t.Fatalf("stale pending status = %s, want exited", got.snapshot().Status)
	}

	// A claim that never dialed back re-arms for redelivery.
	tc, _ := r.create("issue-s", "ws-1", "daemon-1", "WAT-4", 80, 24)
	if got := r.pendingFor("daemon-1", "ws-1"); len(got) != 1 {
		t.Fatalf("claim = %d, want 1", len(got))
	}
	r.sweep(time.Now().Add(termClaimRedeliver + time.Second))
	redelivered := r.pendingFor("daemon-1", "ws-1")
	if len(redelivered) != 1 || redelivered[0].ID != tc.ID {
		t.Fatalf("redelivery = %+v, want the unattached claim again", redelivered)
	}

	// Exited sessions eventually vanish.
	sess, _ := r.get(tp.ID)
	sess.mu.Lock()
	sess.exitedAt = time.Now().Add(-termExitedTTL - time.Minute)
	sess.mu.Unlock()
	r.sweep(time.Now())
	if _, ok := r.get(tp.ID); ok {
		t.Fatal("old exited session still registered")
	}
}

// --- relay e2e over real sockets ---

// termTestRouter adds the viewer socket route the host mounts in router.go.
func termTestRouter(m *Module) http.Handler {
	r := chi.NewRouter()
	r.Mount("/api/worktree", m.UIRouter())
	r.Mount("/api/daemon/worktree", m.DaemonRouter())
	r.Get("/ws/worktree-terminal", m.HandleTerminalSocket)
	return r
}

func wsURL(srv *httptest.Server, path string) string {
	return "ws" + strings.TrimPrefix(srv.URL, "http") + path
}

func dialWS(t *testing.T, url string) *websocket.Conn {
	t.Helper()
	conn, resp, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		t.Fatalf("dial %s: %v", url, err)
	}
	if resp != nil && resp.Body != nil {
		resp.Body.Close()
	}
	return conn
}

func readFrame(t *testing.T, conn *websocket.Conn) (int, []byte) {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	mt, data, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read frame: %v", err)
	}
	return mt, data
}

func readCtl(t *testing.T, conn *websocket.Conn) shared.TermCtl {
	t.Helper()
	mt, data := readFrame(t, conn)
	if mt != websocket.TextMessage {
		t.Fatalf("frame type = %d (%q), want a control frame", mt, data)
	}
	var ctl shared.TermCtl
	if err := json.Unmarshal(data, &ctl); err != nil {
		t.Fatalf("bad control frame %q: %v", data, err)
	}
	return ctl
}

func createTerminal(t *testing.T, srv *httptest.Server, issueID string) shared.Terminal {
	t.Helper()
	body, _ := json.Marshal(shared.CreateTerminalRequest{Cols: 120, Rows: 32})
	resp, err := http.Post(srv.URL+"/api/worktree/issues/"+issueID+"/terminals", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("create terminal: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("create terminal status = %d, want 200", resp.StatusCode)
	}
	var out shared.Terminal
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode terminal: %v", err)
	}
	return out
}

func mintTicket(t *testing.T, srv *httptest.Server, issueID, terminalID string) string {
	t.Helper()
	resp, err := http.Post(srv.URL+"/api/worktree/issues/"+issueID+"/terminals/"+terminalID+"/ticket", "application/json", nil)
	if err != nil {
		t.Fatalf("mint ticket: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("ticket status = %d, want 200", resp.StatusCode)
	}
	var tk shared.TerminalTicket
	_ = json.NewDecoder(resp.Body).Decode(&tk)
	return tk.Ticket
}

// TestTerminalRelayE2E drives the whole relay over real websockets: create →
// poll claim → daemon dial-back → viewer attach → bytes both ways → resize →
// title → scrollback replay for a late viewer → exit → close.
func TestTerminalRelayE2E(t *testing.T) {
	sink := &eventSink{}
	m := newTestModule("member", sink)
	issueID := newIssueID()
	daemonID := "daemon-term-e2e"
	makeReady(t, m.Store(), issueID, "WAT-60", repoA, daemonID, shared.StatusReport{})
	srv := httptest.NewServer(termTestRouter(m))
	defer srv.Close()

	created := createTerminal(t, srv, issueID)
	if created.Status != shared.TermPending || created.Index != 1 {
		t.Fatalf("created = %+v, want pending #1", created)
	}

	// The owning daemon's poll carries the pending session, exactly once.
	resp, err := http.Get(srv.URL + "/api/daemon/worktree/jobs?workspace_id=" + testWorkspaceID + "&daemon_id=" + daemonID)
	if err != nil {
		t.Fatalf("poll: %v", err)
	}
	var jr shared.JobsResponse
	_ = json.NewDecoder(resp.Body).Decode(&jr)
	resp.Body.Close()
	if len(jr.Terminals) != 1 || jr.Terminals[0].ID != created.ID || jr.Terminals[0].Cols != 120 {
		t.Fatalf("poll terminals = %+v, want the created session", jr.Terminals)
	}

	// Daemon dials back and the session opens.
	daemonConn := dialWS(t, wsURL(srv, "/api/daemon/worktree/terminals/"+created.ID+"/ws?daemon_id="+daemonID+"&workspace_id="+testWorkspaceID))
	defer daemonConn.Close()

	// First viewer sees state=open (no scrollback yet).
	viewer := dialWS(t, wsURL(srv, "/ws/worktree-terminal?ticket="+mintTicket(t, srv, issueID, created.ID)))
	defer viewer.Close()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if ctl := readCtl(t, viewer); ctl.Type == shared.TermCtlState && ctl.Status == shared.TermOpen {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("viewer never saw the open state")
		}
	}

	// PTY output fans out to the viewer.
	if err := daemonConn.WriteMessage(websocket.BinaryMessage, []byte("hello from pty")); err != nil {
		t.Fatalf("daemon write: %v", err)
	}
	if mt, data := readFrame(t, viewer); mt != websocket.BinaryMessage || string(data) != "hello from pty" {
		t.Fatalf("viewer got (%d, %q), want the pty bytes", mt, data)
	}

	// Keystrokes and resizes reach the daemon.
	if err := viewer.WriteMessage(websocket.BinaryMessage, []byte("ls\r")); err != nil {
		t.Fatalf("viewer write: %v", err)
	}
	if mt, data := readFrame(t, daemonConn); mt != websocket.BinaryMessage || string(data) != "ls\r" {
		t.Fatalf("daemon got (%d, %q), want the keystrokes", mt, data)
	}
	resize, _ := json.Marshal(shared.TermCtl{Type: shared.TermCtlResize, Cols: 100, Rows: 40})
	if err := viewer.WriteMessage(websocket.TextMessage, resize); err != nil {
		t.Fatalf("viewer resize: %v", err)
	}
	if ctl := readCtl(t, daemonConn); ctl.Type != shared.TermCtlResize || ctl.Cols != 100 || ctl.Rows != 40 {
		t.Fatalf("daemon resize ctl = %+v", ctl)
	}

	// Title updates reach the viewer and the workspace event stream.
	title, _ := json.Marshal(shared.TermCtl{Type: shared.TermCtlTitle, Title: "make"})
	if err := daemonConn.WriteMessage(websocket.TextMessage, title); err != nil {
		t.Fatalf("daemon title: %v", err)
	}
	if ctl := readCtl(t, viewer); ctl.Type != shared.TermCtlState || ctl.Title != "make" {
		t.Fatalf("viewer title state = %+v", ctl)
	}

	// A late viewer replays the scrollback before going live.
	late := dialWS(t, wsURL(srv, "/ws/worktree-terminal?ticket="+mintTicket(t, srv, issueID, created.ID)))
	defer late.Close()
	if ctl := readCtl(t, late); ctl.Type != shared.TermCtlState || ctl.Status != shared.TermOpen || ctl.Title != "make" {
		t.Fatalf("late viewer state = %+v", ctl)
	}
	if mt, data := readFrame(t, late); mt != websocket.BinaryMessage || string(data) != "hello from pty" {
		t.Fatalf("late viewer replay = (%d, %q), want the scrollback", mt, data)
	}

	// Tickets are one-shot.
	oneShot := mintTicket(t, srv, issueID, created.ID)
	c := dialWS(t, wsURL(srv, "/ws/worktree-terminal?ticket="+oneShot))
	c.Close()
	if _, _, err := websocket.DefaultDialer.Dial(wsURL(srv, "/ws/worktree-terminal?ticket="+oneShot), nil); err == nil {
		t.Fatal("reused ticket accepted")
	}

	// Shell exit propagates: viewer state + list status.
	code := 0
	exit, _ := json.Marshal(shared.TermCtl{Type: shared.TermCtlExit, ExitCode: &code})
	if err := daemonConn.WriteMessage(websocket.TextMessage, exit); err != nil {
		t.Fatalf("daemon exit: %v", err)
	}
	for {
		ctl := readCtl(t, viewer)
		if ctl.Type == shared.TermCtlState && ctl.Status == shared.TermExited {
			if ctl.ExitCode == nil || *ctl.ExitCode != 0 {
				t.Fatalf("exit state = %+v, want code 0", ctl)
			}
			break
		}
	}
	listResp, err := http.Get(srv.URL + "/api/worktree/issues/" + issueID + "/terminals")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	var list []shared.Terminal
	_ = json.NewDecoder(listResp.Body).Decode(&list)
	listResp.Body.Close()
	if len(list) != 1 || list[0].Status != shared.TermExited {
		t.Fatalf("list after exit = %+v, want one exited session", list)
	}

	// Closing the tab removes the session.
	req, _ := http.NewRequest(http.MethodDelete, srv.URL+"/api/worktree/issues/"+issueID+"/terminals/"+created.ID, nil)
	delResp, err := http.DefaultClient.Do(req)
	if err != nil || delResp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete = %v (%v), want 204", delResp, err)
	}
	delResp.Body.Close()
	listResp, _ = http.Get(srv.URL + "/api/worktree/issues/" + issueID + "/terminals")
	list = nil
	_ = json.NewDecoder(listResp.Body).Decode(&list)
	listResp.Body.Close()
	if len(list) != 0 {
		t.Fatalf("list after close = %+v, want empty", list)
	}
}

// TestTerminalDaemonAuth rejects a dial-back from anyone but the owning daemon
// and rejects terminal creation when no worktree is ready.
func TestTerminalDaemonAuth(t *testing.T) {
	sink := &eventSink{}
	m := newTestModule("member", sink)
	issueID := newIssueID()
	makeReady(t, m.Store(), issueID, "WAT-61", repoA, "daemon-owner", shared.StatusReport{})
	srv := httptest.NewServer(termTestRouter(m))
	defer srv.Close()

	created := createTerminal(t, srv, issueID)
	if _, _, err := websocket.DefaultDialer.Dial(
		wsURL(srv, "/api/daemon/worktree/terminals/"+created.ID+"/ws?daemon_id=impostor&workspace_id="+testWorkspaceID), nil); err == nil {
		t.Fatal("impostor daemon connected")
	}

	// The daemon socket also 404s for unknown sessions.
	if _, _, err := websocket.DefaultDialer.Dial(
		wsURL(srv, "/api/daemon/worktree/terminals/"+newIssueID()+"/ws?daemon_id=daemon-owner&workspace_id="+testWorkspaceID), nil); err == nil {
		t.Fatal("unknown session accepted")
	}

	// No ready worktree → 409 before any session is registered.
	resp, err := http.Post(srv.URL+"/api/worktree/issues/"+newIssueID()+"/terminals", "application/json", nil)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("create without worktree status = %d, want 409", resp.StatusCode)
	}
}

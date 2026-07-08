package serverside

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"sort"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/gorilla/websocket"

	"github.com/multica-ai/multica/server/addons/worktrees/shared"
)

// In-memory relay for interactive terminals: a member creates a session, the
// issue's owning daemon claims it on its next poll, spawns a shell PTY in the
// issue workspace, and dials back a WebSocket; viewers (the issue side panel)
// attach over their own ticket-authenticated WebSocket and the server pumps
// bytes between the two sides.
//
// Like the file-op relay, this is deliberately memory-only: a terminal is a
// live byte stream between a daemon process and a browser tab — there is
// nothing meaningful to persist across a server restart (both sockets die with
// the process). Single-server by design, like the rest of this add-on.

const (
	// maxTerminalsPerIssue caps live (pending/open) sessions per issue.
	maxTerminalsPerIssue = 8
	// termScrollbackCap bounds the replay buffer kept per session so a
	// reattaching viewer gets recent history, not unbounded memory.
	termScrollbackCap = 256 << 10
	// termInputBufferCap bounds keystrokes buffered while the daemon has not
	// attached yet; overflow is dropped.
	termInputBufferCap = 8 << 10
	// termPendingTimeout ends a session whose daemon never showed up.
	termPendingTimeout = 90 * time.Second
	// termClaimRedeliver re-arms a claimed session whose daemon did not dial
	// back (e.g. it died between the poll and the dial), so a later poll can
	// retry.
	termClaimRedeliver = 30 * time.Second
	// termExitedTTL removes exited sessions nobody closed explicitly.
	termExitedTTL = time.Hour
	// termTicketTTL is the viewer WS ticket lifetime (one shot, seconds-scale:
	// the UI connects immediately after minting it).
	termTicketTTL = 30 * time.Second

	termWriteWait  = 10 * time.Second
	termPongWait   = 60 * time.Second
	termPingPeriod = termPongWait * 9 / 10
)

// termUpgrader upgrades both relay legs. Origin is deliberately not checked:
// the viewer socket is authenticated by a one-time ticket minted over the
// member-authed HTTP API (which cross-origin pages cannot call), the daemon
// socket by its bearer token — and legitimate clients span origins (web,
// desktop, the daemon sends none).
var termUpgrader = websocket.Upgrader{
	ReadBufferSize:  32 << 10,
	WriteBufferSize: 32 << 10,
	CheckOrigin:     func(*http.Request) bool { return true },
}

// termConn serializes writes on one WebSocket (gorilla allows a single
// concurrent writer; frames arrive from several goroutines).
type termConn struct {
	c  *websocket.Conn
	mu sync.Mutex
}

func (tc *termConn) write(msgType int, data []byte) error {
	tc.mu.Lock()
	defer tc.mu.Unlock()
	_ = tc.c.SetWriteDeadline(time.Now().Add(termWriteWait))
	return tc.c.WriteMessage(msgType, data)
}

func (tc *termConn) writeCtl(ctl shared.TermCtl) error {
	b, err := json.Marshal(ctl)
	if err != nil {
		return err
	}
	return tc.write(websocket.TextMessage, b)
}

func (tc *termConn) ping() error {
	return tc.c.WriteControl(websocket.PingMessage, nil, time.Now().Add(termWriteWait))
}

// termSession is one terminal's relay state. info/status transitions happen
// under mu; conn writes go through each termConn's own lock so a slow socket
// never blocks the session for long.
type termSession struct {
	mu sync.Mutex

	info       shared.Terminal
	daemonID   string
	identifier string
	cols, rows int

	claimed   bool
	claimedAt time.Time
	createdAt time.Time
	exitedAt  time.Time

	daemon  *termConn
	viewers map[*termConn]struct{}

	scroll       []byte
	pendingInput []byte
}

func (s *termSession) snapshotLocked() shared.Terminal { return s.info }

func (s *termSession) snapshot() shared.Terminal {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.info
}

func (s *termSession) stateCtlLocked() shared.TermCtl {
	return shared.TermCtl{
		Type:     shared.TermCtlState,
		Status:   s.info.Status,
		Title:    s.info.Title,
		ExitCode: s.info.ExitCode,
		Error:    s.info.Error,
	}
}

// viewerStateLocked snapshots what a state broadcast needs. Callers hold s.mu
// and perform the writes after unlocking — a stalled viewer socket (10s write
// deadline) must never hold the session lock.
func (s *termSession) viewerStateLocked() (shared.TermCtl, []*termConn) {
	conns := make([]*termConn, 0, len(s.viewers))
	for v := range s.viewers {
		conns = append(conns, v)
	}
	return s.stateCtlLocked(), conns
}

func broadcastState(ctl shared.TermCtl, conns []*termConn) {
	for _, v := range conns {
		_ = v.writeCtl(ctl)
	}
}

type termTicket struct {
	terminalID string
	expires    time.Time
}

// terminalRegistry owns every live session + viewer ticket. notify fans a
// session snapshot out to workspace WS subscribers after any lifecycle change
// so tab lists stay in sync without polling.
type terminalRegistry struct {
	mu        sync.Mutex
	sessions  map[string]*termSession
	nextIndex map[string]int // issueID → next tab ordinal
	tickets   map[string]termTicket
	notify    func(t shared.Terminal, removed bool)
}

func newTerminalRegistry(notify func(t shared.Terminal, removed bool)) *terminalRegistry {
	return &terminalRegistry{
		sessions:  map[string]*termSession{},
		nextIndex: map[string]int{},
		tickets:   map[string]termTicket{},
		notify:    notify,
	}
}

func (r *terminalRegistry) emit(t shared.Terminal, removed bool) {
	if r.notify != nil {
		r.notify(t, removed)
	}
}

// create registers a pending session for the issue's owning daemon and
// announces it. Fails when the issue already has the maximum number of live
// terminals.
func (r *terminalRegistry) create(issueID, wsID, daemonID, identifier string, cols, rows int) (shared.Terminal, error) {
	now := time.Now()
	r.mu.Lock()
	live := 0
	for _, s := range r.sessions {
		if s.info.IssueID == issueID && s.snapshot().Status != shared.TermExited {
			live++
		}
	}
	if live >= maxTerminalsPerIssue {
		r.mu.Unlock()
		return shared.Terminal{}, ErrTooManyTerminals
	}
	r.nextIndex[issueID]++
	sess := &termSession{
		info: shared.Terminal{
			ID:          uuid.NewString(),
			IssueID:     issueID,
			WorkspaceID: wsID,
			Index:       r.nextIndex[issueID],
			Status:      shared.TermPending,
			CreatedAt:   now.UTC().Format(time.RFC3339),
		},
		daemonID:   daemonID,
		identifier: identifier,
		cols:       cols,
		rows:       rows,
		createdAt:  now,
		viewers:    map[*termConn]struct{}{},
	}
	r.sessions[sess.info.ID] = sess
	snap := sess.info
	r.mu.Unlock()
	r.emit(snap, false)
	return snap, nil
}

func (r *terminalRegistry) get(id string) (*termSession, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	s, ok := r.sessions[id]
	return s, ok
}

// listByIssue returns snapshots sorted by tab ordinal.
func (r *terminalRegistry) listByIssue(issueID, wsID string) []shared.Terminal {
	r.mu.Lock()
	sessions := make([]*termSession, 0, 4)
	for _, s := range r.sessions {
		if s.info.IssueID == issueID && s.info.WorkspaceID == wsID {
			sessions = append(sessions, s)
		}
	}
	r.mu.Unlock()
	out := make([]shared.Terminal, 0, len(sessions))
	for _, s := range sessions {
		out = append(out, s.snapshot())
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Index < out[j].Index })
	return out
}

// pendingFor hands each unclaimed pending session for (daemon, workspace) to
// the poll response. Sessions stay registered; a claim that never dials back is
// re-armed by the janitor.
func (r *terminalRegistry) pendingFor(daemonID, wsID string) []shared.TerminalOpen {
	r.mu.Lock()
	sessions := make([]*termSession, 0)
	for _, s := range r.sessions {
		sessions = append(sessions, s)
	}
	r.mu.Unlock()

	var out []shared.TerminalOpen
	for _, s := range sessions {
		s.mu.Lock()
		if s.info.Status == shared.TermPending && !s.claimed && s.daemonID == daemonID && s.info.WorkspaceID == wsID {
			s.claimed = true
			s.claimedAt = time.Now()
			out = append(out, shared.TerminalOpen{
				ID:          s.info.ID,
				IssueID:     s.info.IssueID,
				WorkspaceID: s.info.WorkspaceID,
				Identifier:  s.identifier,
				Cols:        s.cols,
				Rows:        s.rows,
			})
		}
		s.mu.Unlock()
	}
	return out
}

// attachDaemon binds the daemon-side socket: the PTY is live, buffered
// keystrokes flush, and the session goes open.
func (r *terminalRegistry) attachDaemon(s *termSession, conn *termConn) bool {
	s.mu.Lock()
	if s.daemon != nil || s.info.Status != shared.TermPending {
		s.mu.Unlock()
		return false
	}
	s.daemon = conn
	s.info.Status = shared.TermOpen
	input := s.pendingInput
	s.pendingInput = nil
	ctl, viewers := s.viewerStateLocked()
	snap := s.info
	s.mu.Unlock()
	if len(input) > 0 {
		_ = conn.write(websocket.BinaryMessage, input)
	}
	broadcastState(ctl, viewers)
	r.emit(snap, false)
	return true
}

// attachViewer binds one UI socket: it receives the current state + scrollback
// before going live so the replay and the live stream never interleave.
func (r *terminalRegistry) attachViewer(s *termSession, conn *termConn) {
	s.mu.Lock()
	_ = conn.writeCtl(s.stateCtlLocked())
	if len(s.scroll) > 0 {
		_ = conn.write(websocket.BinaryMessage, s.scroll)
	}
	s.viewers[conn] = struct{}{}
	s.mu.Unlock()
}

func (r *terminalRegistry) detachViewer(s *termSession, conn *termConn) {
	s.mu.Lock()
	delete(s.viewers, conn)
	s.mu.Unlock()
}

// output appends PTY bytes to the scrollback ring and fans them out to every
// viewer. A viewer whose socket errors is dropped (its read loop cleans up).
func (r *terminalRegistry) output(s *termSession, data []byte) {
	s.mu.Lock()
	s.scroll = append(s.scroll, data...)
	if over := len(s.scroll) - termScrollbackCap; over > 0 {
		s.scroll = append(s.scroll[:0], s.scroll[over:]...)
	}
	conns := make([]*termConn, 0, len(s.viewers))
	for v := range s.viewers {
		conns = append(conns, v)
	}
	s.mu.Unlock()
	for _, v := range conns {
		_ = v.write(websocket.BinaryMessage, data)
	}
}

// forwardInput relays viewer keystrokes to the PTY, buffering (bounded) while
// the daemon has not attached yet.
func (r *terminalRegistry) forwardInput(s *termSession, data []byte) {
	s.mu.Lock()
	daemon := s.daemon
	if daemon == nil && s.info.Status == shared.TermPending {
		if len(s.pendingInput)+len(data) <= termInputBufferCap {
			s.pendingInput = append(s.pendingInput, data...)
		}
	}
	s.mu.Unlock()
	if daemon != nil {
		_ = daemon.write(websocket.BinaryMessage, data)
	}
}

// forwardResize relays a viewer resize to the PTY.
func (r *terminalRegistry) forwardResize(s *termSession, ctl shared.TermCtl) {
	s.mu.Lock()
	daemon := s.daemon
	if ctl.Cols > 0 && ctl.Rows > 0 {
		s.cols, s.rows = ctl.Cols, ctl.Rows
	}
	s.mu.Unlock()
	if daemon != nil {
		_ = daemon.writeCtl(shared.TermCtl{Type: shared.TermCtlResize, Cols: ctl.Cols, Rows: ctl.Rows})
	}
}

// setTitle records the shell's foreground process and announces the change.
func (r *terminalRegistry) setTitle(s *termSession, title string) {
	s.mu.Lock()
	if s.info.Title == title {
		s.mu.Unlock()
		return
	}
	s.info.Title = title
	ctl, viewers := s.viewerStateLocked()
	snap := s.info
	s.mu.Unlock()
	broadcastState(ctl, viewers)
	r.emit(snap, false)
}

// exit marks the session exited (shell ended / daemon lost) but keeps it — the
// scrollback stays viewable until the tab is closed or the janitor expires it.
func (r *terminalRegistry) exit(s *termSession, exitCode *int, errMsg string) {
	s.mu.Lock()
	if s.info.Status == shared.TermExited {
		s.mu.Unlock()
		return
	}
	s.info.Status = shared.TermExited
	s.info.ExitCode = exitCode
	s.info.Error = errMsg
	s.exitedAt = time.Now()
	s.daemon = nil
	ctl, viewers := s.viewerStateLocked()
	snap := s.info
	s.mu.Unlock()
	broadcastState(ctl, viewers)
	r.emit(snap, false)
}

// remove tears a session down entirely: both socket sides close (the daemon
// side kills the PTY on close) and the id disappears from lists.
func (r *terminalRegistry) remove(id string) {
	r.mu.Lock()
	s, ok := r.sessions[id]
	if !ok {
		r.mu.Unlock()
		return
	}
	delete(r.sessions, id)
	issueLeft := false
	for _, other := range r.sessions {
		if other.info.IssueID == s.info.IssueID {
			issueLeft = true
			break
		}
	}
	if !issueLeft {
		// Last tab of the issue gone → restart numbering at "Terminal 1".
		delete(r.nextIndex, s.info.IssueID)
	}
	r.mu.Unlock()

	s.mu.Lock()
	daemon := s.daemon
	s.daemon = nil
	conns := make([]*termConn, 0, len(s.viewers))
	for v := range s.viewers {
		conns = append(conns, v)
	}
	s.viewers = map[*termConn]struct{}{}
	snap := s.info
	s.mu.Unlock()

	if daemon != nil {
		_ = daemon.c.Close()
	}
	for _, v := range conns {
		_ = v.c.Close()
	}
	r.emit(snap, true)
}

// closeForIssue removes every session of an issue (cleanup on done/cancelled).
func (r *terminalRegistry) closeForIssue(issueID string) {
	r.mu.Lock()
	ids := make([]string, 0)
	for id, s := range r.sessions {
		if s.info.IssueID == issueID {
			ids = append(ids, id)
		}
	}
	r.mu.Unlock()
	for _, id := range ids {
		r.remove(id)
	}
}

// issueTicket mints a one-time viewer WS credential for a session.
func (r *terminalRegistry) issueTicket(terminalID string) (string, error) {
	buf := make([]byte, 24)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	t := hex.EncodeToString(buf)
	now := time.Now()
	r.mu.Lock()
	for k, v := range r.tickets {
		if now.After(v.expires) {
			delete(r.tickets, k)
		}
	}
	r.tickets[t] = termTicket{terminalID: terminalID, expires: now.Add(termTicketTTL)}
	r.mu.Unlock()
	return t, nil
}

// redeemTicket consumes a ticket and returns its session.
func (r *terminalRegistry) redeemTicket(ticket string) (*termSession, bool) {
	r.mu.Lock()
	tk, ok := r.tickets[ticket]
	if ok {
		delete(r.tickets, ticket)
	}
	var sess *termSession
	if ok && time.Now().Before(tk.expires) {
		sess = r.sessions[tk.terminalID]
	}
	r.mu.Unlock()
	return sess, sess != nil
}

// sweep advances time-based transitions: unattached claims re-arm, abandoned
// pendings exit, old exited sessions disappear. Called by the module's janitor.
func (r *terminalRegistry) sweep(now time.Time) {
	r.mu.Lock()
	sessions := make([]*termSession, 0, len(r.sessions))
	for _, s := range r.sessions {
		sessions = append(sessions, s)
	}
	r.mu.Unlock()

	for _, s := range sessions {
		s.mu.Lock()
		status := s.info.Status
		claimed, claimedAt := s.claimed, s.claimedAt
		createdAt, exitedAt := s.createdAt, s.exitedAt
		if status == shared.TermPending && claimed && s.daemon == nil && now.Sub(claimedAt) > termClaimRedeliver {
			s.claimed = false
		}
		s.mu.Unlock()

		switch {
		case status == shared.TermPending && now.Sub(createdAt) > termPendingTimeout:
			r.exit(s, nil, "owning machine did not attach")
		case status == shared.TermExited && now.Sub(exitedAt) > termExitedTTL:
			r.remove(s.info.ID)
		}
	}
}

// janitor drives sweep until ctx ends.
func (r *terminalRegistry) janitor(done <-chan struct{}) {
	t := time.NewTicker(15 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-done:
			return
		case now := <-t.C:
			r.sweep(now)
		}
	}
}

// ---- HTTP handlers ----

// handleListTerminals returns the issue's live terminal sessions.
func (m *Module) handleListTerminals(w http.ResponseWriter, r *http.Request) {
	p, ok := m.principal(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, m.terminals.listByIssue(chi.URLParam(r, "issueId"), p.WorkspaceID))
}

// handleCreateTerminal opens a new session on the issue's owning (online)
// daemon and returns it; the daemon attaches on its next poll.
func (m *Module) handleCreateTerminal(w http.ResponseWriter, r *http.Request) {
	p, ok := m.principal(w, r)
	if !ok {
		return
	}
	var req shared.CreateTerminalRequest
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&req) // an empty body means defaults
	}
	cols := clampInt(req.Cols, 20, 500, 80)
	rows := clampInt(req.Rows, 5, 200, 24)

	wt, err := m.store.TerminalHost(r.Context(), chi.URLParam(r, "issueId"), p.WorkspaceID)
	if err != nil {
		writeWorktreeErr(w, err)
		return
	}
	t, err := m.terminals.create(wt.IssueID, wt.WorkspaceID, wt.OwnerDaemonID, wt.Identifier, cols, rows)
	if err != nil {
		writeWorktreeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, t)
}

// handleCloseTerminal kills the session (shell included) and drops it.
func (m *Module) handleCloseTerminal(w http.ResponseWriter, r *http.Request) {
	p, ok := m.principal(w, r)
	if !ok {
		return
	}
	sess, ok := m.loadScopedTerminal(w, r, p)
	if !ok {
		return
	}
	m.terminals.remove(sess.info.ID)
	w.WriteHeader(http.StatusNoContent)
}

// handleTerminalTicket mints the one-time viewer WS credential.
func (m *Module) handleTerminalTicket(w http.ResponseWriter, r *http.Request) {
	p, ok := m.principal(w, r)
	if !ok {
		return
	}
	sess, ok := m.loadScopedTerminal(w, r, p)
	if !ok {
		return
	}
	ticket, err := m.terminals.issueTicket(sess.info.ID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, http.StatusOK, shared.TerminalTicket{Ticket: ticket})
}

// loadScopedTerminal loads the path's session and verifies it belongs to the
// caller's workspace + issue (cross-workspace guard, like loadOwnedWorktree).
func (m *Module) loadScopedTerminal(w http.ResponseWriter, r *http.Request, p Principal) (*termSession, bool) {
	sess, ok := m.terminals.get(chi.URLParam(r, "terminalId"))
	if !ok {
		writeErr(w, http.StatusNotFound, "terminal not found")
		return nil, false
	}
	snap := sess.snapshot()
	if snap.WorkspaceID != p.WorkspaceID || snap.IssueID != chi.URLParam(r, "issueId") {
		writeErr(w, http.StatusNotFound, "terminal not found")
		return nil, false
	}
	return sess, true
}

// HandleTerminalSocket is the viewer WebSocket endpoint. Mounted OUTSIDE the
// member-auth middleware (a browser cannot send auth headers on a WS upgrade);
// the one-time ticket — minted over the member-authed API seconds earlier — is
// the credential.
func (m *Module) HandleTerminalSocket(w http.ResponseWriter, r *http.Request) {
	sess, ok := m.terminals.redeemTicket(r.URL.Query().Get("ticket"))
	if !ok {
		writeErr(w, http.StatusUnauthorized, "invalid or expired ticket")
		return
	}
	conn, err := termUpgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	tc := &termConn{c: conn}
	m.terminals.attachViewer(sess, tc)
	go pingLoop(tc)

	conn.SetReadLimit(1 << 20)
	_ = conn.SetReadDeadline(time.Now().Add(termPongWait))
	conn.SetPongHandler(func(string) error {
		return conn.SetReadDeadline(time.Now().Add(termPongWait))
	})
	for {
		msgType, data, err := conn.ReadMessage()
		if err != nil {
			break
		}
		_ = conn.SetReadDeadline(time.Now().Add(termPongWait))
		switch msgType {
		case websocket.BinaryMessage:
			m.terminals.forwardInput(sess, data)
		case websocket.TextMessage:
			var ctl shared.TermCtl
			if json.Unmarshal(data, &ctl) == nil && ctl.Type == shared.TermCtlResize {
				m.terminals.forwardResize(sess, ctl)
			}
		}
	}
	m.terminals.detachViewer(sess, tc)
	_ = conn.Close()
}

// handleTerminalDaemonWS is the daemon dial-back endpoint (inside the
// daemon-auth router, like every other daemon route — the daemon is a Go
// client and sends its bearer token on the upgrade request).
func (m *Module) handleTerminalDaemonWS(w http.ResponseWriter, r *http.Request) {
	daemonID := r.URL.Query().Get("daemon_id")
	sess, ok := m.terminals.get(chi.URLParam(r, "terminalId"))
	if !ok {
		writeErr(w, http.StatusNotFound, "terminal not found")
		return
	}
	snap := sess.snapshot()
	if !m.canAccess(r, snap.WorkspaceID) {
		writeErr(w, http.StatusForbidden, "forbidden")
		return
	}
	if daemonID == "" || sess.daemonID != daemonID {
		writeErr(w, http.StatusForbidden, "not the owning daemon")
		return
	}
	conn, err := termUpgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	tc := &termConn{c: conn}
	if !m.terminals.attachDaemon(sess, tc) {
		_ = conn.Close()
		return
	}
	go pingLoop(tc)

	conn.SetReadLimit(1 << 20)
	_ = conn.SetReadDeadline(time.Now().Add(termPongWait))
	conn.SetPongHandler(func(string) error {
		return conn.SetReadDeadline(time.Now().Add(termPongWait))
	})
	exited := false
	for {
		msgType, data, err := conn.ReadMessage()
		if err != nil {
			break
		}
		_ = conn.SetReadDeadline(time.Now().Add(termPongWait))
		switch msgType {
		case websocket.BinaryMessage:
			m.terminals.output(sess, data)
		case websocket.TextMessage:
			var ctl shared.TermCtl
			if json.Unmarshal(data, &ctl) != nil {
				continue
			}
			switch ctl.Type {
			case shared.TermCtlTitle:
				m.terminals.setTitle(sess, ctl.Title)
			case shared.TermCtlExit:
				exited = true
				m.terminals.exit(sess, ctl.ExitCode, ctl.Error)
			}
		}
	}
	_ = conn.Close()
	if !exited {
		m.terminals.exit(sess, nil, "connection to the owning machine was lost")
	}
}

// pingLoop keeps one relay socket alive through idle stretches (and lets dead
// peers time out via the read deadline). Ends when the socket dies.
func pingLoop(tc *termConn) {
	t := time.NewTicker(termPingPeriod)
	defer t.Stop()
	for range t.C {
		if tc.ping() != nil {
			return
		}
	}
}

func clampInt(v, min, max, def int) int {
	if v <= 0 {
		return def
	}
	if v < min {
		return min
	}
	if v > max {
		return max
	}
	return v
}

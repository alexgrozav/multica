package serverside

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/multica-ai/multica/server/addons/worktrees/shared"
)

// UIRouter returns the member-authenticated routes. Mounted by the adapter at
// /api/worktree inside the RequireWorkspaceMember group.
func (m *Module) UIRouter() http.Handler {
	r := chi.NewRouter()
	r.Get("/issues/{issueId}", m.handleListWorktrees)
	r.Get("/issues/{issueId}/files", m.handleListFiles)
	r.Get("/issues/{issueId}/changes", m.handleListChanges)
	r.Get("/issues/{issueId}/{worktreeId}/file", m.handleReadFile)
	r.Put("/issues/{issueId}/{worktreeId}/file", m.handleWriteFile)
	r.Get("/issues/{issueId}/{worktreeId}/diff", m.handleReadDiff)
	r.Post("/issues/{issueId}/{worktreeId}/setup", m.handleSetup)
	r.Post("/issues/{issueId}/{worktreeId}/run", m.handleRun)
	r.Post("/issues/{issueId}/{worktreeId}/run/stop", m.handleStop)
	r.Post("/issues/{issueId}/open", m.handleOpen)
	r.Get("/issues/{issueId}/terminals", m.handleListTerminals)
	r.Post("/issues/{issueId}/terminals", m.handleCreateTerminal)
	r.Delete("/issues/{issueId}/terminals/{terminalId}", m.handleCloseTerminal)
	r.Post("/issues/{issueId}/terminals/{terminalId}/ticket", m.handleTerminalTicket)
	return r
}

// DaemonRouter returns the daemon-authenticated routes. Mounted by the adapter
// at /api/daemon/worktree inside the DaemonAuth group.
func (m *Module) DaemonRouter() http.Handler {
	r := chi.NewRouter()
	r.Get("/jobs", m.handlePollJobs)
	r.Post("/reconcile", m.handleReconcile)
	r.Get("/issues/{issueId}/status", m.handleIssueStatus)
	r.Post("/jobs/{worktreeId}/status", m.handleReportStatus)
	r.Post("/runs/{runTaskId}/logs", m.handleRunLogs)
	r.Post("/worktrees/{worktreeId}/files", m.handleReportFiles)
	r.Post("/worktrees/{worktreeId}/changes", m.handleReportChanges)
	r.Post("/file-ops/{opId}/result", m.handleFileOpResult)
	r.Get("/terminals/{terminalId}/ws", m.handleTerminalDaemonWS)
	return r
}

// ---- UI handlers ----

func (m *Module) handleListWorktrees(w http.ResponseWriter, r *http.Request) {
	p, ok := m.principal(w, r)
	if !ok {
		return
	}
	wts, err := m.store.ListByIssue(r.Context(), chi.URLParam(r, "issueId"), p.WorkspaceID)
	if err != nil {
		m.deps.log().Error("worktrees: list failed", "error", err)
		writeErr(w, http.StatusInternalServerError, "failed to list worktrees")
		return
	}
	writeJSON(w, http.StatusOK, wts)
}

// handleListFiles returns the stored per-repo file lists of an issue's
// workspace (the Project tab's tree data).
func (m *Module) handleListFiles(w http.ResponseWriter, r *http.Request) {
	p, ok := m.principal(w, r)
	if !ok {
		return
	}
	out, err := m.store.FilesByIssue(r.Context(), chi.URLParam(r, "issueId"), p.WorkspaceID)
	if err != nil {
		m.deps.log().Error("worktrees: list files failed", "error", err)
		writeErr(w, http.StatusInternalServerError, "failed to list files")
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// handleListChanges returns the stored per-repo git status of an issue's
// workspace (the Changes tab's data).
func (m *Module) handleListChanges(w http.ResponseWriter, r *http.Request) {
	p, ok := m.principal(w, r)
	if !ok {
		return
	}
	out, err := m.store.ChangesByIssue(r.Context(), chi.URLParam(r, "issueId"), p.WorkspaceID)
	if err != nil {
		m.deps.log().Error("worktrees: list changes failed", "error", err)
		writeErr(w, http.StatusInternalServerError, "failed to list changes")
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (m *Module) handleSetup(w http.ResponseWriter, r *http.Request) {
	p, ok := m.principal(w, r)
	if !ok {
		return
	}
	wt, ok := m.loadOwnedWorktree(w, r, p)
	if !ok {
		return
	}
	out, err := m.store.RequestSetup(r.Context(), wt.ID)
	if err != nil {
		writeWorktreeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (m *Module) handleRun(w http.ResponseWriter, r *http.Request) {
	p, ok := m.principal(w, r)
	if !ok {
		return
	}
	wt, ok := m.loadOwnedWorktree(w, r, p)
	if !ok {
		return
	}
	name, ok := decodeRunName(w, r)
	if !ok {
		return
	}
	out, err := m.store.RequestRun(r.Context(), wt.ID, name)
	if err != nil {
		writeWorktreeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (m *Module) handleStop(w http.ResponseWriter, r *http.Request) {
	p, ok := m.principal(w, r)
	if !ok {
		return
	}
	wt, ok := m.loadOwnedWorktree(w, r, p)
	if !ok {
		return
	}
	name, ok := decodeRunName(w, r)
	if !ok {
		return
	}
	out, err := m.store.RequestStop(r.Context(), wt.ID, name)
	if err != nil {
		writeWorktreeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// handleOpen queues an "open the issue working directory in <target>" job for
// the owning daemon. Issue-scoped (one working dir per issue), so it does not
// take a worktreeId — the store picks a ready worktree to carry the job.
func (m *Module) handleOpen(w http.ResponseWriter, r *http.Request) {
	p, ok := m.principal(w, r)
	if !ok {
		return
	}
	var req shared.OpenRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	target := strings.TrimSpace(req.Target)
	if !shared.ValidOpenTarget(target) {
		writeErr(w, http.StatusBadRequest, "unknown open target")
		return
	}
	out, err := m.store.RequestOpen(r.Context(), chi.URLParam(r, "issueId"), p.WorkspaceID, target)
	if err != nil {
		writeWorktreeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// maxFileBytes caps file content through the relay (both directions). Reads
// larger than this come back truncated (read-only in the UI); writes above it
// are rejected — the editor is for source files, not blobs.
const maxFileBytes = 1 << 20

// fileOpWait bounds how long a UI request waits for the daemon round trip:
// next poll (≤2s) + the read/write itself + the result POST, with headroom for
// a slow disk or connection.
const fileOpWait = 20 * time.Second

// handleReadFile fetches one repo-relative file's content from the issue's
// checked-out worktree, relayed live through the owning daemon.
func (m *Module) handleReadFile(w http.ResponseWriter, r *http.Request) {
	p, ok := m.principal(w, r)
	if !ok {
		return
	}
	wt, ok := m.loadOwnedWorktree(w, r, p)
	if !ok {
		return
	}
	relPath := r.URL.Query().Get("path")
	if !shared.ValidRelFilePath(relPath) {
		writeErr(w, http.StatusBadRequest, "invalid path")
		return
	}
	m.relayFileOp(w, r, wt, shared.FileOp{Kind: shared.FileOpRead, Path: relPath})
}

// handleReadDiff fetches one repo-relative file's base + working-tree content
// from the issue's checked-out worktree (the Changes tab's diff view), relayed
// live through the owning daemon.
func (m *Module) handleReadDiff(w http.ResponseWriter, r *http.Request) {
	p, ok := m.principal(w, r)
	if !ok {
		return
	}
	wt, ok := m.loadOwnedWorktree(w, r, p)
	if !ok {
		return
	}
	relPath := r.URL.Query().Get("path")
	if !shared.ValidRelFilePath(relPath) {
		writeErr(w, http.StatusBadRequest, "invalid path")
		return
	}
	m.relayFileOp(w, r, wt, shared.FileOp{Kind: shared.FileOpDiff, Path: relPath})
}

// handleWriteFile saves editor content back into the worktree file, relayed
// live through the owning daemon. Responds with the daemon-confirmed state.
func (m *Module) handleWriteFile(w http.ResponseWriter, r *http.Request) {
	p, ok := m.principal(w, r)
	if !ok {
		return
	}
	wt, ok := m.loadOwnedWorktree(w, r, p)
	if !ok {
		return
	}
	// JSON escaping can inflate content well past the raw cap; bound the body
	// generously and enforce the exact cap on the decoded string below.
	r.Body = http.MaxBytesReader(w, r.Body, 8<<20)
	var req shared.WriteFileRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	if !shared.ValidRelFilePath(req.Path) {
		writeErr(w, http.StatusBadRequest, "invalid path")
		return
	}
	if len(req.Content) > maxFileBytes {
		writeErr(w, http.StatusRequestEntityTooLarge, "file too large")
		return
	}
	m.relayFileOp(w, r, wt, shared.FileOp{Kind: shared.FileOpWrite, Path: req.Path, Content: req.Content})
}

// relayFileOp queues op for the worktree's owning daemon and waits (bounded)
// for the result, mapping the outcome onto the HTTP response.
func (m *Module) relayFileOp(w http.ResponseWriter, r *http.Request, wt shared.Worktree, op shared.FileOp) {
	if wt.Status != shared.StatusReady {
		writeWorktreeErr(w, ErrNotReady)
		return
	}
	online, err := m.store.DaemonOnline(r.Context(), wt.WorkspaceID, wt.OwnerDaemonID)
	if err != nil {
		m.deps.log().Error("worktrees: daemon online check failed", "error", err)
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	if !online {
		writeWorktreeErr(w, ErrDaemonOffline)
		return
	}
	op.ID = uuid.NewString()
	op.WorktreeID, op.IssueID = wt.ID, wt.IssueID
	op.WorkspaceID, op.RepoURL = wt.WorkspaceID, wt.RepoURL
	pend := m.fileOps.enqueue(wt.OwnerDaemonID, op)
	defer m.fileOps.drop(op.ID)

	select {
	case res := <-pend.done:
		switch {
		case res.NotFound:
			writeErr(w, http.StatusNotFound, "file not found")
		case res.Error != "":
			writeErr(w, http.StatusConflict, res.Error)
		case op.Kind == shared.FileOpDiff:
			writeJSON(w, http.StatusOK, shared.FileDiff{
				WorktreeID: wt.ID,
				Path:       op.Path,
				OldContent: res.OldContent,
				NewContent: res.Content,
				Truncated:  res.Truncated,
				Binary:     res.Binary,
			})
		default:
			writeJSON(w, http.StatusOK, shared.FileContent{
				WorktreeID: wt.ID,
				Path:       op.Path,
				Content:    res.Content,
				Size:       res.Size,
				Truncated:  res.Truncated,
				Binary:     res.Binary,
			})
		}
	case <-r.Context().Done():
		// Caller disconnected; nothing left to write.
	case <-time.After(fileOpWait):
		writeErr(w, http.StatusGatewayTimeout, "owning machine did not respond")
	}
}

// decodeRunName reads the {name} target from a run/stop request body.
func decodeRunName(w http.ResponseWriter, r *http.Request) (string, bool) {
	var req shared.RunRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return "", false
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		writeErr(w, http.StatusBadRequest, "missing run script name")
		return "", false
	}
	return name, true
}

// ---- Daemon handlers ----

func (m *Module) handlePollJobs(w http.ResponseWriter, r *http.Request) {
	// The daemon sends its identity explicitly (it knows both from
	// registration) so this works on every auth path — including a mul_ user
	// PAT, where the token-bound daemon-workspace/daemon-id context is empty.
	wsID := r.URL.Query().Get("workspace_id")
	daemonID := r.URL.Query().Get("daemon_id")
	if wsID == "" || daemonID == "" {
		writeErr(w, http.StatusBadRequest, "missing workspace_id or daemon_id")
		return
	}
	if !m.canAccess(r, wsID) {
		writeErr(w, http.StatusForbidden, "forbidden")
		return
	}
	if err := m.store.TouchDaemon(r.Context(), wsID, daemonID); err != nil {
		m.deps.log().Warn("worktrees: touch daemon failed", "error", err)
	}
	jobs, err := m.store.ClaimInitJobs(r.Context(), daemonID, wsID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "failed to claim init jobs")
		return
	}
	actions, err := m.store.ClaimWorktreeActionJobs(r.Context(), daemonID, wsID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "failed to claim action jobs")
		return
	}
	runs, err := m.store.ClaimRunJobs(r.Context(), daemonID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "failed to claim run jobs")
		return
	}
	opens, err := m.store.ClaimOpenJobs(r.Context(), daemonID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "failed to claim open jobs")
		return
	}
	jobs = append(jobs, actions...)
	jobs = append(jobs, runs...)
	jobs = append(jobs, opens...)
	// File-scan targets ride on every poll; a failure here must not block job
	// delivery, so it degrades to "no scan this tick".
	targets, err := m.store.FileScanTargets(r.Context(), daemonID, wsID)
	if err != nil {
		m.deps.log().Warn("worktrees: file scan targets failed", "error", err)
		targets = nil
	}
	fileOps := m.fileOps.claim(daemonID, wsID)
	terminals := m.terminals.pendingFor(daemonID, wsID)
	writeJSON(w, http.StatusOK, shared.JobsResponse{Jobs: jobs, FileScan: targets, FileOps: fileOps, Terminals: terminals})
}

// handleFileOpResult completes a pending file op with the daemon's result,
// unblocking the UI request waiting on it. A late result for an op whose
// caller already gave up is a 404 (harmless — the daemon just logs it).
func (m *Module) handleFileOpResult(w http.ResponseWriter, r *http.Request) {
	opID := chi.URLParam(r, "opId")
	op, ok := m.fileOps.lookup(opID)
	if !ok {
		writeErr(w, http.StatusNotFound, "unknown file op")
		return
	}
	if !m.canAccess(r, op.WorkspaceID) {
		writeErr(w, http.StatusForbidden, "forbidden")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 8<<20)
	var res shared.FileOpResult
	if err := json.NewDecoder(r.Body).Decode(&res); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	m.fileOps.complete(opID, res)
	w.WriteHeader(http.StatusNoContent)
}

// handleReconcile clears the daemon's stale claims on a workspace when it
// (re)starts: orphaned 'running' runs become stoppable again, worktrees
// wedged in 'initializing' by the dead process return to claimable 'pending',
// and cleanups that died mid-removal are re-queued. Runs before the daemon's
// first claim, so nothing it resets can be live.
func (m *Module) handleReconcile(w http.ResponseWriter, r *http.Request) {
	wsID := r.URL.Query().Get("workspace_id")
	daemonID := r.URL.Query().Get("daemon_id")
	if wsID == "" || daemonID == "" {
		writeErr(w, http.StatusBadRequest, "missing workspace_id or daemon_id")
		return
	}
	if !m.canAccess(r, wsID) {
		writeErr(w, http.StatusForbidden, "forbidden")
		return
	}
	if err := m.store.ReconcileDaemon(r.Context(), daemonID, wsID); err != nil {
		writeErr(w, http.StatusInternalServerError, "failed to reconcile")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleIssueStatus returns the readiness of an issue's worktrees so the agent
// task's eager checkout can wait-and-adopt (managed) or self-check-out (not).
func (m *Module) handleIssueStatus(w http.ResponseWriter, r *http.Request) {
	wsID := r.URL.Query().Get("workspace_id")
	if wsID == "" {
		writeErr(w, http.StatusBadRequest, "missing workspace_id")
		return
	}
	if !m.canAccess(r, wsID) {
		writeErr(w, http.StatusForbidden, "forbidden")
		return
	}
	out, err := m.store.IssueStatus(r.Context(), chi.URLParam(r, "issueId"), wsID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "failed to load status")
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (m *Module) handleReportStatus(w http.ResponseWriter, r *http.Request) {
	daemonID := r.URL.Query().Get("daemon_id")
	if daemonID == "" {
		writeErr(w, http.StatusBadRequest, "missing daemon_id")
		return
	}
	wt, err := m.store.Get(r.Context(), chi.URLParam(r, "worktreeId"))
	if err != nil {
		writeWorktreeErr(w, err)
		return
	}
	if !m.canAccess(r, wt.WorkspaceID) {
		writeErr(w, http.StatusForbidden, "forbidden")
		return
	}
	var rep shared.StatusReport
	if err := json.NewDecoder(r.Body).Decode(&rep); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	updated, err := m.store.ReportStatus(r.Context(), daemonID, wt.ID, rep)
	if err != nil {
		writeWorktreeErr(w, err)
		return
	}
	m.publish(updated.WorkspaceID, shared.EventWorktreeUpdated, shared.UpdatedEvent{IssueID: updated.IssueID, Worktree: updated})
	writeJSON(w, http.StatusOK, updated)
}

func (m *Module) handleRunLogs(w http.ResponseWriter, r *http.Request) {
	runTaskID := chi.URLParam(r, "runTaskId")
	var batch shared.LogBatch
	if err := json.NewDecoder(r.Body).Decode(&batch); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	wt, err := m.store.GetByRunTaskID(r.Context(), runTaskID)
	if err != nil {
		writeErr(w, http.StatusNotFound, "run not found")
		return
	}
	if !m.canAccess(r, wt.WorkspaceID) {
		writeErr(w, http.StatusForbidden, "forbidden")
		return
	}
	for _, ln := range batch.Lines {
		m.publish(wt.WorkspaceID, shared.EventWorktreeRunLog, shared.RunLogEvent{
			RunTaskID:  runTaskID,
			IssueID:    wt.IssueID,
			WorktreeID: wt.ID,
			Seq:        ln.Seq,
			Stream:     ln.Stream,
			Content:    m.deps.redact(ln.Content),
		})
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleReportFiles ingests a daemon's file-list scan for one worktree: store
// it and notify workspace clients (ids + digest only; the Project tab refetches
// the list over HTTP). Only the owning daemon of a live worktree is accepted,
// so a late scan can't resurrect data for a cleaned-up workspace.
func (m *Module) handleReportFiles(w http.ResponseWriter, r *http.Request) {
	daemonID := r.URL.Query().Get("daemon_id")
	if daemonID == "" {
		writeErr(w, http.StatusBadRequest, "missing daemon_id")
		return
	}
	wt, err := m.store.Get(r.Context(), chi.URLParam(r, "worktreeId"))
	if err != nil {
		writeWorktreeErr(w, err)
		return
	}
	if !m.canAccess(r, wt.WorkspaceID) {
		writeErr(w, http.StatusForbidden, "forbidden")
		return
	}
	if wt.OwnerDaemonID != daemonID || wt.Status == shared.StatusRemoved || wt.Status == shared.StatusCleaning {
		writeErr(w, http.StatusConflict, "worktree not accepting file reports")
		return
	}
	// A full list can be large (capped at 20k paths daemon-side); bound the body
	// so a misbehaving client can't stream unbounded JSON.
	r.Body = http.MaxBytesReader(w, r.Body, 8<<20)
	var rep shared.FileReport
	if err := json.NewDecoder(r.Body).Decode(&rep); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	if err := m.store.UpsertFiles(r.Context(), wt.ID, rep); err != nil {
		m.deps.log().Error("worktrees: upsert files failed", "error", err)
		writeErr(w, http.StatusInternalServerError, "failed to store files")
		return
	}
	m.publish(wt.WorkspaceID, shared.EventWorktreeFiles, shared.FilesUpdatedEvent{
		IssueID:    wt.IssueID,
		WorktreeID: wt.ID,
		Digest:     rep.Digest,
	})
	w.WriteHeader(http.StatusNoContent)
}

// handleReportChanges ingests a daemon's git-status scan for one worktree:
// store it and notify workspace clients (ids + digest only; the Changes tab
// refetches over HTTP). Only the owning daemon of a live worktree is accepted,
// so a late scan can't resurrect data for a cleaned-up workspace.
func (m *Module) handleReportChanges(w http.ResponseWriter, r *http.Request) {
	daemonID := r.URL.Query().Get("daemon_id")
	if daemonID == "" {
		writeErr(w, http.StatusBadRequest, "missing daemon_id")
		return
	}
	wt, err := m.store.Get(r.Context(), chi.URLParam(r, "worktreeId"))
	if err != nil {
		writeWorktreeErr(w, err)
		return
	}
	if !m.canAccess(r, wt.WorkspaceID) {
		writeErr(w, http.StatusForbidden, "forbidden")
		return
	}
	if wt.OwnerDaemonID != daemonID || wt.Status == shared.StatusRemoved || wt.Status == shared.StatusCleaning {
		writeErr(w, http.StatusConflict, "worktree not accepting change reports")
		return
	}
	// The list is capped at 5k entries daemon-side; bound the body so a
	// misbehaving client can't stream unbounded JSON.
	r.Body = http.MaxBytesReader(w, r.Body, 8<<20)
	var rep shared.ChangesReport
	if err := json.NewDecoder(r.Body).Decode(&rep); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	if err := m.store.UpsertChanges(r.Context(), wt.ID, rep); err != nil {
		m.deps.log().Error("worktrees: upsert changes failed", "error", err)
		writeErr(w, http.StatusInternalServerError, "failed to store changes")
		return
	}
	m.publish(wt.WorkspaceID, shared.EventWorktreeChanges, shared.ChangesUpdatedEvent{
		IssueID:    wt.IssueID,
		WorktreeID: wt.ID,
		Digest:     rep.Digest,
	})
	w.WriteHeader(http.StatusNoContent)
}

// ---- helpers ----

func (m *Module) principal(w http.ResponseWriter, r *http.Request) (Principal, bool) {
	if m.deps.Principal == nil {
		writeErr(w, http.StatusUnauthorized, "unauthorized")
		return Principal{}, false
	}
	p, ok := m.deps.Principal(r)
	if !ok || p.WorkspaceID == "" {
		writeErr(w, http.StatusUnauthorized, "unauthorized")
		return Principal{}, false
	}
	return p, true
}

func (m *Module) canAccess(r *http.Request, workspaceID string) bool {
	return m.deps.CanAccessWorkspace != nil && m.deps.CanAccessWorkspace(r, workspaceID)
}

// loadOwnedWorktree loads the path's worktree and verifies it belongs to the
// caller's workspace + issue (cross-workspace guard).
func (m *Module) loadOwnedWorktree(w http.ResponseWriter, r *http.Request, p Principal) (shared.Worktree, bool) {
	wt, err := m.store.Get(r.Context(), chi.URLParam(r, "worktreeId"))
	if err != nil {
		writeWorktreeErr(w, err)
		return shared.Worktree{}, false
	}
	if wt.WorkspaceID != p.WorkspaceID || wt.IssueID != chi.URLParam(r, "issueId") {
		writeErr(w, http.StatusNotFound, "worktree not found")
		return shared.Worktree{}, false
	}
	return wt, true
}

func (m *Module) publish(wsID, eventType string, payload any) {
	if m.deps.Publish != nil {
		m.deps.Publish(wsID, eventType, payload)
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func writeWorktreeErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrNotFound):
		writeErr(w, http.StatusNotFound, "worktree not found")
	case errors.Is(err, ErrNotReady):
		writeErr(w, http.StatusConflict, "worktree not ready")
	case errors.Is(err, ErrNoRunScript):
		writeErr(w, http.StatusConflict, "no such run script")
	case errors.Is(err, ErrNoSetupScript):
		writeErr(w, http.StatusConflict, "no setup script configured")
	case errors.Is(err, ErrDaemonOffline):
		writeErr(w, http.StatusConflict, "owning machine offline")
	case errors.Is(err, ErrAlreadyRunning):
		writeErr(w, http.StatusConflict, "run already in progress")
	case errors.Is(err, ErrTooManyTerminals):
		writeErr(w, http.StatusConflict, "too many open terminals")
	default:
		writeErr(w, http.StatusInternalServerError, "internal error")
	}
}

package serverside

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/multica-ai/multica/server/addons/worktrees/shared"
)

// UIRouter returns the member-authenticated routes. Mounted by the adapter at
// /api/worktree inside the RequireWorkspaceMember group.
func (m *Module) UIRouter() http.Handler {
	r := chi.NewRouter()
	r.Get("/issues/{issueId}", m.handleListWorktrees)
	r.Post("/issues/{issueId}/{worktreeId}/setup", m.handleSetup)
	r.Post("/issues/{issueId}/{worktreeId}/run", m.handleRun)
	r.Post("/issues/{issueId}/{worktreeId}/run/stop", m.handleStop)
	r.Get("/config", m.handleGetConfig)
	r.Put("/config", m.handlePutConfig)
	return r
}

// DaemonRouter returns the daemon-authenticated routes. Mounted by the adapter
// at /api/daemon/worktree inside the DaemonAuth group.
func (m *Module) DaemonRouter() http.Handler {
	r := chi.NewRouter()
	r.Get("/jobs", m.handlePollJobs)
	r.Post("/jobs/{worktreeId}/status", m.handleReportStatus)
	r.Post("/runs/{runTaskId}/logs", m.handleRunLogs)
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
	out, err := m.store.RequestRun(r.Context(), wt.ID)
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
	out, err := m.store.RequestStop(r.Context(), wt.ID)
	if err != nil {
		writeWorktreeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (m *Module) handleGetConfig(w http.ResponseWriter, r *http.Request) {
	p, ok := m.principal(w, r)
	if !ok {
		return
	}
	cfg, err := m.store.GetConfig(r.Context(), p.WorkspaceID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "failed to load config")
		return
	}
	writeJSON(w, http.StatusOK, cfg)
}

func (m *Module) handlePutConfig(w http.ResponseWriter, r *http.Request) {
	p, ok := m.principal(w, r)
	if !ok {
		return
	}
	if !p.CanManage() {
		writeErr(w, http.StatusForbidden, "insufficient permissions")
		return
	}
	var cfg shared.Config
	if err := json.NewDecoder(r.Body).Decode(&cfg); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	if err := m.store.PutConfig(r.Context(), p.WorkspaceID, cfg); err != nil {
		writeErr(w, http.StatusInternalServerError, "failed to save config")
		return
	}
	out, err := m.store.GetConfig(r.Context(), p.WorkspaceID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "failed to reload config")
		return
	}
	writeJSON(w, http.StatusOK, out)
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
	actions, err := m.store.ClaimActionJobs(r.Context(), daemonID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "failed to claim action jobs")
		return
	}
	writeJSON(w, http.StatusOK, shared.JobsResponse{Jobs: append(jobs, actions...)})
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
// caller's workspace (cross-workspace guard).
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
		writeErr(w, http.StatusConflict, "no run script configured")
	case errors.Is(err, ErrNoSetupScript):
		writeErr(w, http.StatusConflict, "no setup script configured")
	case errors.Is(err, ErrDaemonOffline):
		writeErr(w, http.StatusConflict, "owning machine offline")
	case errors.Is(err, ErrAlreadyRunning):
		writeErr(w, http.StatusConflict, "run already in progress")
	default:
		writeErr(w, http.StatusInternalServerError, "internal error")
	}
}

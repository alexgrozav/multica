package serverside

import (
	"context"
	"encoding/json"
	"errors"
	"path"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/multica-ai/multica/server/addons/worktrees/shared"
)

// Typed errors so handlers can map to HTTP statuses.
var (
	ErrNotFound       = errors.New("worktree not found")
	ErrNotReady       = errors.New("worktree not ready")
	ErrNoRunScript    = errors.New("no such run script")
	ErrNoSetupScript  = errors.New("no setup script configured")
	ErrDaemonOffline  = errors.New("owning machine offline")
	ErrAlreadyRunning = errors.New("run already in progress")
)

// Store is the add-on's data-access layer over a pgx pool.
type Store struct{ db DB }

func NewStore(db DB) *Store { return &Store{db: db} }

const worktreeSelect = `
SELECT iw.id::text, iw.issue_id::text, iw.issue_identifier, iw.workspace_id::text,
       iw.repo_url, iw.owner_daemon_id, iw.path, iw.branch, iw.status,
       iw.setup_status, iw.setup_task_id::text, iw.has_setup, iw.has_cleanup,
       iw.last_error, iw.created_at, iw.updated_at
FROM issue_worktree iw
`

func scanWorktree(row pgx.Row) (shared.Worktree, error) {
	var w shared.Worktree
	var setupTaskID *string
	var createdAt, updatedAt time.Time
	if err := row.Scan(
		&w.ID, &w.IssueID, &w.Identifier, &w.WorkspaceID, &w.RepoURL, &w.OwnerDaemonID,
		&w.Path, &w.Branch, &w.Status, &w.SetupStatus, &setupTaskID, &w.HasSetup, &w.HasCleanup,
		&w.LastError, &createdAt, &updatedAt,
	); err != nil {
		return shared.Worktree{}, err
	}
	if setupTaskID != nil {
		w.SetupTaskID = *setupTaskID
	}
	w.WorkingDir = workingDirOf(w.Path)
	w.Runs = []shared.RunScript{}
	w.CreatedAt = createdAt.UTC().Format(time.RFC3339)
	w.UpdatedAt = updatedAt.UTC().Format(time.RFC3339)
	return w, nil
}

// workingDirOf is the per-issue working directory: the parent that holds every
// repo checkout (worktrees/<shortIssue>/), derived from a repo worktree path.
// Daemon paths are POSIX (macOS/Linux), so path.Dir is the correct separator.
func workingDirOf(p string) string {
	if p == "" {
		return ""
	}
	return path.Dir(p)
}

func collectWorktrees(rows pgx.Rows) ([]shared.Worktree, error) {
	defer rows.Close()
	out := []shared.Worktree{}
	for rows.Next() {
		w, err := scanWorktree(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

// attachRuns loads the worktree_run rows for the given worktrees and attaches
// them, so the UI sees one Run/Stop control + log tab per named script.
func (s *Store) attachRuns(ctx context.Context, wts []shared.Worktree) error {
	if len(wts) == 0 {
		return nil
	}
	ids := make([]string, len(wts))
	for i, w := range wts {
		ids[i] = w.ID
	}
	rows, err := s.db.Query(ctx,
		`SELECT worktree_id::text, name, run_status, run_task_id::text, last_error
		 FROM worktree_run WHERE worktree_id::text = ANY($1) ORDER BY name`, ids)
	if err != nil {
		return err
	}
	defer rows.Close()
	byID := map[string][]shared.RunScript{}
	for rows.Next() {
		var wtID string
		var rs shared.RunScript
		var runTaskID *string
		if err := rows.Scan(&wtID, &rs.Name, &rs.Status, &runTaskID, &rs.LastError); err != nil {
			return err
		}
		if runTaskID != nil {
			rs.RunTaskID = *runTaskID
		}
		byID[wtID] = append(byID[wtID], rs)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for i := range wts {
		if runs := byID[wts[i].ID]; runs != nil {
			wts[i].Runs = runs
		}
	}
	return nil
}

// WorkspaceRepoURLs reads the host workspace.repos JSONB and returns repo URLs.
func (s *Store) WorkspaceRepoURLs(ctx context.Context, wsID string) ([]string, error) {
	var raw []byte
	err := s.db.QueryRow(ctx, `SELECT repos FROM workspace WHERE id=$1`, wsID).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var entries []struct {
		URL string `json:"url"`
	}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &entries); err != nil {
			return nil, err
		}
	}
	urls := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.URL != "" {
			urls = append(urls, e.URL)
		}
	}
	return urls, nil
}

// ---- Worktree rows ----

// CreateWorktreeRow inserts a pending row (idempotent on issue+repo) and returns
// it. The human identifier (e.g. PRO-11) is stamped so the daemon can name the
// worktree branch after it; a later call backfills the identifier if it was
// unknown at first insert.
func (s *Store) CreateWorktreeRow(ctx context.Context, issueID, identifier, wsID, repoURL string) (shared.Worktree, error) {
	// ON CONFLICT backfills the identifier when it was unknown, AND re-arms a
	// tombstoned ('removed') row back to a clean pending state — so reopening a
	// previously-closed, still-assigned issue recreates its workspace. Live rows
	// (any non-removed status) are left untouched.
	if _, err := s.db.Exec(ctx,
		`INSERT INTO issue_worktree (issue_id, issue_identifier, workspace_id, repo_url)
		 VALUES ($1,$2,$3,$4)
		 ON CONFLICT (issue_id, repo_url) DO UPDATE SET
		   issue_identifier = CASE WHEN issue_worktree.issue_identifier='' AND EXCLUDED.issue_identifier<>''
		                          THEN EXCLUDED.issue_identifier ELSE issue_worktree.issue_identifier END,
		   status          = CASE WHEN issue_worktree.status='removed' THEN 'pending'   ELSE issue_worktree.status END,
		   owner_daemon_id = CASE WHEN issue_worktree.status='removed' THEN ''          ELSE issue_worktree.owner_daemon_id END,
		   setup_task_id   = CASE WHEN issue_worktree.status='removed' THEN NULL        ELSE issue_worktree.setup_task_id END,
		   path            = CASE WHEN issue_worktree.status='removed' THEN ''          ELSE issue_worktree.path END,
		   branch          = CASE WHEN issue_worktree.status='removed' THEN ''          ELSE issue_worktree.branch END,
		   setup_status    = CASE WHEN issue_worktree.status='removed' THEN 'none'      ELSE issue_worktree.setup_status END,
		   has_setup       = CASE WHEN issue_worktree.status='removed' THEN false       ELSE issue_worktree.has_setup END,
		   has_cleanup     = CASE WHEN issue_worktree.status='removed' THEN false       ELSE issue_worktree.has_cleanup END,
		   pending_action  = CASE WHEN issue_worktree.status='removed' THEN ''          ELSE issue_worktree.pending_action END,
		   last_error      = CASE WHEN issue_worktree.status='removed' THEN ''          ELSE issue_worktree.last_error END,
		   updated_at      = now()`,
		issueID, identifier, wsID, repoURL); err != nil {
		return shared.Worktree{}, err
	}
	return s.getBy(ctx, worktreeSelect+` WHERE iw.issue_id=$1 AND iw.repo_url=$2`, issueID, repoURL)
}

// getBy scans a single worktree from a query and attaches its runs.
func (s *Store) getBy(ctx context.Context, query string, args ...any) (shared.Worktree, error) {
	w, err := scanWorktree(s.db.QueryRow(ctx, query, args...))
	if errors.Is(err, pgx.ErrNoRows) {
		return shared.Worktree{}, ErrNotFound
	}
	if err != nil {
		return shared.Worktree{}, err
	}
	one := []shared.Worktree{w}
	if err := s.attachRuns(ctx, one); err != nil {
		return shared.Worktree{}, err
	}
	return one[0], nil
}

// ListByIssue returns the (non-removed) worktrees for an issue, scoped to the
// caller's workspace, each with its named run scripts attached.
func (s *Store) ListByIssue(ctx context.Context, issueID, wsID string) ([]shared.Worktree, error) {
	rows, err := s.db.Query(ctx, worktreeSelect+` WHERE iw.issue_id=$1 AND iw.workspace_id=$2 AND iw.status <> 'removed' ORDER BY iw.repo_url`, issueID, wsID)
	if err != nil {
		return nil, err
	}
	wts, err := collectWorktrees(rows)
	if err != nil {
		return nil, err
	}
	if err := s.attachRuns(ctx, wts); err != nil {
		return nil, err
	}
	return wts, nil
}

// Get returns a single worktree by id (with runs).
func (s *Store) Get(ctx context.Context, id string) (shared.Worktree, error) {
	return s.getBy(ctx, worktreeSelect+` WHERE iw.id=$1`, id)
}

// GetByRunTaskID resolves a worktree from a log-channel id, matching either the
// setup channel (issue_worktree.setup_task_id) or any named run channel
// (worktree_run.run_task_id) so streamed Setup and Run output route back home.
func (s *Store) GetByRunTaskID(ctx context.Context, runTaskID string) (shared.Worktree, error) {
	return s.getBy(ctx, worktreeSelect+
		` WHERE iw.setup_task_id=$1 OR iw.id IN (SELECT worktree_id FROM worktree_run WHERE run_task_id=$1)`, runTaskID)
}

// MarkCleanupForIssue transitions an issue's worktrees on close (done/cancelled):
// ready/error -> cleaning (queued for the cleanup script), pending/initializing
// -> removed (nothing on disk yet). Returns the affected rows for events.
func (s *Store) MarkCleanupForIssue(ctx context.Context, issueID string) ([]shared.Worktree, error) {
	if _, err := s.db.Exec(ctx,
		`UPDATE issue_worktree SET status='cleaning', pending_action='cleanup', updated_at=now()
		 WHERE issue_id=$1 AND status IN ('ready','error')`, issueID); err != nil {
		return nil, err
	}
	if _, err := s.db.Exec(ctx,
		`UPDATE issue_worktree SET status='removed', pending_action='', updated_at=now()
		 WHERE issue_id=$1 AND status IN ('pending','initializing')`, issueID); err != nil {
		return nil, err
	}
	rows, err := s.db.Query(ctx, worktreeSelect+` WHERE iw.issue_id=$1 ORDER BY iw.repo_url`, issueID)
	if err != nil {
		return nil, err
	}
	wts, err := collectWorktrees(rows)
	if err != nil {
		return nil, err
	}
	if err := s.attachRuns(ctx, wts); err != nil {
		return nil, err
	}
	return wts, nil
}

// runExists reports whether a named run script is defined on a worktree.
func (s *Store) runExists(ctx context.Context, worktreeID, name string) (bool, error) {
	var ok bool
	err := s.db.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM worktree_run WHERE worktree_id=$1 AND name=$2)`, worktreeID, name).Scan(&ok)
	return ok, err
}

// anyRunActive reports whether any named run script is currently running.
func (s *Store) anyRunActive(ctx context.Context, worktreeID string) (bool, error) {
	var ok bool
	err := s.db.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM worktree_run WHERE worktree_id=$1 AND run_status='running')`, worktreeID).Scan(&ok)
	return ok, err
}

// RequestRun validates and arms an on-demand run of a named script, allocating a
// fresh run channel for that name.
func (s *Store) RequestRun(ctx context.Context, id, name string) (shared.Worktree, error) {
	w, err := s.Get(ctx, id)
	if err != nil {
		return shared.Worktree{}, err
	}
	if w.Status != shared.StatusReady {
		return shared.Worktree{}, ErrNotReady
	}
	if w.SetupStatus == shared.ScriptRunning {
		return shared.Worktree{}, ErrAlreadyRunning
	}
	exists, err := s.runExists(ctx, id, name)
	if err != nil {
		return shared.Worktree{}, err
	}
	if !exists {
		return shared.Worktree{}, ErrNoRunScript
	}
	online, err := s.DaemonOnline(ctx, w.WorkspaceID, w.OwnerDaemonID)
	if err != nil {
		return shared.Worktree{}, err
	}
	if !online {
		return shared.Worktree{}, ErrDaemonOffline
	}
	runTaskID := uuid.NewString()
	ct, err := s.db.Exec(ctx,
		`UPDATE worktree_run SET run_status='running', pending_action='run',
		        run_task_id=$3, last_error='', updated_at=now()
		 WHERE worktree_id=$1 AND name=$2 AND run_status<>'running'`, id, name, runTaskID)
	if err != nil {
		return shared.Worktree{}, err
	}
	if ct.RowsAffected() == 0 {
		return shared.Worktree{}, ErrAlreadyRunning
	}
	return s.Get(ctx, id)
}

// RequestStop queues a stop for a named run (idempotent).
func (s *Store) RequestStop(ctx context.Context, id, name string) (shared.Worktree, error) {
	if _, err := s.db.Exec(ctx,
		`UPDATE worktree_run SET pending_action='stop', updated_at=now()
		 WHERE worktree_id=$1 AND name=$2 AND run_status='running'`, id, name); err != nil {
		return shared.Worktree{}, err
	}
	return s.Get(ctx, id)
}

// RequestSetup re-runs the Setup script in an existing worktree (ready or
// error), streaming its output to a fresh setup channel.
func (s *Store) RequestSetup(ctx context.Context, id string) (shared.Worktree, error) {
	w, err := s.Get(ctx, id)
	if err != nil {
		return shared.Worktree{}, err
	}
	if w.Status != shared.StatusReady && w.Status != shared.StatusError {
		return shared.Worktree{}, ErrNotReady
	}
	if !w.HasSetup {
		return shared.Worktree{}, ErrNoSetupScript
	}
	if w.SetupStatus == shared.ScriptRunning {
		return shared.Worktree{}, ErrAlreadyRunning
	}
	active, err := s.anyRunActive(ctx, id)
	if err != nil {
		return shared.Worktree{}, err
	}
	if active {
		return shared.Worktree{}, ErrAlreadyRunning
	}
	online, err := s.DaemonOnline(ctx, w.WorkspaceID, w.OwnerDaemonID)
	if err != nil {
		return shared.Worktree{}, err
	}
	if !online {
		return shared.Worktree{}, ErrDaemonOffline
	}
	setupTaskID := uuid.NewString()
	ct, err := s.db.Exec(ctx,
		`UPDATE issue_worktree SET setup_status='running', pending_action='setup',
		        setup_task_id=$2, last_error='', updated_at=now()
		 WHERE id=$1 AND setup_status<>'running'`, id, setupTaskID)
	if err != nil {
		return shared.Worktree{}, err
	}
	if ct.RowsAffected() == 0 {
		return shared.Worktree{}, ErrAlreadyRunning
	}
	return s.Get(ctx, id)
}

// RequestOpen queues an "open the issue working directory in <target>" job for
// the owning (online) daemon to execute. The working directory is the per-issue
// parent that holds every repo checkout, so any one ready worktree carries the
// job — the daemon resolves the parent itself from the issue id. pending_open is
// independent of the single-slot pending_action, so an open never clashes with a
// queued setup/cleanup and is fire-and-forget (no status is reported back).
func (s *Store) RequestOpen(ctx context.Context, issueID, wsID, target string) (shared.Worktree, error) {
	wt, err := s.getBy(ctx, worktreeSelect+
		` WHERE iw.issue_id=$1 AND iw.workspace_id=$2 AND iw.status='ready' ORDER BY iw.repo_url LIMIT 1`, issueID, wsID)
	if errors.Is(err, ErrNotFound) {
		return shared.Worktree{}, ErrNotReady // no checked-out worktree yet
	}
	if err != nil {
		return shared.Worktree{}, err
	}
	online, err := s.DaemonOnline(ctx, wt.WorkspaceID, wt.OwnerDaemonID)
	if err != nil {
		return shared.Worktree{}, err
	}
	if !online {
		return shared.Worktree{}, ErrDaemonOffline
	}
	if _, err := s.db.Exec(ctx,
		`UPDATE issue_worktree SET pending_open=$2, updated_at=now() WHERE id=$1`, wt.ID, target); err != nil {
		return shared.Worktree{}, err
	}
	return wt, nil
}

// ---- Daemon coordination ----

// TouchDaemon records daemon liveness for the workspace.
func (s *Store) TouchDaemon(ctx context.Context, wsID, daemonID string) error {
	if daemonID == "" {
		return nil
	}
	_, err := s.db.Exec(ctx,
		`INSERT INTO worktree_daemon_seen (workspace_id, daemon_id, last_seen_at)
		 VALUES ($1,$2,now())
		 ON CONFLICT (workspace_id, daemon_id) DO UPDATE SET last_seen_at=now()`,
		wsID, daemonID)
	return err
}

// DaemonOnline reports whether the daemon has polled recently.
func (s *Store) DaemonOnline(ctx context.Context, wsID, daemonID string) (bool, error) {
	if daemonID == "" {
		return false, nil
	}
	var online bool
	err := s.db.QueryRow(ctx,
		`SELECT (last_seen_at > now() - interval '90 seconds')
		 FROM worktree_daemon_seen WHERE workspace_id=$1 AND daemon_id=$2`, wsID, daemonID).Scan(&online)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	return online, err
}

// ResetRunningRuns clears stale 'running' named-run rows for a daemon's
// worktrees in a workspace back to 'stopped'. Called once per workspace when a
// daemon (re)starts its poll loop: at that instant the daemon owns no live runs,
// so any 'running' row is an orphan from a previous process (whose OS process
// died with the daemon). Without this, a Stop would no-op forever and a re-run
// would be refused (run already in progress).
func (s *Store) ResetRunningRuns(ctx context.Context, daemonID, wsID string) error {
	_, err := s.db.Exec(ctx,
		`UPDATE worktree_run wr SET run_status='stopped', pending_action='', updated_at=now()
		 FROM issue_worktree iw
		 WHERE wr.worktree_id=iw.id AND iw.workspace_id=$1 AND iw.owner_daemon_id=$2
		   AND wr.run_status='running'`, wsID, daemonID)
	return err
}

// IssueStatus returns the readiness snapshot for an issue's worktrees, used by
// the agent task's wait-and-adopt eager checkout. Managed is false (no rows)
// when the add-on is not tracking the issue → the caller self-checks-out.
func (s *Store) IssueStatus(ctx context.Context, issueID, wsID string) (shared.IssueStatusResponse, error) {
	out := shared.IssueStatusResponse{Worktrees: []shared.IssueWorktreeStatus{}}
	rows, err := s.db.Query(ctx,
		`SELECT repo_url, status, setup_status, path FROM issue_worktree
		 WHERE issue_id=$1 AND workspace_id=$2 AND status <> 'removed' ORDER BY repo_url`, issueID, wsID)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var e shared.IssueWorktreeStatus
		if err := rows.Scan(&e.RepoURL, &e.Status, &e.SetupStatus, &e.Path); err != nil {
			return out, err
		}
		out.Worktrees = append(out.Worktrees, e)
	}
	if err := rows.Err(); err != nil {
		return out, err
	}
	out.Managed = len(out.Worktrees) > 0
	return out, nil
}

// ClaimInitJobs atomically claims unclaimed pending worktrees in the workspace
// for this daemon and returns init jobs (carrying the identifier → branch name).
func (s *Store) ClaimInitJobs(ctx context.Context, daemonID, wsID string) ([]shared.Job, error) {
	rows, err := s.db.Query(ctx,
		`WITH claimed AS (
		   SELECT id FROM issue_worktree
		   WHERE workspace_id=$2 AND status='pending' AND owner_daemon_id=''
		   FOR UPDATE SKIP LOCKED
		 )
		 UPDATE issue_worktree iw SET owner_daemon_id=$1, status='initializing',
		        setup_task_id=gen_random_uuid(), updated_at=now()
		 FROM claimed WHERE iw.id=claimed.id
		 RETURNING iw.id::text, iw.issue_id::text, iw.issue_identifier, iw.repo_url, iw.setup_task_id::text`, daemonID, wsID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	jobs := []shared.Job{}
	for rows.Next() {
		j := shared.Job{Kind: shared.JobInit, WorkspaceID: wsID}
		if err := rows.Scan(&j.WorktreeID, &j.IssueID, &j.Identifier, &j.RepoURL, &j.SetupTaskID); err != nil {
			return nil, err
		}
		jobs = append(jobs, j)
	}
	return jobs, rows.Err()
}

// ClaimWorktreeActionJobs atomically claims pending setup/cleanup actions on the
// daemon's worktrees (run/stop live on worktree_run, claimed separately).
func (s *Store) ClaimWorktreeActionJobs(ctx context.Context, daemonID string) ([]shared.Job, error) {
	rows, err := s.db.Query(ctx,
		`WITH claimed AS (
		   SELECT id, pending_action AS act FROM issue_worktree
		   WHERE owner_daemon_id=$1 AND pending_action IN ('setup','cleanup')
		   FOR UPDATE SKIP LOCKED
		 )
		 UPDATE issue_worktree iw SET pending_action='', updated_at=now()
		 FROM claimed WHERE iw.id=claimed.id
		 RETURNING iw.id::text, iw.issue_id::text, iw.issue_identifier, iw.workspace_id::text,
		           iw.repo_url, iw.setup_task_id::text, claimed.act`,
		daemonID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	jobs := []shared.Job{}
	for rows.Next() {
		var act string
		var setupTaskID *string
		j := shared.Job{}
		if err := rows.Scan(&j.WorktreeID, &j.IssueID, &j.Identifier, &j.WorkspaceID, &j.RepoURL, &setupTaskID, &act); err != nil {
			return nil, err
		}
		if setupTaskID != nil {
			j.SetupTaskID = *setupTaskID
		}
		switch act {
		case shared.ActionSetup:
			j.Kind = shared.JobSetup
		case shared.ActionCleanup:
			j.Kind = shared.JobCleanup
		default:
			continue
		}
		jobs = append(jobs, j)
	}
	return jobs, rows.Err()
}

// ClaimRunJobs atomically claims pending run/stop actions on the daemon's named
// run rows and returns the corresponding jobs (with ScriptName + run channel).
func (s *Store) ClaimRunJobs(ctx context.Context, daemonID string) ([]shared.Job, error) {
	rows, err := s.db.Query(ctx,
		`WITH claimed AS (
		   SELECT wr.worktree_id, wr.name, wr.pending_action AS act
		   FROM worktree_run wr JOIN issue_worktree iw ON iw.id = wr.worktree_id
		   WHERE iw.owner_daemon_id=$1 AND wr.pending_action IN ('run','stop')
		   FOR UPDATE OF wr SKIP LOCKED
		 )
		 UPDATE worktree_run wr SET pending_action='', updated_at=now()
		 FROM claimed, issue_worktree iw
		 WHERE wr.worktree_id=claimed.worktree_id AND wr.name=claimed.name AND iw.id=wr.worktree_id
		 RETURNING wr.worktree_id::text, wr.name, wr.run_task_id::text, claimed.act,
		           iw.issue_id::text, iw.issue_identifier, iw.workspace_id::text, iw.repo_url`,
		daemonID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	jobs := []shared.Job{}
	for rows.Next() {
		var act string
		var runTaskID *string
		j := shared.Job{}
		if err := rows.Scan(&j.WorktreeID, &j.ScriptName, &runTaskID, &act, &j.IssueID, &j.Identifier, &j.WorkspaceID, &j.RepoURL); err != nil {
			return nil, err
		}
		if runTaskID != nil {
			j.RunTaskID = *runTaskID
		}
		switch act {
		case shared.ActionRun:
			j.Kind = shared.JobRun
		case shared.ActionStop:
			j.Kind = shared.JobStop
		default:
			continue
		}
		jobs = append(jobs, j)
	}
	return jobs, rows.Err()
}

// ClaimOpenJobs atomically claims pending "open working dir" requests on the
// daemon's worktrees and returns them as JobOpen jobs carrying the target app.
func (s *Store) ClaimOpenJobs(ctx context.Context, daemonID string) ([]shared.Job, error) {
	rows, err := s.db.Query(ctx,
		`WITH claimed AS (
		   SELECT id, pending_open AS tgt FROM issue_worktree
		   WHERE owner_daemon_id=$1 AND pending_open <> ''
		   FOR UPDATE SKIP LOCKED
		 )
		 UPDATE issue_worktree iw SET pending_open='', updated_at=now()
		 FROM claimed WHERE iw.id=claimed.id
		 RETURNING iw.id::text, iw.issue_id::text, iw.workspace_id::text, iw.repo_url, claimed.tgt`,
		daemonID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	jobs := []shared.Job{}
	for rows.Next() {
		j := shared.Job{Kind: shared.JobOpen}
		if err := rows.Scan(&j.WorktreeID, &j.IssueID, &j.WorkspaceID, &j.RepoURL, &j.OpenTarget); err != nil {
			return nil, err
		}
		jobs = append(jobs, j)
	}
	return jobs, rows.Err()
}

// ReportStatus applies a daemon status report to the row and returns it.
func (s *Store) ReportStatus(ctx context.Context, daemonID, worktreeID string, rep shared.StatusReport) (shared.Worktree, error) {
	switch rep.Kind {
	case shared.JobInit:
		status := rep.Status
		if status == "" {
			status = shared.StatusReady
		}
		// Guard against a closed/closing row: if the issue was closed while the
		// daemon was checking out (MarkCleanupForIssue moved the row to 'removed'
		// or 'cleaning'), a late init report must NOT resurrect it — nor recreate
		// its run rows. Any other status (initializing → ready, or a re-report) is
		// applied normally.
		ct, err := s.db.Exec(ctx,
			`UPDATE issue_worktree SET status=$2, path=$3, branch=$4, setup_status=$5,
			        has_setup=$6, has_cleanup=$7, last_error=$8, updated_at=now()
			 WHERE id=$1 AND owner_daemon_id=$9 AND status NOT IN ('removed','cleaning')`,
			worktreeID, status, rep.Path, rep.Branch, nz(rep.SetupStatus, shared.ScriptNone),
			rep.HasSetup, rep.HasCleanup, rep.Error, daemonID)
		if err != nil {
			return shared.Worktree{}, err
		}
		if ct.RowsAffected() == 0 {
			return s.Get(ctx, worktreeID)
		}
		if err := s.syncRunScripts(ctx, worktreeID, rep.RunScripts); err != nil {
			return shared.Worktree{}, err
		}
	case shared.JobSetup:
		if _, err := s.db.Exec(ctx,
			`UPDATE issue_worktree SET setup_status=$2, last_error=$3, updated_at=now()
			 WHERE id=$1 AND owner_daemon_id=$4`,
			worktreeID, nz(rep.SetupStatus, shared.ScriptSucceeded), rep.Error, daemonID); err != nil {
			return shared.Worktree{}, err
		}
	case shared.JobRun, shared.JobStop:
		runStatus := rep.RunStatus
		if runStatus == "" {
			runStatus = shared.RunSucceeded
		}
		if _, err := s.db.Exec(ctx,
			`UPDATE worktree_run wr SET run_status=$3, last_error=$4, updated_at=now()
			 FROM issue_worktree iw
			 WHERE wr.worktree_id=iw.id AND iw.id=$1 AND iw.owner_daemon_id=$2 AND wr.name=$5`,
			worktreeID, daemonID, runStatus, rep.Error, rep.ScriptName); err != nil {
			return shared.Worktree{}, err
		}
	case shared.JobCleanup:
		if _, err := s.db.Exec(ctx,
			`UPDATE issue_worktree SET status='removed', last_error=$2, updated_at=now()
			 WHERE id=$1 AND owner_daemon_id=$3`,
			worktreeID, rep.Error, daemonID); err != nil {
			return shared.Worktree{}, err
		}
		_, _ = s.db.Exec(ctx, `DELETE FROM worktree_run WHERE worktree_id=$1`, worktreeID)
		_, _ = s.db.Exec(ctx, `DELETE FROM worktree_files WHERE worktree_id=$1`, worktreeID)
		_, _ = s.db.Exec(ctx, `DELETE FROM worktree_changes WHERE worktree_id=$1`, worktreeID)
	}
	return s.Get(ctx, worktreeID)
}

// ---- Worktree file lists (Project tab) ----

// FileScanTargets returns the daemon's ready worktrees in a workspace, each
// with the file-list + changes digests the server currently stores, so the
// daemon's poll loop can rescan cheaply and report only real changes.
func (s *Store) FileScanTargets(ctx context.Context, daemonID, wsID string) ([]shared.FileScanTarget, error) {
	rows, err := s.db.Query(ctx,
		`SELECT iw.id::text, iw.issue_id::text, iw.workspace_id::text, iw.repo_url,
		        COALESCE(wf.digest, ''), COALESCE(wc.digest, '')
		 FROM issue_worktree iw
		 LEFT JOIN worktree_files wf ON wf.worktree_id = iw.id
		 LEFT JOIN worktree_changes wc ON wc.worktree_id = iw.id
		 WHERE iw.owner_daemon_id=$1 AND iw.workspace_id=$2 AND iw.status='ready'
		 ORDER BY iw.repo_url`, daemonID, wsID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []shared.FileScanTarget{}
	for rows.Next() {
		var t shared.FileScanTarget
		if err := rows.Scan(&t.WorktreeID, &t.IssueID, &t.WorkspaceID, &t.RepoURL, &t.Digest, &t.ChangesDigest); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// UpsertFiles stores the daemon-reported file list of a worktree.
func (s *Store) UpsertFiles(ctx context.Context, worktreeID string, rep shared.FileReport) error {
	paths := rep.Paths
	if paths == nil {
		paths = []string{}
	}
	b, err := json.Marshal(paths)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(ctx,
		`INSERT INTO worktree_files (worktree_id, digest, paths, truncated, updated_at)
		 VALUES ($1,$2,$3,$4,now())
		 ON CONFLICT (worktree_id) DO UPDATE SET
		   digest=EXCLUDED.digest, paths=EXCLUDED.paths, truncated=EXCLUDED.truncated, updated_at=now()`,
		worktreeID, rep.Digest, b, rep.Truncated)
	return err
}

// FilesByIssue returns the stored file list of each live worktree of an issue,
// scoped to the caller's workspace.
func (s *Store) FilesByIssue(ctx context.Context, issueID, wsID string) ([]shared.WorktreeFiles, error) {
	rows, err := s.db.Query(ctx,
		`SELECT iw.id::text, iw.issue_id::text, iw.repo_url, wf.digest, wf.paths, wf.truncated, wf.updated_at
		 FROM issue_worktree iw
		 JOIN worktree_files wf ON wf.worktree_id = iw.id
		 WHERE iw.issue_id=$1 AND iw.workspace_id=$2 AND iw.status <> 'removed'
		 ORDER BY iw.repo_url`, issueID, wsID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []shared.WorktreeFiles{}
	for rows.Next() {
		var f shared.WorktreeFiles
		var raw []byte
		var updatedAt time.Time
		if err := rows.Scan(&f.WorktreeID, &f.IssueID, &f.RepoURL, &f.Digest, &raw, &f.Truncated, &updatedAt); err != nil {
			return nil, err
		}
		f.Paths = []string{}
		if len(raw) > 0 {
			if err := json.Unmarshal(raw, &f.Paths); err != nil {
				return nil, err
			}
		}
		f.UpdatedAt = updatedAt.UTC().Format(time.RFC3339)
		out = append(out, f)
	}
	return out, rows.Err()
}

// UpsertChanges stores the daemon-reported git status of a worktree.
func (s *Store) UpsertChanges(ctx context.Context, worktreeID string, rep shared.ChangesReport) error {
	files := rep.Files
	if files == nil {
		files = []shared.ChangeEntry{}
	}
	b, err := json.Marshal(files)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(ctx,
		`INSERT INTO worktree_changes (worktree_id, digest, base, files, truncated, updated_at)
		 VALUES ($1,$2,$3,$4,$5,now())
		 ON CONFLICT (worktree_id) DO UPDATE SET
		   digest=EXCLUDED.digest, base=EXCLUDED.base, files=EXCLUDED.files,
		   truncated=EXCLUDED.truncated, updated_at=now()`,
		worktreeID, rep.Digest, rep.Base, b, rep.Truncated)
	return err
}

// ChangesByIssue returns the stored git status of each live worktree of an
// issue, scoped to the caller's workspace.
func (s *Store) ChangesByIssue(ctx context.Context, issueID, wsID string) ([]shared.WorktreeChanges, error) {
	rows, err := s.db.Query(ctx,
		`SELECT iw.id::text, iw.issue_id::text, iw.repo_url, wc.digest, wc.base, wc.files, wc.truncated, wc.updated_at
		 FROM issue_worktree iw
		 JOIN worktree_changes wc ON wc.worktree_id = iw.id
		 WHERE iw.issue_id=$1 AND iw.workspace_id=$2 AND iw.status <> 'removed'
		 ORDER BY iw.repo_url`, issueID, wsID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []shared.WorktreeChanges{}
	for rows.Next() {
		var c shared.WorktreeChanges
		var raw []byte
		var updatedAt time.Time
		if err := rows.Scan(&c.WorktreeID, &c.IssueID, &c.RepoURL, &c.Digest, &c.Base, &raw, &c.Truncated, &updatedAt); err != nil {
			return nil, err
		}
		c.Files = []shared.ChangeEntry{}
		if len(raw) > 0 {
			if err := json.Unmarshal(raw, &c.Files); err != nil {
				return nil, err
			}
		}
		c.UpdatedAt = updatedAt.UTC().Format(time.RFC3339)
		out = append(out, c)
	}
	return out, rows.Err()
}

// syncRunScripts reconciles a worktree's worktree_run rows to the daemon's
// discovered set of run-script names: adds new ones (idle), drops removed ones,
// preserving the status of names that still exist.
func (s *Store) syncRunScripts(ctx context.Context, worktreeID string, names []string) error {
	for _, name := range names {
		if _, err := s.db.Exec(ctx,
			`INSERT INTO worktree_run (worktree_id, name) VALUES ($1,$2)
			 ON CONFLICT (worktree_id, name) DO NOTHING`, worktreeID, name); err != nil {
			return err
		}
	}
	if names == nil {
		names = []string{}
	}
	_, err := s.db.Exec(ctx, `DELETE FROM worktree_run WHERE worktree_id=$1 AND name <> ALL($2)`, worktreeID, names)
	return err
}

func nz(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

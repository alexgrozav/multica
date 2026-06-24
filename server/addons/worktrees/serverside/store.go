package serverside

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/multica-ai/multica/server/addons/worktrees/shared"
)

// Typed errors so handlers can map to HTTP statuses.
var (
	ErrNotFound       = errors.New("worktree not found")
	ErrNotReady       = errors.New("worktree not ready")
	ErrNoRunScript    = errors.New("no run script configured")
	ErrNoSetupScript  = errors.New("no setup script configured")
	ErrDaemonOffline  = errors.New("owning machine offline")
	ErrAlreadyRunning = errors.New("run already in progress")
)

// Store is the add-on's data-access layer over a pgx pool.
type Store struct{ db DB }

func NewStore(db DB) *Store { return &Store{db: db} }

const worktreeSelect = `
SELECT iw.id::text, iw.issue_id::text, iw.workspace_id::text, iw.repo_url,
       iw.owner_daemon_id, iw.path, iw.branch, iw.status, iw.setup_status,
       iw.run_status, iw.run_task_id::text, iw.last_error,
       iw.created_at, iw.updated_at,
       (COALESCE(s.run, '') <> '') AS has_run_script,
       (COALESCE(s.setup, '') <> '') AS has_setup_script
FROM issue_worktree iw
LEFT JOIN worktree_repo_script s
  ON s.workspace_id = iw.workspace_id AND s.repo_url = iw.repo_url
`

func scanWorktree(row pgx.Row) (shared.Worktree, error) {
	var w shared.Worktree
	var runTaskID *string
	var createdAt, updatedAt time.Time
	if err := row.Scan(
		&w.ID, &w.IssueID, &w.WorkspaceID, &w.RepoURL, &w.OwnerDaemonID,
		&w.Path, &w.Branch, &w.Status, &w.SetupStatus, &w.RunStatus,
		&runTaskID, &w.LastError, &createdAt, &updatedAt, &w.HasRunScript, &w.HasSetupScript,
	); err != nil {
		return shared.Worktree{}, err
	}
	if runTaskID != nil {
		w.RunTaskID = *runTaskID
	}
	w.CreatedAt = createdAt.UTC().Format(time.RFC3339)
	w.UpdatedAt = updatedAt.UTC().Format(time.RFC3339)
	return w, nil
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

// ---- Config / settings ----

// AutoInit reports whether auto-init is enabled for the workspace.
func (s *Store) AutoInit(ctx context.Context, wsID string) (bool, error) {
	var enabled bool
	err := s.db.QueryRow(ctx, `SELECT auto_init FROM worktree_settings WHERE workspace_id=$1`, wsID).Scan(&enabled)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	return enabled, err
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

// GetConfig returns the per-workspace toggle + per-repo scripts.
func (s *Store) GetConfig(ctx context.Context, wsID string) (shared.Config, error) {
	cfg := shared.Config{Repos: []shared.RepoScript{}}
	auto, err := s.AutoInit(ctx, wsID)
	if err != nil {
		return cfg, err
	}
	cfg.AutoInit = auto

	rows, err := s.db.Query(ctx, `SELECT repo_url, setup, run, cleanup FROM worktree_repo_script WHERE workspace_id=$1 ORDER BY repo_url`, wsID)
	if err != nil {
		return cfg, err
	}
	defer rows.Close()
	for rows.Next() {
		var rs shared.RepoScript
		if err := rows.Scan(&rs.RepoURL, &rs.Setup, &rs.Run, &rs.Cleanup); err != nil {
			return cfg, err
		}
		cfg.Repos = append(cfg.Repos, rs)
	}
	return cfg, rows.Err()
}

// PutConfig upserts the toggle and per-repo scripts, dropping scripts for repos
// no longer present.
func (s *Store) PutConfig(ctx context.Context, wsID string, cfg shared.Config) error {
	if _, err := s.db.Exec(ctx,
		`INSERT INTO worktree_settings (workspace_id, auto_init, updated_at)
		 VALUES ($1,$2,now())
		 ON CONFLICT (workspace_id) DO UPDATE SET auto_init=EXCLUDED.auto_init, updated_at=now()`,
		wsID, cfg.AutoInit); err != nil {
		return err
	}

	keep := make([]string, 0, len(cfg.Repos))
	for _, r := range cfg.Repos {
		keep = append(keep, r.RepoURL)
		if _, err := s.db.Exec(ctx,
			`INSERT INTO worktree_repo_script (workspace_id, repo_url, setup, run, cleanup, updated_at)
			 VALUES ($1,$2,$3,$4,$5,now())
			 ON CONFLICT (workspace_id, repo_url)
			 DO UPDATE SET setup=EXCLUDED.setup, run=EXCLUDED.run, cleanup=EXCLUDED.cleanup, updated_at=now()`,
			wsID, r.RepoURL, r.Setup, r.Run, r.Cleanup); err != nil {
			return err
		}
	}
	_, err := s.db.Exec(ctx, `DELETE FROM worktree_repo_script WHERE workspace_id=$1 AND repo_url <> ALL($2)`, wsID, keep)
	return err
}

func (s *Store) scripts(ctx context.Context, wsID, repoURL string) (setup, run, cleanup string, err error) {
	err = s.db.QueryRow(ctx, `SELECT setup, run, cleanup FROM worktree_repo_script WHERE workspace_id=$1 AND repo_url=$2`, wsID, repoURL).
		Scan(&setup, &run, &cleanup)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", "", "", nil
	}
	return setup, run, cleanup, err
}

// GetRepoScript returns the configured scripts for a repo (empty when none).
func (s *Store) GetRepoScript(ctx context.Context, wsID, repoURL string) (shared.RepoScript, error) {
	setup, run, cleanup, err := s.scripts(ctx, wsID, repoURL)
	if err != nil {
		return shared.RepoScript{}, err
	}
	return shared.RepoScript{RepoURL: repoURL, Setup: setup, Run: run, Cleanup: cleanup}, nil
}

// ---- Worktree rows ----

// CreateWorktreeRow inserts a pending row (idempotent on issue+repo) and returns it.
func (s *Store) CreateWorktreeRow(ctx context.Context, issueID, wsID, repoURL string) (shared.Worktree, error) {
	if _, err := s.db.Exec(ctx,
		`INSERT INTO issue_worktree (issue_id, workspace_id, repo_url)
		 VALUES ($1,$2,$3) ON CONFLICT (issue_id, repo_url) DO NOTHING`,
		issueID, wsID, repoURL); err != nil {
		return shared.Worktree{}, err
	}
	return scanWorktree(s.db.QueryRow(ctx, worktreeSelect+` WHERE iw.issue_id=$1 AND iw.repo_url=$2`, issueID, repoURL))
}

// ListByIssue returns the (non-removed) worktrees for an issue, scoped to the
// caller's workspace.
func (s *Store) ListByIssue(ctx context.Context, issueID, wsID string) ([]shared.Worktree, error) {
	rows, err := s.db.Query(ctx, worktreeSelect+` WHERE iw.issue_id=$1 AND iw.workspace_id=$2 AND iw.status <> 'removed' ORDER BY iw.repo_url`, issueID, wsID)
	if err != nil {
		return nil, err
	}
	return collectWorktrees(rows)
}

// Get returns a single worktree by id.
func (s *Store) Get(ctx context.Context, id string) (shared.Worktree, error) {
	w, err := scanWorktree(s.db.QueryRow(ctx, worktreeSelect+` WHERE iw.id=$1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return shared.Worktree{}, ErrNotFound
	}
	return w, err
}

// GetByRunTaskID resolves a worktree from its active run channel id.
func (s *Store) GetByRunTaskID(ctx context.Context, runTaskID string) (shared.Worktree, error) {
	w, err := scanWorktree(s.db.QueryRow(ctx, worktreeSelect+` WHERE iw.run_task_id=$1`, runTaskID))
	if errors.Is(err, pgx.ErrNoRows) {
		return shared.Worktree{}, ErrNotFound
	}
	return w, err
}

// MarkCleanupForIssue transitions an issue's worktrees on the move to "done":
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
	return collectWorktrees(rows)
}

// RequestRun validates and arms an on-demand run, allocating a fresh run channel.
func (s *Store) RequestRun(ctx context.Context, id string) (shared.Worktree, error) {
	w, err := s.Get(ctx, id)
	if err != nil {
		return shared.Worktree{}, err
	}
	if w.Status != shared.StatusReady {
		return shared.Worktree{}, ErrNotReady
	}
	if !w.HasRunScript {
		return shared.Worktree{}, ErrNoRunScript
	}
	if w.RunStatus == shared.RunRunning || w.SetupStatus == shared.ScriptRunning {
		return shared.Worktree{}, ErrAlreadyRunning
	}
	online, err := s.DaemonOnline(ctx, w.WorkspaceID, w.OwnerDaemonID)
	if err != nil {
		return shared.Worktree{}, err
	}
	if !online {
		return shared.Worktree{}, ErrDaemonOffline
	}
	// A data-modifying CTE's writes are NOT visible to a sibling SELECT of the
	// same table in one statement, so update-then-Get in two statements.
	runTaskID := uuid.NewString()
	var updatedID string
	err = s.db.QueryRow(ctx,
		`UPDATE issue_worktree SET run_status='running', pending_action='run',
		        run_task_id=$2, last_error='', updated_at=now()
		 WHERE id=$1 AND run_status<>'running' RETURNING id::text`, id, runTaskID).Scan(&updatedID)
	if errors.Is(err, pgx.ErrNoRows) {
		return shared.Worktree{}, ErrAlreadyRunning
	}
	if err != nil {
		return shared.Worktree{}, err
	}
	return s.Get(ctx, updatedID)
}

// RequestSetup re-runs the Setup script in an existing worktree (ready or
// error), streaming its output to a fresh run channel. Gated like RequestRun.
func (s *Store) RequestSetup(ctx context.Context, id string) (shared.Worktree, error) {
	w, err := s.Get(ctx, id)
	if err != nil {
		return shared.Worktree{}, err
	}
	if w.Status != shared.StatusReady && w.Status != shared.StatusError {
		return shared.Worktree{}, ErrNotReady
	}
	if !w.HasSetupScript {
		return shared.Worktree{}, ErrNoSetupScript
	}
	if w.RunStatus == shared.RunRunning || w.SetupStatus == shared.ScriptRunning {
		return shared.Worktree{}, ErrAlreadyRunning
	}
	online, err := s.DaemonOnline(ctx, w.WorkspaceID, w.OwnerDaemonID)
	if err != nil {
		return shared.Worktree{}, err
	}
	if !online {
		return shared.Worktree{}, ErrDaemonOffline
	}
	runTaskID := uuid.NewString()
	var updatedID string
	err = s.db.QueryRow(ctx,
		`UPDATE issue_worktree SET setup_status='running', pending_action='setup',
		        run_task_id=$2, last_error='', updated_at=now()
		 WHERE id=$1 AND setup_status<>'running' AND run_status<>'running' RETURNING id::text`, id, runTaskID).Scan(&updatedID)
	if errors.Is(err, pgx.ErrNoRows) {
		return shared.Worktree{}, ErrAlreadyRunning
	}
	if err != nil {
		return shared.Worktree{}, err
	}
	return s.Get(ctx, updatedID)
}

// RequestStop queues a stop for a running worktree (idempotent).
func (s *Store) RequestStop(ctx context.Context, id string) (shared.Worktree, error) {
	if _, err := s.db.Exec(ctx,
		`UPDATE issue_worktree SET pending_action='stop', updated_at=now()
		 WHERE id=$1 AND run_status='running'`, id); err != nil {
		return shared.Worktree{}, err
	}
	return s.Get(ctx, id)
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

// ClaimInitJobs atomically claims unclaimed pending worktrees in the workspace
// for this daemon and returns init jobs (with the Setup script).
func (s *Store) ClaimInitJobs(ctx context.Context, daemonID, wsID string) ([]shared.Job, error) {
	rows, err := s.db.Query(ctx,
		`WITH claimed AS (
		   SELECT id FROM issue_worktree
		   WHERE workspace_id=$2 AND status='pending' AND owner_daemon_id=''
		   FOR UPDATE SKIP LOCKED
		 )
		 UPDATE issue_worktree iw SET owner_daemon_id=$1, status='initializing', updated_at=now()
		 FROM claimed WHERE iw.id=claimed.id
		 RETURNING iw.id::text, iw.issue_id::text, iw.repo_url`, daemonID, wsID)
	if err != nil {
		return nil, err
	}
	type ref struct{ id, issueID, repoURL string }
	var refs []ref
	for rows.Next() {
		var r ref
		if err := rows.Scan(&r.id, &r.issueID, &r.repoURL); err != nil {
			rows.Close()
			return nil, err
		}
		refs = append(refs, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	jobs := make([]shared.Job, 0, len(refs))
	for _, r := range refs {
		setup, _, _, err := s.scripts(ctx, wsID, r.repoURL)
		if err != nil {
			return nil, err
		}
		jobs = append(jobs, shared.Job{Kind: shared.JobInit, WorktreeID: r.id, IssueID: r.issueID, WorkspaceID: wsID, RepoURL: r.repoURL, Setup: setup})
	}
	return jobs, nil
}

// ClaimActionJobs atomically claims pending run/stop/cleanup actions for the
// daemon's worktrees and returns the corresponding jobs.
func (s *Store) ClaimActionJobs(ctx context.Context, daemonID string) ([]shared.Job, error) {
	rows, err := s.db.Query(ctx,
		`WITH claimed AS (
		   SELECT id, pending_action AS act FROM issue_worktree
		   WHERE owner_daemon_id=$1 AND pending_action <> ''
		   FOR UPDATE SKIP LOCKED
		 )
		 UPDATE issue_worktree iw SET pending_action='', updated_at=now()
		 FROM claimed WHERE iw.id=claimed.id
		 RETURNING iw.id::text, iw.issue_id::text, iw.workspace_id::text, iw.repo_url, iw.run_task_id::text, claimed.act`,
		daemonID)
	if err != nil {
		return nil, err
	}
	type ref struct {
		id, issueID, wsID, repoURL, act string
		runTaskID                       *string
	}
	var refs []ref
	for rows.Next() {
		var r ref
		if err := rows.Scan(&r.id, &r.issueID, &r.wsID, &r.repoURL, &r.runTaskID, &r.act); err != nil {
			rows.Close()
			return nil, err
		}
		refs = append(refs, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	jobs := make([]shared.Job, 0, len(refs))
	for _, r := range refs {
		job := shared.Job{WorktreeID: r.id, IssueID: r.issueID, WorkspaceID: r.wsID, RepoURL: r.repoURL}
		if r.runTaskID != nil {
			job.RunTaskID = *r.runTaskID
		}
		setup, run, cleanup, err := s.scripts(ctx, r.wsID, r.repoURL)
		if err != nil {
			return nil, err
		}
		switch r.act {
		case shared.ActionSetup:
			job.Kind, job.Setup = shared.JobSetup, setup
		case shared.ActionRun:
			job.Kind, job.Run = shared.JobRun, run
		case shared.ActionStop:
			job.Kind = shared.JobStop
		case shared.ActionCleanup:
			job.Kind, job.Cleanup = shared.JobCleanup, cleanup
		default:
			continue
		}
		jobs = append(jobs, job)
	}
	return jobs, nil
}

// ReportStatus applies a daemon status report to the row and returns it.
func (s *Store) ReportStatus(ctx context.Context, daemonID, worktreeID string, rep shared.StatusReport) (shared.Worktree, error) {
	switch rep.Kind {
	case shared.JobInit:
		status := rep.Status
		if status == "" {
			status = shared.StatusReady
		}
		if _, err := s.db.Exec(ctx,
			`UPDATE issue_worktree SET status=$2, path=$3, branch=$4, setup_status=$5, last_error=$6, updated_at=now()
			 WHERE id=$1 AND owner_daemon_id=$7`,
			worktreeID, status, rep.Path, rep.Branch, nz(rep.SetupStatus, shared.ScriptNone), rep.Error, daemonID); err != nil {
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
			`UPDATE issue_worktree SET run_status=$2, last_error=$3, updated_at=now()
			 WHERE id=$1 AND owner_daemon_id=$4`,
			worktreeID, runStatus, rep.Error, daemonID); err != nil {
			return shared.Worktree{}, err
		}
	case shared.JobCleanup:
		if _, err := s.db.Exec(ctx,
			`UPDATE issue_worktree SET status='removed', last_error=$2, updated_at=now()
			 WHERE id=$1 AND owner_daemon_id=$3`,
			worktreeID, rep.Error, daemonID); err != nil {
			return shared.Worktree{}, err
		}
	}
	return s.Get(ctx, worktreeID)
}

func nz(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

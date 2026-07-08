package serverside

import "context"

// schemaDDL is idempotent: safe to run on every server boot. The add-on owns
// these tables and evolves them here (new columns via ALTER ... ADD COLUMN IF
// NOT EXISTS) instead of touching the host migration sequence or sqlc.
//
// Scripts are NOT stored server-side: each repo defines them in a committed
// multica.json the daemon reads. The row only caches what the daemon discovered
// (has_setup / has_cleanup + the per-name worktree_run rows) so the UI can
// render affordances without seeing script bodies.
const schemaDDL = `
CREATE TABLE IF NOT EXISTS issue_worktree (
    id               UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    issue_id         UUID NOT NULL,
    issue_identifier TEXT NOT NULL DEFAULT '',
    workspace_id     UUID NOT NULL,
    repo_url         TEXT NOT NULL,
    owner_daemon_id  TEXT NOT NULL DEFAULT '',
    path             TEXT NOT NULL DEFAULT '',
    branch           TEXT NOT NULL DEFAULT '',
    requested_branch TEXT NOT NULL DEFAULT '',
    status           TEXT NOT NULL DEFAULT 'pending',
    setup_status     TEXT NOT NULL DEFAULT 'none',
    setup_task_id    UUID,
    has_setup        BOOLEAN NOT NULL DEFAULT false,
    has_cleanup      BOOLEAN NOT NULL DEFAULT false,
    pending_action   TEXT NOT NULL DEFAULT '',
    pending_open     TEXT NOT NULL DEFAULT '',
    last_error       TEXT NOT NULL DEFAULT '',
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX IF NOT EXISTS uq_issue_worktree ON issue_worktree(issue_id, repo_url);
CREATE INDEX IF NOT EXISTS idx_issue_worktree_owner ON issue_worktree(owner_daemon_id) WHERE status <> 'removed';
CREATE INDEX IF NOT EXISTS idx_issue_worktree_setuptask ON issue_worktree(setup_task_id);

-- Backfill columns onto older dev databases created by an earlier iteration
-- (no-op on fresh installs the CREATE above already covers).
ALTER TABLE issue_worktree ADD COLUMN IF NOT EXISTS issue_identifier TEXT NOT NULL DEFAULT '';
ALTER TABLE issue_worktree ADD COLUMN IF NOT EXISTS setup_task_id UUID;
ALTER TABLE issue_worktree ADD COLUMN IF NOT EXISTS has_setup BOOLEAN NOT NULL DEFAULT false;
ALTER TABLE issue_worktree ADD COLUMN IF NOT EXISTS has_cleanup BOOLEAN NOT NULL DEFAULT false;
ALTER TABLE issue_worktree ADD COLUMN IF NOT EXISTS pending_open TEXT NOT NULL DEFAULT '';
-- requested_branch is the user-chosen branch from issue.branch_name, copied at
-- row creation so daemon jobs carry it ('' = derive from the identifier).
ALTER TABLE issue_worktree ADD COLUMN IF NOT EXISTS requested_branch TEXT NOT NULL DEFAULT '';
CREATE INDEX IF NOT EXISTS idx_issue_worktree_pending_open ON issue_worktree(owner_daemon_id) WHERE pending_open <> '';

-- Per-named-run state. A worktree exposes multiple run scripts (dev, start, …)
-- from its multica.json; each has its own status, log channel, and stop control.
CREATE TABLE IF NOT EXISTS worktree_run (
    worktree_id    UUID NOT NULL,
    name           TEXT NOT NULL,
    run_task_id    UUID,
    run_status     TEXT NOT NULL DEFAULT 'idle',
    pending_action TEXT NOT NULL DEFAULT '',
    last_error     TEXT NOT NULL DEFAULT '',
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (worktree_id, name)
);
CREATE INDEX IF NOT EXISTS idx_worktree_run_task ON worktree_run(run_task_id);
CREATE INDEX IF NOT EXISTS idx_worktree_run_pending ON worktree_run(worktree_id) WHERE pending_action <> '';

-- Daemon liveness, used to gate on-demand Run when the owning machine is offline.
CREATE TABLE IF NOT EXISTS worktree_daemon_seen (
    workspace_id UUID NOT NULL,
    daemon_id    TEXT NOT NULL,
    last_seen_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (workspace_id, daemon_id)
);

-- Latest file list per worktree (the Project tab's tree). One row per worktree,
-- replaced whenever the daemon's scan digest changes; deleted with the worktree.
-- Kept out of issue_worktree so frequent status updates never rewrite the list.
CREATE TABLE IF NOT EXISTS worktree_files (
    worktree_id UUID PRIMARY KEY,
    digest      TEXT NOT NULL DEFAULT '',
    paths       JSONB NOT NULL DEFAULT '[]',
    truncated   BOOLEAN NOT NULL DEFAULT false,
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Latest git status per worktree (the Changes tab): changed files vs the
-- branch base, as scanned by the daemon. Same lifecycle as worktree_files.
CREATE TABLE IF NOT EXISTS worktree_changes (
    worktree_id UUID PRIMARY KEY,
    digest      TEXT NOT NULL DEFAULT '',
    base        TEXT NOT NULL DEFAULT '',
    files       JSONB NOT NULL DEFAULT '[]',
    truncated   BOOLEAN NOT NULL DEFAULT false,
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
`

// EnsureSchema creates the add-on's tables if they don't exist. Called once at
// server startup by the adapter.
func EnsureSchema(ctx context.Context, db DB) error {
	_, err := db.Exec(ctx, schemaDDL)
	return err
}

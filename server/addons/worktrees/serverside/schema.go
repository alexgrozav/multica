package serverside

import "context"

// schemaDDL is idempotent: safe to run on every server boot. The add-on owns
// these tables and evolves them here (future columns via ALTER ... ADD COLUMN
// IF NOT EXISTS) instead of touching the host migration sequence or sqlc.
const schemaDDL = `
CREATE TABLE IF NOT EXISTS worktree_settings (
    workspace_id UUID PRIMARY KEY,
    auto_init    BOOLEAN NOT NULL DEFAULT false,
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS worktree_repo_script (
    workspace_id UUID NOT NULL,
    repo_url     TEXT NOT NULL,
    setup        TEXT NOT NULL DEFAULT '',
    run          TEXT NOT NULL DEFAULT '',
    cleanup      TEXT NOT NULL DEFAULT '',
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (workspace_id, repo_url)
);

CREATE TABLE IF NOT EXISTS issue_worktree (
    id             UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    issue_id       UUID NOT NULL,
    workspace_id   UUID NOT NULL,
    repo_url       TEXT NOT NULL,
    owner_daemon_id TEXT NOT NULL DEFAULT '',
    path           TEXT NOT NULL DEFAULT '',
    branch         TEXT NOT NULL DEFAULT '',
    status         TEXT NOT NULL DEFAULT 'pending',
    setup_status   TEXT NOT NULL DEFAULT 'none',
    run_status     TEXT NOT NULL DEFAULT 'idle',
    run_task_id    UUID,
    setup_task_id  UUID,
    pending_action TEXT NOT NULL DEFAULT '',
    last_error     TEXT NOT NULL DEFAULT '',
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX IF NOT EXISTS uq_issue_worktree ON issue_worktree(issue_id, repo_url);
CREATE INDEX IF NOT EXISTS idx_issue_worktree_owner ON issue_worktree(owner_daemon_id) WHERE status <> 'removed';
CREATE INDEX IF NOT EXISTS idx_issue_worktree_runtask ON issue_worktree(run_task_id);

-- setup_task_id is the setup-log channel, distinct from run_task_id so the
-- Setup and Run log streams don't clobber each other (added after initial
-- release; ALTER is a no-op on fresh installs created by the CREATE above).
ALTER TABLE issue_worktree ADD COLUMN IF NOT EXISTS setup_task_id UUID;
CREATE INDEX IF NOT EXISTS idx_issue_worktree_setuptask ON issue_worktree(setup_task_id);

-- Daemon liveness, used to gate on-demand Run when the owning machine is offline.
CREATE TABLE IF NOT EXISTS worktree_daemon_seen (
    workspace_id UUID NOT NULL,
    daemon_id    TEXT NOT NULL,
    last_seen_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (workspace_id, daemon_id)
);
`

// EnsureSchema creates the add-on's tables if they don't exist. Called once at
// server startup by the adapter.
func EnsureSchema(ctx context.Context, db DB) error {
	_, err := db.Exec(ctx, schemaDDL)
	return err
}

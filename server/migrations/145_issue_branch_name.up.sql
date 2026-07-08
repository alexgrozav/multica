-- Per-issue custom branch name: the git branch the issue's worktrees are
-- checked out on, set once at creation. Empty means "derive from the issue
-- identifier" (e.g. PRO-11), which is the long-standing default. When set,
-- the worktrees add-on checks out this branch instead — reusing it if it
-- already exists locally or on the remote, creating it from the default
-- branch otherwise.
ALTER TABLE issue ADD COLUMN branch_name TEXT NOT NULL DEFAULT '';

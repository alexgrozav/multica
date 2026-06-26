package daemonside

import "path/filepath"

// These helpers are the SINGLE source of truth for the per-issue worktree
// location + branch. Both the add-on's own worktree creation (handleInit) and
// the daemon's agent-task WorkDir override resolve through them, so the agent
// works in the EXACT tree the sidebar shows. (Critical: shortID here is 12
// chars; execenv/repocache use 8 — never derive the per-issue path with those.)

// IssueWorktreeParent is the per-issue directory that holds one <repo> worktree
// subdir per repo. In the unified model this IS the agent task's working
// directory (env.WorkDir/env.RootDir).
func IssueWorktreeParent(workspacesRoot, workspaceID, issueID string) string {
	return filepath.Join(workspacesRoot, workspaceID, ".issue-worktrees", shortID(issueID))
}

// RepoDirName is the subdir name a repo's worktree gets within the parent.
func RepoDirName(repoURL string) string { return repoName(repoURL) }

// IssueWorktreePath is the worktree dir for one (issue, repo).
func IssueWorktreePath(workspacesRoot, workspaceID, issueID, repoURL string) string {
	return filepath.Join(IssueWorktreeParent(workspacesRoot, workspaceID, issueID), RepoDirName(repoURL))
}

// IssueBranch is the branch a repo's per-issue worktree is checked out on.
// issue/* (NOT agent/*) so the daemon GC's agent-branch sweep never deletes it.
func IssueBranch(issueID, repoURL string) string {
	return "issue/" + shortID(issueID) + "/" + repoName(repoURL)
}

// IsWorktree reports whether path is already an existing git worktree.
func IsWorktree(path string) bool { return isWorktree(path) }

// EnsureWorktreeAt creates (or reuses, without resetting) the per-issue worktree
// at worktreePath on the given branch from the bare clone. Exported so the
// daemon's agent-task path creates the worktree IDENTICALLY to handleInit
// (same path, same issue/* branch, idempotent) rather than via repocache's
// agent/* + destructive-reuse path.
func EnsureWorktreeAt(bare, worktreePath, branch string) (string, error) {
	return ensureWorktree(bare, worktreePath, branch)
}

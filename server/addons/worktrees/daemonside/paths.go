package daemonside

import (
	"path/filepath"
	"strings"
)

// These helpers are the SINGLE source of truth for the per-issue worktree
// location + branch. Both the add-on's own worktree creation (handleInit) and
// the daemon's agent-task WorkDir override resolve through them, so the agent
// works in the EXACT tree the sidebar shows.
//
// The DIRECTORY is keyed by the issue UUID (stable, collision-free) so the
// daemon's WorkDirOverride needs no identifier plumbing; the BRANCH is named
// after the human identifier (e.g. PRO-11) per the product requirement.

// IssueWorktreeParent is the per-issue directory that holds one <repo> worktree
// subdir per repo. In the unified model this IS the agent task's working
// directory (env.WorkDir/env.RootDir).
func IssueWorktreeParent(workspacesRoot, workspaceID, issueID string) string {
	return filepath.Join(workspacesRoot, workspaceID, "worktrees", shortID(issueID))
}

// RepoDirName is the subdir name a repo's worktree gets within the parent.
func RepoDirName(repoURL string) string { return repoName(repoURL) }

// IssueWorktreePath is the worktree dir for one (issue, repo).
func IssueWorktreePath(workspacesRoot, workspaceID, issueID, repoURL string) string {
	return filepath.Join(IssueWorktreeParent(workspacesRoot, workspaceID, issueID), RepoDirName(repoURL))
}

// IssueBranch is the branch a repo's per-issue worktree is checked out on: the
// human identifier (e.g. PRO-11), sanitized to a valid git ref. It is NOT under
// agent/*, so the daemon GC's agent-branch sweep never touches it. Falls back to
// a short issue id when no identifier is available.
func IssueBranch(identifier, issueID string) string {
	if b := safeRef(identifier); b != "" {
		return b
	}
	return "issue-" + shortID(issueID)
}

// safeRef maps an identifier to a valid, collision-free git branch ref. Keeps
// ref-safe characters and collapses the rest to '-', trimming stray separators.
func safeRef(s string) string {
	s = strings.TrimSpace(s)
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	return strings.Trim(b.String(), "-.")
}

// IsWorktree reports whether path is already an existing git worktree.
func IsWorktree(path string) bool { return isWorktree(path) }

// EnsureWorktreeAt creates (or reuses, without resetting) the per-issue worktree
// at worktreePath on the given branch from the bare clone. Exported so a caller
// outside the poll loop can create the worktree identically (same path, same
// branch, idempotent).
func EnsureWorktreeAt(bare, worktreePath, branch string) (string, error) {
	return ensureWorktree(bare, worktreePath, branch)
}

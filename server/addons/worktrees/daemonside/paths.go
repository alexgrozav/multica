package daemonside

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/multica-ai/multica/server/addons/worktrees/shared"
)

// These helpers are the SINGLE source of truth for the per-issue worktree
// location + branch. Both the add-on's own worktree creation (handleInit) and
// the daemon's agent-task WorkDir override resolve through them, so the agent
// works in the EXACT tree the sidebar shows.
//
// The DIRECTORY is keyed by the issue UUID (stable, collision-free) so the
// daemon's WorkDirOverride needs no identifier plumbing; the BRANCH is named
// after the human identifier (e.g. PRO-11) per the product requirement, unless
// the issue pinned a custom branch at create (Job.Branch), which wins.

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

// IssueBranch is the branch a repo's per-issue worktree is checked out on when
// no custom branch was requested. Thin alias over the shared implementation —
// the server derives the same name for agent task payloads (issue_branch), so
// the two sides can never drift.
func IssueBranch(identifier, issueID string) string {
	return shared.IssueBranch(identifier, issueID)
}

// IsWorktree reports whether path is already an existing git worktree.
func IsWorktree(path string) bool { return isWorktree(path) }

// EnsureWorktreeAt creates (or reuses, without resetting) the per-issue worktree
// at worktreePath on the given branch from the bare clone. Exported so a caller
// outside the poll loop can create the worktree identically (same path, same
// branch, idempotent). reuseExisting must be true when branch is a
// user-requested name (existing local/remote branch is continued, never reset)
// and false for derived identifier branches (leftovers restart from base).
func EnsureWorktreeAt(bare, worktreePath, branch string, reuseExisting bool) (string, error) {
	return ensureWorktree(bare, worktreePath, branch, reuseExisting)
}

// CurrentBranch returns the branch a worktree has checked out, or "" for a
// detached HEAD. Used by the daemon's repo-checkout adoption path to report
// the managed worktree's actual branch back to the agent.
func CurrentBranch(worktreePath string) (string, error) {
	out, err := runGit(worktreePath, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return "", fmt.Errorf("git rev-parse --abbrev-ref HEAD: %s: %w", strings.TrimSpace(out), err)
	}
	b := strings.TrimSpace(out)
	if b == "HEAD" { // detached
		return "", nil
	}
	return b, nil
}

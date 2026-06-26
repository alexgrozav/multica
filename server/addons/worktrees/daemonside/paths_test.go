package daemonside

import (
	"path/filepath"
	"testing"
)

// TestIssueWorktreePathIdentity locks in the guarantee the unified model relies
// on: the daemon's WorkDir override (IssueWorktreeParent + RepoDirName) resolves
// to the EXACT path the add-on's worktreePath(job) uses — so the agent works in
// the same tree the sidebar's Run/Setup/Cleanup operate on.
func TestIssueWorktreePathIdentity(t *testing.T) {
	root := "/wsroot"
	ws := "ws-1"
	issue := "11112222-3333-4444-5555-666677778888"
	repo := "https://github.com/foo/bar.git"

	parent := IssueWorktreeParent(root, ws, issue)
	if want := filepath.Join(root, ws, ".issue-worktrees", "111122223333"); parent != want {
		t.Fatalf("IssueWorktreeParent = %q, want %q", parent, want)
	}
	// the identity the daemon override depends on:
	if got, want := IssueWorktreePath(root, ws, issue, repo), filepath.Join(parent, RepoDirName(repo)); got != want {
		t.Fatalf("IssueWorktreePath = %q, want parent/repo %q", got, want)
	}
	if got := RepoDirName(repo); got != "bar" {
		t.Fatalf("RepoDirName = %q, want bar", got)
	}
	if got := IssueBranch(issue, repo); got != "issue/111122223333/bar" {
		t.Fatalf("IssueBranch = %q, want issue/111122223333/bar", got)
	}
}

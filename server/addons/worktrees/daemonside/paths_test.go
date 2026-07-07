package daemonside

import (
	"path/filepath"
	"testing"
)

// TestIssueWorktreePathIdentity locks in the guarantee the unified model relies
// on: the daemon's WorkDir override (IssueWorktreeParent + RepoDirName) resolves
// to the EXACT path the add-on's worktreePath(job) uses — so the agent works in
// the same tree the sidebar's Run/Setup/Cleanup operate on. The DIR is keyed by
// the issue UUID; the BRANCH is the human identifier.
func TestIssueWorktreePathIdentity(t *testing.T) {
	root := "/wsroot"
	ws := "ws-1"
	issue := "11112222-3333-4444-5555-666677778888"
	repo := "https://github.com/foo/bar.git"

	parent := IssueWorktreeParent(root, ws, issue)
	if want := filepath.Join(root, ws, "worktrees", "111122223333"); parent != want {
		t.Fatalf("IssueWorktreeParent = %q, want %q", parent, want)
	}
	if got, want := IssueWorktreePath(root, ws, issue, repo), filepath.Join(parent, RepoDirName(repo)); got != want {
		t.Fatalf("IssueWorktreePath = %q, want parent/repo %q", got, want)
	}
	if got := RepoDirName(repo); got != "bar" {
		t.Fatalf("RepoDirName = %q, want bar", got)
	}
}

func TestIssueBranchFromIdentifier(t *testing.T) {
	issue := "11112222-3333-4444-5555-666677778888"
	cases := []struct {
		identifier, want string
	}{
		{"PRO-11", "PRO-11"},
		{"MUL-3617", "MUL-3617"},
		{"  spaced-9  ", "spaced-9"},
		{"weird~name:42", "weird-name-42"},
		{"", "issue-111122223333"}, // no identifier → stable fallback
	}
	for _, c := range cases {
		if got := IssueBranch(c.identifier, issue); got != c.want {
			t.Fatalf("IssueBranch(%q) = %q, want %q", c.identifier, got, c.want)
		}
	}
}

//go:build !windows

package daemonside

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/addons/worktrees/shared"
)

// addOriginBranch creates a branch with one extra commit on the origin repo and
// returns its head SHA, leaving origin back on main.
func addOriginBranch(t *testing.T, origin, branch string) string {
	t.Helper()
	mustGit(t, origin, "checkout", "-b", branch)
	if err := os.WriteFile(filepath.Join(origin, "feature.txt"), []byte(branch+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustGit(t, origin, "add", ".")
	mustGit(t, origin, "commit", "-m", "work on "+branch)
	head := strings.TrimSpace(mustGitOut(t, origin, "rev-parse", "HEAD"))
	mustGit(t, origin, "checkout", "main")
	return head
}

// TestCustomBranchReusedAndKeptE2E covers the user-requested branch lifecycle:
// a Job carrying Branch checks the worktree out ON that branch AT its existing
// head (never reset to base), and cleanup removes the worktree but KEEPS the
// branch — unlike derived identifier branches, which die with the issue.
func TestCustomBranchReusedAndKeptE2E(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	origin := initOriginRepo(t, "")
	wantHead := addOriginBranch(t, origin, "feature/keep")

	fake := newFakeServer()
	defer fake.srv.Close()

	root := t.TempDir()
	m := New(Deps{
		ServerBaseURL:  fake.srv.URL,
		WorkspacesRoot: root,
		DaemonID:       "daemon-custom",
		TokenProvider:  func() string { return "test-token" },
	})

	const ws = "ws-custom"
	const issue = "aaaa1111-2222-3333-4444-555555555555"
	job := shared.Job{
		Kind: shared.JobInit, WorktreeID: "wt-custom", IssueID: issue, Identifier: "PRO-9",
		Branch: "feature/keep", WorkspaceID: ws, RepoURL: origin,
	}
	m.handleInit(context.Background(), job)

	wtPath := filepath.Join(root, ws, "worktrees", shortID(issue), repoName(origin))
	if !isWorktree(wtPath) {
		t.Fatalf("worktree not created at %s", wtPath)
	}
	if got := strings.TrimSpace(mustGitOut(t, wtPath, "rev-parse", "--abbrev-ref", "HEAD")); got != "feature/keep" {
		t.Fatalf("worktree branch = %q, want feature/keep (custom branch ignored?)", got)
	}
	if got := strings.TrimSpace(mustGitOut(t, wtPath, "rev-parse", "HEAD")); got != wantHead {
		t.Fatalf("worktree HEAD = %q, want %q (existing branch was reset to base?)", got, wantHead)
	}
	if st := fake.lastStatus(t); st.Status != shared.StatusReady || st.Branch != "feature/keep" {
		t.Fatalf("init status report = %+v, want ready on feature/keep", st)
	}

	// Cleanup: worktree goes, the user's branch stays in the bare cache.
	job.Kind = shared.JobCleanup
	m.handleCleanup(context.Background(), job)
	if isWorktree(wtPath) {
		t.Fatalf("worktree still present after cleanup: %s", wtPath)
	}
	bare := filepath.Join(root, ".worktrees-cache", ws, repoName(origin)+".git")
	if !refExists(bare, "refs/heads/feature/keep") {
		t.Fatalf("cleanup deleted the user-requested branch — custom branches must be kept")
	}
	if st := fake.lastStatus(t); st.Kind != shared.JobCleanup || st.Status != shared.StatusRemoved {
		t.Fatalf("cleanup status report = %+v, want removed", st)
	}
}

// TestCustomBranchFromRemoteTracking covers the repo-cache layout: the branch
// exists only as refs/remotes/origin/<name> (no local head). The daemon must
// seed the local branch from the remote-tracking ref, not from base.
func TestCustomBranchFromRemoteTracking(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	origin := initOriginRepo(t, "")
	wantHead := addOriginBranch(t, origin, "feature/remote")
	bare := initBareCache(t, origin)
	// Drop the local head the bare clone brought along so only the
	// remote-tracking ref remains — the state a fetched repo cache is in.
	mustGit(t, bare, "update-ref", "-d", "refs/heads/feature/remote")

	fake := newFakeServer()
	defer fake.srv.Close()

	root := t.TempDir()
	m := New(Deps{
		ServerBaseURL:  fake.srv.URL,
		WorkspacesRoot: root,
		DaemonID:       "daemon-remote",
		TokenProvider:  func() string { return "test-token" },
		LookupBare:     func(string, string) string { return bare },
	})

	const ws = "ws-remote"
	const issue = "bbbb1111-2222-3333-4444-555555555555"
	m.handleInit(context.Background(), shared.Job{
		Kind: shared.JobInit, WorktreeID: "wt-remote", IssueID: issue, Identifier: "PRO-10",
		Branch: "feature/remote", WorkspaceID: ws, RepoURL: origin,
	})

	wtPath := filepath.Join(root, ws, "worktrees", shortID(issue), repoName(origin))
	if got := strings.TrimSpace(mustGitOut(t, wtPath, "rev-parse", "--abbrev-ref", "HEAD")); got != "feature/remote" {
		t.Fatalf("worktree branch = %q, want feature/remote", got)
	}
	if got := strings.TrimSpace(mustGitOut(t, wtPath, "rev-parse", "HEAD")); got != wantHead {
		t.Fatalf("worktree HEAD = %q, want %q (should continue the remote branch, not branch off base)", got, wantHead)
	}
}

// TestCustomBranchCreatedWhenMissing: a requested branch that exists nowhere is
// created from base (origin's default branch head).
func TestCustomBranchCreatedWhenMissing(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	origin := initOriginRepo(t, "")
	wantHead := strings.TrimSpace(mustGitOut(t, origin, "rev-parse", "HEAD"))

	fake := newFakeServer()
	defer fake.srv.Close()

	root := t.TempDir()
	m := New(Deps{
		ServerBaseURL:  fake.srv.URL,
		WorkspacesRoot: root,
		DaemonID:       "daemon-new",
		TokenProvider:  func() string { return "test-token" },
	})

	const ws = "ws-new"
	const issue = "cccc1111-2222-3333-4444-555555555555"
	m.handleInit(context.Background(), shared.Job{
		Kind: shared.JobInit, WorktreeID: "wt-new", IssueID: issue, Identifier: "PRO-11",
		Branch: "feature/new", WorkspaceID: ws, RepoURL: origin,
	})

	wtPath := filepath.Join(root, ws, "worktrees", shortID(issue), repoName(origin))
	if got := strings.TrimSpace(mustGitOut(t, wtPath, "rev-parse", "--abbrev-ref", "HEAD")); got != "feature/new" {
		t.Fatalf("worktree branch = %q, want feature/new", got)
	}
	if got := strings.TrimSpace(mustGitOut(t, wtPath, "rev-parse", "HEAD")); got != wantHead {
		t.Fatalf("worktree HEAD = %q, want base %q", got, wantHead)
	}
	if st := fake.lastStatus(t); st.Status != shared.StatusReady || st.Branch != "feature/new" {
		t.Fatalf("init status report = %+v, want ready on feature/new", st)
	}
}

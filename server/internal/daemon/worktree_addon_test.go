package daemon

import (
	"context"
	"testing"

	"github.com/multica-ai/multica/server/internal/daemon/execenv"
)

// TestEagerCheckoutTaskReposNoOps locks in the guards that must NOT touch the
// agent's tree: no repo cache, a local_directory task (the agent works in the
// user's own repo), and a nil env. Each must return nil without checking out.
func TestEagerCheckoutTaskReposNoOps(t *testing.T) {
	task := Task{WorkspaceID: "ws", ID: "task-1", Repos: []RepoData{{URL: "https://example.com/r.git"}}}

	// No repo cache → no-op.
	d := &Daemon{}
	if err := d.eagerCheckoutTaskRepos(context.Background(), task, &execenv.Environment{WorkDir: "/tmp/wd"}, "agent", ""); err != nil {
		t.Fatalf("nil repoCache: got %v, want nil (no-op)", err)
	}

	// local_directory task → no-op even with a repo cache (must not create
	// sibling worktree dirs inside the user's own repo).
	withCache := &Daemon{repoCache: &blockingLookupRepoCache{}}
	if err := withCache.eagerCheckoutTaskRepos(context.Background(), task, &execenv.Environment{WorkDir: "/tmp/wd", LocalDirectory: true}, "agent", ""); err != nil {
		t.Fatalf("local_directory: got %v, want nil (no-op)", err)
	}

	// nil env → no-op.
	if err := withCache.eagerCheckoutTaskRepos(context.Background(), task, nil, "agent", ""); err != nil {
		t.Fatalf("nil env: got %v, want nil (no-op)", err)
	}
}

// TestIssueWorktreeWorkDir covers the unified-workdir gating: a per-issue dir
// when enabled + repos + cache + issue; "" when repos are absent or the
// kill-switch is off.
func TestIssueWorktreeWorkDir(t *testing.T) {
	t.Setenv("MULTICA_WORKTREE_UNIFIED", "")
	d := &Daemon{repoCache: &blockingLookupRepoCache{}}
	d.cfg.WorkspacesRoot = "/wsroot"
	withRepos := Task{WorkspaceID: "ws1", IssueID: "issue-1234", ID: "t", Repos: []RepoData{{URL: "https://x/r.git"}}}

	if got := d.issueWorktreeWorkDir(withRepos); got == "" {
		t.Fatal("enabled + repos: expected a per-issue dir, got empty")
	}
	if got := d.issueWorktreeWorkDir(Task{WorkspaceID: "ws1", IssueID: "issue-1234", ID: "t"}); got != "" {
		t.Fatalf("no repos: got %q, want empty", got)
	}

	t.Setenv("MULTICA_WORKTREE_UNIFIED", "0")
	if got := d.issueWorktreeWorkDir(withRepos); got != "" {
		t.Fatalf("kill-switch off: got %q, want empty", got)
	}
}

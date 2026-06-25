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
	if err := d.eagerCheckoutTaskRepos(context.Background(), task, &execenv.Environment{WorkDir: "/tmp/wd"}, "agent"); err != nil {
		t.Fatalf("nil repoCache: got %v, want nil (no-op)", err)
	}

	// local_directory task → no-op even with a repo cache (must not create
	// sibling worktree dirs inside the user's own repo).
	withCache := &Daemon{repoCache: &blockingLookupRepoCache{}}
	if err := withCache.eagerCheckoutTaskRepos(context.Background(), task, &execenv.Environment{WorkDir: "/tmp/wd", LocalDirectory: true}, "agent"); err != nil {
		t.Fatalf("local_directory: got %v, want nil (no-op)", err)
	}

	// nil env → no-op.
	if err := withCache.eagerCheckoutTaskRepos(context.Background(), task, nil, "agent"); err != nil {
		t.Fatalf("nil env: got %v, want nil (no-op)", err)
	}
}

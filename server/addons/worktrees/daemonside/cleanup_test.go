package daemonside

import (
	"os"
	"path/filepath"
	"testing"
)

// TestHasWorktreeChild covers the predicate that gates removing the per-issue
// parent on cleanup: the parent is torn down only once its last repo worktree
// is gone, so a plain scratch subdir must not keep it alive.
func TestHasWorktreeChild(t *testing.T) {
	origin := initOriginRepo(t, "")
	parent := t.TempDir()

	if hasWorktreeChild(parent) {
		t.Fatal("empty dir should have no worktree children")
	}

	// A plain (non-git) subdir — e.g. leftover scratch — does not count.
	if err := os.MkdirAll(filepath.Join(parent, "notes"), 0o755); err != nil {
		t.Fatal(err)
	}
	if hasWorktreeChild(parent) {
		t.Fatal("a plain subdir is not a worktree child")
	}

	// A real git worktree child is detected.
	mustGit(t, origin, "worktree", "add", "--detach", filepath.Join(parent, "repo"), "main")
	if !hasWorktreeChild(parent) {
		t.Fatal("a git worktree subdir should be detected")
	}

	// A missing dir is false, not a panic.
	if hasWorktreeChild(filepath.Join(parent, "does-not-exist")) {
		t.Fatal("missing dir should report no worktree children")
	}
}

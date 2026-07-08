package daemon

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/daemon/repocache"
)

func adoptTestGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %s: %v", args, strings.TrimSpace(string(out)), err)
	}
}

// A `multica repo checkout` that targets the add-on-managed per-issue workdir
// must ADOPT the existing worktree: same path, its CURRENT branch, and — the
// critical part — no reset, no clean, no fresh agent/* branch. Before this
// guardrail the handler fell through to CreateWorktree/updateExistingWorktree,
// which ran `git reset --hard` + `git clean -fd` and switched the managed
// worktree onto agent/<name>/<task>, destroying uncommitted work and
// abandoning the issue's branch.
func TestRepoCheckoutAdoptsManagedIssueWorktree(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	const workspaceID = "ws-adopt"
	const repoURL = "https://github.com/org/repo.git"
	cache := &recordingRepoCache{lookupPath: "/cache/org/repo.git"}
	d := newRepoCheckoutTestDaemon(t, workspaceID, repoURL, cache)
	root := t.TempDir()
	d.cfg.WorkspacesRoot = root

	// Fabricate the add-on layout: {root}/{ws}/worktrees/{shortIssue}/{repo}
	// checked out on the issue's branch, with uncommitted work present.
	perIssueDir := filepath.Join(root, workspaceID, "worktrees", "abc123def456")
	wtPath := filepath.Join(perIssueDir, "repo")
	if err := os.MkdirAll(wtPath, 0o755); err != nil {
		t.Fatal(err)
	}
	adoptTestGit(t, wtPath, "init")
	adoptTestGit(t, wtPath, "config", "user.email", "adopt@test.local")
	adoptTestGit(t, wtPath, "config", "user.name", "adopt")
	adoptTestGit(t, wtPath, "commit", "--allow-empty", "-m", "base")
	adoptTestGit(t, wtPath, "checkout", "-b", "feature/adopt-me")
	uncommitted := filepath.Join(wtPath, "in-progress.txt")
	if err := os.WriteFile(uncommitted, []byte("unsaved agent work\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	raw, err := json.Marshal(map[string]string{
		"url": repoURL, "workspace_id": workspaceID, "workdir": perIssueDir,
		"task_id": "task-adopt", "ref": "main",
	})
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	d.repoCheckoutHandler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/repo/checkout", bytes.NewReader(raw)))

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var result repocache.WorktreeResult
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if result.Path != wtPath {
		t.Fatalf("adopted path = %q, want %q", result.Path, wtPath)
	}
	if result.BranchName != "feature/adopt-me" {
		t.Fatalf("adopted branch = %q, want feature/adopt-me (worktree rebranched?)", result.BranchName)
	}
	// The worktree must be untouched: uncommitted work survives (no reset /
	// clean) and the repo cache was never asked to create anything.
	if _, err := os.Stat(uncommitted); err != nil {
		t.Fatalf("uncommitted file gone after adoption — worktree was reset: %v", err)
	}
	cache.mu.Lock()
	createCalls := len(cache.params)
	cache.mu.Unlock()
	if createCalls != 0 {
		t.Fatalf("CreateWorktree called %d times for a managed worktree, want 0", createCalls)
	}
}

// A workdir OUTSIDE the managed worktrees root keeps the legacy behavior:
// the request falls through to the repo cache's CreateWorktree.
func TestRepoCheckoutOutsideManagedRootFallsThrough(t *testing.T) {
	const workspaceID = "ws-adopt-legacy"
	const repoURL = "https://github.com/org/repo.git"
	cache := &recordingRepoCache{lookupPath: "/cache/org/repo.git"}
	d := newRepoCheckoutTestDaemon(t, workspaceID, repoURL, cache)
	d.cfg.WorkspacesRoot = t.TempDir()

	workDir := t.TempDir() // not under {root}/{ws}/worktrees

	raw, err := json.Marshal(map[string]string{
		"url": repoURL, "workspace_id": workspaceID, "workdir": workDir, "task_id": "task-legacy",
	})
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	d.repoCheckoutHandler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/repo/checkout", bytes.NewReader(raw)))

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	cache.mu.Lock()
	createCalls := len(cache.params)
	cache.mu.Unlock()
	if createCalls != 1 {
		t.Fatalf("CreateWorktree calls = %d, want 1 (legacy fall-through)", createCalls)
	}
}

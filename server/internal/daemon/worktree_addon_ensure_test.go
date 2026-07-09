//go:build !windows

package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	worktreesdaemon "github.com/multica-ai/multica/server/addons/worktrees/daemonside"
	"github.com/multica-ai/multica/server/addons/worktrees/shared"
	"github.com/multica-ai/multica/server/internal/daemon/execenv"
	"github.com/multica-ai/multica/server/internal/daemon/repocache"
)

// These tests exercise the machine-local ensure of the unified eager checkout
// against REAL git repos: an issue whose worktree rows are "ready" on ANOTHER
// machine (or not tracked at all) must still get local worktrees on the issue
// branch — continuing from origin/<branch> when it was pushed — plus a Setup
// run, before the agent starts. This is the multi-machine scenario: issue
// assigned to an agent on machine A, a question later asked of an agent on
// machine B.

// localBareRepoCache is a repoCacheBackend over one real bare clone, with the
// same remote-tracking fetch behavior as the production repo cache.
type localBareRepoCache struct {
	barePath string
	mu       sync.Mutex
}

func (c *localBareRepoCache) Lookup(_, _ string) string { return c.barePath }

func (c *localBareRepoCache) Sync(string, []repocache.RepoInfo) error { return nil }

func (c *localBareRepoCache) Fetch(bare string) error {
	cmd := exec.Command("git", "-C", bare, "fetch", "origin", "+refs/heads/*:refs/remotes/origin/*")
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git fetch: %s: %w", strings.TrimSpace(string(out)), err)
	}
	return nil
}

func (c *localBareRepoCache) WithRepoLock(_ string, fn func() error) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return fn()
}

func (c *localBareRepoCache) CreateWorktree(repocache.WorktreeParams) (*repocache.WorktreeResult, error) {
	return nil, errors.New("unexpected CreateWorktree call in local-ensure test")
}

// ensureFixture is a real-git reproduction of the two-machine timeline:
//  1. origin repo exists with main (+ multica.json defining Setup);
//  2. this machine's bare cache is cloned (it knows only main);
//  3. machine A pushes the issue branch to origin (when pushBranch is set) —
//     the cache only learns about it through Fetch, exactly like production.
func ensureFixture(t *testing.T, setupCmd, pushBranch string) (originDir, cacheBare string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	base := t.TempDir()

	originDir = filepath.Join(base, "origin")
	if err := os.MkdirAll(originDir, 0o755); err != nil {
		t.Fatal(err)
	}
	adoptTestGit(t, originDir, "init")
	adoptTestGit(t, originDir, "config", "user.email", "ensure@test.local")
	adoptTestGit(t, originDir, "config", "user.name", "ensure")
	manifest := fmt.Sprintf(`{"scripts":{"setup":%q}}`, setupCmd)
	if err := os.WriteFile(filepath.Join(originDir, "multica.json"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	adoptTestGit(t, originDir, "add", ".")
	adoptTestGit(t, originDir, "commit", "-m", "init")
	adoptTestGit(t, originDir, "branch", "-M", "main")

	// Clone the machine-local bare cache BEFORE the issue branch exists, and
	// convert it to the remote-tracking layout the production cache uses.
	cacheBare = filepath.Join(base, "cache.git")
	adoptTestGit(t, base, "clone", "--bare", originDir, cacheBare)
	adoptTestGit(t, cacheBare, "config", "remote.origin.fetch", "+refs/heads/*:refs/remotes/origin/*")

	if pushBranch != "" {
		// Machine A's work: the issue branch with one commit, on origin only.
		adoptTestGit(t, originDir, "checkout", "-b", pushBranch)
		if err := os.WriteFile(filepath.Join(originDir, "from-machine-a.txt"), []byte("pushed work\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		adoptTestGit(t, originDir, "add", ".")
		adoptTestGit(t, originDir, "commit", "-m", "work from machine A")
		adoptTestGit(t, originDir, "checkout", "main")
	}
	return originDir, cacheBare
}

// newEnsureTestDaemon builds a daemon whose fake server serves the workspace
// repos endpoint (for ensureRepoReady) and the add-on issue-status endpoint
// (for WaitIssueReady) with the given response.
func newEnsureTestDaemon(t *testing.T, wsID, repoURL string, cache *localBareRepoCache, status shared.IssueStatusResponse) *Daemon {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/daemon/workspaces/"+wsID+"/repos", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, WorkspaceReposResponse{WorkspaceID: wsID, Repos: []RepoData{{URL: repoURL}}, ReposVersion: "v1"})
	})
	mux.HandleFunc("/api/daemon/worktree/issues/", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, status)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	d := &Daemon{
		cfg:       Config{CLIVersion: "v1.0.0"},
		client:    NewClient(srv.URL),
		repoCache: cache,
		workspaces: map[string]*workspaceState{
			wsID: newWorkspaceState(wsID, nil, "", []RepoData{{URL: repoURL}}, nil),
		},
		logger: slog.Default(),
	}
	d.cfg.ServerBaseURL = srv.URL
	d.cfg.DaemonID = "daemon-machine-b"
	d.cfg.WorkspacesRoot = t.TempDir()
	return d
}

func readyStatus(repoURL string) shared.IssueStatusResponse {
	return shared.IssueStatusResponse{
		Managed: true,
		Worktrees: []shared.IssueWorktreeStatus{{
			RepoURL:     repoURL,
			Status:      shared.StatusReady,
			SetupStatus: shared.ScriptSucceeded,
			Path:        "/machines/a/worktrees/does-not-exist-here",
		}},
	}
}

// The core multi-machine bug: the issue's worktree rows are ready (checked out
// on machine A), so the wait returns instantly — but nothing exists on THIS
// machine. The eager checkout must create the local worktree on the SAME issue
// branch, seeded from origin/<branch> (machine A's pushed commit is visible),
// and run Setup here, before the agent starts.
func TestEagerCheckoutCreatesLocalWorktreeWhenReadyOnAnotherMachine(t *testing.T) {
	const wsID = "ws-ensure"
	const issueID = "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
	const branch = "PRO-11"
	setup := `echo ran >> .setup-marker && printf '%s' "$MULTICA_ISSUE_BRANCH" > .setup-branch`
	origin, cacheBare := ensureFixture(t, setup, branch)
	cache := &localBareRepoCache{barePath: cacheBare}
	d := newEnsureTestDaemon(t, wsID, origin, cache, readyStatus(origin))

	perIssueDir := worktreesdaemon.IssueWorktreeParent(d.cfg.WorkspacesRoot, wsID, issueID)
	task := Task{
		ID: "task-b", WorkspaceID: wsID, IssueID: issueID, IssueBranch: branch,
		Repos: []RepoData{{URL: origin}},
	}
	env := &execenv.Environment{WorkDir: perIssueDir}
	if err := d.eagerCheckoutTaskRepos(context.Background(), task, env, "agent-b", perIssueDir); err != nil {
		t.Fatalf("eagerCheckoutTaskRepos: %v", err)
	}

	wtPath := worktreesdaemon.IssueWorktreePath(d.cfg.WorkspacesRoot, wsID, issueID, origin)
	if !worktreesdaemon.IsWorktree(wtPath) {
		t.Fatalf("no local worktree created at %s", wtPath)
	}
	if got, err := worktreesdaemon.CurrentBranch(wtPath); err != nil || got != branch {
		t.Fatalf("local worktree branch = %q (%v), want %q", got, err, branch)
	}
	// Continuation, not a reset: machine A's pushed commit must be present.
	if _, err := os.Stat(filepath.Join(wtPath, "from-machine-a.txt")); err != nil {
		t.Fatalf("local worktree is missing machine A's pushed work (branched off base instead of origin/%s): %v", branch, err)
	}
	marker, err := os.ReadFile(filepath.Join(wtPath, ".setup-marker"))
	if err != nil {
		t.Fatalf("setup did not run in the local worktree: %v", err)
	}
	if got := strings.Count(string(marker), "ran"); got != 1 {
		t.Fatalf("setup ran %d times, want exactly 1", got)
	}
	if b, err := os.ReadFile(filepath.Join(wtPath, ".setup-branch")); err != nil || string(b) != branch {
		t.Fatalf("setup env MULTICA_ISSUE_BRANCH = %q (%v), want %q", string(b), err, branch)
	}

	// A second task on the same issue adopts the existing local worktree:
	// no re-checkout, no second Setup run.
	if err := d.eagerCheckoutTaskRepos(context.Background(), task, env, "agent-b", perIssueDir); err != nil {
		t.Fatalf("second eagerCheckoutTaskRepos: %v", err)
	}
	marker, err = os.ReadFile(filepath.Join(wtPath, ".setup-marker"))
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(string(marker), "ran"); got != 1 {
		t.Fatalf("setup ran %d times after re-entry, want exactly 1", got)
	}
}

// When machine A has not pushed the branch yet (or the issue was never
// checked out anywhere), the local worktree starts the branch from base.
func TestEagerCheckoutCreatesLocalWorktreeFromBaseWhenBranchUnpushed(t *testing.T) {
	const wsID = "ws-ensure-base"
	const issueID = "bbbbbbbb-cccc-dddd-eeee-ffffffffffff"
	const branch = "PRO-12"
	origin, cacheBare := ensureFixture(t, "echo ran >> .setup-marker", "")
	cache := &localBareRepoCache{barePath: cacheBare}
	d := newEnsureTestDaemon(t, wsID, origin, cache, readyStatus(origin))

	perIssueDir := worktreesdaemon.IssueWorktreeParent(d.cfg.WorkspacesRoot, wsID, issueID)
	task := Task{
		ID: "task-b", WorkspaceID: wsID, IssueID: issueID, IssueBranch: branch,
		Repos: []RepoData{{URL: origin}},
	}
	if err := d.eagerCheckoutTaskRepos(context.Background(), task, &execenv.Environment{WorkDir: perIssueDir}, "agent-b", perIssueDir); err != nil {
		t.Fatalf("eagerCheckoutTaskRepos: %v", err)
	}

	wtPath := worktreesdaemon.IssueWorktreePath(d.cfg.WorkspacesRoot, wsID, issueID, origin)
	if got, err := worktreesdaemon.CurrentBranch(wtPath); err != nil || got != branch {
		t.Fatalf("local worktree branch = %q (%v), want %q", got, err, branch)
	}
	if _, err := os.Stat(filepath.Join(wtPath, "multica.json")); err != nil {
		t.Fatalf("worktree does not contain base content: %v", err)
	}
	if _, err := os.Stat(filepath.Join(wtPath, ".setup-marker")); err != nil {
		t.Fatalf("setup did not run: %v", err)
	}
}

// An issue the add-on does not track (e.g. a mention on an issue never
// assigned to an agent) must still get a local checkout — the agent's brief
// promises one.
func TestEagerCheckoutCreatesLocalWorktreeWhenUnmanaged(t *testing.T) {
	const wsID = "ws-ensure-unmanaged"
	const issueID = "cccccccc-dddd-eeee-ffff-000000000000"
	const branch = "PRO-13"
	origin, cacheBare := ensureFixture(t, "echo ran >> .setup-marker", "")
	cache := &localBareRepoCache{barePath: cacheBare}
	d := newEnsureTestDaemon(t, wsID, origin, cache, shared.IssueStatusResponse{Managed: false, Worktrees: []shared.IssueWorktreeStatus{}})

	perIssueDir := worktreesdaemon.IssueWorktreeParent(d.cfg.WorkspacesRoot, wsID, issueID)
	task := Task{
		ID: "task-q", WorkspaceID: wsID, IssueID: issueID, IssueBranch: branch,
		Repos: []RepoData{{URL: origin}},
	}
	if err := d.eagerCheckoutTaskRepos(context.Background(), task, &execenv.Environment{WorkDir: perIssueDir}, "agent-b", perIssueDir); err != nil {
		t.Fatalf("eagerCheckoutTaskRepos: %v", err)
	}
	wtPath := worktreesdaemon.IssueWorktreePath(d.cfg.WorkspacesRoot, wsID, issueID, origin)
	if !worktreesdaemon.IsWorktree(wtPath) {
		t.Fatalf("no local worktree created for unmanaged issue at %s", wtPath)
	}
	if got, err := worktreesdaemon.CurrentBranch(wtPath); err != nil || got != branch {
		t.Fatalf("local worktree branch = %q (%v), want %q", got, err, branch)
	}
}

// Old-server compatibility: a task without issue_branch keeps the previous
// wait-only behavior — no local worktree is invented on a guessed branch.
func TestEagerCheckoutSkipsLocalEnsureWithoutIssueBranch(t *testing.T) {
	const wsID = "ws-ensure-nobranch"
	const issueID = "dddddddd-eeee-ffff-0000-111111111111"
	origin, cacheBare := ensureFixture(t, "echo ran >> .setup-marker", "")
	cache := &localBareRepoCache{barePath: cacheBare}
	d := newEnsureTestDaemon(t, wsID, origin, cache, readyStatus(origin))

	perIssueDir := worktreesdaemon.IssueWorktreeParent(d.cfg.WorkspacesRoot, wsID, issueID)
	task := Task{ID: "task-old", WorkspaceID: wsID, IssueID: issueID, Repos: []RepoData{{URL: origin}}}
	if err := d.eagerCheckoutTaskRepos(context.Background(), task, &execenv.Environment{WorkDir: perIssueDir}, "agent-b", perIssueDir); err != nil {
		t.Fatalf("eagerCheckoutTaskRepos: %v", err)
	}
	if worktreesdaemon.IsWorktree(worktreesdaemon.IssueWorktreePath(d.cfg.WorkspacesRoot, wsID, issueID, origin)) {
		t.Fatal("local worktree created despite missing issue_branch")
	}
}

// A failing Setup fails the task before the agent starts, with the script
// output attached — same contract as the owning machine's checkout.
func TestEagerCheckoutLocalEnsureSetupFailureFailsTask(t *testing.T) {
	const wsID = "ws-ensure-fail"
	const issueID = "eeeeeeee-ffff-0000-1111-222222222222"
	const branch = "PRO-14"
	origin, cacheBare := ensureFixture(t, "echo doomed && exit 7", "")
	cache := &localBareRepoCache{barePath: cacheBare}
	d := newEnsureTestDaemon(t, wsID, origin, cache, readyStatus(origin))

	perIssueDir := worktreesdaemon.IssueWorktreeParent(d.cfg.WorkspacesRoot, wsID, issueID)
	task := Task{
		ID: "task-fail", WorkspaceID: wsID, IssueID: issueID, IssueBranch: branch,
		Repos: []RepoData{{URL: origin}},
	}
	err := d.eagerCheckoutTaskRepos(context.Background(), task, &execenv.Environment{WorkDir: perIssueDir}, "agent-b", perIssueDir)
	if err == nil {
		t.Fatal("expected setup failure to fail the eager checkout")
	}
	if !strings.Contains(err.Error(), "setup") || !strings.Contains(err.Error(), "doomed") {
		t.Fatalf("error should carry setup context + output, got: %v", err)
	}
}

func writeJSON(t *testing.T, w http.ResponseWriter, v any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		t.Errorf("encode response: %v", err)
	}
}

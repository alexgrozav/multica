//go:build !windows

package daemonside

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/multica-ai/multica/server/addons/worktrees/shared"
)

// End-to-end exercise of the daemon worktree lifecycle against a REAL local git
// repo and a fake server (httptest) capturing the daemon's reports/logs. No DB
// required — this proves the part that can't be checked by reading: bare clone,
// worktree add on the IDENTIFIER branch, multica.json-driven Setup/Run/Cleanup
// execution, log streaming, and worktree removal.
func TestDaemonWorktreeLifecycleE2E(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	manifest := `{
	  "scripts": {
	    "setup": "echo SETUP_OK > marker.txt",
	    "run": {"dev": "cat marker.txt; echo OOPS 1>&2"},
	    "cleanup": "echo cleaning"
	  }
	}`
	origin := initOriginRepo(t, manifest)
	fake := newFakeServer()
	defer fake.srv.Close()

	root := t.TempDir()
	m := New(Deps{
		ServerBaseURL:  fake.srv.URL,
		WorkspacesRoot: root,
		DaemonID:       "daemon-e2e",
		TokenProvider:  func() string { return "test-token" },
	})

	const ws = "ws-e2e"
	const issue = "11111111-2222-3333-4444-555555555555"
	const wtID = "wt-1"
	const identifier = "PRO-11"
	wtPath := filepath.Join(root, ws, "worktrees", shortID(issue), repoName(origin))

	// 1) INIT + SETUP — reads multica.json, checks out on the PRO-11 branch, runs
	//    Setup, and reports the discovered scripts up.
	m.handleInit(context.Background(), shared.Job{
		Kind: shared.JobInit, WorktreeID: wtID, IssueID: issue, Identifier: identifier,
		WorkspaceID: ws, RepoURL: origin, SetupTaskID: "init-setup-1",
	})
	if !isWorktree(wtPath) {
		t.Fatalf("worktree not created at %s", wtPath)
	}
	if _, err := os.Stat(filepath.Join(wtPath, "marker.txt")); err != nil {
		t.Fatalf("setup script did not run in the worktree: %v", err)
	}
	if out, _ := runGit(wtPath, "rev-parse", "--abbrev-ref", "HEAD"); strings.TrimSpace(out) != identifier {
		t.Fatalf("worktree branch = %q, want %q", strings.TrimSpace(out), identifier)
	}
	st := fake.lastStatus(t)
	if st.Kind != shared.JobInit || st.Status != shared.StatusReady || st.SetupStatus != shared.ScriptSucceeded {
		t.Fatalf("init status report = %+v, want ready + setup succeeded", st)
	}
	if !st.HasSetup || !st.HasCleanup || len(st.RunScripts) != 1 || st.RunScripts[0] != "dev" {
		t.Fatalf("init report scripts = %+v, want has_setup+has_cleanup+run[dev]", st)
	}
	if !strings.Contains(fake.logText(), "[multica] setup finished") {
		t.Fatalf("boot setup did not stream to the setup channel; got:\n%s", fake.logText())
	}

	// 1b) RE-RUN SETUP — reads multica.json again; reports only setup_status.
	m.handleSetup(context.Background(), shared.Job{
		Kind: shared.JobSetup, WorktreeID: wtID, IssueID: issue, WorkspaceID: ws,
		RepoURL: origin, SetupTaskID: "setup-rerun-1",
	})
	if st := fake.lastStatus(t); st.Kind != shared.JobSetup || st.SetupStatus != shared.ScriptSucceeded {
		t.Fatalf("re-run setup status report = %+v, want setup succeeded", st)
	}

	// 2) RUN — runs the named "dev" script in the worktree, streaming stdout+stderr.
	m.handleRun(context.Background(), shared.Job{
		Kind: shared.JobRun, WorktreeID: wtID, IssueID: issue, WorkspaceID: ws,
		RepoURL: origin, ScriptName: "dev", RunTaskID: "run-1",
	})
	logs := fake.logText()
	if !strings.Contains(logs, "SETUP_OK") {
		t.Fatalf("run output missing worktree file contents; got:\n%s", logs)
	}
	if !strings.Contains(logs, "OOPS") {
		t.Fatalf("run stderr not streamed; got:\n%s", logs)
	}
	if st := fake.lastStatus(t); st.Kind != shared.JobRun || st.RunStatus != shared.RunSucceeded || st.ScriptName != "dev" {
		t.Fatalf("run status report = %+v, want run succeeded for dev", st)
	}

	// 2b) RUN unknown script → reported failed, not crashed.
	m.handleRun(context.Background(), shared.Job{
		Kind: shared.JobRun, WorktreeID: wtID, IssueID: issue, WorkspaceID: ws,
		RepoURL: origin, ScriptName: "ghost", RunTaskID: "run-2",
	})
	if st := fake.lastStatus(t); st.RunStatus != shared.RunFailed {
		t.Fatalf("unknown run status = %+v, want failed", st)
	}

	// 3) CLEANUP — runs the multica.json cleanup, removes the worktree + branch.
	m.handleCleanup(context.Background(), shared.Job{
		Kind: shared.JobCleanup, WorktreeID: wtID, IssueID: issue, Identifier: identifier,
		WorkspaceID: ws, RepoURL: origin,
	})
	if isWorktree(wtPath) {
		t.Fatalf("worktree still present after cleanup: %s", wtPath)
	}
	if st := fake.lastStatus(t); st.Kind != shared.JobCleanup || st.Status != shared.StatusRemoved {
		t.Fatalf("cleanup status report = %+v, want removed", st)
	}
}

// TestConcurrentInitSameRepo guards the config.lock race: many worktrees being
// initialized for the SAME bare repo at once must all succeed, not collide on
// git's lockfiles.
func TestConcurrentInitSameRepo(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	origin := initOriginRepo(t, "") // no multica.json → no setup
	fake := newFakeServer()
	defer fake.srv.Close()

	m := New(Deps{
		ServerBaseURL:  fake.srv.URL,
		WorkspacesRoot: t.TempDir(),
		DaemonID:       "daemon-conc",
		TokenProvider:  func() string { return "test-token" },
	})

	const ws = "ws-conc"
	const n = 6
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		issue := fmt.Sprintf("%08d-1111-2222-3333-444444444444", i)
		go func(i int, issue string) {
			defer wg.Done()
			m.handleInit(context.Background(), shared.Job{
				Kind: shared.JobInit, WorktreeID: "wt-" + issue, IssueID: issue,
				Identifier: fmt.Sprintf("CON-%d", i), WorkspaceID: ws, RepoURL: origin,
			})
		}(i, issue)
	}
	wg.Wait()

	fake.mu.Lock()
	defer fake.mu.Unlock()
	if len(fake.statuses) != n {
		t.Fatalf("got %d init reports, want %d", len(fake.statuses), n)
	}
	for _, st := range fake.statuses {
		if st.Status != shared.StatusReady {
			t.Fatalf("init status = %q (err %q); want ready — config.lock race?", st.Status, st.Error)
		}
	}
}

// TestInitFetchesBeforeBranching guards the "outdated main" bug: when the bare
// cache was located by a Lookup that does not fetch, its origin/<default> can be
// stale. handleInit must fetch (via Deps.FetchBare) before creating the worktree
// so the checkout lands on CURRENT origin, not whatever the cache held.
func TestInitFetchesBeforeBranching(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	origin := initOriginRepo(t, "")  // commit 1 on main
	bare := initBareCache(t, origin) // cache pinned at commit 1

	// Advance origin past what the cache holds.
	if err := os.WriteFile(filepath.Join(origin, "feature.txt"), []byte("new\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustGit(t, origin, "add", ".")
	mustGit(t, origin, "commit", "-m", "advance main")
	wantHead := strings.TrimSpace(mustGitOut(t, origin, "rev-parse", "HEAD"))

	fake := newFakeServer()
	defer fake.srv.Close()

	root := t.TempDir()
	var fetched bool
	m := New(Deps{
		ServerBaseURL:  fake.srv.URL,
		WorkspacesRoot: root,
		DaemonID:       "daemon-fetch",
		TokenProvider:  func() string { return "test-token" },
		// Mirror the daemon: LookupBare returns the cached bare WITHOUT fetching, so
		// freshness must come from FetchBare.
		LookupBare: func(string, string) string { return bare },
		FetchBare: func(p string) error {
			fetched = true
			_, err := runGit(p, "fetch", "origin")
			return err
		},
	})

	const ws = "ws-fetch"
	const issue = "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
	m.handleInit(context.Background(), shared.Job{
		Kind: shared.JobInit, WorktreeID: "wt-f", IssueID: issue, Identifier: "PRO-42",
		WorkspaceID: ws, RepoURL: origin,
	})

	if !fetched {
		t.Fatal("handleInit did not fetch the bare cache before branching")
	}
	wtPath := filepath.Join(root, ws, "worktrees", shortID(issue), repoName(origin))
	got := strings.TrimSpace(mustGitOut(t, wtPath, "rev-parse", "HEAD"))
	if got != wantHead {
		t.Fatalf("worktree HEAD = %q, want %q (branched off a stale base — pre-init fetch missing?)", got, wantHead)
	}
}

// TestRunSetupAt covers the legacy checkout Setup hook: it reads the worktree's
// multica.json and runs the Setup script, reporting ran/failure correctly.
func TestRunSetupAt(t *testing.T) {
	// configured setup runs in the worktree
	dir := t.TempDir()
	writeManifest(t, dir, `{"scripts":{"setup":"echo HELLO > setup-ran.txt"}}`)
	ran, err := RunSetupAt(context.Background(), "ws", "r", dir, func(string, string) {})
	if !ran || err != nil {
		t.Fatalf("RunSetupAt = ran %v err %v, want ran/nil", ran, err)
	}
	if _, e := os.Stat(filepath.Join(dir, "setup-ran.txt")); e != nil {
		t.Fatalf("setup script did not run in the worktree: %v", e)
	}

	// no multica.json → no-op
	if ran, err := RunSetupAt(context.Background(), "ws", "r", t.TempDir(), func(string, string) {}); ran || err != nil {
		t.Fatalf("no manifest = ran %v err %v, want false/nil", ran, err)
	}

	// failing setup → ran=true + error (caller fails the checkout)
	failDir := t.TempDir()
	writeManifest(t, failDir, `{"scripts":{"setup":"exit 7"}}`)
	if ran, err := RunSetupAt(context.Background(), "ws", "r", failDir, func(string, string) {}); !ran || err == nil {
		t.Fatalf("failing setup = ran %v err %v, want true/error", ran, err)
	}
}

// --- fake server ---

type fakeServer struct {
	mu       sync.Mutex
	statuses []shared.StatusReport
	logs     []shared.LogLine
	srv      *httptest.Server
}

func newFakeServer() *fakeServer {
	f := &fakeServer{}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/daemon/worktree/jobs/", func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/status") {
			var rep shared.StatusReport
			_ = json.NewDecoder(r.Body).Decode(&rep)
			f.mu.Lock()
			f.statuses = append(f.statuses, rep)
			f.mu.Unlock()
		}
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("/api/daemon/worktree/runs/", func(w http.ResponseWriter, r *http.Request) {
		var batch shared.LogBatch
		_ = json.NewDecoder(r.Body).Decode(&batch)
		f.mu.Lock()
		f.logs = append(f.logs, batch.Lines...)
		f.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	})
	f.srv = httptest.NewServer(mux)
	return f
}

func (f *fakeServer) lastStatus(t *testing.T) shared.StatusReport {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.statuses) == 0 {
		t.Fatal("no status report received")
	}
	return f.statuses[len(f.statuses)-1]
}

func (f *fakeServer) logText() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var b strings.Builder
	for _, l := range f.logs {
		b.WriteString(l.Content)
		b.WriteByte('\n')
	}
	return b.String()
}

// --- local git origin ---

func mustGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	if out, err := runGit(dir, args...); err != nil {
		t.Fatalf("git %v failed: %s: %v", args, strings.TrimSpace(out), err)
	}
}

func mustGitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := runGit(dir, args...)
	if err != nil {
		t.Fatalf("git %v failed: %s: %v", args, strings.TrimSpace(out), err)
	}
	return out
}

// initBareCache builds a bare clone of origin in the modern remote-tracking
// layout the daemon's repo cache uses (refs/remotes/origin/*), so resolveBaseRef
// and `git fetch origin` operate on the same refs a real cache would.
func initBareCache(t *testing.T, origin string) string {
	t.Helper()
	bare := filepath.Join(t.TempDir(), "cache.git")
	mustGit(t, "", "clone", "--bare", origin, bare)
	mustGit(t, bare, "config", "remote.origin.fetch", "+refs/heads/*:refs/remotes/origin/*")
	mustGit(t, bare, "fetch", "origin")
	return bare
}

func writeManifest(t *testing.T, dir, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, shared.ManifestFile), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func initOriginRepo(t *testing.T, manifest string) string {
	dir := filepath.Join(t.TempDir(), "origin")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	mustGit(t, dir, "init")
	mustGit(t, dir, "config", "user.email", "e2e@test.local")
	mustGit(t, dir, "config", "user.name", "e2e")
	if manifest != "" {
		writeManifest(t, dir, manifest)
	}
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("hi\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustGit(t, dir, "add", ".")
	mustGit(t, dir, "commit", "-m", "init")
	mustGit(t, dir, "branch", "-M", "main")
	return dir
}

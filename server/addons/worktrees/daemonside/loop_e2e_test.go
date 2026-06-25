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
// worktree add on an issue/* branch, Setup/Run/Cleanup execution, log streaming,
// and worktree removal.
func TestDaemonWorktreeLifecycleE2E(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	origin := initOriginRepo(t)
	fake := newFakeServer()
	defer fake.srv.Close()

	root := t.TempDir()
	m := New(Deps{
		ServerBaseURL:  fake.srv.URL,
		WorkspacesRoot: root,
		DaemonID:       "daemon-e2e",
		TokenProvider:  func() string { return "test-token" },
		// LookupBare/EnsureBare nil → self-clone path is exercised.
	})

	const ws = "ws-e2e"
	const issue = "11111111-2222-3333-4444-555555555555"
	const wtID = "wt-1"
	wtPath := filepath.Join(root, ws, ".issue-worktrees", shortID(issue), repoName(origin))
	wantBranch := "issue/" + shortID(issue) + "/" + repoName(origin)

	// 1) INIT + SETUP — boot Setup streams its output to the setup channel.
	m.handleInit(context.Background(), shared.Job{
		Kind: shared.JobInit, WorktreeID: wtID, IssueID: issue, WorkspaceID: ws,
		RepoURL: origin, SetupTaskID: "init-setup-1", Setup: "echo SETUP_OK > marker.txt",
	})
	if !isWorktree(wtPath) {
		t.Fatalf("worktree not created at %s", wtPath)
	}
	if _, err := os.Stat(filepath.Join(wtPath, "marker.txt")); err != nil {
		t.Fatalf("setup script did not run in the worktree: %v", err)
	}
	if out, _ := runGit(wtPath, "rev-parse", "--abbrev-ref", "HEAD"); strings.TrimSpace(out) != wantBranch {
		t.Fatalf("worktree branch = %q, want %q", strings.TrimSpace(out), wantBranch)
	}
	if st := fake.lastStatus(t); st.Kind != shared.JobInit || st.Status != shared.StatusReady || st.SetupStatus != shared.ScriptSucceeded {
		t.Fatalf("init status report = %+v, want ready + setup succeeded", st)
	}
	if !strings.Contains(fake.logText(), "[multica] setup finished") {
		t.Fatalf("boot setup did not stream to the setup channel; got:\n%s", fake.logText())
	}

	// 1b) RE-RUN SETUP — appends to the marker; streams to the setup channel and
	//     reports only setup_status (not worktree status).
	m.handleSetup(context.Background(), shared.Job{
		Kind: shared.JobSetup, WorktreeID: wtID, IssueID: issue, WorkspaceID: ws,
		RepoURL: origin, SetupTaskID: "setup-rerun-1", Setup: "echo RESETUP_OK >> marker.txt",
	})
	if st := fake.lastStatus(t); st.Kind != shared.JobSetup || st.SetupStatus != shared.ScriptSucceeded {
		t.Fatalf("re-run setup status report = %+v, want setup succeeded", st)
	}

	// 2) RUN — proves it executes in the persistent worktree (reads marker.txt)
	//    and streams both stdout and stderr back to the server.
	m.handleRun(context.Background(), shared.Job{
		Kind: shared.JobRun, WorktreeID: wtID, IssueID: issue, WorkspaceID: ws,
		RepoURL: origin, RunTaskID: "run-1", Run: "cat marker.txt; echo OOPS 1>&2",
	})
	logs := fake.logText()
	if !strings.Contains(logs, "SETUP_OK") {
		t.Fatalf("run output missing worktree file contents; got:\n%s", logs)
	}
	if !strings.Contains(logs, "OOPS") {
		t.Fatalf("run stderr not streamed; got:\n%s", logs)
	}
	if st := fake.lastStatus(t); st.Kind != shared.JobRun || st.RunStatus != shared.RunSucceeded {
		t.Fatalf("run status report = %+v, want run succeeded", st)
	}

	// 3) CLEANUP — runs the script, removes the worktree + branch.
	m.handleCleanup(context.Background(), shared.Job{
		Kind: shared.JobCleanup, WorktreeID: wtID, IssueID: issue, WorkspaceID: ws,
		RepoURL: origin, Cleanup: "echo cleaning",
	})
	if isWorktree(wtPath) {
		t.Fatalf("worktree still present after cleanup: %s", wtPath)
	}
	if st := fake.lastStatus(t); st.Kind != shared.JobCleanup || st.Status != shared.StatusRemoved {
		t.Fatalf("cleanup status report = %+v, want removed", st)
	}
}

// TestConcurrentInitSameRepo guards the config.lock race: many worktrees being
// initialized for the SAME bare repo at once (e.g. several tasks created
// together) must all succeed, not collide on git's lockfiles.
func TestConcurrentInitSameRepo(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	origin := initOriginRepo(t)
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
		go func(issue string) {
			defer wg.Done()
			m.handleInit(context.Background(), shared.Job{
				Kind: shared.JobInit, WorktreeID: "wt-" + issue, IssueID: issue,
				WorkspaceID: ws, RepoURL: origin,
			})
		}(issue)
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

// TestRunRepoSetup covers the checkout Setup hook: it fetches the configured
// Setup script and runs it in the worktree, reports ran/failure correctly.
func TestRunRepoSetup(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	fake := newFakeServer()
	defer fake.srv.Close()
	params := func(dir string) SetupParams {
		return SetupParams{
			ServerBaseURL: fake.srv.URL, TokenProvider: func() string { return "t" },
			DaemonID: "d", WorkspaceID: "ws", RepoURL: "r", WorktreePath: dir,
		}
	}

	// configured setup runs in the worktree
	fake.setScript(shared.RepoScript{Setup: "echo HELLO > setup-ran.txt"})
	dir := t.TempDir()
	ran, err := RunRepoSetup(context.Background(), params(dir), func(string, string) {})
	if !ran || err != nil {
		t.Fatalf("RunRepoSetup = ran %v err %v, want ran/nil", ran, err)
	}
	if _, e := os.Stat(filepath.Join(dir, "setup-ran.txt")); e != nil {
		t.Fatalf("setup script did not run in the worktree: %v", e)
	}

	// no setup configured → no-op
	fake.setScript(shared.RepoScript{})
	if ran, err := RunRepoSetup(context.Background(), params(t.TempDir()), func(string, string) {}); ran || err != nil {
		t.Fatalf("empty setup = ran %v err %v, want false/nil", ran, err)
	}

	// failing setup → ran=true + error (caller fails the checkout)
	fake.setScript(shared.RepoScript{Setup: "exit 7"})
	if ran, err := RunRepoSetup(context.Background(), params(t.TempDir()), func(string, string) {}); !ran || err == nil {
		t.Fatalf("failing setup = ran %v err %v, want true/error", ran, err)
	}
}

// --- fake server ---

type fakeServer struct {
	mu       sync.Mutex
	statuses []shared.StatusReport
	logs     []shared.LogLine
	script   shared.RepoScript
	srv      *httptest.Server
}

func (f *fakeServer) setScript(s shared.RepoScript) {
	f.mu.Lock()
	f.script = s
	f.mu.Unlock()
}

func newFakeServer() *fakeServer {
	f := &fakeServer{}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/daemon/worktree/scripts", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		s := f.script
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(s)
	})
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

func initOriginRepo(t *testing.T) string {
	dir := filepath.Join(t.TempDir(), "origin")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	mustGit(t, dir, "init")
	mustGit(t, dir, "config", "user.email", "e2e@test.local")
	mustGit(t, dir, "config", "user.name", "e2e")
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("hi\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustGit(t, dir, "add", ".")
	mustGit(t, dir, "commit", "-m", "init")
	mustGit(t, dir, "branch", "-M", "main")
	return dir
}

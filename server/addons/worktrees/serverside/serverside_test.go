package serverside

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/multica-ai/multica/server/addons/worktrees/shared"
)

// These tests exercise the serverside store, listeners, and HTTP handlers
// against a real Postgres (DATABASE_URL, default local dev DB). They skip
// cleanly when no DB is reachable, mirroring the host handler test harness.

var (
	testPool        *pgxpool.Pool
	testWorkspaceID string
)

const (
	testWorkspaceSlug = "worktree-addon-tests"
	repoA             = "https://example.com/a.git"
	repoB             = "https://example.com/b.git"
)

func TestMain(m *testing.M) {
	ctx := context.Background()
	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		dbURL = "postgres://multica:multica@localhost:5432/multica?sslmode=disable"
	}
	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		fmt.Printf("Skipping worktrees serverside tests: cannot connect to DB: %v\n", err)
		os.Exit(0)
	}
	if err := pool.Ping(ctx); err != nil {
		fmt.Printf("Skipping worktrees serverside tests: DB not reachable: %v\n", err)
		pool.Close()
		os.Exit(0)
	}
	var hasWorkspace bool
	_ = pool.QueryRow(ctx, `SELECT to_regclass('public.workspace') IS NOT NULL`).Scan(&hasWorkspace)
	if !hasWorkspace {
		fmt.Println("Skipping worktrees serverside tests: host schema not migrated in this DB (set DATABASE_URL to your migrated/worktree DB).")
		pool.Close()
		os.Exit(0)
	}
	if err := EnsureSchema(ctx, pool); err != nil {
		fmt.Printf("EnsureSchema failed (is the host schema migrated?): %v\n", err)
		pool.Close()
		os.Exit(1)
	}
	testPool = pool

	cleanup(ctx, pool)
	repos := []byte(fmt.Sprintf(`[{"url":%q},{"url":%q}]`, repoA, repoB))
	if err := pool.QueryRow(ctx,
		`INSERT INTO workspace (name, slug, description, issue_prefix, repos)
		 VALUES ($1,$2,'','WAT',$3) RETURNING id`,
		"Worktree Addon Tests", testWorkspaceSlug, repos).Scan(&testWorkspaceID); err != nil {
		fmt.Printf("seed workspace failed: %v\n", err)
		pool.Close()
		os.Exit(1)
	}

	code := m.Run()
	cleanup(context.Background(), pool)
	pool.Close()
	os.Exit(code)
}

func cleanup(ctx context.Context, pool *pgxpool.Pool) {
	var wsID string
	_ = pool.QueryRow(ctx, `SELECT id FROM workspace WHERE slug=$1`, testWorkspaceSlug).Scan(&wsID)
	if wsID != "" {
		_, _ = pool.Exec(ctx,
			`DELETE FROM worktree_run WHERE worktree_id IN (SELECT id FROM issue_worktree WHERE workspace_id=$1)`, wsID)
		_, _ = pool.Exec(ctx,
			`DELETE FROM worktree_files WHERE worktree_id IN (SELECT id FROM issue_worktree WHERE workspace_id=$1)`, wsID)
		for _, tbl := range []string{"issue_worktree", "worktree_daemon_seen"} {
			_, _ = pool.Exec(ctx, fmt.Sprintf(`DELETE FROM %s WHERE workspace_id=$1`, tbl), wsID)
		}
	}
	_, _ = pool.Exec(ctx, `DELETE FROM workspace WHERE slug=$1`, testWorkspaceSlug)
}

// --- helpers ---

type eventSink struct {
	mu     sync.Mutex
	events []sinkEvent
}

type sinkEvent struct {
	ws        string
	eventType string
	payload   any
}

func (s *eventSink) publish(ws, eventType string, payload any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, sinkEvent{ws, eventType, payload})
}

func (s *eventSink) count(eventType string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, e := range s.events {
		if e.eventType == eventType {
			n++
		}
	}
	return n
}

func newTestModule(role string, sink *eventSink) *Module {
	return New(Deps{
		Pool:      testPool,
		Publish:   sink.publish,
		Subscribe: func(string, func(IssueEvent)) {},
		Principal: func(*http.Request) (Principal, bool) {
			return Principal{WorkspaceID: testWorkspaceID, UserID: "test-user", Role: role}, true
		},
		CanAccessWorkspace: func(*http.Request, string) bool { return true },
	})
}

func testRouter(m *Module) http.Handler {
	r := chi.NewRouter()
	r.Mount("/api/worktree", m.UIRouter())
	r.Mount("/api/daemon/worktree", m.DaemonRouter())
	return r
}

func ctx() context.Context { return context.Background() }

func newIssueID() string { return uuid.NewString() }

// makeReady drives a fresh worktree row to ready, owned by daemonID + online,
// with the given discovered scripts, and returns it.
func makeReady(t *testing.T, s *Store, issueID, identifier, repoURL, daemonID string, rep shared.StatusReport) shared.Worktree {
	t.Helper()
	w, err := s.CreateWorktreeRow(ctx(), issueID, identifier, testWorkspaceID, repoURL)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := s.ClaimInitJobs(ctx(), daemonID, testWorkspaceID); err != nil {
		t.Fatalf("claim init: %v", err)
	}
	rep.Kind = shared.JobInit
	if rep.Status == "" {
		rep.Status = shared.StatusReady
	}
	if _, err := s.ReportStatus(ctx(), daemonID, w.ID, rep); err != nil {
		t.Fatalf("report ready: %v", err)
	}
	if err := s.TouchDaemon(ctx(), testWorkspaceID, daemonID); err != nil {
		t.Fatalf("touch: %v", err)
	}
	got, _ := s.Get(ctx(), w.ID)
	return got
}

// --- store tests ---

func TestCreateAndListWorktrees(t *testing.T) {
	s := NewStore(testPool)
	issueID := newIssueID()

	w1, err := s.CreateWorktreeRow(ctx(), issueID, "WAT-1", testWorkspaceID, repoA)
	if err != nil {
		t.Fatalf("CreateWorktreeRow: %v", err)
	}
	if w1.Status != shared.StatusPending || w1.Identifier != "WAT-1" {
		t.Fatalf("row = %+v, want pending + identifier WAT-1", w1)
	}
	if w1.Runs == nil {
		t.Fatalf("Runs should be non-nil (empty), got nil")
	}
	// idempotent on (issue, repo)
	if _, err := s.CreateWorktreeRow(ctx(), issueID, "WAT-1", testWorkspaceID, repoA); err != nil {
		t.Fatalf("CreateWorktreeRow idempotent: %v", err)
	}
	list, err := s.ListByIssue(ctx(), issueID, testWorkspaceID)
	if err != nil {
		t.Fatalf("ListByIssue: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("ListByIssue len = %d, want 1 (idempotent)", len(list))
	}
	// cross-workspace read is empty
	other, _ := s.ListByIssue(ctx(), issueID, uuid.NewString())
	if len(other) != 0 {
		t.Fatalf("cross-workspace ListByIssue len = %d, want 0", len(other))
	}
}

func TestClaimInitJobsCarriesIdentifier(t *testing.T) {
	s := NewStore(testPool)
	issueID := newIssueID()
	if _, err := s.CreateWorktreeRow(ctx(), issueID, "WAT-7", testWorkspaceID, repoA); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := s.CreateWorktreeRow(ctx(), issueID, "WAT-7", testWorkspaceID, repoB); err != nil {
		t.Fatalf("create: %v", err)
	}

	jobs, err := s.ClaimInitJobs(ctx(), "daemon-1", testWorkspaceID)
	if err != nil {
		t.Fatalf("ClaimInitJobs: %v", err)
	}
	if len(jobs) < 2 {
		t.Fatalf("claimed %d init jobs, want >= 2", len(jobs))
	}
	for _, j := range jobs {
		if j.Kind != shared.JobInit || j.WorkspaceID != testWorkspaceID {
			t.Fatalf("job = %+v, want init/workspace-scoped", j)
		}
		if j.IssueID == issueID && j.Identifier != "WAT-7" {
			t.Fatalf("init job = %+v, want identifier WAT-7 → branch name", j)
		}
		if j.SetupTaskID == "" {
			t.Fatalf("init job = %+v, want a setup_task_id allocated", j)
		}
	}

	// A second daemon claims nothing — they were atomically taken.
	again, _ := s.ClaimInitJobs(ctx(), "daemon-2", testWorkspaceID)
	for _, j := range again {
		if j.IssueID == issueID {
			t.Fatalf("second daemon re-claimed an already-claimed init job: %+v", j)
		}
	}
}

// RequestOpen arms a pending open on a ready worktree; ClaimOpenJobs hands it to
// the owning daemon exactly once (carrying the target + resolvable issue) and the
// derived WorkingDir is the per-issue parent.
func TestRequestOpenAndClaim(t *testing.T) {
	s := NewStore(testPool)
	issueID := newIssueID()
	const daemonID = "daemon-open"
	w := makeReady(t, s, issueID, "WAT-9", repoA, daemonID, shared.StatusReport{
		Path: "/root/ws/worktrees/" + issueID[:8] + "/a",
	})
	if w.WorkingDir != "/root/ws/worktrees/"+issueID[:8] {
		t.Fatalf("WorkingDir = %q, want the per-issue parent", w.WorkingDir)
	}

	if _, err := s.RequestOpen(ctx(), issueID, testWorkspaceID, shared.OpenZed); err != nil {
		t.Fatalf("RequestOpen: %v", err)
	}
	jobs, err := s.ClaimOpenJobs(ctx(), daemonID)
	if err != nil {
		t.Fatalf("ClaimOpenJobs: %v", err)
	}
	var got *shared.Job
	for i := range jobs {
		if jobs[i].IssueID == issueID {
			got = &jobs[i]
		}
	}
	if got == nil {
		t.Fatalf("claimed jobs %+v, want one JobOpen for issue %s", jobs, issueID)
	}
	if got.Kind != shared.JobOpen || got.OpenTarget != shared.OpenZed || got.WorkspaceID != testWorkspaceID {
		t.Fatalf("open job = %+v, want kind=open target=zed workspace-scoped", *got)
	}

	// Cleared: a re-poll (this or another daemon) does not re-claim it.
	again, _ := s.ClaimOpenJobs(ctx(), daemonID)
	for _, j := range again {
		if j.IssueID == issueID {
			t.Fatalf("open job re-claimed after clear: %+v", j)
		}
	}
}

// RequestOpen refuses when the issue has no ready (checked-out) worktree yet.
func TestRequestOpenNotReady(t *testing.T) {
	s := NewStore(testPool)
	issueID := newIssueID()
	if _, err := s.CreateWorktreeRow(ctx(), issueID, "WAT-10", testWorkspaceID, repoA); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := s.RequestOpen(ctx(), issueID, testWorkspaceID, shared.OpenFinder); !errors.Is(err, ErrNotReady) {
		t.Fatalf("RequestOpen on pending worktree err = %v, want ErrNotReady", err)
	}
}

// The daemon discovers the scripts from multica.json and reports them on init;
// the server materializes one worktree_run row per name and the has_* flags.
func TestReportInitDiscoversRunScripts(t *testing.T) {
	s := NewStore(testPool)
	issueID := newIssueID()
	w := makeReady(t, s, issueID, "WAT-2", repoA, "daemon-1", shared.StatusReport{
		SetupStatus: shared.ScriptSucceeded, HasSetup: true, HasCleanup: true,
		RunScripts: []string{"dev", "start"},
	})
	if !w.HasSetup || !w.HasCleanup {
		t.Fatalf("worktree = %+v, want has_setup + has_cleanup", w)
	}
	if len(w.Runs) != 2 {
		t.Fatalf("Runs = %+v, want 2 (dev, start)", w.Runs)
	}
	for _, r := range w.Runs {
		if r.Status != shared.RunIdle {
			t.Fatalf("run %q status = %q, want idle", r.Name, r.Status)
		}
	}

	// Re-report with a narrowed set → the removed name is dropped, the kept one
	// preserved.
	if _, err := s.ReportStatus(ctx(), "daemon-1", w.ID, shared.StatusReport{
		Kind: shared.JobInit, Status: shared.StatusReady, HasSetup: true, RunScripts: []string{"dev"},
	}); err != nil {
		t.Fatalf("re-report: %v", err)
	}
	got, _ := s.Get(ctx(), w.ID)
	if len(got.Runs) != 1 || got.Runs[0].Name != "dev" {
		t.Fatalf("after narrowing, Runs = %+v, want just dev", got.Runs)
	}
}

func TestRequestRunPerNameGating(t *testing.T) {
	s := NewStore(testPool)
	issueID := newIssueID()

	// pending row → not ready
	pending, _ := s.CreateWorktreeRow(ctx(), issueID, "WAT-3", testWorkspaceID, repoB)
	if _, err := s.RequestRun(ctx(), pending.ID, "dev"); err != ErrNotReady {
		t.Fatalf("RequestRun(pending) = %v, want ErrNotReady", err)
	}

	w := makeReady(t, s, newIssueID(), "WAT-4", repoA, "daemon-1", shared.StatusReport{
		SetupStatus: shared.ScriptSucceeded, RunScripts: []string{"dev"},
	})

	// unknown script name → ErrNoRunScript
	if _, err := s.RequestRun(ctx(), w.ID, "nope"); err != ErrNoRunScript {
		t.Fatalf("RequestRun(unknown) = %v, want ErrNoRunScript", err)
	}

	// known name, online → armed
	out, err := s.RequestRun(ctx(), w.ID, "dev")
	if err != nil {
		t.Fatalf("RequestRun(dev) = %v, want success", err)
	}
	var dev *shared.RunScript
	for i := range out.Runs {
		if out.Runs[i].Name == "dev" {
			dev = &out.Runs[i]
		}
	}
	if dev == nil || dev.Status != shared.RunRunning || dev.RunTaskID == "" {
		t.Fatalf("dev run = %+v, want running + run_task_id", dev)
	}

	// double-run of the same name rejected
	if _, err := s.RequestRun(ctx(), w.ID, "dev"); err != ErrAlreadyRunning {
		t.Fatalf("RequestRun(dev again) = %v, want ErrAlreadyRunning", err)
	}

	// daemon claims the run job with the name + run channel
	runs, err := s.ClaimRunJobs(ctx(), "daemon-1")
	if err != nil {
		t.Fatalf("ClaimRunJobs: %v", err)
	}
	var runJob *shared.Job
	for i := range runs {
		if runs[i].WorktreeID == w.ID {
			runJob = &runs[i]
		}
	}
	if runJob == nil || runJob.Kind != shared.JobRun || runJob.ScriptName != "dev" || runJob.RunTaskID != dev.RunTaskID {
		t.Fatalf("run job = %+v, want run/dev/run_task_id", runJob)
	}

	// report the named run complete
	if _, err := s.ReportStatus(ctx(), "daemon-1", w.ID, shared.StatusReport{
		Kind: shared.JobRun, ScriptName: "dev", RunStatus: shared.RunSucceeded,
	}); err != nil {
		t.Fatalf("report run: %v", err)
	}
	got, _ := s.Get(ctx(), w.ID)
	if got.Runs[0].Status != shared.RunSucceeded {
		t.Fatalf("dev status = %q, want succeeded", got.Runs[0].Status)
	}
}

func TestRequestRunOfflineDaemon(t *testing.T) {
	s := NewStore(testPool)
	// ready but the owning daemon never checked in → offline
	w, err := s.CreateWorktreeRow(ctx(), newIssueID(), "WAT-5", testWorkspaceID, repoA)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	_, _ = s.ClaimInitJobs(ctx(), "ghost-daemon", testWorkspaceID)
	_, _ = s.ReportStatus(ctx(), "ghost-daemon", w.ID, shared.StatusReport{Kind: shared.JobInit, Status: shared.StatusReady, RunScripts: []string{"dev"}})
	if _, err := s.RequestRun(ctx(), w.ID, "dev"); err != ErrDaemonOffline {
		t.Fatalf("RequestRun(offline) = %v, want ErrDaemonOffline", err)
	}
}

func TestRequestSetupRerun(t *testing.T) {
	s := NewStore(testPool)
	// no setup discovered → ErrNoSetupScript
	noSetup := makeReady(t, s, newIssueID(), "WAT-6", repoB, "daemon-1", shared.StatusReport{HasSetup: false})
	if _, err := s.RequestSetup(ctx(), noSetup.ID); err != ErrNoSetupScript {
		t.Fatalf("RequestSetup(no setup) = %v, want ErrNoSetupScript", err)
	}

	w := makeReady(t, s, newIssueID(), "WAT-8", repoA, "daemon-1", shared.StatusReport{
		SetupStatus: shared.ScriptSucceeded, HasSetup: true,
	})
	out, err := s.RequestSetup(ctx(), w.ID)
	if err != nil {
		t.Fatalf("RequestSetup(ready) = %v, want success", err)
	}
	if out.SetupStatus != shared.ScriptRunning || out.SetupTaskID == "" {
		t.Fatalf("after RequestSetup = %+v, want setup running + setup_task_id", out)
	}

	// daemon claims a setup job (setup channel travels; script body does not)
	actions, err := s.ClaimWorktreeActionJobs(ctx(), "daemon-1")
	if err != nil {
		t.Fatalf("ClaimWorktreeActionJobs: %v", err)
	}
	var setupJob *shared.Job
	for i := range actions {
		if actions[i].WorktreeID == w.ID {
			setupJob = &actions[i]
		}
	}
	if setupJob == nil || setupJob.Kind != shared.JobSetup || setupJob.SetupTaskID != out.SetupTaskID {
		t.Fatalf("setup action job = %+v, want setup/setup_task_id", setupJob)
	}

	// report setup result — only setup_status changes, worktree stays ready
	if _, err := s.ReportStatus(ctx(), "daemon-1", w.ID, shared.StatusReport{Kind: shared.JobSetup, SetupStatus: shared.ScriptFailed, Error: "boom"}); err != nil {
		t.Fatalf("report setup: %v", err)
	}
	got, _ := s.Get(ctx(), w.ID)
	if got.SetupStatus != shared.ScriptFailed || got.Status != shared.StatusReady {
		t.Fatalf("after setup report = status %q setup %q, want ready + failed", got.Status, got.SetupStatus)
	}
}

func TestMarkCleanupForIssue(t *testing.T) {
	s := NewStore(testPool)
	issueID := newIssueID()
	ready := makeReady(t, s, issueID, "WAT-9", repoA, "daemon-1", shared.StatusReport{})
	pending, _ := s.CreateWorktreeRow(ctx(), issueID, "WAT-9", testWorkspaceID, repoB)

	if _, err := s.MarkCleanupForIssue(ctx(), issueID); err != nil {
		t.Fatalf("MarkCleanupForIssue: %v", err)
	}
	gotReady, _ := s.Get(ctx(), ready.ID)
	if gotReady.Status != shared.StatusCleaning {
		t.Fatalf("ready row status = %q, want cleaning", gotReady.Status)
	}
	gotPending, _ := s.Get(ctx(), pending.ID)
	if gotPending.Status != shared.StatusRemoved {
		t.Fatalf("pending row status = %q, want removed", gotPending.Status)
	}
}

func TestIssueStatusReadiness(t *testing.T) {
	s := NewStore(testPool)
	// unmanaged issue → managed false
	st, err := s.IssueStatus(ctx(), newIssueID(), testWorkspaceID)
	if err != nil {
		t.Fatalf("IssueStatus: %v", err)
	}
	if st.Managed || len(st.Worktrees) != 0 {
		t.Fatalf("unmanaged issue = %+v, want managed=false", st)
	}

	issueID := newIssueID()
	w := makeReady(t, s, issueID, "WAT-10", repoA, "daemon-1", shared.StatusReport{SetupStatus: shared.ScriptSucceeded})
	st, _ = s.IssueStatus(ctx(), issueID, testWorkspaceID)
	if !st.Managed || len(st.Worktrees) != 1 || st.Worktrees[0].Status != shared.StatusReady {
		t.Fatalf("managed issue = %+v, want one ready worktree", st)
	}
	_ = w
}

// A late init report for an issue that was closed mid-checkout must NOT
// resurrect the removed worktree (it would orphan an on-disk tree for a closed
// issue that never gets cleaned up).
func TestReportInitDoesNotResurrectRemoved(t *testing.T) {
	s := NewStore(testPool)
	issueID := newIssueID()
	w, err := s.CreateWorktreeRow(ctx(), issueID, "WAT-50", testWorkspaceID, repoA)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	// claim → initializing (owned), then the issue closes mid-checkout → removed.
	if _, err := s.ClaimInitJobs(ctx(), "daemon-1", testWorkspaceID); err != nil {
		t.Fatalf("claim: %v", err)
	}
	if _, err := s.MarkCleanupForIssue(ctx(), issueID); err != nil {
		t.Fatalf("cleanup: %v", err)
	}
	if g, _ := s.Get(ctx(), w.ID); g.Status != shared.StatusRemoved {
		t.Fatalf("expected removed after cleanup of an initializing row, got %q", g.Status)
	}
	// The daemon finishes and posts a late init report — it must be a no-op.
	if _, err := s.ReportStatus(ctx(), "daemon-1", w.ID, shared.StatusReport{
		Kind: shared.JobInit, Status: shared.StatusReady, RunScripts: []string{"dev"},
	}); err != nil {
		t.Fatalf("late report: %v", err)
	}
	g, _ := s.Get(ctx(), w.ID)
	if g.Status != shared.StatusRemoved {
		t.Fatalf("late init report resurrected the worktree to %q", g.Status)
	}
	if len(g.Runs) != 0 {
		t.Fatalf("late init report recreated run rows: %+v", g.Runs)
	}
}

// Reopening a previously-closed, still-assigned issue must recreate its
// workspace: CreateWorktreeRow re-arms the removed tombstone back to pending.
func TestReopenReactivatesRemovedWorktree(t *testing.T) {
	s := NewStore(testPool)
	issueID := newIssueID()
	w := makeReady(t, s, issueID, "WAT-51", repoA, "daemon-1", shared.StatusReport{RunScripts: []string{"dev"}})
	// close it: ready → cleaning → removed.
	if _, err := s.MarkCleanupForIssue(ctx(), issueID); err != nil {
		t.Fatalf("cleanup: %v", err)
	}
	if _, err := s.ReportStatus(ctx(), "daemon-1", w.ID, shared.StatusReport{Kind: shared.JobCleanup}); err != nil {
		t.Fatalf("report cleanup: %v", err)
	}
	if g, _ := s.Get(ctx(), w.ID); g.Status != shared.StatusRemoved {
		t.Fatalf("expected removed, got %q", g.Status)
	}
	// reopen + still assigned → the row is reactivated to a clean pending state.
	re, err := s.CreateWorktreeRow(ctx(), issueID, "WAT-51", testWorkspaceID, repoA)
	if err != nil {
		t.Fatalf("recreate: %v", err)
	}
	if re.Status != shared.StatusPending || re.OwnerDaemonID != "" || len(re.Runs) != 0 {
		t.Fatalf("reactivated row = %+v, want pending + no owner + no runs", re)
	}
	// and it is re-claimable by the daemon.
	jobs, _ := s.ClaimInitJobs(ctx(), "daemon-1", testWorkspaceID)
	found := false
	for _, j := range jobs {
		if j.WorktreeID == re.ID {
			found = true
		}
	}
	if !found {
		t.Fatalf("reactivated worktree was not re-claimed by ClaimInitJobs")
	}
}

// A daemon restart leaves 'running' run rows behind (their OS processes died);
// the reconcile resets them so Stop/Run work again.
func TestReconcileResetsStaleRuns(t *testing.T) {
	s := NewStore(testPool)
	w := makeReady(t, s, newIssueID(), "WAT-52", repoA, "daemon-1", shared.StatusReport{RunScripts: []string{"dev"}})
	if _, err := s.RequestRun(ctx(), w.ID, "dev"); err != nil {
		t.Fatalf("run: %v", err)
	}
	// simulate daemon restart reconciling the workspace.
	if err := s.ResetRunningRuns(ctx(), "daemon-1", testWorkspaceID); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	g, _ := s.Get(ctx(), w.ID)
	if len(g.Runs) != 1 || g.Runs[0].Status != shared.RunStopped {
		t.Fatalf("after reconcile, runs = %+v, want dev stopped", g.Runs)
	}
	// the stuck run is runnable again.
	if _, err := s.RequestRun(ctx(), w.ID, "dev"); err != nil {
		t.Fatalf("re-run after reconcile = %v, want success", err)
	}
}

// --- listener tests ---

func TestListenerCheckoutOnAssignment(t *testing.T) {
	sink := &eventSink{}
	m := newTestModule("owner", sink)

	// unassigned → no rows
	unassigned := newIssueID()
	m.onIssueEvent(IssueEvent{Type: "issue:created", WorkspaceID: testWorkspaceID, IssueID: unassigned, Status: "todo"})
	if rows, _ := m.store.ListByIssue(ctx(), unassigned, testWorkspaceID); len(rows) != 0 {
		t.Fatalf("unassigned issue created %d rows, want 0", len(rows))
	}

	// assigned to an agent but parked in backlog → no rows
	backlog := newIssueID()
	m.onIssueEvent(IssueEvent{Type: "issue:updated", WorkspaceID: testWorkspaceID, IssueID: backlog, Identifier: "WAT-20", Status: "backlog", AssigneeType: "agent", AssigneeID: "a1", AssigneeChanged: true})
	if rows, _ := m.store.ListByIssue(ctx(), backlog, testWorkspaceID); len(rows) != 0 {
		t.Fatalf("backlog assign created %d rows, want 0", len(rows))
	}

	// assigned to an agent in a workable status → one row per repo, with identifier
	onIssue := newIssueID()
	m.onIssueEvent(IssueEvent{Type: "issue:updated", WorkspaceID: testWorkspaceID, IssueID: onIssue, Identifier: "WAT-21", Status: "todo", AssigneeType: "agent", AssigneeID: "a1", AssigneeChanged: true})
	rows, _ := m.store.ListByIssue(ctx(), onIssue, testWorkspaceID)
	if len(rows) != 2 {
		t.Fatalf("agent assign created %d rows, want 2 (one per repo)", len(rows))
	}
	for _, r := range rows {
		if r.Identifier != "WAT-21" {
			t.Fatalf("row identifier = %q, want WAT-21", r.Identifier)
		}
	}
	if sink.count(shared.EventWorktreeUpdated) < 2 {
		t.Fatalf("expected >= 2 worktree:updated events, got %d", sink.count(shared.EventWorktreeUpdated))
	}
}

func TestListenerCleanupOnDoneOrCancelled(t *testing.T) {
	sink := &eventSink{}
	m := newTestModule("owner", sink)

	for _, terminal := range []string{"done", "cancelled"} {
		issueID := newIssueID()
		row := makeReady(t, m.store, issueID, "WAT-30", repoA, "daemon-1", shared.StatusReport{})

		// not a status change → no-op
		m.onIssueEvent(IssueEvent{Type: "issue:updated", WorkspaceID: testWorkspaceID, IssueID: issueID, Status: terminal, PrevStatus: terminal, StatusChanged: false})
		if g, _ := m.store.Get(ctx(), row.ID); g.Status != shared.StatusReady {
			t.Fatalf("[%s] no-op transition changed status to %q", terminal, g.Status)
		}
		// transition INTO terminal → cleaning
		m.onIssueEvent(IssueEvent{Type: "issue:updated", WorkspaceID: testWorkspaceID, IssueID: issueID, Status: terminal, PrevStatus: "in_progress", StatusChanged: true})
		if g, _ := m.store.Get(ctx(), row.ID); g.Status != shared.StatusCleaning {
			t.Fatalf("[%s] transition status = %q, want cleaning", terminal, g.Status)
		}
	}
}

// --- HTTP tests ---

func TestHTTPListAndRunGating(t *testing.T) {
	sink := &eventSink{}
	m := newTestModule("owner", sink)
	router := testRouter(m)
	issueID := newIssueID()
	row, _ := m.store.CreateWorktreeRow(ctx(), issueID, "WAT-40", testWorkspaceID, repoA)

	// GET list
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest("GET", "/api/worktree/issues/"+issueID, nil))
	if w.Code != http.StatusOK {
		t.Fatalf("GET list = %d: %s", w.Code, w.Body.String())
	}
	var list []shared.Worktree
	if err := json.NewDecoder(w.Body).Decode(&list); err != nil || len(list) != 1 {
		t.Fatalf("list decode err=%v len=%d, want 1", err, len(list))
	}

	// POST run on a pending worktree → 409
	w = httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest("POST", "/api/worktree/issues/"+issueID+"/"+row.ID+"/run", strings.NewReader(`{"name":"dev"}`)))
	if w.Code != http.StatusConflict {
		t.Fatalf("POST run(pending) = %d, want 409: %s", w.Code, w.Body.String())
	}

	// POST run with no name → 400
	w = httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest("POST", "/api/worktree/issues/"+issueID+"/"+row.ID+"/run", strings.NewReader(`{}`)))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("POST run(no name) = %d, want 400: %s", w.Code, w.Body.String())
	}
}

func TestHTTPDaemonLogStreaming(t *testing.T) {
	sink := &eventSink{}
	m := newTestModule("owner", sink)
	router := testRouter(m)
	w := makeReady(t, m.store, newIssueID(), "WAT-41", repoA, "daemon-1", shared.StatusReport{RunScripts: []string{"dev"}})
	armed, err := m.store.RequestRun(ctx(), w.ID, "dev")
	if err != nil {
		t.Fatalf("RequestRun: %v", err)
	}
	var runTaskID string
	for _, r := range armed.Runs {
		if r.Name == "dev" {
			runTaskID = r.RunTaskID
		}
	}

	before := sink.count(shared.EventWorktreeRunLog)
	body := `{"lines":[{"seq":1,"stream":"stdout","content":"hello"},{"seq":2,"stream":"stderr","content":"warn"}]}`
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest("POST", "/api/daemon/worktree/runs/"+runTaskID+"/logs", strings.NewReader(body)))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("POST logs = %d, want 204: %s", rec.Code, rec.Body.String())
	}
	if got := sink.count(shared.EventWorktreeRunLog) - before; got != 2 {
		t.Fatalf("published %d run-log events, want 2", got)
	}
}

// Setup logs stream to setup_task_id, a different channel than a run's
// run_task_id; the log endpoint resolves the worktree from either.
func TestHTTPDaemonSetupLogStreaming(t *testing.T) {
	sink := &eventSink{}
	m := newTestModule("owner", sink)
	router := testRouter(m)
	w := makeReady(t, m.store, newIssueID(), "WAT-42", repoA, "daemon-1", shared.StatusReport{
		SetupStatus: shared.ScriptSucceeded, HasSetup: true,
	})
	armed, err := m.store.RequestSetup(ctx(), w.ID)
	if err != nil {
		t.Fatalf("RequestSetup: %v", err)
	}
	if armed.SetupTaskID == "" {
		t.Fatalf("setup channel empty")
	}

	before := sink.count(shared.EventWorktreeRunLog)
	body := `{"lines":[{"seq":1,"stream":"stdout","content":"setup line"}]}`
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest("POST", "/api/daemon/worktree/runs/"+armed.SetupTaskID+"/logs", strings.NewReader(body)))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("POST setup logs = %d, want 204: %s", rec.Code, rec.Body.String())
	}
	if got := sink.count(shared.EventWorktreeRunLog) - before; got != 1 {
		t.Fatalf("published %d setup-log events, want 1", got)
	}
}

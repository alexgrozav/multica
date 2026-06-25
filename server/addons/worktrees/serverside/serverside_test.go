package serverside

import (
	"context"
	"encoding/json"
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
	// The host schema must be migrated (we read workspace.repos). The default
	// `multica` DB may be empty; the migrated DB is the worktree's isolated one.
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
	// Worktree tables key by workspace_id; resolve via slug to survive a prior
	// crashed run where testWorkspaceID isn't set yet.
	var wsID string
	_ = pool.QueryRow(ctx, `SELECT id FROM workspace WHERE slug=$1`, testWorkspaceSlug).Scan(&wsID)
	if wsID != "" {
		for _, tbl := range []string{"issue_worktree", "worktree_repo_script", "worktree_settings", "worktree_daemon_seen"} {
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

func newTestModule(role, daemonID string, sink *eventSink) *Module {
	_ = daemonID // daemon identity now travels as a query param, not via Deps
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

// --- store tests ---

func TestConfigRoundTrip(t *testing.T) {
	s := NewStore(testPool)
	cfg := shared.Config{AutoInit: true, Repos: []shared.RepoScript{
		{RepoURL: repoA, Setup: "npm ci", Run: "npm run dev", Cleanup: "echo bye"},
		{RepoURL: repoB, Setup: "make", Run: "", Cleanup: ""},
	}}
	if err := s.PutConfig(ctx(), testWorkspaceID, cfg); err != nil {
		t.Fatalf("PutConfig: %v", err)
	}
	got, err := s.GetConfig(ctx(), testWorkspaceID)
	if err != nil {
		t.Fatalf("GetConfig: %v", err)
	}
	if !got.AutoInit || len(got.Repos) != 2 {
		t.Fatalf("config = %+v, want auto_init + 2 repos", got)
	}
	auto, err := s.AutoInit(ctx(), testWorkspaceID)
	if err != nil || !auto {
		t.Fatalf("AutoInit = %v, %v; want true", auto, err)
	}

	// Dropping a repo from config removes its scripts.
	if err := s.PutConfig(ctx(), testWorkspaceID, shared.Config{AutoInit: false, Repos: []shared.RepoScript{{RepoURL: repoA, Run: "x"}}}); err != nil {
		t.Fatalf("PutConfig shrink: %v", err)
	}
	got, _ = s.GetConfig(ctx(), testWorkspaceID)
	if got.AutoInit || len(got.Repos) != 1 || got.Repos[0].RepoURL != repoA {
		t.Fatalf("after shrink config = %+v, want auto_init=false + only repoA", got)
	}
}

func TestCreateAndListWorktrees(t *testing.T) {
	s := NewStore(testPool)
	issueID := newIssueID()
	// run script configured for repoA so has_run_script is true
	if err := s.PutConfig(ctx(), testWorkspaceID, shared.Config{Repos: []shared.RepoScript{{RepoURL: repoA, Run: "npm run dev"}}}); err != nil {
		t.Fatalf("PutConfig: %v", err)
	}

	w1, err := s.CreateWorktreeRow(ctx(), issueID, testWorkspaceID, repoA)
	if err != nil {
		t.Fatalf("CreateWorktreeRow: %v", err)
	}
	if w1.Status != shared.StatusPending || !w1.HasRunScript {
		t.Fatalf("row = %+v, want pending + has_run_script", w1)
	}
	// idempotent on (issue, repo)
	if _, err := s.CreateWorktreeRow(ctx(), issueID, testWorkspaceID, repoA); err != nil {
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

func TestClaimInitJobsAtomicAndScoped(t *testing.T) {
	s := NewStore(testPool)
	issueID := newIssueID()
	_ = s.PutConfig(ctx(), testWorkspaceID, shared.Config{Repos: []shared.RepoScript{{RepoURL: repoA, Setup: "setup-a"}}})
	if _, err := s.CreateWorktreeRow(ctx(), issueID, testWorkspaceID, repoA); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := s.CreateWorktreeRow(ctx(), issueID, testWorkspaceID, repoB); err != nil {
		t.Fatalf("create: %v", err)
	}

	jobs, err := s.ClaimInitJobs(ctx(), "daemon-1", testWorkspaceID)
	if err != nil {
		t.Fatalf("ClaimInitJobs: %v", err)
	}
	if len(jobs) < 2 {
		t.Fatalf("claimed %d init jobs, want >= 2", len(jobs))
	}
	var sawSetup bool
	for _, j := range jobs {
		if j.Kind != shared.JobInit || j.WorkspaceID != testWorkspaceID {
			t.Fatalf("job = %+v, want init/workspace-scoped", j)
		}
		// Every claimed init allocates a setup channel up-front so the boot
		// Setup's output can stream to the UI's default Setup tab.
		if j.SetupTaskID == "" {
			t.Fatalf("init job = %+v, want a setup_task_id allocated", j)
		}
		if j.RepoURL == repoA && j.Setup == "setup-a" {
			sawSetup = true
		}
	}
	if !sawSetup {
		t.Fatalf("init job for repoA missing its setup script")
	}

	// A second daemon claims nothing — they were atomically taken.
	again, err := s.ClaimInitJobs(ctx(), "daemon-2", testWorkspaceID)
	if err != nil {
		t.Fatalf("ClaimInitJobs 2: %v", err)
	}
	for _, j := range again {
		if j.IssueID == issueID {
			t.Fatalf("second daemon re-claimed an already-claimed init job: %+v", j)
		}
	}
}

func TestRequestRunGatingAndActionClaim(t *testing.T) {
	s := NewStore(testPool)
	issueID := newIssueID()
	_ = s.PutConfig(ctx(), testWorkspaceID, shared.Config{Repos: []shared.RepoScript{{RepoURL: repoA, Run: "npm run dev"}}})
	w, err := s.CreateWorktreeRow(ctx(), issueID, testWorkspaceID, repoA)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	// pending → not ready
	if _, err := s.RequestRun(ctx(), w.ID); err != ErrNotReady {
		t.Fatalf("RequestRun(pending) = %v, want ErrNotReady", err)
	}

	// claim + report ready, owned by daemon-1
	if _, err := s.ClaimInitJobs(ctx(), "daemon-1", testWorkspaceID); err != nil {
		t.Fatalf("claim: %v", err)
	}
	if _, err := s.ReportStatus(ctx(), "daemon-1", w.ID, shared.StatusReport{Kind: shared.JobInit, Status: shared.StatusReady, Path: "/tmp/wt", Branch: "issue/x/a", SetupStatus: shared.ScriptSucceeded}); err != nil {
		t.Fatalf("ReportStatus ready: %v", err)
	}

	// ready but owning daemon hasn't been seen → offline
	if _, err := s.RequestRun(ctx(), w.ID); err != ErrDaemonOffline {
		t.Fatalf("RequestRun(offline) = %v, want ErrDaemonOffline", err)
	}

	// daemon checks in → run is armed
	if err := s.TouchDaemon(ctx(), testWorkspaceID, "daemon-1"); err != nil {
		t.Fatalf("TouchDaemon: %v", err)
	}
	out, err := s.RequestRun(ctx(), w.ID)
	if err != nil {
		t.Fatalf("RequestRun(ready,online) = %v, want success", err)
	}
	if out.RunStatus != shared.RunRunning || out.RunTaskID == "" {
		t.Fatalf("after RequestRun = %+v, want running + run_task_id", out)
	}

	// double-run rejected
	if _, err := s.RequestRun(ctx(), w.ID); err != ErrAlreadyRunning {
		t.Fatalf("RequestRun(running) = %v, want ErrAlreadyRunning", err)
	}

	// daemon claims the run action (pending_action cleared, run script included)
	actions, err := s.ClaimActionJobs(ctx(), "daemon-1")
	if err != nil {
		t.Fatalf("ClaimActionJobs: %v", err)
	}
	var runJob *shared.Job
	for i := range actions {
		if actions[i].WorktreeID == w.ID {
			runJob = &actions[i]
		}
	}
	if runJob == nil || runJob.Kind != shared.JobRun || runJob.Run != "npm run dev" || runJob.RunTaskID != out.RunTaskID {
		t.Fatalf("run action job = %+v, want run/script/run_task_id", runJob)
	}

	// report run complete
	if _, err := s.ReportStatus(ctx(), "daemon-1", w.ID, shared.StatusReport{Kind: shared.JobRun, RunStatus: shared.RunSucceeded}); err != nil {
		t.Fatalf("ReportStatus run: %v", err)
	}
	got, _ := s.Get(ctx(), w.ID)
	if got.RunStatus != shared.RunSucceeded {
		t.Fatalf("run_status = %q, want succeeded", got.RunStatus)
	}

	// no run script configured → ErrNoRunScript
	_ = s.PutConfig(ctx(), testWorkspaceID, shared.Config{Repos: []shared.RepoScript{{RepoURL: repoA}}})
	if _, err := s.RequestRun(ctx(), w.ID); err != ErrNoRunScript {
		t.Fatalf("RequestRun(no script) = %v, want ErrNoRunScript", err)
	}
}

func TestMarkCleanupForIssue(t *testing.T) {
	s := NewStore(testPool)
	issueID := newIssueID()
	ready, _ := s.CreateWorktreeRow(ctx(), issueID, testWorkspaceID, repoA)
	pending, _ := s.CreateWorktreeRow(ctx(), issueID, testWorkspaceID, repoB)
	// promote `ready` to ready
	_, _ = s.ClaimInitJobs(ctx(), "daemon-1", testWorkspaceID)
	_, _ = s.ReportStatus(ctx(), "daemon-1", ready.ID, shared.StatusReport{Kind: shared.JobInit, Status: shared.StatusReady})
	// force `pending` back to pending (claim moved it to initializing)
	_, _ = testPool.Exec(ctx(), `UPDATE issue_worktree SET status='pending', owner_daemon_id='' WHERE id=$1`, pending.ID)

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

func TestRequestSetupRerun(t *testing.T) {
	s := NewStore(testPool)
	issueID := newIssueID()
	_ = s.PutConfig(ctx(), testWorkspaceID, shared.Config{Repos: []shared.RepoScript{{RepoURL: repoA, Setup: "make setup"}}})
	w, err := s.CreateWorktreeRow(ctx(), issueID, testWorkspaceID, repoA)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if !w.HasSetupScript {
		t.Fatalf("row should report has_setup_script")
	}

	// pending → not ready
	if _, err := s.RequestSetup(ctx(), w.ID); err != ErrNotReady {
		t.Fatalf("RequestSetup(pending) = %v, want ErrNotReady", err)
	}

	// make ready, owned, online
	if _, err := s.ClaimInitJobs(ctx(), "daemon-1", testWorkspaceID); err != nil {
		t.Fatalf("claim: %v", err)
	}
	if _, err := s.ReportStatus(ctx(), "daemon-1", w.ID, shared.StatusReport{Kind: shared.JobInit, Status: shared.StatusReady, SetupStatus: shared.ScriptSucceeded}); err != nil {
		t.Fatalf("report ready: %v", err)
	}
	if err := s.TouchDaemon(ctx(), testWorkspaceID, "daemon-1"); err != nil {
		t.Fatalf("touch: %v", err)
	}

	out, err := s.RequestSetup(ctx(), w.ID)
	if err != nil {
		t.Fatalf("RequestSetup(ready) = %v, want success", err)
	}
	if out.SetupStatus != shared.ScriptRunning || out.SetupTaskID == "" {
		t.Fatalf("after RequestSetup = %+v, want setup running + setup_task_id", out)
	}

	// daemon claims a setup job (with the setup script + setup channel)
	actions, err := s.ClaimActionJobs(ctx(), "daemon-1")
	if err != nil {
		t.Fatalf("ClaimActionJobs: %v", err)
	}
	var setupJob *shared.Job
	for i := range actions {
		if actions[i].WorktreeID == w.ID {
			setupJob = &actions[i]
		}
	}
	if setupJob == nil || setupJob.Kind != shared.JobSetup || setupJob.Setup != "make setup" || setupJob.SetupTaskID != out.SetupTaskID {
		t.Fatalf("setup action job = %+v, want setup/script/setup_task_id", setupJob)
	}

	// report setup result — only setup_status changes
	if _, err := s.ReportStatus(ctx(), "daemon-1", w.ID, shared.StatusReport{Kind: shared.JobSetup, SetupStatus: shared.ScriptFailed, Error: "boom"}); err != nil {
		t.Fatalf("report setup: %v", err)
	}
	got, _ := s.Get(ctx(), w.ID)
	if got.SetupStatus != shared.ScriptFailed || got.Status != shared.StatusReady {
		t.Fatalf("after setup report = status %q setup %q, want ready + failed", got.Status, got.SetupStatus)
	}

	// no setup script configured → ErrNoSetupScript
	_ = s.PutConfig(ctx(), testWorkspaceID, shared.Config{Repos: []shared.RepoScript{{RepoURL: repoA}}})
	if _, err := s.RequestSetup(ctx(), w.ID); err != ErrNoSetupScript {
		t.Fatalf("RequestSetup(no script) = %v, want ErrNoSetupScript", err)
	}
}

// --- listener tests ---

func TestListenerAutoInit(t *testing.T) {
	sink := &eventSink{}
	m := newTestModule("owner", "daemon-1", sink)

	// auto_init OFF → no rows created
	_ = m.store.PutConfig(ctx(), testWorkspaceID, shared.Config{AutoInit: false})
	offIssue := newIssueID()
	m.onIssueCreated(IssueEvent{Type: "issue:created", WorkspaceID: testWorkspaceID, IssueID: offIssue})
	if rows, _ := m.store.ListByIssue(ctx(), offIssue, testWorkspaceID); len(rows) != 0 {
		t.Fatalf("auto_init off created %d rows, want 0", len(rows))
	}

	// auto_init ON → one row per workspace repo (2)
	_ = m.store.PutConfig(ctx(), testWorkspaceID, shared.Config{AutoInit: true})
	onIssue := newIssueID()
	m.onIssueCreated(IssueEvent{Type: "issue:created", WorkspaceID: testWorkspaceID, IssueID: onIssue})
	rows, _ := m.store.ListByIssue(ctx(), onIssue, testWorkspaceID)
	if len(rows) != 2 {
		t.Fatalf("auto_init on created %d rows, want 2 (one per repo)", len(rows))
	}
	if sink.count(shared.EventWorktreeUpdated) < 2 {
		t.Fatalf("expected >= 2 worktree:updated events, got %d", sink.count(shared.EventWorktreeUpdated))
	}

	// agent/squad-assigned issues skip the persistent worktree — the agent task
	// eagerly checks out + runs Setup in its own workdir instead.
	for _, at := range []string{"agent", "squad"} {
		iss := newIssueID()
		m.onIssueCreated(IssueEvent{Type: "issue:created", WorkspaceID: testWorkspaceID, IssueID: iss, AssigneeType: at})
		if rows, _ := m.store.ListByIssue(ctx(), iss, testWorkspaceID); len(rows) != 0 {
			t.Fatalf("assignee %q created %d rows, want 0 (skipped)", at, len(rows))
		}
	}

	// member-assigned (and unassigned) issues still get persistent worktrees.
	memIssue := newIssueID()
	m.onIssueCreated(IssueEvent{Type: "issue:created", WorkspaceID: testWorkspaceID, IssueID: memIssue, AssigneeType: "member"})
	if rows, _ := m.store.ListByIssue(ctx(), memIssue, testWorkspaceID); len(rows) != 2 {
		t.Fatalf("member-assigned created %d rows, want 2", len(rows))
	}
}

func TestListenerCleanupOnlyOnTransitionIntoDone(t *testing.T) {
	sink := &eventSink{}
	m := newTestModule("owner", "daemon-1", sink)
	issueID := newIssueID()
	row, _ := m.store.CreateWorktreeRow(ctx(), issueID, testWorkspaceID, repoA)
	_, _ = m.store.ClaimInitJobs(ctx(), "daemon-1", testWorkspaceID)
	_, _ = m.store.ReportStatus(ctx(), "daemon-1", row.ID, shared.StatusReport{Kind: shared.JobInit, Status: shared.StatusReady})

	// not a status change → no-op
	m.onIssueUpdated(IssueEvent{Type: "issue:updated", WorkspaceID: testWorkspaceID, IssueID: issueID, Status: "done", PrevStatus: "done", StatusChanged: false})
	if g, _ := m.store.Get(ctx(), row.ID); g.Status != shared.StatusReady {
		t.Fatalf("no-op transition changed status to %q", g.Status)
	}
	// transition to a non-done status → no-op
	m.onIssueUpdated(IssueEvent{Type: "issue:updated", WorkspaceID: testWorkspaceID, IssueID: issueID, Status: "in_progress", PrevStatus: "todo", StatusChanged: true})
	if g, _ := m.store.Get(ctx(), row.ID); g.Status != shared.StatusReady {
		t.Fatalf("in_progress transition changed status to %q", g.Status)
	}
	// transition INTO done → cleaning
	m.onIssueUpdated(IssueEvent{Type: "issue:updated", WorkspaceID: testWorkspaceID, IssueID: issueID, Status: "done", PrevStatus: "in_progress", StatusChanged: true})
	if g, _ := m.store.Get(ctx(), row.ID); g.Status != shared.StatusCleaning {
		t.Fatalf("done transition status = %q, want cleaning", g.Status)
	}
}

// --- HTTP tests ---

func TestHTTPListAndRunGating(t *testing.T) {
	sink := &eventSink{}
	m := newTestModule("owner", "daemon-1", sink)
	router := testRouter(m)
	issueID := newIssueID()
	row, _ := m.store.CreateWorktreeRow(ctx(), issueID, testWorkspaceID, repoA)

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
	router.ServeHTTP(w, httptest.NewRequest("POST", "/api/worktree/issues/"+issueID+"/"+row.ID+"/run", nil))
	if w.Code != http.StatusConflict {
		t.Fatalf("POST run(pending) = %d, want 409: %s", w.Code, w.Body.String())
	}
}

func TestHTTPConfigPermissions(t *testing.T) {
	sink := &eventSink{}
	owner := testRouter(newTestModule("owner", "daemon-1", sink))
	member := testRouter(newTestModule("member", "daemon-1", sink))

	body := `{"auto_init":true,"repos":[{"repo_url":"https://example.com/a.git","setup":"s","run":"r","cleanup":"c"}]}`

	// member cannot write
	w := httptest.NewRecorder()
	req := httptest.NewRequest("PUT", "/api/worktree/config", strings.NewReader(body))
	member.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("member PUT config = %d, want 403", w.Code)
	}

	// owner can write
	w = httptest.NewRecorder()
	req = httptest.NewRequest("PUT", "/api/worktree/config", strings.NewReader(body))
	owner.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("owner PUT config = %d, want 200: %s", w.Code, w.Body.String())
	}

	// GET reflects it
	w = httptest.NewRecorder()
	owner.ServeHTTP(w, httptest.NewRequest("GET", "/api/worktree/config", nil))
	var cfg shared.Config
	_ = json.NewDecoder(w.Body).Decode(&cfg)
	if !cfg.AutoInit || len(cfg.Repos) != 1 {
		t.Fatalf("GET config = %+v, want auto_init + 1 repo", cfg)
	}
}

func TestHTTPDaemonLogStreaming(t *testing.T) {
	sink := &eventSink{}
	m := newTestModule("owner", "daemon-1", sink)
	router := testRouter(m)
	issueID := newIssueID()
	_ = m.store.PutConfig(ctx(), testWorkspaceID, shared.Config{Repos: []shared.RepoScript{{RepoURL: repoA, Run: "x"}}})
	row, _ := m.store.CreateWorktreeRow(ctx(), issueID, testWorkspaceID, repoA)
	_, _ = m.store.ClaimInitJobs(ctx(), "daemon-1", testWorkspaceID)
	_, _ = m.store.ReportStatus(ctx(), "daemon-1", row.ID, shared.StatusReport{Kind: shared.JobInit, Status: shared.StatusReady})
	_ = m.store.TouchDaemon(ctx(), testWorkspaceID, "daemon-1")
	armed, err := m.store.RequestRun(ctx(), row.ID)
	if err != nil {
		t.Fatalf("RequestRun: %v", err)
	}

	before := sink.count(shared.EventWorktreeRunLog)
	body := `{"lines":[{"seq":1,"stream":"stdout","content":"hello"},{"seq":2,"stream":"stderr","content":"warn"}]}`
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest("POST", "/api/daemon/worktree/runs/"+armed.RunTaskID+"/logs", strings.NewReader(body)))
	if w.Code != http.StatusNoContent {
		t.Fatalf("POST logs = %d, want 204: %s", w.Code, w.Body.String())
	}
	if got := sink.count(shared.EventWorktreeRunLog) - before; got != 2 {
		t.Fatalf("published %d run-log events, want 2", got)
	}
}

// Setup logs stream to setup_task_id, a different channel than run_task_id; the
// log endpoint must resolve the worktree from either (GetByRunTaskID).
func TestHTTPDaemonSetupLogStreaming(t *testing.T) {
	sink := &eventSink{}
	m := newTestModule("owner", "daemon-1", sink)
	router := testRouter(m)
	issueID := newIssueID()
	_ = m.store.PutConfig(ctx(), testWorkspaceID, shared.Config{Repos: []shared.RepoScript{{RepoURL: repoA, Setup: "make"}}})
	row, _ := m.store.CreateWorktreeRow(ctx(), issueID, testWorkspaceID, repoA)
	_, _ = m.store.ClaimInitJobs(ctx(), "daemon-1", testWorkspaceID)
	_, _ = m.store.ReportStatus(ctx(), "daemon-1", row.ID, shared.StatusReport{Kind: shared.JobInit, Status: shared.StatusReady, SetupStatus: shared.ScriptSucceeded})
	_ = m.store.TouchDaemon(ctx(), testWorkspaceID, "daemon-1")
	armed, err := m.store.RequestSetup(ctx(), row.ID)
	if err != nil {
		t.Fatalf("RequestSetup: %v", err)
	}
	if armed.SetupTaskID == "" || armed.SetupTaskID == armed.RunTaskID {
		t.Fatalf("setup channel = %q (run %q), want a distinct non-empty id", armed.SetupTaskID, armed.RunTaskID)
	}

	before := sink.count(shared.EventWorktreeRunLog)
	body := `{"lines":[{"seq":1,"stream":"stdout","content":"setup line"}]}`
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest("POST", "/api/daemon/worktree/runs/"+armed.SetupTaskID+"/logs", strings.NewReader(body)))
	if w.Code != http.StatusNoContent {
		t.Fatalf("POST setup logs = %d, want 204: %s", w.Code, w.Body.String())
	}
	if got := sink.count(shared.EventWorktreeRunLog) - before; got != 1 {
		t.Fatalf("published %d setup-log events, want 1", got)
	}
}

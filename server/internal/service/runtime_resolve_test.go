package service

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// Fallback-runtime dispatch resolution (agent_fallback_runtime, migration
// 146). These tests pin the four behaviors the feature hangs on, at the
// layer where each decision actually lives:
//
//  1. ResolveDispatchRuntime preference order: current-computer hint >
//     main > fallbacks by position > offline main (queue-and-wait).
//  2. AgentReadiness counts an online fallback as "ready".
//  3. The auto-retry path reroutes runtime-shaped failures onto an online
//     fallback with a forced-fresh session (CLI sessions are machine-local).
//  4. The sweeper's queued-reroute query only touches tasks that the retry
//     path will actually pick up.

func newRuntimeResolvePool(t *testing.T) *pgxpool.Pool {
	t.Helper()

	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		dbURL = "postgres://multica:multica@localhost:5432/multica?sslmode=disable"
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		t.Skipf("database unavailable: %v", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		t.Skipf("database unreachable: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

type runtimeResolveFixture struct {
	agentID     pgtype.UUID
	mainRuntime pgtype.UUID
	fb1Runtime  pgtype.UUID
	fb2Runtime  pgtype.UUID
	mainDaemon  string
	fb1Daemon   string
	fb2Daemon   string
	issueID     pgtype.UUID
	workspaceID string
	userID      string
	pool        *pgxpool.Pool
}

// createRuntimeResolveFixture builds workspace / member / three same-provider
// runtimes (main + two ordered fallbacks) / agent / issue. All runtimes start
// online; individual tests flip status via setRuntimeStatus.
func createRuntimeResolveFixture(t *testing.T, ctx context.Context, pool *pgxpool.Pool) runtimeResolveFixture {
	t.Helper()

	suffix := time.Now().UnixNano()
	email := fmt.Sprintf("runtime-resolve-%d@multica.ai", suffix)
	slug := fmt.Sprintf("runtime-resolve-%d", suffix)
	provider := fmt.Sprintf("resolve_test_%d", suffix)

	var userID string
	if err := pool.QueryRow(ctx, `
		INSERT INTO "user" (name, email) VALUES ($1, $2) RETURNING id
	`, "Runtime Resolve Test", email).Scan(&userID); err != nil {
		t.Fatalf("create user: %v", err)
	}

	var workspaceID string
	if err := pool.QueryRow(ctx, `
		INSERT INTO workspace (name, slug, description, issue_prefix)
		VALUES ($1, $2, $3, $4) RETURNING id
	`, "Runtime Resolve Test", slug, "fallback runtime resolution tests", "RRT").Scan(&workspaceID); err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO member (workspace_id, user_id, role) VALUES ($1, $2, 'owner')
	`, workspaceID, userID); err != nil {
		t.Fatalf("create member: %v", err)
	}

	createRuntime := func(name, daemonID string) string {
		var id string
		if err := pool.QueryRow(ctx, `
			INSERT INTO agent_runtime (
				workspace_id, daemon_id, name, runtime_mode, provider,
				status, device_info, metadata, last_seen_at, visibility, owner_id
			)
			VALUES ($1, $2, $3, 'local', $4, 'online', 'test machine', '{}'::jsonb, now(), 'private', $5)
			RETURNING id
		`, workspaceID, daemonID, name, provider, userID).Scan(&id); err != nil {
			t.Fatalf("create runtime %s: %v", name, err)
		}
		return id
	}

	mainDaemon := fmt.Sprintf("daemon-main-%d", suffix)
	fb1Daemon := fmt.Sprintf("daemon-fb1-%d", suffix)
	fb2Daemon := fmt.Sprintf("daemon-fb2-%d", suffix)
	mainID := createRuntime("Main Machine", mainDaemon)
	fb1ID := createRuntime("Fallback One", fb1Daemon)
	fb2ID := createRuntime("Fallback Two", fb2Daemon)

	var agentID string
	if err := pool.QueryRow(ctx, `
		INSERT INTO agent (
			workspace_id, name, description, runtime_mode, runtime_config,
			runtime_id, visibility, max_concurrent_tasks, owner_id
		)
		VALUES ($1, $2, '', 'local', '{}'::jsonb, $3, 'private', 4, $4)
		RETURNING id
	`, workspaceID, "Runtime Resolve Agent", mainID, userID).Scan(&agentID); err != nil {
		t.Fatalf("create agent: %v", err)
	}

	if _, err := pool.Exec(ctx, `
		INSERT INTO agent_fallback_runtime (agent_id, runtime_id, position)
		VALUES ($1, $2, 0), ($1, $3, 1)
	`, agentID, fb1ID, fb2ID); err != nil {
		t.Fatalf("create fallback rows: %v", err)
	}

	var issueID string
	if err := pool.QueryRow(ctx, `
		INSERT INTO issue (workspace_id, title, status, priority, creator_id, creator_type, number, position)
		VALUES ($1, $2, 'in_progress', 'none', $3, 'member', $4, 0)
		RETURNING id
	`, workspaceID, "runtime resolve issue", userID, 980000+int(suffix%1000)).Scan(&issueID); err != nil {
		t.Fatalf("create issue: %v", err)
	}

	t.Cleanup(func() {
		c := context.Background()
		pool.Exec(c, `DELETE FROM agent_task_queue WHERE agent_id = $1`, agentID)
		pool.Exec(c, `DELETE FROM issue WHERE id = $1`, issueID)
		pool.Exec(c, `DELETE FROM agent WHERE id = $1`, agentID)
		pool.Exec(c, `DELETE FROM agent_runtime WHERE workspace_id = $1`, workspaceID)
		pool.Exec(c, `DELETE FROM member WHERE workspace_id = $1`, workspaceID)
		pool.Exec(c, `DELETE FROM workspace WHERE id = $1`, workspaceID)
		pool.Exec(c, `DELETE FROM "user" WHERE id = $1`, userID)
	})

	return runtimeResolveFixture{
		agentID:     util.MustParseUUID(agentID),
		mainRuntime: util.MustParseUUID(mainID),
		fb1Runtime:  util.MustParseUUID(fb1ID),
		fb2Runtime:  util.MustParseUUID(fb2ID),
		mainDaemon:  mainDaemon,
		fb1Daemon:   fb1Daemon,
		fb2Daemon:   fb2Daemon,
		issueID:     util.MustParseUUID(issueID),
		workspaceID: workspaceID,
		userID:      userID,
		pool:        pool,
	}
}

func setRuntimeStatus(t *testing.T, ctx context.Context, pool *pgxpool.Pool, runtimeID pgtype.UUID, status string) {
	t.Helper()
	if _, err := pool.Exec(ctx, `UPDATE agent_runtime SET status = $2 WHERE id = $1`, util.UUIDToString(runtimeID), status); err != nil {
		t.Fatalf("set runtime %s status=%s: %v", util.UUIDToString(runtimeID), status, err)
	}
}

func TestResolveDispatchRuntime_PreferenceOrder(t *testing.T) {
	ctx := context.Background()
	pool := newRuntimeResolvePool(t)
	queries := db.New(pool)
	fx := createRuntimeResolveFixture(t, ctx, pool)

	t.Run("main online wins over online fallbacks", func(t *testing.T) {
		got, err := ResolveDispatchRuntime(ctx, queries, fx.agentID)
		if err != nil {
			t.Fatalf("resolve: %v", err)
		}
		if got.RuntimeID != fx.mainRuntime || !got.Online || got.IsFallback {
			t.Fatalf("expected online main runtime, got %+v", got)
		}
	})

	t.Run("first online fallback when main offline", func(t *testing.T) {
		setRuntimeStatus(t, ctx, pool, fx.mainRuntime, "offline")
		defer setRuntimeStatus(t, ctx, pool, fx.mainRuntime, "online")

		got, err := ResolveDispatchRuntime(ctx, queries, fx.agentID)
		if err != nil {
			t.Fatalf("resolve: %v", err)
		}
		if got.RuntimeID != fx.fb1Runtime || !got.Online || !got.IsFallback {
			t.Fatalf("expected online fb1, got %+v", got)
		}
	})

	t.Run("fallback order respects position", func(t *testing.T) {
		setRuntimeStatus(t, ctx, pool, fx.mainRuntime, "offline")
		setRuntimeStatus(t, ctx, pool, fx.fb1Runtime, "offline")
		defer func() {
			setRuntimeStatus(t, ctx, pool, fx.mainRuntime, "online")
			setRuntimeStatus(t, ctx, pool, fx.fb1Runtime, "online")
		}()

		got, err := ResolveDispatchRuntime(ctx, queries, fx.agentID)
		if err != nil {
			t.Fatalf("resolve: %v", err)
		}
		if got.RuntimeID != fx.fb2Runtime || !got.Online {
			t.Fatalf("expected online fb2, got %+v", got)
		}
	})

	t.Run("all offline returns main with Online=false", func(t *testing.T) {
		setRuntimeStatus(t, ctx, pool, fx.mainRuntime, "offline")
		setRuntimeStatus(t, ctx, pool, fx.fb1Runtime, "offline")
		setRuntimeStatus(t, ctx, pool, fx.fb2Runtime, "offline")
		defer func() {
			setRuntimeStatus(t, ctx, pool, fx.mainRuntime, "online")
			setRuntimeStatus(t, ctx, pool, fx.fb1Runtime, "online")
			setRuntimeStatus(t, ctx, pool, fx.fb2Runtime, "online")
		}()

		got, err := ResolveDispatchRuntime(ctx, queries, fx.agentID)
		if err != nil {
			t.Fatalf("resolve: %v", err)
		}
		if got.RuntimeID != fx.mainRuntime || got.Online {
			t.Fatalf("expected offline main (queue-and-wait), got %+v", got)
		}
	})

	t.Run("current computer hint beats online main", func(t *testing.T) {
		hinted := WithPreferredDaemonID(ctx, fx.fb2Daemon)
		got, err := ResolveDispatchRuntime(hinted, queries, fx.agentID)
		if err != nil {
			t.Fatalf("resolve: %v", err)
		}
		if got.RuntimeID != fx.fb2Runtime || !got.DaemonMatch || !got.IsFallback {
			t.Fatalf("expected daemon-matched fb2, got %+v", got)
		}
	})

	t.Run("offline current computer hint is ignored", func(t *testing.T) {
		setRuntimeStatus(t, ctx, pool, fx.fb2Runtime, "offline")
		defer setRuntimeStatus(t, ctx, pool, fx.fb2Runtime, "online")

		hinted := WithPreferredDaemonID(ctx, fx.fb2Daemon)
		got, err := ResolveDispatchRuntime(hinted, queries, fx.agentID)
		if err != nil {
			t.Fatalf("resolve: %v", err)
		}
		if got.RuntimeID != fx.mainRuntime || !got.Online || got.DaemonMatch {
			t.Fatalf("expected online main when hinted daemon is offline, got %+v", got)
		}
	})

	t.Run("hint for a daemon outside the bound set is ignored", func(t *testing.T) {
		hinted := WithPreferredDaemonID(ctx, "daemon-not-bound-to-agent")
		got, err := ResolveDispatchRuntime(hinted, queries, fx.agentID)
		if err != nil {
			t.Fatalf("resolve: %v", err)
		}
		if got.RuntimeID != fx.mainRuntime || got.DaemonMatch {
			t.Fatalf("expected main runtime for unbound hint, got %+v", got)
		}
	})
}

// TestFallbackRuntimeWriteRoundTrip exercises the generated write path the
// agent handler uses (delete + unnest-insert + ordered list-back) — the
// fixture elsewhere seeds rows with raw SQL, which would hide a broken
// sqlc binding for the uuid[] unnest parameter.
func TestFallbackRuntimeWriteRoundTrip(t *testing.T) {
	ctx := context.Background()
	pool := newRuntimeResolvePool(t)
	queries := db.New(pool)
	fx := createRuntimeResolveFixture(t, ctx, pool)

	// Rewrite the fixture's (fb1, fb2) list to (fb2, fb1) through the
	// handler-shaped path and verify both membership and ORDER round-trip.
	if err := queries.DeleteAgentFallbackRuntimes(ctx, fx.agentID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if err := queries.InsertAgentFallbackRuntimes(ctx, db.InsertAgentFallbackRuntimesParams{
		AgentID:    fx.agentID,
		RuntimeIds: []pgtype.UUID{fx.fb2Runtime, fx.fb1Runtime},
	}); err != nil {
		t.Fatalf("insert: %v", err)
	}

	rows, err := queries.ListAgentFallbackRuntimes(ctx, fx.agentID)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("expected 2 fallback rows, got %d", len(rows))
	}
	if rows[0].RuntimeID != fx.fb2Runtime || rows[0].Position != 0 {
		t.Fatalf("expected fb2 first at position 0, got %+v", rows[0])
	}
	if rows[1].RuntimeID != fx.fb1Runtime || rows[1].Position != 1 {
		t.Fatalf("expected fb1 second at position 1, got %+v", rows[1])
	}
}

func TestAgentReadiness_FallbackOnlineIsReady(t *testing.T) {
	ctx := context.Background()
	pool := newRuntimeResolvePool(t)
	queries := db.New(pool)
	fx := createRuntimeResolveFixture(t, ctx, pool)

	agent, err := queries.GetAgent(ctx, fx.agentID)
	if err != nil {
		t.Fatalf("load agent: %v", err)
	}

	setRuntimeStatus(t, ctx, pool, fx.mainRuntime, "offline")
	ready, reason, err := AgentReadiness(ctx, queries, agent)
	if err != nil {
		t.Fatalf("readiness: %v", err)
	}
	if !ready {
		t.Fatalf("expected ready with online fallback, got reason %q", reason)
	}

	setRuntimeStatus(t, ctx, pool, fx.fb1Runtime, "offline")
	setRuntimeStatus(t, ctx, pool, fx.fb2Runtime, "offline")
	ready, reason, err = AgentReadiness(ctx, queries, agent)
	if err != nil {
		t.Fatalf("readiness: %v", err)
	}
	if ready {
		t.Fatal("expected not ready when every bound runtime is offline")
	}
	if reason != "agent runtime is offline" {
		t.Fatalf("expected stable offline reason, got %q", reason)
	}
}

// insertResolveTask inserts a task row shaped like the retry/sweep paths
// expect and returns it.
func insertResolveTask(t *testing.T, ctx context.Context, queries *db.Queries, pool *pgxpool.Pool, fx runtimeResolveFixture, runtimeID pgtype.UUID, status, failureReason, sessionID string) db.AgentTaskQueue {
	t.Helper()
	var id string
	if err := pool.QueryRow(ctx, `
		INSERT INTO agent_task_queue (
			agent_id, runtime_id, issue_id, status, priority,
			attempt, max_attempts, failure_reason, session_id, work_dir,
			started_at, completed_at
		)
		VALUES ($1, $2, $3, $4, 0, 1, 3,
			NULLIF($5, ''), NULLIF($6, ''), CASE WHEN $6 <> '' THEN '/tmp/resolve-test-workdir' END,
			now(), CASE WHEN $4 IN ('failed', 'completed') THEN now() END)
		RETURNING id
	`, util.UUIDToString(fx.agentID), util.UUIDToString(runtimeID), util.UUIDToString(fx.issueID), status, failureReason, sessionID).Scan(&id); err != nil {
		t.Fatalf("insert %s task: %v", status, err)
	}
	task, err := queries.GetAgentTask(ctx, util.MustParseUUID(id))
	if err != nil {
		t.Fatalf("load inserted task: %v", err)
	}
	return task
}

func TestMaybeRetryFailedTask_ReroutesRuntimeOfflineToFallback(t *testing.T) {
	ctx := context.Background()
	pool := newRuntimeResolvePool(t)
	queries := db.New(pool)
	fx := createRuntimeResolveFixture(t, ctx, pool)
	svc := NewTaskService(queries, pool, nil, events.New())

	// Parent ran (and recorded a session) on the main runtime, which then
	// went offline; fallbacks are online.
	setRuntimeStatus(t, ctx, pool, fx.mainRuntime, "offline")
	parent := insertResolveTask(t, ctx, queries, pool, fx, fx.mainRuntime, "failed", "runtime_offline", "main-session")

	child, err := svc.MaybeRetryFailedTask(ctx, parent)
	if err != nil {
		t.Fatalf("retry: %v", err)
	}
	if child == nil {
		t.Fatal("expected a retry task")
	}
	if child.RuntimeID != fx.fb1Runtime {
		t.Fatalf("expected retry pinned to fb1, got %s", util.UUIDToString(child.RuntimeID))
	}
	if !child.ForceFreshSession {
		t.Fatal("cross-runtime retry must force a fresh session")
	}
	if child.SessionID.Valid || child.WorkDir.Valid {
		t.Fatalf("cross-runtime retry must not inherit session/workdir, got session=%v workdir=%v", child.SessionID, child.WorkDir)
	}
}

func TestMaybeRetryFailedTask_TimeoutKeepsParentRuntimeAndSession(t *testing.T) {
	ctx := context.Background()
	pool := newRuntimeResolvePool(t)
	queries := db.New(pool)
	fx := createRuntimeResolveFixture(t, ctx, pool)
	svc := NewTaskService(queries, pool, nil, events.New())

	// Task-shaped failure on a still-online runtime: the retry must stay on
	// that runtime and keep the resumable session even though other bound
	// runtimes are online too.
	parent := insertResolveTask(t, ctx, queries, pool, fx, fx.mainRuntime, "failed", "timeout", "timeout-session")

	child, err := svc.MaybeRetryFailedTask(ctx, parent)
	if err != nil {
		t.Fatalf("retry: %v", err)
	}
	if child == nil {
		t.Fatal("expected a retry task")
	}
	if child.RuntimeID != fx.mainRuntime {
		t.Fatalf("expected retry to stay on main, got %s", util.UUIDToString(child.RuntimeID))
	}
	if child.ForceFreshSession {
		t.Fatal("same-runtime timeout retry must not force a fresh session")
	}
	if !child.SessionID.Valid || child.SessionID.String != "timeout-session" {
		t.Fatalf("expected inherited session, got %v", child.SessionID)
	}
}

func TestMaybeRetryFailedTask_NoOnlineCandidateKeepsParentRuntime(t *testing.T) {
	ctx := context.Background()
	pool := newRuntimeResolvePool(t)
	queries := db.New(pool)
	fx := createRuntimeResolveFixture(t, ctx, pool)
	svc := NewTaskService(queries, pool, nil, events.New())

	setRuntimeStatus(t, ctx, pool, fx.mainRuntime, "offline")
	setRuntimeStatus(t, ctx, pool, fx.fb1Runtime, "offline")
	setRuntimeStatus(t, ctx, pool, fx.fb2Runtime, "offline")
	parent := insertResolveTask(t, ctx, queries, pool, fx, fx.mainRuntime, "failed", "runtime_offline", "stranded-session")

	child, err := svc.MaybeRetryFailedTask(ctx, parent)
	if err != nil {
		t.Fatalf("retry: %v", err)
	}
	if child == nil {
		t.Fatal("expected a retry task")
	}
	// No override: the child re-parks on the parent's runtime queue and
	// keeps the resumable session for when that machine reconnects.
	if child.RuntimeID != fx.mainRuntime {
		t.Fatalf("expected retry to keep main, got %s", util.UUIDToString(child.RuntimeID))
	}
	if child.ForceFreshSession {
		t.Fatal("same-runtime retry must not force a fresh session")
	}
	if !child.SessionID.Valid {
		t.Fatal("expected inherited session for same-runtime retry")
	}
}

func TestFailQueuedTasksForOfflineRuntimesWithFallback(t *testing.T) {
	ctx := context.Background()
	pool := newRuntimeResolvePool(t)
	queries := db.New(pool)
	fx := createRuntimeResolveFixture(t, ctx, pool)

	setRuntimeStatus(t, ctx, pool, fx.mainRuntime, "offline")
	queued := insertResolveTask(t, ctx, queries, pool, fx, fx.mainRuntime, "queued", "", "")

	failed, err := queries.FailQueuedTasksForOfflineRuntimesWithFallback(ctx, 500)
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	var mine *db.AgentTaskQueue
	for i := range failed {
		if failed[i].ID == queued.ID {
			mine = &failed[i]
		}
	}
	if mine == nil {
		t.Fatal("expected queued task on offline runtime with online fallback to be failed for reroute")
	}
	if !mine.FailureReason.Valid || mine.FailureReason.String != "runtime_offline" {
		t.Fatalf("expected runtime_offline failure reason, got %v", mine.FailureReason)
	}

	// With every bound runtime offline the queued task must keep waiting.
	setRuntimeStatus(t, ctx, pool, fx.fb1Runtime, "offline")
	setRuntimeStatus(t, ctx, pool, fx.fb2Runtime, "offline")
	waiting := insertResolveTask(t, ctx, queries, pool, fx, fx.mainRuntime, "queued", "", "")

	failed, err = queries.FailQueuedTasksForOfflineRuntimesWithFallback(ctx, 500)
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	for i := range failed {
		if failed[i].ID == waiting.ID {
			t.Fatal("queued task with no online candidate must not be failed")
		}
	}
	got, err := queries.GetAgentTask(ctx, waiting.ID)
	if err != nil {
		t.Fatalf("reload waiting task: %v", err)
	}
	if got.Status != "queued" {
		t.Fatalf("expected task to keep waiting, got status %s", got.Status)
	}

	// Retry budget exhausted → not a reroute candidate (the retry path
	// would drop it, so failing it here would be a plain failure). Cancel
	// the still-waiting row first: the pending-task unique index allows
	// only one active task per (issue, agent).
	if _, err := pool.Exec(ctx, `UPDATE agent_task_queue SET status = 'cancelled', completed_at = now() WHERE id = $1`, util.UUIDToString(waiting.ID)); err != nil {
		t.Fatalf("cancel waiting task: %v", err)
	}
	setRuntimeStatus(t, ctx, pool, fx.fb1Runtime, "online")
	exhausted := insertResolveTask(t, ctx, queries, pool, fx, fx.mainRuntime, "queued", "", "")
	if _, err := pool.Exec(ctx, `UPDATE agent_task_queue SET attempt = 3, max_attempts = 3 WHERE id = $1`, util.UUIDToString(exhausted.ID)); err != nil {
		t.Fatalf("exhaust attempts: %v", err)
	}

	failed, err = queries.FailQueuedTasksForOfflineRuntimesWithFallback(ctx, 500)
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	for i := range failed {
		if failed[i].ID == exhausted.ID {
			t.Fatal("queued task with exhausted retry budget must not be swept")
		}
	}
}

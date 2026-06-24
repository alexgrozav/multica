package serverside

import (
	"context"
	"time"

	"github.com/multica-ai/multica/server/addons/worktrees/shared"
)

// Module is the server half of the worktrees add-on.
type Module struct {
	deps  Deps
	store *Store
}

// New constructs the module over the supplied ports.
func New(deps Deps) *Module {
	return &Module{deps: deps, store: NewStore(deps.Pool)}
}

// Store exposes the data layer (used by tests and the adapter if needed).
func (m *Module) Store() *Store { return m.store }

// Register runs idempotent schema setup and subscribes the issue-event
// reactions. Call once at server startup.
func (m *Module) Register(ctx context.Context) error {
	if err := EnsureSchema(ctx, m.deps.Pool); err != nil {
		return err
	}
	m.deps.Subscribe("issue:created", m.onIssueCreated)
	m.deps.Subscribe("issue:updated", m.onIssueUpdated)
	return nil
}

func (m *Module) bgCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 15*time.Second)
}

// onIssueCreated initializes a worktree per workspace repo when auto-init is on.
func (m *Module) onIssueCreated(ev IssueEvent) {
	ctx, cancel := m.bgCtx()
	defer cancel()

	auto, err := m.store.AutoInit(ctx, ev.WorkspaceID)
	if err != nil {
		m.deps.log().Error("worktrees: read auto_init failed", "error", err, "workspace_id", ev.WorkspaceID)
		return
	}
	if !auto {
		return
	}
	urls, err := m.store.WorkspaceRepoURLs(ctx, ev.WorkspaceID)
	if err != nil {
		m.deps.log().Error("worktrees: read workspace repos failed", "error", err, "workspace_id", ev.WorkspaceID)
		return
	}
	for _, url := range urls {
		w, err := m.store.CreateWorktreeRow(ctx, ev.IssueID, ev.WorkspaceID, url)
		if err != nil {
			m.deps.log().Error("worktrees: create row failed", "error", err, "issue_id", ev.IssueID, "repo", url)
			continue
		}
		m.publishUpdated(ev.WorkspaceID, w)
	}
}

// onIssueUpdated queues cleanup when an issue transitions into "done".
func (m *Module) onIssueUpdated(ev IssueEvent) {
	if !(ev.StatusChanged && ev.PrevStatus != "done" && ev.Status == "done") {
		return
	}
	ctx, cancel := m.bgCtx()
	defer cancel()

	wts, err := m.store.MarkCleanupForIssue(ctx, ev.IssueID)
	if err != nil {
		m.deps.log().Error("worktrees: mark cleanup failed", "error", err, "issue_id", ev.IssueID)
		return
	}
	for _, w := range wts {
		m.publishUpdated(ev.WorkspaceID, w)
	}
}

func (m *Module) publishUpdated(wsID string, w shared.Worktree) {
	if m.deps.Publish == nil {
		return
	}
	m.deps.Publish(wsID, shared.EventWorktreeUpdated, shared.UpdatedEvent{IssueID: w.IssueID, Worktree: w})
}

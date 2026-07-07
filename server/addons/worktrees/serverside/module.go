package serverside

import (
	"context"
	"time"

	"github.com/multica-ai/multica/server/addons/worktrees/shared"
)

// Module is the server half of the worktrees add-on.
type Module struct {
	deps    Deps
	store   *Store
	fileOps *fileOpRegistry
}

// New constructs the module over the supplied ports.
func New(deps Deps) *Module {
	return &Module{deps: deps, store: NewStore(deps.Pool), fileOps: newFileOpRegistry()}
}

// Store exposes the data layer (used by tests and the adapter if needed).
func (m *Module) Store() *Store { return m.store }

// Register runs idempotent schema setup and subscribes the issue-event
// reactions. Call once at server startup.
func (m *Module) Register(ctx context.Context) error {
	if err := EnsureSchema(ctx, m.deps.Pool); err != nil {
		return err
	}
	m.deps.Subscribe("issue:created", m.onIssueEvent)
	m.deps.Subscribe("issue:updated", m.onIssueEvent)
	return nil
}

func (m *Module) bgCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 15*time.Second)
}

// onIssueEvent drives the whole lifecycle off issue events (create + update):
//   - assignment to an agent/squad in a workable status → check out one
//     workspace (a worktree per repo, branched on the issue identifier).
//   - close (done/cancelled) → queue cleanup (script + worktree removal).
//
// Both paths are idempotent, so re-firing on an unrelated edit is harmless — it
// simply self-heals a missing workspace for an assigned, active issue.
func (m *Module) onIssueEvent(ev IssueEvent) {
	if ev.IssueID == "" || ev.WorkspaceID == "" {
		return
	}
	// Close takes precedence: a done/cancelled issue is never (re)checked out.
	if ev.StatusChanged && isTerminal(ev.Status) && !isTerminal(ev.PrevStatus) {
		m.cleanup(ev)
		return
	}
	if ev.shouldCheckout() {
		m.checkout(ev)
	}
}

// checkout creates one pending worktree row per workspace repo (idempotent). The
// daemon's poll loop claims them and does the actual git checkout + setup.
func (m *Module) checkout(ev IssueEvent) {
	ctx, cancel := m.bgCtx()
	defer cancel()
	urls, err := m.store.WorkspaceRepoURLs(ctx, ev.WorkspaceID)
	if err != nil {
		m.deps.log().Error("worktrees: read workspace repos failed", "error", err, "workspace_id", ev.WorkspaceID)
		return
	}
	for _, url := range urls {
		w, err := m.store.CreateWorktreeRow(ctx, ev.IssueID, ev.Identifier, ev.WorkspaceID, url)
		if err != nil {
			m.deps.log().Error("worktrees: create row failed", "error", err, "issue_id", ev.IssueID, "repo", url)
			continue
		}
		m.publishUpdated(ev.WorkspaceID, w)
	}
}

// cleanup queues the cleanup script + worktree removal for every worktree of a
// closed issue.
func (m *Module) cleanup(ev IssueEvent) {
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

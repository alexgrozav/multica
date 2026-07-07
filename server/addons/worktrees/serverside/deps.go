// Package serverside implements the server half of the worktrees add-on: the
// idempotent schema, the pgx-backed store, the issue-event reactions, and the
// UI + daemon HTTP routers.
//
// It depends on the host application only through the Deps struct (a "port").
// All host-specific wiring lives in the adapter (server/cmd/server/worktree_addon.go),
// so a parent-repo restructuring never reaches into this package.
package serverside

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// DB is the minimal subset of *pgxpool.Pool the store needs. Implemented by the
// pool the adapter passes in; trivially fakeable in tests.
type DB interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// Principal is the authenticated caller on member-scoped UI routes.
type Principal struct {
	WorkspaceID string
	UserID      string
	Role        string // "owner" | "admin" | "member"
}

// IssueEvent is the host-agnostic shape of an issue lifecycle event. The adapter
// translates the host's events.Event (a map payload) into this struct so the
// module never imports host event/payload types.
//
// Checkout is triggered on assignment, so the event carries the assignee + the
// human identifier (e.g. PRO-11, used as the worktree branch name), not just the
// status fields the cleanup reaction needs.
type IssueEvent struct {
	Type             string // "issue:created" | "issue:updated"
	WorkspaceID      string
	IssueID          string
	Identifier       string
	Status           string
	PrevStatus       string
	StatusChanged    bool
	AssigneeType     string // "agent" | "squad" | "member" | ""
	AssigneeID       string
	PrevAssigneeType string
	AssigneeChanged  bool
}

// assignedToAgent reports whether the issue is currently owned by an agent or a
// squad (the only assignees that run in a checked-out workspace).
func (e IssueEvent) assignedToAgent() bool {
	return e.AssigneeType == "agent" || e.AssigneeType == "squad"
}

// workable reports whether the issue is in a status where an agent would work —
// i.e. not parked in backlog and not already closed.
func (e IssueEvent) workable() bool {
	switch e.Status {
	case "", "backlog", "done", "cancelled":
		return false
	default:
		return true
	}
}

// shouldCheckout is the single predicate for "this issue needs a checked-out
// workspace": it is assigned to an agent/squad, in a workable status, and this
// event actually (re)assigned or activated it — a new issue, an assignee change,
// or a status change (e.g. backlog → active). Gating on the change keeps an
// unrelated field edit (title, priority) of an already-assigned, active issue
// from re-emitting worktree events. Row creation is still idempotent.
func (e IssueEvent) shouldCheckout() bool {
	if !e.assignedToAgent() || !e.workable() {
		return false
	}
	return e.Type == "issue:created" || e.AssigneeChanged || e.StatusChanged
}

// terminal reports whether a status is a closed state (cleanup trigger).
func isTerminal(status string) bool { return status == "done" || status == "cancelled" }

// Deps is the port through which the host supplies everything the server module
// needs. Every field is populated by the adapter.
type Deps struct {
	Pool DB

	// Publish a WS event to a workspace room (the host bus auto-broadcasts).
	Publish func(workspaceID, eventType string, payload any)
	// Subscribe to a host event type; the adapter feeds a translated IssueEvent.
	Subscribe func(eventType string, handler func(IssueEvent))

	// Principal extraction on member-authenticated routes.
	Principal func(r *http.Request) (Principal, bool)
	// CanAccessWorkspace authorizes a daemon-authenticated request for a
	// workspace. The daemon sends workspace_id + daemon_id as query params (it
	// knows both from registration), so we do NOT depend on a token-bound
	// workspace — that only exists on the mdt_ daemon-token path, and a local
	// daemon often authenticates with a mul_ user PAT instead. The adapter
	// implements the same check the host uses (daemon-token match OR membership).
	CanAccessWorkspace func(r *http.Request, workspaceID string) bool

	// Redact strips secrets from streamed log text. Identity if nil.
	Redact func(string) string

	Logger *slog.Logger
}

func (d *Deps) redact(s string) string {
	if d.Redact == nil {
		return s
	}
	return d.Redact(s)
}

func (d *Deps) log() *slog.Logger {
	if d.Logger == nil {
		return slog.Default()
	}
	return d.Logger
}

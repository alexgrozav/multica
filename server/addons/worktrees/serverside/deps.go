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

// CanManage reports whether the principal may edit worktree config.
func (p Principal) CanManage() bool { return p.Role == "owner" || p.Role == "admin" }

// IssueEvent is the host-agnostic shape of an issue lifecycle event. The adapter
// translates the host's events.Event (a map payload) into this struct so the
// module never imports host event/payload types.
type IssueEvent struct {
	Type          string // "issue:created" | "issue:updated"
	WorkspaceID   string
	IssueID       string
	Status        string
	PrevStatus    string
	StatusChanged bool
}

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
	// DaemonWorkspaceID returns the workspace bound to the daemon token (or "").
	DaemonWorkspaceID func(r *http.Request) string
	// DaemonID returns the authenticated daemon's id (the worktree owner).
	DaemonID func(r *http.Request) string

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

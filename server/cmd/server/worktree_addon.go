package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"sync"

	"github.com/jackc/pgx/v5/pgxpool"

	worktreesserver "github.com/multica-ai/multica/server/addons/worktrees/serverside"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/middleware"
)

// worktree_addon.go is the server-side ADAPTER for the worktrees add-on. It is
// the only place in cmd/server that knows host internals (the event bus, the
// auth middleware accessors). It binds those to the add-on module's Deps port so
// the module (server/addons/worktrees/serverside) stays import-clean and immune
// to host restructurings. Wiring seam: two r.Mount calls in router.go.

var (
	worktreeOnce sync.Once
	worktreeMod  *worktreesserver.Module
)

// worktreeAddon lazily builds and registers the worktrees module (idempotent).
// Called from router.go at startup when mounting the routes; Register runs the
// idempotent schema setup and subscribes the issue-event reactions exactly once.
func worktreeAddon(pool *pgxpool.Pool, bus *events.Bus) *worktreesserver.Module {
	worktreeOnce.Do(func() {
		worktreeMod = worktreesserver.New(worktreesserver.Deps{
			Pool: pool,
			Publish: func(workspaceID, eventType string, payload any) {
				bus.Publish(events.Event{Type: eventType, WorkspaceID: workspaceID, ActorType: "system", Payload: payload})
			},
			Subscribe: func(eventType string, handler func(worktreesserver.IssueEvent)) {
				bus.Subscribe(eventType, func(e events.Event) { handler(translateIssueEvent(e)) })
			},
			Principal: func(r *http.Request) (worktreesserver.Principal, bool) {
				wsID := middleware.WorkspaceIDFromContext(r.Context())
				if wsID == "" {
					return worktreesserver.Principal{}, false
				}
				role := ""
				if member, ok := middleware.MemberFromContext(r.Context()); ok {
					role = member.Role
				}
				return worktreesserver.Principal{
					WorkspaceID: wsID,
					UserID:      r.Header.Get("X-User-ID"),
					Role:        role,
				}, true
			},
			CanAccessWorkspace: func(r *http.Request, workspaceID string) bool {
				if workspaceID == "" {
					return false
				}
				// Daemon-token (mdt_) path: the token is bound to a workspace.
				if dws := middleware.DaemonWorkspaceIDFromContext(r.Context()); dws != "" {
					return dws == workspaceID
				}
				// PAT/JWT path (e.g. a local daemon using a mul_ user token):
				// the authenticated user must be a member of the workspace.
				userID := r.Header.Get("X-User-ID")
				if userID == "" {
					return false
				}
				var ok bool
				if err := pool.QueryRow(r.Context(),
					`SELECT EXISTS(SELECT 1 FROM member WHERE workspace_id=$1 AND user_id=$2)`,
					workspaceID, userID).Scan(&ok); err != nil {
					slog.Warn("worktrees: workspace access check failed", "error", err)
					return false
				}
				return ok
			},
			Logger: slog.Default(),
		})
		if err := worktreeMod.Register(context.Background()); err != nil {
			slog.Error("worktrees: addon registration failed", "error", err)
		}
	})
	return worktreeMod
}

// translateIssueEvent converts a host issue event (map payload) into the
// add-on's host-agnostic IssueEvent, so the module never imports host types.
func translateIssueEvent(e events.Event) worktreesserver.IssueEvent {
	out := worktreesserver.IssueEvent{Type: e.Type, WorkspaceID: e.WorkspaceID}
	m, ok := e.Payload.(map[string]any)
	if !ok {
		return out
	}
	if v, ok := m["status_changed"].(bool); ok {
		out.StatusChanged = v
	}
	if v, ok := m["prev_status"].(string); ok {
		out.PrevStatus = v
	}
	// The "issue" subfield may be a typed struct (HTTP path) or a map; round-trip
	// through JSON to read the fields we need without importing the host type.
	if iss, ok := m["issue"]; ok {
		var tmp struct {
			ID           string  `json:"id"`
			Status       string  `json:"status"`
			WorkspaceID  string  `json:"workspace_id"`
			AssigneeType *string `json:"assignee_type"` // nullable in the payload
		}
		if b, err := json.Marshal(iss); err == nil {
			_ = json.Unmarshal(b, &tmp)
		}
		out.IssueID = tmp.ID
		if tmp.Status != "" {
			out.Status = tmp.Status
		}
		if out.WorkspaceID == "" {
			out.WorkspaceID = tmp.WorkspaceID
		}
		if tmp.AssigneeType != nil {
			out.AssigneeType = *tmp.AssigneeType
		}
	}
	if out.IssueID == "" {
		if v, ok := m["issue_id"].(string); ok {
			out.IssueID = v
		}
	}
	return out
}

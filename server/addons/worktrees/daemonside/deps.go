// Package daemonside implements the daemon half of the worktrees add-on: a poll
// loop that claims worktree jobs from the server, creates/cleans persistent
// per-issue worktrees, and runs the Setup/Run/Cleanup scripts (streaming Run
// output back to the server).
//
// It depends on the daemon only through the Deps struct, populated by the thin
// adapter at server/internal/daemon/worktree_addon.go. It imports nothing from
// package daemon, so a daemon refactor only touches the adapter.
package daemonside

import (
	"context"
	"log/slog"
	"net/http"
	"sync"
	"time"
)

// Deps is the port the daemon fills in at the Run() seam.
type Deps struct {
	// ServerBaseURL is the Multica server API root (e.g. http://127.0.0.1:8080).
	ServerBaseURL string
	// TokenProvider returns the current daemon auth token. Read per request so
	// in-place renewal is picked up.
	TokenProvider func() string
	// WorkspacesRoot is the daemon's local working-directory root.
	WorkspacesRoot string

	// DaemonID is this daemon's stable id. Sent to the server as the worktree
	// owner and identity, so the endpoints work on any auth path (including a
	// mul_ user PAT, where a token-bound daemon id is absent).
	DaemonID string
	// ListWorkspaces returns the workspace IDs this daemon currently watches.
	ListWorkspaces func() []string

	Logger  *slog.Logger
	RootCtx context.Context

	// LookupBare returns an existing local bare-clone path for (workspace, repo)
	// or "" if not cached. Optional (nil → always self-clone).
	LookupBare func(workspaceID, repoURL string) string
	// EnsureBare clones/fetches the repo into the daemon's cache and returns the
	// bare path. Optional (nil → self-clone into the add-on's own cache dir).
	EnsureBare func(workspaceID, repoURL string) (string, error)

	// PollInterval defaults to 5s when zero.
	PollInterval time.Duration
}

// Module is the daemon-side worktrees add-on.
type Module struct {
	deps    Deps
	cl      *client
	httpC   *http.Client
	mu      sync.Mutex
	running map[string]context.CancelFunc // worktreeID -> cancel of the active Run
}

// New constructs the module from its dependencies.
func New(deps Deps) *Module {
	if deps.PollInterval <= 0 {
		deps.PollInterval = 5 * time.Second
	}
	hc := &http.Client{Timeout: 30 * time.Second}
	return &Module{
		deps:    deps,
		httpC:   hc,
		cl:      &client{baseURL: deps.ServerBaseURL, token: deps.TokenProvider, daemonID: deps.DaemonID, hc: hc},
		running: map[string]context.CancelFunc{},
	}
}

func (m *Module) log() *slog.Logger {
	if m.deps.Logger == nil {
		return slog.Default()
	}
	return m.deps.Logger
}

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
	"sync/atomic"
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
	// FetchBare runs `git fetch origin` on an already-cached bare clone so a new
	// worktree branches off CURRENT origin/<default> instead of a remote-tracking
	// ref left stale by an earlier LookupBare (which does not fetch). Self-locks on
	// the bare repo. Optional (nil → the self-clone / EnsureBare paths already
	// fetch, so only the LookupBare fast path needs this).
	FetchBare func(barePath string) error
	// WithRepoLock serializes git mutations on a bare repo. MUST be the daemon's
	// own repo lock so our worktree adds don't race the daemon's agent-task
	// worktree creation on the same bare clone (git's config.lock/packed-refs.lock
	// can't tolerate parallel mutation). Optional (nil → a local per-repo mutex).
	WithRepoLock func(barePath string, fn func() error) error

	// PollInterval defaults to 5s when zero.
	PollInterval time.Duration
}

// Module is the daemon-side worktrees add-on.
type Module struct {
	deps       Deps
	cl         *client
	httpC      *http.Client
	mu         sync.Mutex
	running    map[string]context.CancelFunc // runKey(worktreeID,name) -> cancel of the active Run
	locks      sync.Map                      // bare/cache path -> *sync.Mutex (fallback when WithRepoLock is nil)
	reconciled sync.Map                      // workspaceID -> true once stale runs are reset (one-shot per process)
	scanBusy   atomic.Bool                   // one file-scan pass at a time; a slow pass skips ticks, never piles up
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

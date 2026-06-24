package daemonside

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// SetupParams configures a one-off Setup run for a freshly checked-out worktree.
type SetupParams struct {
	ServerBaseURL string
	TokenProvider func() string
	DaemonID      string
	WorkspaceID   string
	RepoURL       string
	WorktreePath  string
}

// RunRepoSetup fetches the repo's configured Setup script and runs it in the
// worktree the agent just checked out (so the agent waits and works in a
// prepared environment).
//
// Returns ran=true when a Setup script was configured (regardless of outcome).
// err is non-nil only when the script itself fails (exit != 0) — so the caller
// can fail the checkout. A fetch/transport failure returns (false, err) so the
// caller can choose to SKIP rather than block checkouts on a flaky endpoint.
func RunRepoSetup(ctx context.Context, p SetupParams, onLine func(stream, text string)) (bool, error) {
	cl := &client{
		baseURL:  p.ServerBaseURL,
		token:    p.TokenProvider,
		daemonID: p.DaemonID,
		hc:       &http.Client{Timeout: 30 * time.Second},
	}
	scripts, err := cl.fetchScripts(ctx, p.WorkspaceID, p.RepoURL)
	if err != nil {
		return false, err
	}
	if strings.TrimSpace(scripts.Setup) == "" {
		return false, nil
	}
	env := []string{
		"MULTICA_WORKSPACE_ID=" + p.WorkspaceID,
		"MULTICA_REPO_URL=" + p.RepoURL,
		"MULTICA_WORKTREE_PATH=" + p.WorktreePath,
	}
	code, runErr := runScript(ctx, p.WorktreePath, scripts.Setup, env, onLine)
	if runErr != nil {
		return true, runErr
	}
	if code != 0 {
		return true, fmt.Errorf("setup exited with code %d", code)
	}
	return true, nil
}

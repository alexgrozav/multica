package daemonside

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/multica-ai/multica/server/addons/worktrees/shared"
)

// WaitParams configures WaitIssueReady.
type WaitParams struct {
	ServerBaseURL string
	TokenProvider func() string
	DaemonID      string
	WorkspaceID   string
	IssueID       string
	Timeout       time.Duration // total wait budget (default 5m)
	Poll          time.Duration // poll interval (default 2s)
}

// WaitIssueReady polls the server until every worktree the add-on is preparing
// for the issue is ready (checked out + setup finished). It is how an agent task
// "waits until checkout is complete" while a SINGLE owner (the daemon poll loop,
// handleInit) does the actual checkout — no second checkout, no double setup.
//
// Returns managed=false when the add-on is not tracking the issue (no rows), so
// the caller can fall back. A non-nil error means a worktree failed to check out
// or its setup failed (fail the task); a timeout is also a (managed) error.
func WaitIssueReady(ctx context.Context, p WaitParams) (managed bool, err error) {
	if p.Timeout <= 0 {
		p.Timeout = 5 * time.Minute
	}
	if p.Poll <= 0 {
		p.Poll = 2 * time.Second
	}
	cl := &client{baseURL: p.ServerBaseURL, token: p.TokenProvider, daemonID: p.DaemonID, hc: &http.Client{Timeout: 15 * time.Second}}
	deadline := time.Now().Add(p.Timeout)

	for {
		if st, e := cl.issueStatus(ctx, p.WorkspaceID, p.IssueID); e == nil {
			if !st.Managed {
				return false, nil
			}
			ready := true
			for _, wt := range st.Worktrees {
				switch {
				case wt.SetupStatus == shared.ScriptFailed:
					return true, fmt.Errorf("checkout setup failed for %s", wt.RepoURL)
				case wt.Status == shared.StatusError:
					return true, fmt.Errorf("checkout failed for %s", wt.RepoURL)
				case wt.Status != shared.StatusReady:
					ready = false
				}
			}
			if ready {
				return true, nil
			}
		}
		if time.Now().After(deadline) {
			return true, fmt.Errorf("timed out waiting for issue %s workspace to be ready", p.IssueID)
		}
		select {
		case <-ctx.Done():
			return true, ctx.Err()
		case <-time.After(p.Poll):
		}
	}
}

// RunSetupAt reads the worktree's multica.json and runs its Setup script in
// place, for the legacy (unified-disabled) agent-checkout path. Returns ran=true
// when a Setup command was defined; err is non-nil only when the script itself
// fails (so the caller can fail the checkout).
func RunSetupAt(ctx context.Context, workspaceID, repoURL, worktreePath string, onLine func(stream, text string)) (bool, error) {
	mf, err := readManifest(worktreePath)
	if err != nil {
		return false, err
	}
	if !mf.HasSetup() {
		return false, nil
	}
	env := []string{
		"MULTICA_WORKSPACE_ID=" + workspaceID,
		"MULTICA_REPO_URL=" + repoURL,
		"MULTICA_WORKTREE_PATH=" + worktreePath,
	}
	code, runErr := runScript(ctx, worktreePath, mf.Scripts.Setup, env, onLine)
	if runErr != nil {
		return true, runErr
	}
	if code != 0 {
		return true, fmt.Errorf("setup exited with code %d", code)
	}
	return true, nil
}

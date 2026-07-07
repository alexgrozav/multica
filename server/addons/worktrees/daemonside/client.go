package daemonside

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"

	"github.com/multica-ai/multica/server/addons/worktrees/shared"
)

// client talks to the add-on's daemon-authenticated server endpoints, reusing
// the daemon's bearer token (provided fresh per request). It identifies itself
// with daemonID via query params so the endpoints work on any auth path.
type client struct {
	baseURL  string
	token    func() string
	daemonID string
	hc       *http.Client
}

func (c *client) authToken() string {
	if c.token == nil {
		return ""
	}
	return c.token()
}

func (c *client) do(ctx context.Context, method, path string, body any, out any) error {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, rdr)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if tok := c.authToken(); tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return fmt.Errorf("%s %s: %d: %s", method, path, resp.StatusCode, string(msg))
	}
	if out != nil {
		return json.NewDecoder(resp.Body).Decode(out)
	}
	return nil
}

// poll claims jobs for the given workspace.
func (c *client) poll(ctx context.Context, workspaceID string) (shared.JobsResponse, error) {
	var out shared.JobsResponse
	q := url.Values{"workspace_id": {workspaceID}, "daemon_id": {c.daemonID}}
	err := c.do(ctx, http.MethodGet, "/api/daemon/worktree/jobs?"+q.Encode(), nil, &out)
	return out, err
}

// reconcile asks the server to clear this daemon's stale 'running' runs in a
// workspace (called once per workspace when the poll loop (re)starts).
func (c *client) reconcile(ctx context.Context, workspaceID string) error {
	q := url.Values{"workspace_id": {workspaceID}, "daemon_id": {c.daemonID}}
	return c.do(ctx, http.MethodPost, "/api/daemon/worktree/reconcile?"+q.Encode(), nil, nil)
}

// issueStatus fetches the readiness snapshot for an issue's worktrees (used by
// the agent task's wait-and-adopt eager checkout).
func (c *client) issueStatus(ctx context.Context, workspaceID, issueID string) (shared.IssueStatusResponse, error) {
	var out shared.IssueStatusResponse
	q := url.Values{"workspace_id": {workspaceID}, "daemon_id": {c.daemonID}}
	err := c.do(ctx, http.MethodGet, "/api/daemon/worktree/issues/"+issueID+"/status?"+q.Encode(), nil, &out)
	return out, err
}

// reportStatus reports the outcome of an init/setup/run/cleanup job.
func (c *client) reportStatus(ctx context.Context, worktreeID string, rep shared.StatusReport) error {
	q := url.Values{"daemon_id": {c.daemonID}}
	return c.do(ctx, http.MethodPost, "/api/daemon/worktree/jobs/"+worktreeID+"/status?"+q.Encode(), rep, nil)
}

// postLogs streams a batch of run-output lines.
func (c *client) postLogs(ctx context.Context, runTaskID string, lines []shared.LogLine) error {
	return c.do(ctx, http.MethodPost, "/api/daemon/worktree/runs/"+runTaskID+"/logs", shared.LogBatch{Lines: lines}, nil)
}

// postFiles reports a worktree's current file list. Callers gate on the digest
// so this only fires when the list actually changed.
func (c *client) postFiles(ctx context.Context, worktreeID string, rep shared.FileReport) error {
	q := url.Values{"daemon_id": {c.daemonID}}
	return c.do(ctx, http.MethodPost, "/api/daemon/worktree/worktrees/"+worktreeID+"/files?"+q.Encode(), rep, nil)
}

// postChanges reports a worktree's current git status (changed files vs base).
// Callers gate on the digest so this only fires when the status actually
// changed.
func (c *client) postChanges(ctx context.Context, worktreeID string, rep shared.ChangesReport) error {
	q := url.Values{"daemon_id": {c.daemonID}}
	return c.do(ctx, http.MethodPost, "/api/daemon/worktree/worktrees/"+worktreeID+"/changes?"+q.Encode(), rep, nil)
}

// postFileOpResult reports the outcome of an on-demand file read/write; the
// server routes it to the UI request waiting on the op.
func (c *client) postFileOpResult(ctx context.Context, opID string, res shared.FileOpResult) error {
	q := url.Values{"daemon_id": {c.daemonID}}
	return c.do(ctx, http.MethodPost, "/api/daemon/worktree/file-ops/"+opID+"/result?"+q.Encode(), res, nil)
}

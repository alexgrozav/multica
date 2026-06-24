package daemonside

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/multica-ai/multica/server/addons/worktrees/shared"
)

// client talks to the add-on's daemon-authenticated server endpoints, reusing
// the daemon's bearer token (provided fresh per request).
type client struct {
	baseURL string
	token   func() string
	hc      *http.Client
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

// poll claims jobs for this daemon's workspace.
func (c *client) poll(ctx context.Context) (shared.JobsResponse, error) {
	var out shared.JobsResponse
	err := c.do(ctx, http.MethodGet, "/api/daemon/worktree/jobs", nil, &out)
	return out, err
}

// reportStatus reports the outcome of an init/run/cleanup job.
func (c *client) reportStatus(ctx context.Context, worktreeID string, rep shared.StatusReport) error {
	return c.do(ctx, http.MethodPost, "/api/daemon/worktree/jobs/"+worktreeID+"/status", rep, nil)
}

// postLogs streams a batch of run-output lines.
func (c *client) postLogs(ctx context.Context, runTaskID string, lines []shared.LogLine) error {
	return c.do(ctx, http.MethodPost, "/api/daemon/worktree/runs/"+runTaskID+"/logs", shared.LogBatch{Lines: lines}, nil)
}

package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	worktreeshared "github.com/multica-ai/multica/server/addons/worktrees/shared"
)

// claimIssueBranchForTest claims the next queued task for runtimeID and
// returns the issue_branch field carried on the claim response, immediately
// completing the claimed task so the one-pending-task-per-(issue, agent)
// constraint allows the next enqueue.
func claimIssueBranchForTest(t *testing.T, runtimeID string) string {
	t.Helper()
	w := httptest.NewRecorder()
	req := newDaemonTokenRequest("POST", "/api/daemon/runtimes/"+runtimeID+"/tasks/claim", nil,
		testWorkspaceID, "issue-branch-claim")
	req = withURLParam(req, "runtimeId", runtimeID)
	testHandler.ClaimTaskByRuntime(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("ClaimTaskByRuntime: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp struct {
		Task *struct {
			ID          string `json:"id"`
			IssueBranch string `json:"issue_branch"`
		} `json:"task"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode claim response: %v", err)
	}
	if resp.Task == nil {
		t.Fatalf("no task claimed: %s", w.Body.String())
	}
	if _, err := testPool.Exec(context.Background(),
		`UPDATE agent_task_queue SET status = 'completed' WHERE id = $1`, resp.Task.ID); err != nil {
		t.Fatalf("complete claimed task: %v", err)
	}
	return resp.Task.IssueBranch
}

func enqueueIssueBranchClaimTask(t *testing.T, ctx context.Context, agentID, runtimeID, issueID string) {
	t.Helper()
	var taskID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO agent_task_queue (agent_id, runtime_id, issue_id, status, priority)
		VALUES ($1, $2, $3, 'queued', 0)
		RETURNING id
	`, agentID, runtimeID, issueID).Scan(&taskID); err != nil {
		t.Fatalf("enqueue claim task: %v", err)
	}
	t.Cleanup(func() { testPool.Exec(context.Background(), `DELETE FROM agent_task_queue WHERE id = $1`, taskID) })
}

// The daemon claim payload must carry the issue's worktree branch — the
// custom branch_name when pinned, else the identifier default derived with
// the SAME shared helper the worktrees daemon uses for the actual checkout —
// so the agent brief/env can announce the branch the agent is standing on.
func TestClaim_CarriesIssueBranch(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()

	runtimeID := createClaimReclaimRuntime(t, ctx, "issue-branch runtime")
	agentID, issueID := createClaimReclaimAgentAndIssue(t, ctx, runtimeID, "issue-branch")

	// Case 1: custom branch pinned on the issue.
	if _, err := testPool.Exec(ctx, `UPDATE issue SET branch_name = 'feature/pinned-claim' WHERE id = $1`, issueID); err != nil {
		t.Fatalf("pin branch_name: %v", err)
	}
	enqueueIssueBranchClaimTask(t, ctx, agentID, runtimeID, issueID)
	if got := claimIssueBranchForTest(t, runtimeID); got != "feature/pinned-claim" {
		t.Fatalf("claim issue_branch = %q, want feature/pinned-claim", got)
	}

	// Case 2: no custom branch → the identifier-derived default.
	if _, err := testPool.Exec(ctx, `UPDATE issue SET branch_name = '' WHERE id = $1`, issueID); err != nil {
		t.Fatalf("clear branch_name: %v", err)
	}
	var prefix string
	var number int32
	if err := testPool.QueryRow(ctx, `
		SELECT w.issue_prefix, i.number FROM issue i JOIN workspace w ON w.id = i.workspace_id
		WHERE i.id = $1`, issueID).Scan(&prefix, &number); err != nil {
		t.Fatalf("read identifier parts: %v", err)
	}
	want := worktreeshared.IssueBranch(fmt.Sprintf("%s-%d", prefix, number), issueID)
	enqueueIssueBranchClaimTask(t, ctx, agentID, runtimeID, issueID)
	if got := claimIssueBranchForTest(t, runtimeID); got != want {
		t.Fatalf("claim issue_branch = %q, want identifier default %q", got, want)
	}
}

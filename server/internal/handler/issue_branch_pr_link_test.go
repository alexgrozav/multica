package handler

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// postPRWebhook signs and delivers a pull_request webhook payload.
func postPRWebhook(t *testing.T, secret string, body map[string]any) {
	t.Helper()
	raw, _ := json.Marshal(body)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(raw)
	sig := "sha256=" + hex.EncodeToString(mac.Sum(nil))

	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/webhooks/github", bytes.NewReader(raw))
	req.Header.Set("X-GitHub-Event", "pull_request")
	req.Header.Set("X-Hub-Signature-256", sig)
	testHandler.HandleGitHubWebhook(w, req)
	if w.Code != http.StatusAccepted {
		t.Fatalf("webhook: expected 202, got %d (%s)", w.Code, w.Body.String())
	}
}

// prWebhookBody builds a minimal pull_request payload with NO issue
// identifiers anywhere — linking must come from the head branch alone.
func prWebhookBody(action, state string, merged bool, prNumber int, headRef string, installationID int64, repo string) map[string]any {
	pr := map[string]any{
		"number":     prNumber,
		"html_url":   "https://github.com/acme/" + repo + "/pull/1",
		"title":      "Refactor the widget flow",
		"body":       "No issue references here.",
		"state":      state,
		"draft":      false,
		"merged":     merged,
		"created_at": "2026-07-01T00:00:00Z",
		"updated_at": "2026-07-02T00:00:00Z",
		"head":       map[string]any{"ref": headRef},
		"user":       map[string]any{"login": "octocat", "avatar_url": ""},
	}
	if merged {
		pr["merged_at"] = "2026-07-02T00:00:00Z"
		pr["closed_at"] = "2026-07-02T00:00:00Z"
	}
	return map[string]any{
		"action":       action,
		"pull_request": pr,
		"repository":   map[string]any{"name": repo, "owner": map[string]any{"login": "acme"}},
		"installation": map[string]any{"id": installationID},
	}
}

func cleanupBranchLinkFixtures(t *testing.T, ctx context.Context, issueID string) {
	t.Helper()
	t.Cleanup(func() {
		if issueID != "" {
			testPool.Exec(ctx, `DELETE FROM issue_pull_request WHERE issue_id = $1`, issueID)
			testPool.Exec(ctx, `DELETE FROM activity_log WHERE issue_id = $1`, issueID)
		}
		testPool.Exec(ctx, `DELETE FROM github_pull_request WHERE workspace_id = $1`, testWorkspaceID)
		testPool.Exec(ctx, `DELETE FROM github_installation WHERE workspace_id = $1`, testWorkspaceID)
		if issueID != "" {
			testPool.Exec(ctx, `DELETE FROM issue WHERE id = $1`, issueID)
		}
	})
}

// A PR whose head branch IS an issue's custom branch_name auto-links to that
// issue with no identifier anywhere in the PR — and the link is a working link
// (visible in the PR list, which filters reference_only rows out).
func TestWebhook_PRHeadBranchLinksIssueByBranchName(t *testing.T) {
	if testHandler == nil {
		t.Skip("handler test fixture not initialized (no DB?)")
	}
	ctx := context.Background()
	secret := "branch-link-test-secret"
	t.Setenv("GITHUB_WEBHOOK_SECRET", secret)

	const branch = "feature/branch-link-test"
	w := httptest.NewRecorder()
	req := newRequest("POST", "/api/issues?workspace_id="+testWorkspaceID, map[string]any{
		"title":       "Branch-linked PR test",
		"status":      "in_progress",
		"branch_name": branch,
	})
	testHandler.CreateIssue(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("CreateIssue: %d %s", w.Code, w.Body.String())
	}
	var created IssueResponse
	json.NewDecoder(w.Body).Decode(&created)
	cleanupBranchLinkFixtures(t, ctx, created.ID)

	const installationID int64 = 99887701
	if _, err := testHandler.Queries.CreateGitHubInstallation(ctx, db.CreateGitHubInstallationParams{
		WorkspaceID:    parseUUID(testWorkspaceID),
		InstallationID: installationID,
		AccountLogin:   "branch-link-acct",
		AccountType:    "User",
	}); err != nil {
		t.Fatalf("CreateGitHubInstallation: %v", err)
	}

	// PR opened from the issue's branch; no identifier in title/body/branch.
	postPRWebhook(t, secret, prWebhookBody("opened", "open", false, 4401, branch, installationID, "widget"))

	linked, err := testHandler.Queries.ListPullRequestsByIssue(ctx, parseUUID(created.ID))
	if err != nil {
		t.Fatalf("ListPullRequestsByIssue: %v", err)
	}
	if len(linked) != 1 {
		t.Fatalf("expected 1 branch-linked PR, got %d", len(linked))
	}

	// Merging that PR must NOT auto-advance the issue: a branch match links
	// but never declares close intent (same contract as identifier-in-branch).
	postPRWebhook(t, secret, prWebhookBody("closed", "closed", true, 4401, branch, installationID, "widget"))

	updated, err := testHandler.Queries.GetIssue(ctx, parseUUID(created.ID))
	if err != nil {
		t.Fatalf("GetIssue: %v", err)
	}
	if updated.Status != "in_progress" {
		t.Errorf("branch-linked merge must not close the issue: status = %q, want in_progress", updated.Status)
	}
}

// Creating an issue pinned to a branch that ALREADY has mirrored PRs backfills
// the links immediately — the PR section works without waiting for the PR's
// next webhook delivery.
func TestCreateIssueBackfillsExistingPRsOnBranch(t *testing.T) {
	if testHandler == nil {
		t.Skip("handler test fixture not initialized (no DB?)")
	}
	ctx := context.Background()
	secret := "branch-backfill-test-secret"
	t.Setenv("GITHUB_WEBHOOK_SECRET", secret)

	const branch = "feature/pre-existing-pr"
	const installationID int64 = 99887702
	if _, err := testHandler.Queries.CreateGitHubInstallation(ctx, db.CreateGitHubInstallationParams{
		WorkspaceID:    parseUUID(testWorkspaceID),
		InstallationID: installationID,
		AccountLogin:   "branch-backfill-acct",
		AccountType:    "User",
	}); err != nil {
		t.Fatalf("CreateGitHubInstallation: %v", err)
	}

	// Mirror a PR from the branch BEFORE any issue references it. No
	// identifiers anywhere, so no links exist yet.
	postPRWebhook(t, secret, prWebhookBody("opened", "open", false, 4402, branch, installationID, "gadget"))

	w := httptest.NewRecorder()
	req := newRequest("POST", "/api/issues?workspace_id="+testWorkspaceID, map[string]any{
		"title":       "Adopts the existing PR",
		"branch_name": branch,
	})
	testHandler.CreateIssue(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("CreateIssue: %d %s", w.Code, w.Body.String())
	}
	var created IssueResponse
	json.NewDecoder(w.Body).Decode(&created)
	cleanupBranchLinkFixtures(t, ctx, created.ID)

	linked, err := testHandler.Queries.ListPullRequestsByIssue(ctx, parseUUID(created.ID))
	if err != nil {
		t.Fatalf("ListPullRequestsByIssue: %v", err)
	}
	if len(linked) != 1 {
		t.Fatalf("expected the pre-existing PR to be backfilled onto the new issue, got %d links", len(linked))
	}
	if linked[0].PrNumber != 4402 {
		t.Fatalf("linked PR number = %d, want 4402", linked[0].PrNumber)
	}
}

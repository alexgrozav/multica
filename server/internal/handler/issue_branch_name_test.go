package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func createIssueForBranchTest(t *testing.T, body map[string]any) (IssueResponse, *httptest.ResponseRecorder) {
	t.Helper()
	w := httptest.NewRecorder()
	req := newRequest("POST", "/api/issues?workspace_id="+testWorkspaceID, body)
	testHandler.CreateIssue(w, req)
	var created IssueResponse
	if w.Code == http.StatusCreated {
		_ = json.NewDecoder(w.Body).Decode(&created)
		t.Cleanup(func() {
			cleanupReq := newRequest("DELETE", "/api/issues/"+created.ID, nil)
			cleanupReq = withURLParam(cleanupReq, "id", created.ID)
			testHandler.DeleteIssue(httptest.NewRecorder(), cleanupReq)
		})
	}
	return created, w
}

// A valid branch_name is stored (trimmed) and echoed on the create response
// and subsequent reads — the worktrees add-on reads it off the issue row.
func TestCreateIssueStoresBranchName(t *testing.T) {
	created, w := createIssueForBranchTest(t, map[string]any{
		"title":       "Issue with custom branch",
		"branch_name": "  feature/custom-checkout  ",
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("CreateIssue: expected 201, got %d: %s", w.Code, w.Body.String())
	}
	if created.BranchName != "feature/custom-checkout" {
		t.Fatalf("create response branch_name = %q, want trimmed 'feature/custom-checkout'", created.BranchName)
	}

	getW := httptest.NewRecorder()
	getReq := newRequest("GET", "/api/issues/"+created.ID, nil)
	getReq = withURLParam(getReq, "id", created.ID)
	testHandler.GetIssue(getW, getReq)
	if getW.Code != http.StatusOK {
		t.Fatalf("GetIssue: expected 200, got %d: %s", getW.Code, getW.Body.String())
	}
	var fetched IssueResponse
	_ = json.NewDecoder(getW.Body).Decode(&fetched)
	if fetched.BranchName != "feature/custom-checkout" {
		t.Fatalf("GetIssue branch_name = %q, want 'feature/custom-checkout'", fetched.BranchName)
	}
}

// Omitted branch_name keeps the identifier-derived default ("" on the row).
func TestCreateIssueBranchNameDefaultsEmpty(t *testing.T) {
	created, w := createIssueForBranchTest(t, map[string]any{
		"title": "Issue without custom branch",
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("CreateIssue: expected 201, got %d: %s", w.Code, w.Body.String())
	}
	if created.BranchName != "" {
		t.Fatalf("branch_name = %q, want empty", created.BranchName)
	}
}

// Malformed git refs are rejected with a clean 400 before any row is written,
// so a bad name never reaches a daemon's `git worktree add`.
func TestCreateIssueRejectsInvalidBranchName(t *testing.T) {
	for _, bad := range []string{"has space", "-leading", "a..b", "end/", "a?b", ".hidden"} {
		_, w := createIssueForBranchTest(t, map[string]any{
			"title":       "Issue with bad branch " + bad,
			"branch_name": bad,
		})
		if w.Code != http.StatusBadRequest {
			t.Fatalf("CreateIssue(branch_name=%q): expected 400, got %d: %s", bad, w.Code, w.Body.String())
		}
		if !strings.Contains(w.Body.String(), "invalid branch_name") {
			t.Fatalf("CreateIssue(branch_name=%q): expected 'invalid branch_name' in error, got %s", bad, w.Body.String())
		}
	}
}

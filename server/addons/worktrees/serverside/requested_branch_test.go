package serverside

import (
	"testing"

	"github.com/google/uuid"

	"github.com/multica-ai/multica/server/addons/worktrees/shared"
)

// insertIssueWithBranch inserts a minimal host issue row carrying branch_name
// and registers its cleanup. The store's CreateWorktreeRow reads the branch
// from this row, so these tests exercise the real join, not a fixture shortcut.
func insertIssueWithBranch(t *testing.T, branch string) string {
	t.Helper()
	var id string
	if err := testPool.QueryRow(ctx(),
		`INSERT INTO issue (workspace_id, title, status, priority, creator_type, creator_id, number, branch_name)
		 VALUES ($1, 'branch test', 'todo', 'none', 'member', $2, $3, $4)
		 RETURNING id::text`,
		testWorkspaceID, uuid.NewString(), 900000+testIssueNumber(), branch).Scan(&id); err != nil {
		t.Fatalf("insert issue: %v", err)
	}
	t.Cleanup(func() { _, _ = testPool.Exec(ctx(), `DELETE FROM issue WHERE id=$1`, id) })
	return id
}

var issueNumberSeq int32

func testIssueNumber() int32 {
	issueNumberSeq++
	return issueNumberSeq
}

// CreateWorktreeRow copies issue.branch_name into requested_branch, and every
// job kind that touches branches (init, cleanup) carries it to the daemon.
func TestRequestedBranchFlowsToJobs(t *testing.T) {
	s := NewStore(testPool)
	issueID := insertIssueWithBranch(t, "feature/shared-work")

	if _, err := s.CreateWorktreeRow(ctx(), issueID, "WAT-60", testWorkspaceID, repoA); err != nil {
		t.Fatalf("create: %v", err)
	}

	const daemonID = "daemon-branch"
	jobs, err := s.ClaimInitJobs(ctx(), daemonID, testWorkspaceID)
	if err != nil {
		t.Fatalf("ClaimInitJobs: %v", err)
	}
	var wt string
	for _, j := range jobs {
		if j.IssueID != issueID {
			continue
		}
		wt = j.WorktreeID
		if j.Branch != "feature/shared-work" {
			t.Fatalf("init job branch = %q, want feature/shared-work", j.Branch)
		}
	}
	if wt == "" {
		t.Fatalf("no init job claimed for issue %s", issueID)
	}

	// Ready the row, then close the issue → the cleanup job must carry the
	// branch too (the daemon skips branch deletion for user branches).
	if _, err := s.ReportStatus(ctx(), daemonID, wt, shared.StatusReport{
		Kind: shared.JobInit, Status: shared.StatusReady, Branch: "feature/shared-work",
	}); err != nil {
		t.Fatalf("report ready: %v", err)
	}
	if _, err := s.MarkCleanupForIssue(ctx(), issueID); err != nil {
		t.Fatalf("mark cleanup: %v", err)
	}
	actions, err := s.ClaimWorktreeActionJobs(ctx(), daemonID, testWorkspaceID)
	if err != nil {
		t.Fatalf("ClaimWorktreeActionJobs: %v", err)
	}
	found := false
	for _, j := range actions {
		if j.WorktreeID == wt && j.Kind == shared.JobCleanup {
			found = true
			if j.Branch != "feature/shared-work" {
				t.Fatalf("cleanup job branch = %q, want feature/shared-work", j.Branch)
			}
		}
	}
	if !found {
		t.Fatalf("no cleanup job claimed for worktree %s", wt)
	}
}

// An issue with no branch_name (or no issue row at all — e.g. rows created for
// issues the host deleted mid-flight) yields an empty requested branch, which
// the daemon maps to the identifier-derived default.
func TestRequestedBranchDefaultsEmpty(t *testing.T) {
	s := NewStore(testPool)

	// Real issue row, unset branch_name.
	withRow := insertIssueWithBranch(t, "")
	if _, err := s.CreateWorktreeRow(ctx(), withRow, "WAT-61", testWorkspaceID, repoA); err != nil {
		t.Fatalf("create: %v", err)
	}
	// No issue row at all.
	noRow := newIssueID()
	if _, err := s.CreateWorktreeRow(ctx(), noRow, "WAT-62", testWorkspaceID, repoB); err != nil {
		t.Fatalf("create: %v", err)
	}

	jobs, err := s.ClaimInitJobs(ctx(), "daemon-branch-default", testWorkspaceID)
	if err != nil {
		t.Fatalf("ClaimInitJobs: %v", err)
	}
	for _, j := range jobs {
		if (j.IssueID == withRow || j.IssueID == noRow) && j.Branch != "" {
			t.Fatalf("job for %s carries branch %q, want empty (identifier default)", j.IssueID, j.Branch)
		}
	}
}

// Reopening a closed issue re-arms the tombstoned row with the branch intact.
func TestRequestedBranchSurvivesReopen(t *testing.T) {
	s := NewStore(testPool)
	issueID := insertIssueWithBranch(t, "feature/reopen")

	if _, err := s.CreateWorktreeRow(ctx(), issueID, "WAT-63", testWorkspaceID, repoA); err != nil {
		t.Fatalf("create: %v", err)
	}
	const daemonID = "daemon-reopen"
	jobs, err := s.ClaimInitJobs(ctx(), daemonID, testWorkspaceID)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	var wt string
	for _, j := range jobs {
		if j.IssueID == issueID {
			wt = j.WorktreeID
		}
	}
	if wt == "" {
		t.Fatalf("no init job for issue")
	}
	// Close while initializing → row tombstones to 'removed'.
	if _, err := s.MarkCleanupForIssue(ctx(), issueID); err != nil {
		t.Fatalf("mark cleanup: %v", err)
	}
	// Reopen (same issue, still assigned) → re-armed pending row keeps the branch.
	if _, err := s.CreateWorktreeRow(ctx(), issueID, "WAT-63", testWorkspaceID, repoA); err != nil {
		t.Fatalf("re-create: %v", err)
	}
	again, err := s.ClaimInitJobs(ctx(), daemonID, testWorkspaceID)
	if err != nil {
		t.Fatalf("re-claim: %v", err)
	}
	for _, j := range again {
		if j.IssueID == issueID {
			if j.Branch != "feature/reopen" {
				t.Fatalf("re-armed job branch = %q, want feature/reopen", j.Branch)
			}
			return
		}
	}
	t.Fatalf("no re-armed init job for reopened issue")
}

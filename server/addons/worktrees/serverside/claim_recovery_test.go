package serverside

import (
	"testing"

	"github.com/multica-ai/multica/server/addons/worktrees/shared"
)

// backdateDaemonSeen makes a daemon look silent for long enough that the
// offline-owner rescue window (5 minutes) has elapsed.
func backdateDaemonSeen(t *testing.T, daemonID string) {
	t.Helper()
	if _, err := testPool.Exec(ctx(),
		`UPDATE worktree_daemon_seen SET last_seen_at = now() - interval '10 minutes'
		 WHERE workspace_id=$1 AND daemon_id=$2`, testWorkspaceID, daemonID); err != nil {
		t.Fatalf("backdate daemon seen: %v", err)
	}
}

// claimForIssue claims init jobs for daemonID and returns the job for issueID
// (nil when none was claimed).
func claimForIssue(t *testing.T, s *Store, daemonID, issueID string) *shared.Job {
	t.Helper()
	jobs, err := s.ClaimInitJobs(ctx(), daemonID, testWorkspaceID)
	if err != nil {
		t.Fatalf("ClaimInitJobs(%s): %v", daemonID, err)
	}
	for i := range jobs {
		if jobs[i].IssueID == issueID {
			return &jobs[i]
		}
	}
	return nil
}

// A daemon restart between claim and report used to wedge the row in
// 'initializing' forever — the poll only claimed pending+unowned rows, so
// every subsequent task on the issue timed out in the eager-checkout wait.
// The restart reconcile now resets this daemon's own orphans back to pending.
func TestReconcileRequeuesOwnOrphanedInit(t *testing.T) {
	s := NewStore(testPool)
	issueID := newIssueID()
	const daemonID = "daemon-restart"

	if _, err := s.CreateWorktreeRow(ctx(), issueID, "WAT-70", testWorkspaceID, repoA); err != nil {
		t.Fatalf("create: %v", err)
	}
	if job := claimForIssue(t, s, daemonID, issueID); job == nil {
		t.Fatalf("no init job claimed")
	}
	// Process dies mid-init. New process reconciles at loop start, BEFORE any
	// claim — its orphan returns to pending and is claimable again.
	if err := s.ReconcileDaemon(ctx(), daemonID, testWorkspaceID); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	job := claimForIssue(t, s, daemonID, issueID)
	if job == nil {
		t.Fatalf("orphaned init row was not re-claimable after restart reconcile")
	}
	if job.SetupTaskID == "" {
		t.Fatalf("re-claimed job carries no fresh setup channel")
	}
}

// A daemon that disappears entirely (crash without restart, reinstall under a
// new id, machine gone) stops touching worktree_daemon_seen; once it has been
// silent past the rescue window, ANY live daemon may steal its initializing
// rows. A late init report from the old owner must be rejected.
func TestClaimInitJobsRescuesOfflineOwner(t *testing.T) {
	s := NewStore(testPool)
	issueID := newIssueID()
	const deadDaemon = "daemon-dead"
	const liveDaemon = "daemon-live"

	if _, err := s.CreateWorktreeRow(ctx(), issueID, "WAT-71", testWorkspaceID, repoA); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := s.TouchDaemon(ctx(), testWorkspaceID, deadDaemon); err != nil {
		t.Fatalf("touch: %v", err)
	}
	job := claimForIssue(t, s, deadDaemon, issueID)
	if job == nil {
		t.Fatalf("no init job claimed by dead daemon")
	}

	// While the owner is fresh, other daemons must NOT steal the row.
	if stolen := claimForIssue(t, s, liveDaemon, issueID); stolen != nil {
		t.Fatalf("initializing row with a fresh owner was stolen: %+v", stolen)
	}

	backdateDaemonSeen(t, deadDaemon)
	rescued := claimForIssue(t, s, liveDaemon, issueID)
	if rescued == nil {
		t.Fatalf("initializing row with an offline owner was not rescued")
	}

	// The dead daemon's late report must not clobber the new owner's claim.
	if _, err := s.ReportStatus(ctx(), deadDaemon, rescued.WorktreeID, shared.StatusReport{
		Kind: shared.JobInit, Status: shared.StatusReady, Path: "/stale/path", Branch: "stale",
	}); err != nil {
		t.Fatalf("late report errored (should be a silent no-op): %v", err)
	}
	got, err := s.Get(ctx(), rescued.WorktreeID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.OwnerDaemonID != liveDaemon || got.Status != shared.StatusInitializing || got.Path == "/stale/path" {
		t.Fatalf("late report from dead owner mutated the row: %+v", got)
	}
}

// A cleanup orphaned by a dead daemon — either the queued action was never
// claimed, or the row is stuck 'cleaning' after the action was consumed — is
// rescued by any live daemon so the row can reach 'removed' and the issue can
// be re-armed later, instead of wedging every future checkout.
func TestClaimActionJobsRescuesOrphanedCleanup(t *testing.T) {
	s := NewStore(testPool)
	const deadDaemon = "daemon-dead-clean"
	const liveDaemon = "daemon-live-clean"

	// Case 1: cleanup queued on a dead daemon's row (never claimed).
	issueA := newIssueID()
	wA := makeReady(t, s, issueA, "WAT-72", repoA, deadDaemon, shared.StatusReport{})
	if _, err := s.MarkCleanupForIssue(ctx(), issueA); err != nil {
		t.Fatalf("mark cleanup: %v", err)
	}
	// Case 2: 'cleaning' with the action already consumed (died mid-removal).
	issueB := newIssueID()
	wB := makeReady(t, s, issueB, "WAT-73", repoB, deadDaemon, shared.StatusReport{})
	if _, err := s.MarkCleanupForIssue(ctx(), issueB); err != nil {
		t.Fatalf("mark cleanup: %v", err)
	}
	if _, err := s.ClaimWorktreeActionJobs(ctx(), deadDaemon, testWorkspaceID); err != nil {
		t.Fatalf("dead daemon claims own cleanup: %v", err)
	}

	backdateDaemonSeen(t, deadDaemon)
	if err := s.TouchDaemon(ctx(), testWorkspaceID, liveDaemon); err != nil {
		t.Fatalf("touch live: %v", err)
	}
	jobs, err := s.ClaimWorktreeActionJobs(ctx(), liveDaemon, testWorkspaceID)
	if err != nil {
		t.Fatalf("rescue claim: %v", err)
	}
	gotCleanup := map[string]bool{}
	for _, j := range jobs {
		if j.Kind == shared.JobCleanup {
			gotCleanup[j.WorktreeID] = true
		}
	}
	if !gotCleanup[wA.ID] {
		t.Fatalf("queued cleanup on a dead daemon's row was not rescued")
	}
	if !gotCleanup[wB.ID] {
		t.Fatalf("'cleaning' row whose action died mid-flight was not rescued")
	}
	// Ownership transferred: the rescuer's completion report is accepted.
	if _, err := s.ReportStatus(ctx(), liveDaemon, wA.ID, shared.StatusReport{Kind: shared.JobCleanup}); err != nil {
		t.Fatalf("rescuer cleanup report: %v", err)
	}
	got, err := s.Get(ctx(), wA.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Status != shared.StatusRemoved {
		t.Fatalf("rescued cleanup did not reach removed: %+v", got)
	}
}

// Reconcile re-queues this daemon's own cleanup that died mid-removal.
func TestReconcileRequeuesOwnStalledCleanup(t *testing.T) {
	s := NewStore(testPool)
	const daemonID = "daemon-restart-clean"
	issueID := newIssueID()
	w := makeReady(t, s, issueID, "WAT-74", repoA, daemonID, shared.StatusReport{})
	if _, err := s.MarkCleanupForIssue(ctx(), issueID); err != nil {
		t.Fatalf("mark cleanup: %v", err)
	}
	// Consume the action, then die before reporting.
	if _, err := s.ClaimWorktreeActionJobs(ctx(), daemonID, testWorkspaceID); err != nil {
		t.Fatalf("claim: %v", err)
	}
	if err := s.ReconcileDaemon(ctx(), daemonID, testWorkspaceID); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	jobs, err := s.ClaimWorktreeActionJobs(ctx(), daemonID, testWorkspaceID)
	if err != nil {
		t.Fatalf("re-claim: %v", err)
	}
	found := false
	for _, j := range jobs {
		if j.WorktreeID == w.ID && j.Kind == shared.JobCleanup {
			found = true
		}
	}
	if !found {
		t.Fatalf("stalled cleanup was not re-queued by restart reconcile")
	}
}

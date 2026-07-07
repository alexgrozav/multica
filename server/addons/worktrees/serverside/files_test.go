package serverside

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/addons/worktrees/shared"
)

// Exercises the Project-tab file-list flow end to end over HTTP: poll targets →
// daemon report (owner-gated) → WS event → UI list → cleanup deletion. Uses the
// shared DB harness from serverside_test.go.

func postFilesReq(worktreeID, daemonID, body string) *http.Request {
	return httptest.NewRequest("POST",
		"/api/daemon/worktree/worktrees/"+worktreeID+"/files?daemon_id="+daemonID,
		strings.NewReader(body))
}

func TestFileReportListAndScanTargets(t *testing.T) {
	sink := &eventSink{}
	m := newTestModule("member", sink)
	router := testRouter(m)
	issueID := newIssueID()
	w := makeReady(t, m.store, issueID, "WAT-7", repoA, "daemon-1", shared.StatusReport{})

	// A ready worktree is a scan target with an empty digest before any report.
	targets, err := m.store.FileScanTargets(ctx(), "daemon-1", testWorkspaceID)
	if err != nil {
		t.Fatalf("FileScanTargets: %v", err)
	}
	var target *shared.FileScanTarget
	for i := range targets {
		if targets[i].WorktreeID == w.ID {
			target = &targets[i]
		}
	}
	if target == nil || target.Digest != "" || target.IssueID != issueID || target.RepoURL != repoA {
		t.Fatalf("scan target = %+v, want empty-digest target for %s", target, w.ID)
	}

	// Poll response carries the target too.
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest("GET",
		"/api/daemon/worktree/jobs?workspace_id="+testWorkspaceID+"&daemon_id=daemon-1", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("poll = %d: %s", rec.Code, rec.Body.String())
	}
	var polled shared.JobsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &polled); err != nil {
		t.Fatalf("decode poll: %v", err)
	}
	found := false
	for _, tg := range polled.FileScan {
		if tg.WorktreeID == w.ID {
			found = true
		}
	}
	if !found {
		t.Fatalf("poll file_scan missing worktree %s: %+v", w.ID, polled.FileScan)
	}

	// A non-owner daemon's report is rejected.
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, postFilesReq(w.ID, "daemon-imposter", `{"digest":"x","paths":["a.txt"]}`))
	if rec.Code != http.StatusConflict {
		t.Fatalf("imposter report = %d, want 409", rec.Code)
	}

	// The owner's report lands, updates the stored digest, and publishes the
	// files event.
	before := sink.count(shared.EventWorktreeFiles)
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, postFilesReq(w.ID, "daemon-1", `{"digest":"d1","paths":["README.md","src/main.ts"],"truncated":false}`))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("owner report = %d, want 204: %s", rec.Code, rec.Body.String())
	}
	if got := sink.count(shared.EventWorktreeFiles) - before; got != 1 {
		t.Fatalf("published %d files events, want 1", got)
	}

	targets, err = m.store.FileScanTargets(ctx(), "daemon-1", testWorkspaceID)
	if err != nil {
		t.Fatalf("FileScanTargets after report: %v", err)
	}
	for _, tg := range targets {
		if tg.WorktreeID == w.ID && tg.Digest != "d1" {
			t.Fatalf("stored digest = %q, want d1", tg.Digest)
		}
	}

	// UI list returns the stored paths for the issue.
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest("GET", "/api/worktree/issues/"+issueID+"/files", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET files = %d: %s", rec.Code, rec.Body.String())
	}
	var lists []shared.WorktreeFiles
	if err := json.Unmarshal(rec.Body.Bytes(), &lists); err != nil {
		t.Fatalf("decode files: %v", err)
	}
	if len(lists) != 1 || lists[0].WorktreeID != w.ID || lists[0].Digest != "d1" {
		t.Fatalf("files list = %+v, want one entry for %s@d1", lists, w.ID)
	}
	if len(lists[0].Paths) != 2 || lists[0].Paths[0] != "README.md" {
		t.Fatalf("paths = %v, want [README.md src/main.ts]", lists[0].Paths)
	}

	// A re-report replaces (upsert), not duplicates.
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, postFilesReq(w.ID, "daemon-1", `{"digest":"d2","paths":["README.md"],"truncated":true}`))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("re-report = %d, want 204", rec.Code)
	}
	lists, err = m.store.FilesByIssue(ctx(), issueID, testWorkspaceID)
	if err != nil {
		t.Fatalf("FilesByIssue: %v", err)
	}
	if len(lists) != 1 || lists[0].Digest != "d2" || !lists[0].Truncated || len(lists[0].Paths) != 1 {
		t.Fatalf("after upsert = %+v, want single truncated d2 entry", lists)
	}

	// Cleanup removes the stored file list with the worktree, and the removed
	// worktree stops being a scan target.
	if _, err := m.store.ReportStatus(ctx(), "daemon-1", w.ID, shared.StatusReport{Kind: shared.JobCleanup}); err != nil {
		t.Fatalf("cleanup report: %v", err)
	}
	lists, err = m.store.FilesByIssue(ctx(), issueID, testWorkspaceID)
	if err != nil {
		t.Fatalf("FilesByIssue after cleanup: %v", err)
	}
	if len(lists) != 0 {
		t.Fatalf("files after cleanup = %+v, want none", lists)
	}
	targets, _ = m.store.FileScanTargets(ctx(), "daemon-1", testWorkspaceID)
	for _, tg := range targets {
		if tg.WorktreeID == w.ID {
			t.Fatalf("removed worktree still a scan target")
		}
	}

	// Late report for the removed worktree is refused (no resurrection).
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, postFilesReq(w.ID, "daemon-1", `{"digest":"d3","paths":["ghost.txt"]}`))
	if rec.Code != http.StatusConflict {
		t.Fatalf("late report = %d, want 409", rec.Code)
	}
}

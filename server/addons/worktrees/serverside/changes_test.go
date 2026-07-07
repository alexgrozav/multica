package serverside

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/addons/worktrees/shared"
)

// Exercises the Changes-tab flow end to end over HTTP: poll targets carry the
// changes digest → daemon report (owner-gated) → WS event → UI list → cleanup
// deletion, plus the on-demand diff relay. Uses the shared DB harness from
// serverside_test.go.

func postChangesReq(worktreeID, daemonID, body string) *http.Request {
	return httptest.NewRequest("POST",
		"/api/daemon/worktree/worktrees/"+worktreeID+"/changes?daemon_id="+daemonID,
		strings.NewReader(body))
}

func TestChangesReportListAndScanTargets(t *testing.T) {
	sink := &eventSink{}
	m := newTestModule("member", sink)
	router := testRouter(m)
	issueID := newIssueID()
	w := makeReady(t, m.store, issueID, "WAT-50", repoA, "daemon-ch", shared.StatusReport{})

	// A ready worktree's scan target starts with an empty changes digest.
	targets, err := m.store.FileScanTargets(ctx(), "daemon-ch", testWorkspaceID)
	if err != nil {
		t.Fatalf("FileScanTargets: %v", err)
	}
	var target *shared.FileScanTarget
	for i := range targets {
		if targets[i].WorktreeID == w.ID {
			target = &targets[i]
		}
	}
	if target == nil || target.ChangesDigest != "" {
		t.Fatalf("scan target = %+v, want empty changes digest for %s", target, w.ID)
	}

	// A non-owner daemon's report is rejected.
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, postChangesReq(w.ID, "daemon-imposter", `{"digest":"x","files":[]}`))
	if rec.Code != http.StatusConflict {
		t.Fatalf("imposter report = %d, want 409", rec.Code)
	}

	// The owner's report lands, updates the stored digest, and publishes the
	// changes event.
	before := sink.count(shared.EventWorktreeChanges)
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, postChangesReq(w.ID, "daemon-ch",
		`{"digest":"c1","base":"origin/main","files":[{"path":"src/main.ts","status":"modified","additions":3,"deletions":1,"uncommitted":true},{"path":"new.ts","status":"added","additions":10,"deletions":0}],"truncated":false}`))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("owner report = %d, want 204: %s", rec.Code, rec.Body.String())
	}
	if got := sink.count(shared.EventWorktreeChanges) - before; got != 1 {
		t.Fatalf("published %d changes events, want 1", got)
	}

	targets, err = m.store.FileScanTargets(ctx(), "daemon-ch", testWorkspaceID)
	if err != nil {
		t.Fatalf("FileScanTargets after report: %v", err)
	}
	for _, tg := range targets {
		if tg.WorktreeID == w.ID && tg.ChangesDigest != "c1" {
			t.Fatalf("stored changes digest = %q, want c1", tg.ChangesDigest)
		}
	}

	// UI list returns the stored entries for the issue.
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest("GET", "/api/worktree/issues/"+issueID+"/changes", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET changes = %d: %s", rec.Code, rec.Body.String())
	}
	var lists []shared.WorktreeChanges
	if err := json.Unmarshal(rec.Body.Bytes(), &lists); err != nil {
		t.Fatalf("decode changes: %v", err)
	}
	if len(lists) != 1 || lists[0].WorktreeID != w.ID || lists[0].Digest != "c1" || lists[0].Base != "origin/main" {
		t.Fatalf("changes list = %+v, want one entry for %s@c1", lists, w.ID)
	}
	if len(lists[0].Files) != 2 || lists[0].Files[0].Path != "src/main.ts" || !lists[0].Files[0].Uncommitted ||
		lists[0].Files[1].Status != shared.ChangeAdded || lists[0].Files[1].Additions != 10 {
		t.Fatalf("files = %+v, want the reported entries", lists[0].Files)
	}

	// A re-report replaces (upsert), not duplicates.
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, postChangesReq(w.ID, "daemon-ch", `{"digest":"c2","base":"origin/main","files":[],"truncated":true}`))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("re-report = %d, want 204", rec.Code)
	}
	lists, err = m.store.ChangesByIssue(ctx(), issueID, testWorkspaceID)
	if err != nil {
		t.Fatalf("ChangesByIssue: %v", err)
	}
	if len(lists) != 1 || lists[0].Digest != "c2" || !lists[0].Truncated || len(lists[0].Files) != 0 {
		t.Fatalf("after upsert = %+v, want single truncated c2 entry", lists)
	}

	// Cleanup removes the stored changes with the worktree.
	if _, err := m.store.ReportStatus(ctx(), "daemon-ch", w.ID, shared.StatusReport{Kind: shared.JobCleanup}); err != nil {
		t.Fatalf("cleanup report: %v", err)
	}
	lists, err = m.store.ChangesByIssue(ctx(), issueID, testWorkspaceID)
	if err != nil {
		t.Fatalf("ChangesByIssue after cleanup: %v", err)
	}
	if len(lists) != 0 {
		t.Fatalf("changes after cleanup = %+v, want none", lists)
	}

	// Late report for the removed worktree is refused (no resurrection).
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, postChangesReq(w.ID, "daemon-ch", `{"digest":"c3","files":[]}`))
	if rec.Code != http.StatusConflict {
		t.Fatalf("late report = %d, want 409", rec.Code)
	}
}

// TestFileDiffRelay drives the diff round trip over HTTP: the UI GET blocks,
// the daemon poll claims a diff op, and the posted result unblocks the GET
// with both content sides.
func TestFileDiffRelay(t *testing.T) {
	m := newTestModule("member", &eventSink{})
	issueID := newIssueID()
	wt := makeReady(t, m.Store(), issueID, "WAT-51", repoA, "daemon-diff", shared.StatusReport{})
	srv := httptest.NewServer(testRouter(m))
	defer srv.Close()

	type out struct {
		status int
		fd     shared.FileDiff
	}
	got := make(chan out, 1)
	go func() {
		resp, err := http.Get(srv.URL + "/api/worktree/issues/" + issueID + "/" + wt.ID + "/diff?path=src/main.ts")
		if err != nil {
			got <- out{status: -1}
			return
		}
		defer resp.Body.Close()
		var fd shared.FileDiff
		_ = json.NewDecoder(resp.Body).Decode(&fd)
		got <- out{resp.StatusCode, fd}
	}()

	ops := pollFileOps(t, srv, "daemon-diff")
	if len(ops) != 1 {
		t.Fatalf("claimed %d ops, want 1", len(ops))
	}
	op := ops[0]
	if op.Kind != shared.FileOpDiff || op.Path != "src/main.ts" || op.WorktreeID != wt.ID {
		t.Fatalf("op = %+v, want diff src/main.ts on the worktree", op)
	}

	if code := postFileOpResult(t, srv, op.ID, shared.FileOpResult{
		Content: "new\n", OldContent: "old\n", Size: 4,
	}); code != http.StatusNoContent {
		t.Fatalf("post result status = %d, want 204", code)
	}

	select {
	case o := <-got:
		if o.status != http.StatusOK || o.fd.NewContent != "new\n" || o.fd.OldContent != "old\n" ||
			o.fd.Path != "src/main.ts" || o.fd.WorktreeID != wt.ID {
			t.Fatalf("diff response = %+v, want 200 with both sides", o)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("UI diff never unblocked")
	}

	// Invalid path fails fast with no relay.
	resp, err := http.Get(srv.URL + "/api/worktree/issues/" + issueID + "/" + wt.ID + "/diff?path=..%2Fescape")
	if err != nil {
		t.Fatalf("bad-path get: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("bad path status = %d, want 400", resp.StatusCode)
	}
}

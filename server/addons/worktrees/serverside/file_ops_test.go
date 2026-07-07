package serverside

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/addons/worktrees/shared"
)

// pollFileOps polls the daemon jobs endpoint until a file op shows up (the UI
// request registers it asynchronously) or the deadline passes.
func pollFileOps(t *testing.T, srv *httptest.Server, daemonID string) []shared.FileOp {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		resp, err := http.Get(srv.URL + "/api/daemon/worktree/jobs?workspace_id=" + testWorkspaceID + "&daemon_id=" + daemonID)
		if err != nil {
			t.Fatalf("poll: %v", err)
		}
		var jr shared.JobsResponse
		if err := json.NewDecoder(resp.Body).Decode(&jr); err != nil {
			t.Fatalf("decode poll: %v", err)
		}
		resp.Body.Close()
		if len(jr.FileOps) > 0 {
			return jr.FileOps
		}
		if time.Now().After(deadline) {
			t.Fatal("file op never appeared on the daemon poll")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func postFileOpResult(t *testing.T, srv *httptest.Server, opID string, res shared.FileOpResult) int {
	t.Helper()
	b, _ := json.Marshal(res)
	resp, err := http.Post(srv.URL+"/api/daemon/worktree/file-ops/"+opID+"/result", "application/json", bytes.NewReader(b))
	if err != nil {
		t.Fatalf("post result: %v", err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode
}

// TestFileReadRelay drives the full read round trip over HTTP: the UI GET
// blocks, the daemon poll claims the op exactly once, the posted result
// unblocks the GET with the content.
func TestFileReadRelay(t *testing.T) {
	m := newTestModule("member", &eventSink{})
	issueID := newIssueID()
	wt := makeReady(t, m.Store(), issueID, "WAT-40", repoA, "daemon-file-r", shared.StatusReport{})
	srv := httptest.NewServer(testRouter(m))
	defer srv.Close()

	type out struct {
		status int
		fc     shared.FileContent
	}
	got := make(chan out, 1)
	go func() {
		resp, err := http.Get(srv.URL + "/api/worktree/issues/" + issueID + "/" + wt.ID + "/file?path=src/main.ts")
		if err != nil {
			got <- out{status: -1}
			return
		}
		defer resp.Body.Close()
		var fc shared.FileContent
		_ = json.NewDecoder(resp.Body).Decode(&fc)
		got <- out{resp.StatusCode, fc}
	}()

	ops := pollFileOps(t, srv, "daemon-file-r")
	if len(ops) != 1 {
		t.Fatalf("claimed %d ops, want 1", len(ops))
	}
	op := ops[0]
	if op.Kind != shared.FileOpRead || op.Path != "src/main.ts" || op.WorktreeID != wt.ID || op.WorkspaceID != testWorkspaceID {
		t.Fatalf("op = %+v, want read src/main.ts on the worktree", op)
	}

	// Claimed ops are not re-delivered on the next poll.
	resp, err := http.Get(srv.URL + "/api/daemon/worktree/jobs?workspace_id=" + testWorkspaceID + "&daemon_id=daemon-file-r")
	if err != nil {
		t.Fatalf("second poll: %v", err)
	}
	var jr shared.JobsResponse
	_ = json.NewDecoder(resp.Body).Decode(&jr)
	resp.Body.Close()
	if len(jr.FileOps) != 0 {
		t.Fatalf("second poll re-delivered %d ops, want 0", len(jr.FileOps))
	}

	if code := postFileOpResult(t, srv, op.ID, shared.FileOpResult{Content: "hello", Size: 5}); code != http.StatusNoContent {
		t.Fatalf("post result status = %d, want 204", code)
	}

	select {
	case o := <-got:
		if o.status != http.StatusOK || o.fc.Content != "hello" || o.fc.Path != "src/main.ts" || o.fc.WorktreeID != wt.ID {
			t.Fatalf("read response = %+v, want 200 with the daemon content", o)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("UI read never unblocked")
	}

	// A duplicate/late result finds no pending op.
	if code := postFileOpResult(t, srv, op.ID, shared.FileOpResult{Content: "late"}); code != http.StatusNotFound {
		t.Fatalf("late result status = %d, want 404", code)
	}
}

// TestFileWriteRelay drives the write round trip and the daemon-error mapping
// (NotFound → 404).
func TestFileWriteRelay(t *testing.T) {
	m := newTestModule("member", &eventSink{})
	issueID := newIssueID()
	wt := makeReady(t, m.Store(), issueID, "WAT-41", repoA, "daemon-file-w", shared.StatusReport{})
	srv := httptest.NewServer(testRouter(m))
	defer srv.Close()

	put := func(path, content string) chan *http.Response {
		ch := make(chan *http.Response, 1)
		go func() {
			body, _ := json.Marshal(shared.WriteFileRequest{Path: path, Content: content})
			req, _ := http.NewRequest(http.MethodPut,
				srv.URL+"/api/worktree/issues/"+issueID+"/"+wt.ID+"/file", bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				ch <- nil
				return
			}
			ch <- resp
		}()
		return ch
	}

	done := put("docs/readme.md", "# hi\n")
	ops := pollFileOps(t, srv, "daemon-file-w")
	if len(ops) != 1 || ops[0].Kind != shared.FileOpWrite || ops[0].Content != "# hi\n" {
		t.Fatalf("ops = %+v, want one write op carrying the content", ops)
	}
	postFileOpResult(t, srv, ops[0].ID, shared.FileOpResult{Size: 5})
	select {
	case resp := <-done:
		if resp == nil || resp.StatusCode != http.StatusOK {
			t.Fatalf("write response = %v, want 200", resp)
		}
		resp.Body.Close()
	case <-time.After(5 * time.Second):
		t.Fatal("UI write never unblocked")
	}

	// Daemon-side NotFound maps to a 404 for the caller.
	done = put("gone.md", "x")
	ops = pollFileOps(t, srv, "daemon-file-w")
	postFileOpResult(t, srv, ops[0].ID, shared.FileOpResult{NotFound: true, Error: "file not found"})
	select {
	case resp := <-done:
		if resp == nil || resp.StatusCode != http.StatusNotFound {
			t.Fatalf("write-missing response = %v, want 404", resp)
		}
		resp.Body.Close()
	case <-time.After(5 * time.Second):
		t.Fatal("UI write (missing) never unblocked")
	}
}

// TestFileOpValidation covers the fast-fail paths that never reach a daemon:
// bad paths, a not-ready worktree, and an offline owning machine.
func TestFileOpValidation(t *testing.T) {
	m := newTestModule("member", &eventSink{})
	s := m.Store()
	srv := httptest.NewServer(testRouter(m))
	defer srv.Close()

	get := func(issueID, wtID, path string) int {
		t.Helper()
		resp, err := http.Get(srv.URL + "/api/worktree/issues/" + issueID + "/" + wtID + "/file?path=" + path)
		if err != nil {
			t.Fatalf("get: %v", err)
		}
		defer resp.Body.Close()
		_, _ = io.Copy(io.Discard, resp.Body)
		return resp.StatusCode
	}

	// Ready + online, but the path is invalid → immediate 400.
	issueID := newIssueID()
	wt := makeReady(t, s, issueID, "WAT-42", repoA, "daemon-file-v", shared.StatusReport{})
	if code := get(issueID, wt.ID, "..%2Fescape"); code != http.StatusBadRequest {
		t.Fatalf("traversal path status = %d, want 400", code)
	}
	if code := get(issueID, wt.ID, ".git%2Fconfig"); code != http.StatusBadRequest {
		t.Fatalf(".git path status = %d, want 400", code)
	}

	// Pending worktree (never claimed) → 409 not ready.
	pendingIssue := newIssueID()
	pending, err := s.CreateWorktreeRow(ctx(), pendingIssue, "WAT-43", testWorkspaceID, repoB)
	if err != nil {
		t.Fatalf("create pending: %v", err)
	}
	if code := get(pendingIssue, pending.ID, "a.txt"); code != http.StatusConflict {
		t.Fatalf("pending worktree status = %d, want 409", code)
	}

	// Ready but the owning daemon has never been seen → 409 offline.
	offIssue := newIssueID()
	row, err := s.CreateWorktreeRow(ctx(), offIssue, "WAT-44", testWorkspaceID, repoB)
	if err != nil {
		t.Fatalf("create offline row: %v", err)
	}
	if _, err := s.ClaimInitJobs(ctx(), "daemon-file-off", testWorkspaceID); err != nil {
		t.Fatalf("claim: %v", err)
	}
	if _, err := s.ReportStatus(ctx(), "daemon-file-off", row.ID, shared.StatusReport{
		Kind: shared.JobInit, Status: shared.StatusReady,
	}); err != nil {
		t.Fatalf("report: %v", err)
	}
	if code := get(offIssue, row.ID, "a.txt"); code != http.StatusConflict {
		t.Fatalf("offline daemon status = %d, want 409", code)
	}

	// Oversized write body → 413 before any relay.
	body, _ := json.Marshal(shared.WriteFileRequest{Path: "a.txt", Content: string(make([]byte, maxFileBytes+1))})
	req, _ := http.NewRequest(http.MethodPut,
		fmt.Sprintf("%s/api/worktree/issues/%s/%s/file", srv.URL, issueID, wt.ID), bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("oversized put: %v", err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized write status = %d, want 413", resp.StatusCode)
	}
}

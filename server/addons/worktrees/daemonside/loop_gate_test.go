package daemonside

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/multica-ai/multica/server/addons/worktrees/shared"
)

// The reconcile-before-claim invariant: a workspace whose restart reconcile
// has not succeeded must not be polled for jobs at all. Reconcile resets this
// daemon's orphans from a previous life — if claims could land first, a later
// reconcile retry would reset LIVE work back to pending and double-run it.
func TestPollSkipsWorkspaceUntilReconcileSucceeds(t *testing.T) {
	var mu sync.Mutex
	reconcileCalls, jobsCalls := 0, 0
	failReconcile := true
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/api/daemon/worktree/reconcile"):
			mu.Lock()
			reconcileCalls++
			fail := failReconcile
			mu.Unlock()
			if fail {
				http.Error(w, "boom", http.StatusInternalServerError)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		case strings.HasPrefix(r.URL.Path, "/api/daemon/worktree/jobs"):
			mu.Lock()
			jobsCalls++
			mu.Unlock()
			_ = json.NewEncoder(w).Encode(shared.JobsResponse{Jobs: []shared.Job{}})
		default:
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	defer srv.Close()

	m := New(Deps{
		ServerBaseURL:  srv.URL,
		WorkspacesRoot: t.TempDir(),
		DaemonID:       "daemon-gate",
		TokenProvider:  func() string { return "test-token" },
		ListWorkspaces: func() []string { return []string{"ws-gate"} },
	})

	counts := func() (int, int) {
		mu.Lock()
		defer mu.Unlock()
		return reconcileCalls, jobsCalls
	}

	m.pollOnce(context.Background())
	if r, j := counts(); r != 1 || j != 0 {
		t.Fatalf("after failed reconcile: reconcile=%d jobs=%d, want 1/0 (claims must wait)", r, j)
	}

	mu.Lock()
	failReconcile = false
	mu.Unlock()
	m.pollOnce(context.Background())
	if r, j := counts(); r != 2 || j != 1 {
		t.Fatalf("after reconcile succeeds: reconcile=%d jobs=%d, want 2/1", r, j)
	}

	// Reconcile runs once per workspace lifetime — later ticks go straight to
	// the poll.
	m.pollOnce(context.Background())
	if r, j := counts(); r != 2 || j != 2 {
		t.Fatalf("steady state: reconcile=%d jobs=%d, want 2/2", r, j)
	}
}

package serverside

import (
	"sync"

	"github.com/multica-ai/multica/server/addons/worktrees/shared"
)

// In-memory relay for on-demand file reads/writes (the issue view's file-tab
// editor): a UI request enqueues an op for the worktree's owning daemon, the
// daemon claims it on its next poll (~2s), executes it inside the worktree, and
// POSTs the result back, which unblocks the waiting UI request.
//
// Deliberately memory-only: the op is request/response state — its HTTP caller
// is blocked on it, so persistence buys nothing (a server restart kills the
// caller too). Like the rest of this add-on, it assumes a single server
// process; a multi-instance deployment would need a shared bus instead.

type pendingFileOp struct {
	op       shared.FileOp
	daemonID string
	claimed  bool
	done     chan shared.FileOpResult // buffered(1) so complete() never blocks
}

type fileOpRegistry struct {
	mu  sync.Mutex
	ops map[string]*pendingFileOp
}

func newFileOpRegistry() *fileOpRegistry {
	return &fileOpRegistry{ops: map[string]*pendingFileOp{}}
}

// enqueue registers a pending op for the given owning daemon and returns the
// handle the caller waits on. The caller MUST drop(op.ID) when done waiting.
func (r *fileOpRegistry) enqueue(daemonID string, op shared.FileOp) *pendingFileOp {
	p := &pendingFileOp{op: op, daemonID: daemonID, done: make(chan shared.FileOpResult, 1)}
	r.mu.Lock()
	r.ops[op.ID] = p
	r.mu.Unlock()
	return p
}

// claim hands each unclaimed op for (daemon, workspace) to the poll response.
// Claimed ops stay registered until completed or dropped — they are not
// re-delivered, so a daemon that dies mid-op just times the caller out.
func (r *fileOpRegistry) claim(daemonID, wsID string) []shared.FileOp {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []shared.FileOp
	for _, p := range r.ops {
		if !p.claimed && p.daemonID == daemonID && p.op.WorkspaceID == wsID {
			p.claimed = true
			out = append(out, p.op)
		}
	}
	return out
}

// lookup returns a registered op (for the result handler's workspace check).
func (r *fileOpRegistry) lookup(opID string) (shared.FileOp, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	p, ok := r.ops[opID]
	if !ok {
		return shared.FileOp{}, false
	}
	return p.op, true
}

// complete resolves a pending op with the daemon's result. Returns false when
// the op is unknown (caller stopped waiting, or a duplicate report).
func (r *fileOpRegistry) complete(opID string, res shared.FileOpResult) bool {
	r.mu.Lock()
	p := r.ops[opID]
	delete(r.ops, opID)
	r.mu.Unlock()
	if p == nil {
		return false
	}
	p.done <- res
	return true
}

// drop unregisters an op the caller stopped waiting on (timeout / disconnect).
func (r *fileOpRegistry) drop(opID string) {
	r.mu.Lock()
	delete(r.ops, opID)
	r.mu.Unlock()
}

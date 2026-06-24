package daemonside

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/multica-ai/multica/server/addons/worktrees/shared"
)

// Run drives the poll loop until ctx is cancelled.
func (m *Module) Run(ctx context.Context) {
	t := time.NewTicker(m.deps.PollInterval)
	defer t.Stop()
	m.log().Info("worktrees daemon loop started", "interval", m.deps.PollInterval, "root", m.deps.WorkspacesRoot)
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			m.pollOnce(ctx)
		}
	}
}

func (m *Module) pollOnce(ctx context.Context) {
	for _, wsID := range m.workspaces() {
		resp, err := m.cl.poll(ctx, wsID)
		if err != nil {
			m.log().Debug("worktrees: poll failed", "workspace_id", wsID, "error", err)
			continue
		}
		for _, job := range resp.Jobs {
			job := job
			switch job.Kind {
			case shared.JobInit:
				go m.handleInit(ctx, job)
			case shared.JobSetup:
				go m.handleSetup(ctx, job)
			case shared.JobRun:
				go m.handleRun(ctx, job)
			case shared.JobStop:
				m.handleStop(job)
			case shared.JobCleanup:
				go m.handleCleanup(ctx, job)
			}
		}
	}
}

func (m *Module) workspaces() []string {
	if m.deps.ListWorkspaces == nil {
		return nil
	}
	return m.deps.ListWorkspaces()
}

func (m *Module) handleInit(ctx context.Context, job shared.Job) {
	bare, err := m.barePath(job.WorkspaceID, job.RepoURL)
	if err != nil {
		m.reportInitError(ctx, job, "prepare repo: "+err.Error())
		return
	}
	wtPath := m.worktreePath(job)
	// Serialize worktree creation on the bare repo (shared with the daemon's
	// agent-task worktree creation) — git's config/packed-refs locks can't
	// tolerate parallel mutation on the same bare clone.
	var actualBranch string
	if err := m.withRepoLock(bare, func() error {
		b, e := ensureWorktree(bare, wtPath, m.branchName(job))
		actualBranch = b
		return e
	}); err != nil {
		m.reportInitError(ctx, job, "create worktree: "+err.Error())
		return
	}

	setupStatus := shared.ScriptNone
	var exitCode *int
	errMsg := ""
	if strings.TrimSpace(job.Setup) != "" {
		setupStatus = shared.ScriptSucceeded
		code, runErr := runScript(ctx, wtPath, job.Setup, m.scriptEnv(job, wtPath), func(stream, text string) {
			m.log().Debug("worktree setup", "issue", job.IssueID, "repo", job.RepoURL, "stream", stream, "line", text)
		})
		exitCode = &code
		if runErr != nil {
			setupStatus, errMsg = shared.ScriptFailed, "setup: "+runErr.Error()
		} else if code != 0 {
			setupStatus, errMsg = shared.ScriptFailed, fmt.Sprintf("setup exited with code %d", code)
		}
	}

	// The worktree exists regardless of setup outcome, so it stays "ready" and
	// usable; setup_status + last_error surface a failed setup to the UI.
	_ = m.cl.reportStatus(ctx, job.WorktreeID, shared.StatusReport{
		Kind:        shared.JobInit,
		Status:      shared.StatusReady,
		Path:        wtPath,
		Branch:      actualBranch,
		SetupStatus: setupStatus,
		ExitCode:    exitCode,
		Error:       errMsg,
	})
}

func (m *Module) handleRun(ctx context.Context, job shared.Job) {
	wtPath := m.worktreePath(job)
	runCtx, cancel := context.WithCancel(ctx)
	m.mu.Lock()
	m.running[job.WorktreeID] = cancel
	m.mu.Unlock()
	defer func() {
		m.mu.Lock()
		delete(m.running, job.WorktreeID)
		m.mu.Unlock()
		cancel()
	}()

	stream := newLogStreamer(ctx, m.cl, job.RunTaskID)
	stopFlush := stream.start()

	code, runErr := runScript(runCtx, wtPath, job.Run, m.scriptEnv(job, wtPath), stream.add)

	runStatus, errMsg := shared.RunSucceeded, ""
	switch {
	case runCtx.Err() != nil:
		runStatus = shared.RunStopped
	case runErr != nil:
		runStatus, errMsg = shared.RunFailed, runErr.Error()
	case code != 0:
		runStatus, errMsg = shared.RunFailed, fmt.Sprintf("exited with code %d", code)
	}
	stream.add(shared.StreamStderr, finalMarker(runStatus, code))
	stopFlush()

	_ = m.cl.reportStatus(ctx, job.WorktreeID, shared.StatusReport{
		Kind:      shared.JobRun,
		RunStatus: runStatus,
		ExitCode:  &code,
		Error:     errMsg,
	})
}

// handleSetup re-runs the Setup script in an existing worktree, streaming its
// output to the run channel (so it shows in the same sidebar log viewer) and
// reporting the setup_status outcome.
func (m *Module) handleSetup(ctx context.Context, job shared.Job) {
	wtPath := m.worktreePath(job)
	stream := newLogStreamer(ctx, m.cl, job.RunTaskID)
	stopFlush := stream.start()

	code, runErr := runScript(ctx, wtPath, job.Setup, m.scriptEnv(job, wtPath), stream.add)

	setupStatus, errMsg := shared.ScriptSucceeded, ""
	switch {
	case runErr != nil:
		setupStatus, errMsg = shared.ScriptFailed, runErr.Error()
	case code != 0:
		setupStatus, errMsg = shared.ScriptFailed, fmt.Sprintf("setup exited with code %d", code)
	}
	if setupStatus == shared.ScriptSucceeded {
		stream.add(shared.StreamStderr, "[multica] setup finished")
	} else {
		stream.add(shared.StreamStderr, fmt.Sprintf("[multica] setup failed (exit %d)", code))
	}
	stopFlush()

	_ = m.cl.reportStatus(ctx, job.WorktreeID, shared.StatusReport{
		Kind:        shared.JobSetup,
		SetupStatus: setupStatus,
		ExitCode:    &code,
		Error:       errMsg,
	})
}

func (m *Module) handleStop(job shared.Job) {
	m.mu.Lock()
	cancel := m.running[job.WorktreeID]
	m.mu.Unlock()
	if cancel != nil {
		cancel() // handleRun reports run_status=stopped when the script exits
	}
}

func (m *Module) handleCleanup(ctx context.Context, job shared.Job) {
	wtPath := m.worktreePath(job)
	if strings.TrimSpace(job.Cleanup) != "" {
		_, _ = runScript(ctx, wtPath, job.Cleanup, m.scriptEnv(job, wtPath), func(stream, text string) {
			m.log().Debug("worktree cleanup", "issue", job.IssueID, "repo", job.RepoURL, "stream", stream, "line", text)
		})
	}
	if bare, err := m.barePath(job.WorkspaceID, job.RepoURL); err == nil {
		_ = m.withRepoLock(bare, func() error {
			removeWorktree(bare, wtPath, m.branchName(job))
			return nil
		})
	} else {
		_ = os.RemoveAll(wtPath)
	}
	_ = m.cl.reportStatus(ctx, job.WorktreeID, shared.StatusReport{Kind: shared.JobCleanup, Status: shared.StatusRemoved})
}

func (m *Module) reportInitError(ctx context.Context, job shared.Job, msg string) {
	m.log().Warn("worktrees: init failed", "issue", job.IssueID, "repo", job.RepoURL, "error", msg)
	_ = m.cl.reportStatus(ctx, job.WorktreeID, shared.StatusReport{
		Kind:        shared.JobInit,
		Status:      shared.StatusError,
		SetupStatus: shared.ScriptNone,
		Error:       msg,
	})
}

// ---- path / env helpers ----

func (m *Module) worktreePath(job shared.Job) string {
	return filepath.Join(m.deps.WorkspacesRoot, job.WorkspaceID, ".issue-worktrees", shortID(job.IssueID), repoName(job.RepoURL))
}

func (m *Module) branchName(job shared.Job) string {
	return "issue/" + shortID(job.IssueID) + "/" + repoName(job.RepoURL)
}

func (m *Module) scriptEnv(job shared.Job, wtPath string) []string {
	return []string{
		"MULTICA_ISSUE_ID=" + job.IssueID,
		"MULTICA_WORKSPACE_ID=" + job.WorkspaceID,
		"MULTICA_REPO_URL=" + job.RepoURL,
		"MULTICA_WORKTREE_PATH=" + wtPath,
	}
}

func (m *Module) barePath(wsID, repoURL string) (string, error) {
	if m.deps.LookupBare != nil {
		if p := m.deps.LookupBare(wsID, repoURL); p != "" {
			return p, nil
		}
	}
	if m.deps.EnsureBare != nil {
		if p, err := m.deps.EnsureBare(wsID, repoURL); err == nil && p != "" {
			return p, nil
		} else if err != nil {
			m.log().Warn("worktrees: EnsureBare failed, self-cloning", "repo", repoURL, "error", err)
		}
	}
	return m.selfClone(wsID, repoURL)
}

func (m *Module) selfClone(wsID, repoURL string) (string, error) {
	dir := filepath.Join(m.deps.WorkspacesRoot, ".issue-worktrees-cache", wsID, repoName(repoURL)+".git")
	// Serialize concurrent clones/fetches of the same self-managed cache dir.
	mu := m.localLock(dir)
	mu.Lock()
	defer mu.Unlock()
	if isGitDir(dir) {
		_, _ = runGit(dir, "fetch", "--all", "--prune")
		return dir, nil
	}
	if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
		return "", err
	}
	if out, err := runGit("", "clone", "--bare", repoURL, dir); err != nil {
		return "", fmt.Errorf("clone bare: %s: %w", strings.TrimSpace(out), err)
	}
	return dir, nil
}

// withRepoLock serializes git mutations on a bare repo: the daemon's own repo
// lock when provided (so we don't race agent-task worktree creation), otherwise
// a local per-path mutex.
func (m *Module) withRepoLock(bare string, fn func() error) error {
	if m.deps.WithRepoLock != nil {
		return m.deps.WithRepoLock(bare, fn)
	}
	mu := m.localLock(bare)
	mu.Lock()
	defer mu.Unlock()
	return fn()
}

func (m *Module) localLock(key string) *sync.Mutex {
	v, _ := m.locks.LoadOrStore(key, &sync.Mutex{})
	return v.(*sync.Mutex)
}

func finalMarker(runStatus string, code int) string {
	switch runStatus {
	case shared.RunStopped:
		return "[multica] run stopped"
	case shared.RunFailed:
		return fmt.Sprintf("[multica] run failed (exit %d)", code)
	default:
		return "[multica] run finished"
	}
}

// logStreamer batches run-output lines and flushes them to the server on a
// short interval so a chatty process doesn't trigger an HTTP POST per line.
type logStreamer struct {
	ctx       context.Context
	cl        *client
	runTaskID string

	mu  sync.Mutex
	buf []shared.LogLine
	seq int

	stop chan struct{}
	done chan struct{}
}

func newLogStreamer(ctx context.Context, cl *client, runTaskID string) *logStreamer {
	return &logStreamer{ctx: ctx, cl: cl, runTaskID: runTaskID, stop: make(chan struct{}), done: make(chan struct{})}
}

func (s *logStreamer) add(stream, text string) {
	s.mu.Lock()
	s.seq++
	s.buf = append(s.buf, shared.LogLine{Seq: s.seq, Stream: stream, Content: text})
	s.mu.Unlock()
}

func (s *logStreamer) flush() {
	s.mu.Lock()
	if len(s.buf) == 0 {
		s.mu.Unlock()
		return
	}
	batch := s.buf
	s.buf = nil
	s.mu.Unlock()
	_ = s.cl.postLogs(s.ctx, s.runTaskID, batch)
}

// start launches the flush ticker and returns a stop function that flushes the
// remainder and waits for the ticker goroutine to exit.
func (s *logStreamer) start() func() {
	t := time.NewTicker(200 * time.Millisecond)
	go func() {
		defer close(s.done)
		for {
			select {
			case <-t.C:
				s.flush()
			case <-s.ctx.Done():
				return
			case <-s.stop:
				return
			}
		}
	}()
	return func() {
		t.Stop()
		close(s.stop)
		<-s.done
		s.flush()
	}
}

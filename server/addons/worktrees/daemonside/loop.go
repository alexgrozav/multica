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
		m.reconcileOnce(ctx, wsID)
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
			case shared.JobOpen:
				go m.handleOpen(ctx, job)
			}
		}
		// A UI request is blocked on each file op, so they run concurrently and
		// never wait behind a slow job or the scan pass.
		for _, op := range resp.FileOps {
			op := op
			go m.handleFileOp(ctx, op)
		}
		m.handleTerminals(ctx, resp.Terminals)
		m.scheduleFileScans(ctx, resp.FileScan)
	}
}

// scheduleFileScans runs one background pass over the poll's file-scan targets
// (ready worktrees this daemon owns), posting a fresh file list + git status
// for each whose digest no longer matches what the server stores. At most one
// pass runs at a time — if scans outlast the poll interval, later ticks are
// skipped, not queued; the next free tick self-heals since targets ride on
// every poll.
func (m *Module) scheduleFileScans(ctx context.Context, targets []shared.FileScanTarget) {
	if len(targets) == 0 || !m.scanBusy.CompareAndSwap(false, true) {
		return
	}
	go func() {
		defer m.scanBusy.Store(false)
		for _, t := range targets {
			if ctx.Err() != nil {
				return
			}
			m.scanAndReportFiles(ctx, t)
			m.scanAndReportChanges(ctx, t)
		}
	}()
}

// scanAndReportFiles lists one worktree's project files and POSTs them when the
// digest differs from what the server holds. Change detection is stateless:
// the server's digest arrives with the target, so a daemon restart never
// re-posts an unchanged tree.
func (m *Module) scanAndReportFiles(ctx context.Context, t shared.FileScanTarget) {
	wtPath := IssueWorktreePath(m.deps.WorkspacesRoot, t.WorkspaceID, t.IssueID, t.RepoURL)
	if !isWorktree(wtPath) {
		return
	}
	paths, truncated, err := listWorktreeFiles(wtPath)
	if err != nil {
		m.log().Debug("worktrees: file scan failed", "worktree", t.WorktreeID, "error", err)
		return
	}
	digest := filesDigest(paths, truncated)
	if digest == t.Digest {
		return
	}
	if err := m.cl.postFiles(ctx, t.WorktreeID, shared.FileReport{Digest: digest, Paths: paths, Truncated: truncated}); err != nil {
		m.log().Debug("worktrees: post files failed", "worktree", t.WorktreeID, "error", err)
	}
}

// scanAndReportChanges computes one worktree's git status (changed files vs
// the branch base) and POSTs it when the digest differs from what the server
// holds — the Changes tab's live feed, change-detected exactly like the file
// list.
func (m *Module) scanAndReportChanges(ctx context.Context, t shared.FileScanTarget) {
	wtPath := IssueWorktreePath(m.deps.WorkspacesRoot, t.WorkspaceID, t.IssueID, t.RepoURL)
	if !isWorktree(wtPath) {
		return
	}
	entries, base, truncated, err := listWorktreeChanges(wtPath)
	if err != nil {
		m.log().Debug("worktrees: changes scan failed", "worktree", t.WorktreeID, "error", err)
		return
	}
	digest := changesDigest(entries, base, truncated)
	if digest == t.ChangesDigest {
		return
	}
	rep := shared.ChangesReport{Digest: digest, Base: base, Files: entries, Truncated: truncated}
	if err := m.cl.postChanges(ctx, t.WorktreeID, rep); err != nil {
		m.log().Debug("worktrees: post changes failed", "worktree", t.WorktreeID, "error", err)
	}
}

func (m *Module) workspaces() []string {
	if m.deps.ListWorkspaces == nil {
		return nil
	}
	return m.deps.ListWorkspaces()
}

// reconcileOnce resets this daemon's stale 'running' runs for a workspace the
// first time the poll loop sees it after (re)start. At that moment no run is
// live in this process, so any DB row still marked 'running' is an orphan from a
// previous process — leaving it would wedge Stop/Run for that script forever.
func (m *Module) reconcileOnce(ctx context.Context, wsID string) {
	if _, done := m.reconciled.LoadOrStore(wsID, true); done {
		return
	}
	if err := m.cl.reconcile(ctx, wsID); err != nil {
		m.log().Debug("worktrees: reconcile failed", "workspace_id", wsID, "error", err)
		m.reconciled.Delete(wsID) // retry on the next poll
	}
}

// cancelRunsFor cancels every in-flight named run of a worktree and waits
// briefly for the killed processes to exit — used before cleanup so the working
// tree isn't torn out from under a live run.
func (m *Module) cancelRunsFor(worktreeID string) {
	prefix := worktreeID + "\x00"
	m.mu.Lock()
	for key, cancel := range m.running {
		if strings.HasPrefix(key, prefix) {
			cancel()
		}
	}
	m.mu.Unlock()
	// Wait (bounded) for the run goroutines to release the worktree.
	for i := 0; i < 50; i++ {
		m.mu.Lock()
		active := false
		for key := range m.running {
			if strings.HasPrefix(key, prefix) {
				active = true
				break
			}
		}
		m.mu.Unlock()
		if !active {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// handleInit is the SINGLE owner of a per-issue worktree's checkout + first
// setup: create the worktree on the identifier branch, read multica.json, run
// Setup once, and report the discovered scripts up so the UI can render them.
func (m *Module) handleInit(ctx context.Context, job shared.Job) {
	bare, err := m.barePath(job.WorkspaceID, job.RepoURL)
	if err != nil {
		m.reportInitError(ctx, job, "prepare repo: "+err.Error())
		return
	}
	wtPath := m.worktreePath(job)
	// Branch off CURRENT origin: the bare cache may have been located by a Lookup
	// that does not fetch, leaving origin/<default> stale — creating the worktree
	// then checks out an outdated base. Fetch first (matching the daemon's own
	// agent-task checkout). Skip when the worktree already exists (a re-poll must
	// not disturb it) and treat a fetch failure as non-fatal: branch off what we
	// have rather than block the checkout. FetchBare self-locks, so it runs before
	// withRepoLock, not nested inside it.
	if !isWorktree(wtPath) && m.deps.FetchBare != nil {
		if ferr := m.deps.FetchBare(bare); ferr != nil {
			m.log().Warn("worktrees: pre-init fetch failed; worktree may branch off stale base", "repo", job.RepoURL, "error", ferr)
		}
	}
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

	mf, mfErr := readManifest(wtPath)
	setupStatus := shared.ScriptNone
	var exitCode *int
	errMsg := ""
	switch {
	case mfErr != nil:
		errMsg = mfErr.Error()
	case mf.HasSetup():
		// Stream the boot Setup to the setup channel so its logs land in the UI's
		// default Setup tab, just like a re-run (handleSetup).
		stream := newLogStreamer(ctx, m.cl, job.SetupTaskID)
		stopFlush := stream.start()
		code, runErr := runScript(ctx, wtPath, mf.Scripts.Setup, m.scriptEnv(job, wtPath), stream.add)
		exitCode = &code
		setupStatus = shared.ScriptSucceeded
		if runErr != nil {
			setupStatus, errMsg = shared.ScriptFailed, "setup: "+runErr.Error()
		} else if code != 0 {
			setupStatus, errMsg = shared.ScriptFailed, fmt.Sprintf("setup exited with code %d", code)
		}
		stream.add(shared.StreamStderr, setupMarker(setupStatus, code))
		stopFlush()
	}

	// The worktree exists regardless of setup outcome, so it becomes "ready" and
	// usable; setup_status + last_error surface a failed setup to the UI (and
	// fail the waiting agent task).
	_ = m.cl.reportStatus(ctx, job.WorktreeID, shared.StatusReport{
		Kind:        shared.JobInit,
		Status:      shared.StatusReady,
		Path:        wtPath,
		Branch:      actualBranch,
		SetupStatus: setupStatus,
		HasSetup:    mf.HasSetup(),
		HasCleanup:  mf.HasCleanup(),
		RunScripts:  mf.RunNames(),
		ExitCode:    exitCode,
		Error:       errMsg,
	})
	// Seed the Project + Changes tabs immediately (empty target digests →
	// always posts) instead of waiting for the next poll's scan pass.
	seed := shared.FileScanTarget{
		WorktreeID: job.WorktreeID, IssueID: job.IssueID, WorkspaceID: job.WorkspaceID, RepoURL: job.RepoURL,
	}
	m.scanAndReportFiles(ctx, seed)
	m.scanAndReportChanges(ctx, seed)
}

// handleRun runs one named run script from multica.json, streaming its output to
// that run's log channel. Each (worktree, name) runs independently and can be
// stopped independently.
func (m *Module) handleRun(ctx context.Context, job shared.Job) {
	wtPath := m.worktreePath(job)
	cmd := ""
	if mf, err := readManifest(wtPath); err == nil {
		cmd = mf.RunCommand(job.ScriptName)
	}
	if cmd == "" {
		_ = m.cl.reportStatus(ctx, job.WorktreeID, shared.StatusReport{
			Kind: shared.JobRun, ScriptName: job.ScriptName, RunStatus: shared.RunFailed,
			Error: "run script not found: " + job.ScriptName,
		})
		return
	}

	runCtx, cancel := context.WithCancel(ctx)
	key := runKey(job.WorktreeID, job.ScriptName)
	m.mu.Lock()
	m.running[key] = cancel
	m.mu.Unlock()
	defer func() {
		m.mu.Lock()
		delete(m.running, key)
		m.mu.Unlock()
		cancel()
	}()

	stream := newLogStreamer(ctx, m.cl, job.RunTaskID)
	stopFlush := stream.start()

	code, runErr := runScript(runCtx, wtPath, cmd, m.scriptEnv(job, wtPath), stream.add)

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
		Kind: shared.JobRun, ScriptName: job.ScriptName, RunStatus: runStatus, ExitCode: &code, Error: errMsg,
	})
}

// handleSetup re-runs the Setup script in an existing worktree, streaming its
// output to the setup channel and reporting the setup_status outcome.
func (m *Module) handleSetup(ctx context.Context, job shared.Job) {
	wtPath := m.worktreePath(job)
	mf, mfErr := readManifest(wtPath)
	if mfErr != nil {
		_ = m.cl.reportStatus(ctx, job.WorktreeID, shared.StatusReport{
			Kind: shared.JobSetup, SetupStatus: shared.ScriptFailed, Error: mfErr.Error(),
		})
		return
	}
	if !mf.HasSetup() {
		_ = m.cl.reportStatus(ctx, job.WorktreeID, shared.StatusReport{Kind: shared.JobSetup, SetupStatus: shared.ScriptNone})
		return
	}

	stream := newLogStreamer(ctx, m.cl, job.SetupTaskID)
	stopFlush := stream.start()

	code, runErr := runScript(ctx, wtPath, mf.Scripts.Setup, m.scriptEnv(job, wtPath), stream.add)

	setupStatus, errMsg := shared.ScriptSucceeded, ""
	switch {
	case runErr != nil:
		setupStatus, errMsg = shared.ScriptFailed, runErr.Error()
	case code != 0:
		setupStatus, errMsg = shared.ScriptFailed, fmt.Sprintf("setup exited with code %d", code)
	}
	stream.add(shared.StreamStderr, setupMarker(setupStatus, code))
	stopFlush()

	_ = m.cl.reportStatus(ctx, job.WorktreeID, shared.StatusReport{
		Kind: shared.JobSetup, SetupStatus: setupStatus, ExitCode: &code, Error: errMsg,
	})
}

func (m *Module) handleStop(job shared.Job) {
	m.mu.Lock()
	cancel := m.running[runKey(job.WorktreeID, job.ScriptName)]
	m.mu.Unlock()
	if cancel != nil {
		cancel() // handleRun reports run_status=stopped when the script exits
	}
}

// handleCleanup runs the manifest's Cleanup script (if any) then tears down the
// worktree + its identifier branch. Fires when the issue is marked done/cancelled.
func (m *Module) handleCleanup(ctx context.Context, job shared.Job) {
	wtPath := m.worktreePath(job)
	// Stop any run still executing in this worktree first, so the cleanup script
	// and the worktree removal don't yank the tree out from under a live process.
	// Terminal shells live in (and under) the per-issue parent, so they go too.
	m.cancelRunsFor(job.WorktreeID)
	m.killTerminalsFor(job.IssueID)
	if mf, err := readManifest(wtPath); err == nil && mf.HasCleanup() {
		_, _ = runScript(ctx, wtPath, mf.Scripts.Cleanup, m.scriptEnv(job, wtPath), func(stream, text string) {
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
	// Once this issue's last repo worktree is gone, remove the per-issue parent
	// too: in the unified model the agent works there, so it also holds the
	// runtime brief (CLAUDE.md) + hidden env scratch that no repo worktree owns
	// and that the daemon GC never touches (the worktrees dir is skipped).
	// Serialized per parent so concurrent per-repo cleanups don't race on the
	// "any worktrees left?" check — whichever runs its check last clears it.
	parent := IssueWorktreeParent(m.deps.WorkspacesRoot, job.WorkspaceID, job.IssueID)
	mu := m.localLock(parent)
	mu.Lock()
	if !hasWorktreeChild(parent) {
		_ = os.RemoveAll(parent)
	}
	mu.Unlock()
	_ = m.cl.reportStatus(ctx, job.WorktreeID, shared.StatusReport{Kind: shared.JobCleanup, Status: shared.StatusRemoved})
}

// hasWorktreeChild reports whether dir still holds at least one git-worktree
// subdir (a repo checkout). Used to decide when the per-issue parent — which
// also holds the agent's scratch/brief — can be removed wholesale on cleanup.
func hasWorktreeChild(dir string) bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, e := range entries {
		if e.IsDir() && isWorktree(filepath.Join(dir, e.Name())) {
			return true
		}
	}
	return false
}

// handleOpen opens the issue's working directory (the per-issue parent that
// holds every repo checkout + the agent's brief) in the requested local
// application. Fire-and-forget: there is nothing to report, so a failure (e.g.
// the app isn't installed) is only logged.
func (m *Module) handleOpen(ctx context.Context, job shared.Job) {
	dir := IssueWorktreeParent(m.deps.WorkspacesRoot, job.WorkspaceID, job.IssueID)
	if err := openInApp(ctx, job.OpenTarget, dir); err != nil {
		m.log().Warn("worktrees: open-in failed", "target", job.OpenTarget, "dir", dir, "error", err)
	}
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

// runKey identifies one named run of a worktree in the daemon's running map.
func runKey(worktreeID, name string) string { return worktreeID + "\x00" + name }

func (m *Module) worktreePath(job shared.Job) string {
	return IssueWorktreePath(m.deps.WorkspacesRoot, job.WorkspaceID, job.IssueID, job.RepoURL)
}

func (m *Module) branchName(job shared.Job) string {
	return IssueBranch(job.Identifier, job.IssueID)
}

func (m *Module) scriptEnv(job shared.Job, wtPath string) []string {
	return []string{
		"MULTICA_ISSUE_ID=" + job.IssueID,
		"MULTICA_ISSUE_IDENTIFIER=" + job.Identifier,
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
	dir := filepath.Join(m.deps.WorkspacesRoot, ".worktrees-cache", wsID, repoName(repoURL)+".git")
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

func setupMarker(setupStatus string, code int) string {
	if setupStatus == shared.ScriptSucceeded {
		return "[multica] setup finished"
	}
	return fmt.Sprintf("[multica] setup failed (exit %d)", code)
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

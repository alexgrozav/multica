package daemon

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	worktreesdaemon "github.com/multica-ai/multica/server/addons/worktrees/daemonside"
	"github.com/multica-ai/multica/server/internal/daemon/execenv"
	"github.com/multica-ai/multica/server/internal/daemon/repocache"
)

// worktree_addon.go is the daemon-side ADAPTER for the worktrees add-on. It is
// the only file in package daemon that the add-on touches: it reads the private
// daemon state (token, workspaces root, repo cache) and hands it to the add-on
// module's Deps port. The module (server/addons/worktrees/daemonside) imports
// nothing from package daemon, so a daemon refactor only updates this builder.
//
// Wiring seam: one `go d.startWorktreeAddon(ctx)` line in Run().
func (d *Daemon) startWorktreeAddon(ctx context.Context) {
	deps := worktreesdaemon.Deps{
		ServerBaseURL:  d.cfg.ServerBaseURL,
		WorkspacesRoot: d.cfg.WorkspacesRoot,
		DaemonID:       d.cfg.DaemonID,
		Logger:         d.logger,
		RootCtx:        d.rootCtx,
		// On-demand actions (Run, re-Setup, Open in …) are claimed off this poll,
		// so keep it short enough that they feel responsive. The poll is a single
		// lightweight GET per workspace, so 2s is cheap.
		PollInterval:  2 * time.Second,
		TokenProvider: func() string { return d.client.Token() },
		ListWorkspaces: func() []string {
			d.mu.Lock()
			defer d.mu.Unlock()
			ids := make([]string, 0, len(d.workspaces))
			for id := range d.workspaces {
				ids = append(ids, id)
			}
			return ids
		},
	}
	if d.repoCache != nil {
		deps.LookupBare = func(workspaceID, repoURL string) string {
			return d.repoCache.Lookup(workspaceID, repoURL)
		}
		deps.EnsureBare = func(workspaceID, repoURL string) (string, error) {
			if err := d.repoCache.Sync(workspaceID, []repocache.RepoInfo{{URL: repoURL}}); err != nil {
				return "", err
			}
			return d.repoCache.Lookup(workspaceID, repoURL), nil
		}
		// Refresh the bare cache before branching a new worktree so it starts from
		// current origin/<default>, not whatever a prior Lookup left cached. Same
		// self-locking fetch the daemon's own agent-task checkout uses.
		deps.FetchBare = d.repoCache.Fetch
		// Reuse the daemon's own per-repo lock so our worktree adds serialize
		// with the daemon's agent-task worktree creation on the same bare clone.
		deps.WithRepoLock = d.repoCache.WithRepoLock
	}
	worktreesdaemon.New(deps).Run(ctx)
}

// runCheckoutSetup runs the repo's multica.json Setup script in a freshly
// checked-out worktree, before the checkout returns — so the agent waits and
// then works in a prepared environment. Used by every checkout made outside
// the add-on poll loop's handleInit: the legacy (unified-disabled) eager
// checkout, the `multica repo checkout` handler, and the unified model's
// machine-local ensure. Returns the captured output and an error only when
// the Setup script itself fails (the caller fails the checkout). A
// missing/invalid multica.json is treated as "no setup" so checkouts are
// never blocked.
func (d *Daemon) runCheckoutSetup(ctx context.Context, workspaceID, repoURL, worktreePath string, extraEnv ...string) (string, error) {
	var buf strings.Builder
	ran, err := worktreesdaemon.RunSetupAt(ctx, workspaceID, repoURL, worktreePath, func(stream, text string) {
		buf.WriteString(text)
		buf.WriteByte('\n')
		d.logger.Debug("checkout setup", "repo", repoURL, "stream", stream, "line", text)
	}, extraEnv...)
	if err != nil {
		if !ran {
			// Manifest read/parse failure — don't block the checkout; skip Setup.
			d.logger.Warn("worktrees: could not read multica.json; skipping setup", "repo", repoURL, "error", err)
			return "", nil
		}
		return buf.String(), err
	}
	if ran {
		d.logger.Info("worktrees: setup script completed", "repo", repoURL, "worktree", worktreePath)
	}
	return buf.String(), nil
}

// eagerCheckoutTaskRepos makes the agent task "wait until checkout is complete
// before it starts working". In the unified model the add-on's poll loop
// (handleInit) is the SINGLE owner of the issue's authoritative checkout +
// first Setup — kicked off the moment the issue was assigned — so here the
// agent first WAITS for that workspace to be ready. But worktree rows are
// global per (issue, repo) with ONE owner daemon, while disk state is
// per-machine: "ready" may mean "checked out on ANOTHER machine" (issue
// assigned to an agent on the studio, question later asked of an agent on the
// laptop). So after the wait, ensure every task repo also exists as a LOCAL
// worktree on the same issue branch — with Setup — before the agent starts;
// on the owning machine that ensure is a no-op and the agent simply adopts
// the one existing tree (no second checkout, no double Setup).
//
// Returns the first error so the caller fails the task before StartTask (the
// agent never runs). No-op for local_directory tasks and when there is no repo
// cache.
func (d *Daemon) eagerCheckoutTaskRepos(ctx context.Context, task Task, env *execenv.Environment, agentName, perIssueDir string) error {
	if env == nil || env.LocalDirectory || d.repoCache == nil {
		return nil
	}

	if perIssueDir != "" {
		managed, err := worktreesdaemon.WaitIssueReady(ctx, worktreesdaemon.WaitParams{
			ServerBaseURL: d.cfg.ServerBaseURL,
			TokenProvider: func() string { return d.client.Token() },
			DaemonID:      d.cfg.DaemonID,
			WorkspaceID:   task.WorkspaceID,
			IssueID:       task.IssueID,
		})
		if err != nil {
			return fmt.Errorf("eager checkout: %w", err)
		}
		if !managed {
			// Not tracked by the add-on (e.g. a mention on an issue that was
			// never assigned to an agent). The local ensure below still gives
			// the agent the checked-out workspace its brief promises.
			d.logger.Warn("worktrees: issue not managed by add-on; performing local checkout only", "issue", task.IssueID)
		}
		return d.ensureLocalIssueWorktrees(ctx, task, perIssueDir)
	}

	// Legacy path (unified disabled): fresh per-task workdir, agent/* branch, with
	// Setup read from the checked-out repo's multica.json.
	coAuthored := d.workspaceCoAuthoredByEnabled(task.WorkspaceID)
	for _, repo := range task.Repos {
		url := strings.TrimSpace(repo.URL)
		if url == "" {
			continue
		}
		if err := d.ensureRepoReady(ctx, task.WorkspaceID, url); err != nil {
			return fmt.Errorf("eager checkout: repo not ready %s: %w", url, err)
		}
		result, err := d.repoCache.CreateWorktree(repocache.WorktreeParams{
			WorkspaceID:         task.WorkspaceID,
			RepoURL:             url,
			WorkDir:             env.WorkDir,
			AgentName:           agentName,
			TaskID:              task.ID,
			CoAuthoredByEnabled: coAuthored,
		})
		if err != nil {
			return fmt.Errorf("eager checkout: create worktree %s: %w", url, err)
		}
		if out, serr := d.runCheckoutSetup(ctx, task.WorkspaceID, url, result.Path); serr != nil {
			return fmt.Errorf("eager checkout: setup %s: %w\n%s", url, serr, out)
		}
	}
	return nil
}

// ensureLocalIssueWorktrees makes the issue's workspace real ON THIS MACHINE:
// for every task repo whose per-issue worktree is missing locally, create it
// on the issue branch and run its Setup script, before the agent launches.
//
// The branch semantics are those of a COLLABORATOR's checkout, never a reset:
// an existing local branch is checked out as-is, a branch on origin is
// continued from origin/<branch> (so work pushed from the owning machine is
// visible here), and only a branch that exists nowhere is created from base —
// when the two machines then both push, ordinary git non-fast-forward rules
// reconcile them. That is exactly EnsureWorktreeAt's reuseExisting=true mode;
// the owner-only force-reset (-B) mode is deliberately not used here.
//
// The created worktree is NOT reported to the server: the issue_worktree row
// keeps its single owner daemon, whose checkout stays the authoritative one
// for the sidebar (files, changes, terminals, open-in). This is purely the
// task's working copy.
func (d *Daemon) ensureLocalIssueWorktrees(ctx context.Context, task Task, perIssueDir string) error {
	branch := strings.TrimSpace(task.IssueBranch)
	if branch == "" {
		// Old server: the claim payload predates issue_branch. Keep the prior
		// behavior (wait only; the agent lazy-checks-out).
		d.logger.Warn("worktrees: task carries no issue_branch; skipping local worktree ensure", "issue", task.IssueID)
		return nil
	}
	// Serialize per issue dir so two tasks starting together on this machine
	// create + Setup each repo exactly once (the loser of the race sees the
	// worktree and skips).
	mu := issueEnsureLock(perIssueDir)
	mu.Lock()
	defer mu.Unlock()
	for _, repo := range task.Repos {
		url := strings.TrimSpace(repo.URL)
		if url == "" {
			continue
		}
		wtPath := worktreesdaemon.IssueWorktreePath(d.cfg.WorkspacesRoot, task.WorkspaceID, task.IssueID, url)
		if worktreesdaemon.IsWorktree(wtPath) {
			continue // already on this machine (add-on owner here, or a prior task's ensure)
		}
		if err := d.ensureRepoReady(ctx, task.WorkspaceID, url); err != nil {
			return fmt.Errorf("eager checkout: repo not ready %s: %w", url, err)
		}
		bare := d.repoCache.Lookup(task.WorkspaceID, url)
		if bare == "" {
			return fmt.Errorf("eager checkout: no cached clone for %s", url)
		}
		// Branch off CURRENT origin (same reasoning as handleInit): a stale
		// cache would base the branch — or miss origin/<branch> entirely — and
		// hide work pushed from the owning machine. Non-fatal: branch off what
		// we have rather than block the task.
		if ferr := d.repoCache.Fetch(bare); ferr != nil {
			d.logger.Warn("worktrees: pre-ensure fetch failed; local worktree may branch off a stale base", "repo", url, "error", ferr)
		}
		if err := d.repoCache.WithRepoLock(bare, func() error {
			_, e := worktreesdaemon.EnsureWorktreeAt(bare, wtPath, branch, true)
			return e
		}); err != nil {
			return fmt.Errorf("eager checkout: create local worktree %s: %w", url, err)
		}
		d.logger.Info("worktrees: created machine-local issue worktree",
			"issue", task.IssueID, "repo", url, "branch", branch, "path", wtPath)
		if out, serr := d.runCheckoutSetup(ctx, task.WorkspaceID, url, wtPath,
			"MULTICA_ISSUE_ID="+task.IssueID,
			"MULTICA_ISSUE_BRANCH="+branch,
		); serr != nil {
			return fmt.Errorf("eager checkout: setup %s: %w\n%s", url, serr, out)
		}
	}
	return nil
}

// issueEnsureLocks serializes ensureLocalIssueWorktrees per per-issue dir.
// Package-level so the whole mechanism stays inside this add-on adapter file.
var issueEnsureLocks sync.Map

func issueEnsureLock(dir string) *sync.Mutex {
	v, _ := issueEnsureLocks.LoadOrStore(dir, &sync.Mutex{})
	return v.(*sync.Mutex)
}

// adoptManagedWorktree reports whether a `multica repo checkout` request
// targets a worktree the add-on already manages — the repo's checkout inside
// the per-issue workdir — and, when it does, returns that checkout as-is
// (path + current branch). Adoption is what keeps an agent's habitual
// re-checkout from resetting the managed tree onto a fresh agent/* branch:
// the worktree is NEVER mutated on this path.
//
// A request qualifies when the unified model is on, the requested workdir
// lies inside this workspace's managed worktrees root (…/<ws>/worktrees/…),
// and the repo's subdirectory there is an existing git worktree. Anything
// else falls through to the legacy CreateWorktree behavior.
func (d *Daemon) adoptManagedWorktree(workspaceID, workDir, repoURL string) (*repocache.WorktreeResult, bool) {
	if !d.worktreeUnifyEnabled() || workspaceID == "" || workDir == "" {
		return nil, false
	}
	managedRoot := filepath.Join(d.cfg.WorkspacesRoot, workspaceID, "worktrees")
	rel, err := filepath.Rel(managedRoot, workDir)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return nil, false
	}
	wtPath := filepath.Join(workDir, worktreesdaemon.RepoDirName(repoURL))
	if !worktreesdaemon.IsWorktree(wtPath) {
		return nil, false
	}
	branch, err := worktreesdaemon.CurrentBranch(wtPath)
	if err != nil {
		d.logger.Warn("worktrees: adopt: current branch unreadable; reporting empty", "path", wtPath, "error", err)
		branch = ""
	}
	d.logger.Info("repo checkout: adopted managed issue worktree (no reset, no new branch)",
		"url", repoURL, "path", wtPath, "branch", branch)
	return &repocache.WorktreeResult{Path: wtPath, BranchName: branch}, true
}

// worktreeUnifyEnabled gates the unified per-issue agent workdir. Default ON;
// set MULTICA_WORKTREE_UNIFIED=0 (or false/off) to fall back to the legacy
// per-task workdir + agent/* branch.
func (d *Daemon) worktreeUnifyEnabled() bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv("MULTICA_WORKTREE_UNIFIED")))
	return v != "0" && v != "false" && v != "off"
}

// issueWorktreeWorkDir returns the per-issue worktree dir to use as BOTH the
// agent's working directory and the sidebar's worktree location, or "" to keep
// the default per-task workdir (unified disabled / no repos / no cache / no
// issue). The caller also passes this as the eager-checkout perIssueDir.
func (d *Daemon) issueWorktreeWorkDir(task Task) string {
	if !d.worktreeUnifyEnabled() || d.repoCache == nil || len(task.Repos) == 0 || task.IssueID == "" {
		return ""
	}
	return worktreesdaemon.IssueWorktreeParent(d.cfg.WorkspacesRoot, task.WorkspaceID, task.IssueID)
}

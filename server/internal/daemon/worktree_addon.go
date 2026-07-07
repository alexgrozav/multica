package daemon

import (
	"context"
	"fmt"
	"os"
	"strings"
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
// checked-out worktree (the LEGACY, unified-disabled path only), before the
// checkout returns — so the agent waits and then works in a prepared
// environment. Returns the captured output and an error only when the Setup
// script itself fails (the caller fails the checkout). A missing/invalid
// multica.json is treated as "no setup" so checkouts are never blocked.
func (d *Daemon) runCheckoutSetup(ctx context.Context, workspaceID, repoURL, worktreePath string) (string, error) {
	var buf strings.Builder
	ran, err := worktreesdaemon.RunSetupAt(ctx, workspaceID, repoURL, worktreePath, func(stream, text string) {
		buf.WriteString(text)
		buf.WriteByte('\n')
		d.logger.Debug("checkout setup", "repo", repoURL, "stream", stream, "line", text)
	})
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
// (handleInit) is the SINGLE owner of checkout + first Setup — kicked off the
// moment the issue was assigned — so here the agent simply WAITS for that one
// workspace to be ready and then adopts it. There is no second checkout and no
// double Setup, which is what made the previous two-owner design misbehave.
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
		if managed {
			return nil
		}
		// Not tracked by the add-on (rare edge). Let the agent lazy-checkout into
		// the per-issue dir itself rather than block the task.
		d.logger.Warn("worktrees: issue not managed by add-on; skipping eager checkout", "issue", task.IssueID)
		return nil
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

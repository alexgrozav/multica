package daemon

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

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
		TokenProvider:  func() string { return d.client.Token() },
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
		// Reuse the daemon's own per-repo lock so our worktree adds serialize
		// with the daemon's agent-task worktree creation on the same bare clone.
		deps.WithRepoLock = d.repoCache.WithRepoLock
	}
	worktreesdaemon.New(deps).Run(ctx)
}

// runCheckoutSetup runs the worktrees add-on's per-repo Setup script in a
// freshly checked-out worktree (the agent's own worktree), before the checkout
// returns — so an agent that runs `multica repo checkout` waits for Setup and
// then works in a prepared environment. Returns the captured output and an
// error only when the Setup script itself fails (the caller fails the
// checkout). A missing daemon repo cache or an unreachable add-on endpoint is
// treated as "no setup" so checkouts are never blocked by add-on issues.
func (d *Daemon) runCheckoutSetup(ctx context.Context, workspaceID, repoURL, worktreePath string) (string, error) {
	var buf strings.Builder
	ran, err := worktreesdaemon.RunRepoSetup(ctx, worktreesdaemon.SetupParams{
		ServerBaseURL: d.cfg.ServerBaseURL,
		TokenProvider: func() string { return d.client.Token() },
		DaemonID:      d.cfg.DaemonID,
		WorkspaceID:   workspaceID,
		RepoURL:       repoURL,
		WorktreePath:  worktreePath,
	}, func(stream, text string) {
		buf.WriteString(text)
		buf.WriteByte('\n')
		d.logger.Debug("checkout setup", "repo", repoURL, "stream", stream, "line", text)
	})
	if err != nil {
		if !ran {
			// Fetch/transport failure — don't block the checkout; just skip Setup.
			d.logger.Warn("worktrees: could not fetch setup script; skipping", "repo", repoURL, "error", err)
			return "", nil
		}
		return buf.String(), err
	}
	if ran {
		d.logger.Info("worktrees: setup script completed", "repo", repoURL, "worktree", worktreePath)
	}
	return buf.String(), nil
}

// eagerCheckoutTaskRepos checks out each of the task's repos into the agent's
// workdir and runs the repo Setup script there, BEFORE the agent launches — so
// the agent starts in a prepared tree rather than an empty one. It mirrors the
// /repo/checkout handler (the agent's lazy-checkout path), looped over
// task.Repos, and returns the first error so the caller fails the task before
// StartTask (the agent never runs). No-op for local_directory tasks (the agent
// works in the user's own tree) and when there is no repo cache.
//
// The agent's later lazy `multica repo checkout` reuses the same worktree
// idempotently; if it does a destructive reuse, the existing checkout-Setup hook
// re-runs Setup, so the tree is always consistent (no marker needed).
func (d *Daemon) eagerCheckoutTaskRepos(ctx context.Context, task Task, env *execenv.Environment, agentName, perIssueDir string) error {
	if env == nil || env.LocalDirectory || d.repoCache == nil {
		return nil
	}
	coAuthored := d.workspaceCoAuthoredByEnabled(task.WorkspaceID)
	for _, repo := range task.Repos {
		url := strings.TrimSpace(repo.URL)
		if url == "" {
			continue
		}

		// Unified model: the agent works IN the per-issue worktree the sidebar
		// controls. Create it on the issue/* branch at the exact path the add-on
		// uses (so handleInit, the agent, and Run/Cleanup all agree), idempotent
		// and non-destructive; skip if already prepared (handleInit ran on issue
		// creation, or a prior task) so Setup isn't re-run.
		if perIssueDir != "" {
			wtPath := filepath.Join(perIssueDir, worktreesdaemon.RepoDirName(url))
			if worktreesdaemon.IsWorktree(wtPath) {
				continue
			}
			if err := d.ensureRepoReady(ctx, task.WorkspaceID, url); err != nil {
				return fmt.Errorf("eager checkout: repo not ready %s: %w", url, err)
			}
			bare := d.repoCache.Lookup(task.WorkspaceID, url)
			if bare == "" {
				return fmt.Errorf("eager checkout: repo not cached %s", url)
			}
			branch := worktreesdaemon.IssueBranch(task.IssueID, url)
			if err := d.repoCache.WithRepoLock(bare, func() error {
				_, e := worktreesdaemon.EnsureWorktreeAt(bare, wtPath, branch)
				return e
			}); err != nil {
				return fmt.Errorf("eager checkout: create worktree %s: %w", url, err)
			}
			if out, serr := d.runCheckoutSetup(ctx, task.WorkspaceID, url, wtPath); serr != nil {
				return fmt.Errorf("eager checkout: setup %s: %w\n%s", url, serr, out)
			}
			continue
		}

		// Legacy path (unified disabled): fresh per-task workdir, agent/* branch.
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

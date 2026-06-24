package daemon

import (
	"context"
	"strings"

	worktreesdaemon "github.com/multica-ai/multica/server/addons/worktrees/daemonside"
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

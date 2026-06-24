package daemon

import (
	"context"

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
		Logger:         d.logger,
		RootCtx:        d.rootCtx,
		TokenProvider:  func() string { return d.client.Token() },
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
	}
	worktreesdaemon.New(deps).Run(ctx)
}

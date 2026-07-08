package daemonside

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/multica-ai/multica/server/addons/worktrees/shared"
)

func runGit(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// shortID returns a short, filesystem-safe prefix of an id (shared so the
// server can derive matching values where needed).
func shortID(id string) string { return shared.ShortID(id) }

// repoName derives a stable directory name from a repo URL.
func repoName(url string) string {
	u := strings.TrimSpace(url)
	u = strings.TrimSuffix(u, "/")
	u = strings.TrimSuffix(u, ".git")
	if i := strings.LastIndexAny(u, "/:"); i >= 0 {
		u = u[i+1:]
	}
	u = sanitize(u)
	if u == "" {
		return "repo"
	}
	return u
}

func sanitize(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	return b.String()
}

func isGitDir(dir string) bool {
	if st, err := os.Stat(filepath.Join(dir, "HEAD")); err == nil && !st.IsDir() {
		return true
	}
	return false
}

func isWorktree(path string) bool {
	// A linked worktree has a `.git` file (gitdir pointer), not a directory.
	if _, err := os.Stat(filepath.Join(path, ".git")); err == nil {
		return true
	}
	return false
}

// resolveBaseRef picks a base commit-ish for a new worktree, tolerating both the
// remote-tracking layout (daemon repo cache) and a plain bare clone (self-clone).
func resolveBaseRef(bare string) string {
	if out, err := runGit(bare, "symbolic-ref", "--quiet", "--short", "refs/remotes/origin/HEAD"); err == nil {
		if ref := strings.TrimSpace(out); ref != "" {
			return ref
		}
	}
	for _, c := range []string{"refs/remotes/origin/main", "refs/remotes/origin/master"} {
		if _, err := runGit(bare, "rev-parse", "--verify", "--quiet", c); err == nil {
			return c
		}
	}
	if out, err := runGit(bare, "symbolic-ref", "--quiet", "--short", "HEAD"); err == nil {
		if ref := strings.TrimSpace(out); ref != "" {
			return ref
		}
	}
	return "HEAD"
}

// refExists reports whether a fully-qualified ref resolves in the bare repo.
func refExists(bare, ref string) bool {
	_, err := runGit(bare, "rev-parse", "--verify", "--quiet", ref)
	return err == nil
}

// ensureWorktree creates the persistent worktree if absent (reusing an existing
// one as-is) and returns the branch name.
//
// reuseExisting selects the semantics for the branch itself and is true for a
// USER-REQUESTED branch name (issue.branch_name): an existing local branch is
// checked out as-is (never reset — it may carry work), an existing remote
// branch seeds a new local one, and only a branch that exists nowhere is
// created from base. Derived identifier branches (reuseExisting=false) keep the
// historical `-B` force-reset: a leftover branch from an earlier lifetime of
// the same identifier restarts from base.
func ensureWorktree(bare, worktreePath, branch string, reuseExisting bool) (string, error) {
	if isWorktree(worktreePath) {
		return branch, nil
	}
	if err := os.MkdirAll(filepath.Dir(worktreePath), 0o755); err != nil {
		return "", err
	}
	// Clear any half-registered worktree admin entry left by a prior failed add.
	_, _ = runGit(bare, "worktree", "prune")
	// branch.autoSetupMerge=false: the issue branch must NOT track its start
	// point. Writing upstream config is both wrong here (an issue branch
	// shouldn't push to main) and the operation that contended on config.lock
	// when the daemon's agent-task worktree creation ran on the same bare repo
	// concurrently.
	args := []string{"-c", "branch.autoSetupMerge=false", "worktree", "add"}
	switch {
	case reuseExisting && refExists(bare, "refs/heads/"+branch):
		// Existing local branch: check it out where it stands.
		args = append(args, worktreePath, branch)
	case reuseExisting && refExists(bare, "refs/remotes/origin/"+branch):
		// Branch exists on the remote only: continue it locally from there.
		args = append(args, "-b", branch, worktreePath, "refs/remotes/origin/"+branch)
	case reuseExisting:
		// Nowhere yet: create it from base. -b (not -B) so a concurrent creation
		// of the same user branch fails loudly instead of silently resetting it.
		args = append(args, "-b", branch, worktreePath, resolveBaseRef(bare))
	default:
		args = append(args, "-B", branch, worktreePath, resolveBaseRef(bare))
	}
	if out, err := runGit(bare, args...); err != nil {
		return "", fmt.Errorf("git worktree add: %s: %w", strings.TrimSpace(out), err)
	}
	return branch, nil
}

// removeWorktree tears down a persistent worktree (best-effort). deleteBranch
// is true for derived identifier branches, which die with the issue; a
// user-requested branch (issue.branch_name) outlives its issues — it may
// pre-date the issue, be shared, or be picked up again later — so it is kept.
func removeWorktree(bare, worktreePath, branch string, deleteBranch bool) {
	_, _ = runGit(bare, "worktree", "remove", "--force", worktreePath)
	_, _ = runGit(bare, "worktree", "prune")
	if deleteBranch {
		_, _ = runGit(bare, "branch", "-D", branch)
	}
	_ = os.RemoveAll(worktreePath)
}

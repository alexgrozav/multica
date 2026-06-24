package daemonside

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func runGit(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// shortID returns a short, filesystem-safe prefix of an id.
func shortID(id string) string {
	id = strings.TrimSpace(id)
	id = strings.ReplaceAll(id, "-", "")
	if len(id) > 12 {
		id = id[:12]
	}
	if id == "" {
		return "issue"
	}
	return id
}

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

// ensureWorktree creates the persistent worktree if absent (reusing an existing
// one as-is) and returns the branch name.
func ensureWorktree(bare, worktreePath, branch string) (string, error) {
	if isWorktree(worktreePath) {
		return branch, nil
	}
	if err := os.MkdirAll(filepath.Dir(worktreePath), 0o755); err != nil {
		return "", err
	}
	// Clear any half-registered worktree admin entry left by a prior failed add.
	_, _ = runGit(bare, "worktree", "prune")
	base := resolveBaseRef(bare)
	// branch.autoSetupMerge=false: the issue/* branch must NOT track origin/<base>.
	// Writing upstream config is both wrong here (an issue branch shouldn't push
	// to main) and the operation that contended on config.lock when the daemon's
	// agent-task worktree creation ran on the same bare repo concurrently.
	if out, err := runGit(bare, "-c", "branch.autoSetupMerge=false", "worktree", "add", "-B", branch, worktreePath, base); err != nil {
		return "", fmt.Errorf("git worktree add: %s: %w", strings.TrimSpace(out), err)
	}
	return branch, nil
}

// removeWorktree tears down a persistent worktree and its branch (best-effort).
func removeWorktree(bare, worktreePath, branch string) {
	_, _ = runGit(bare, "worktree", "remove", "--force", worktreePath)
	_, _ = runGit(bare, "worktree", "prune")
	_, _ = runGit(bare, "branch", "-D", branch)
	_ = os.RemoveAll(worktreePath)
}

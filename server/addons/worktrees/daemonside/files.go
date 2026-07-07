package daemonside

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os/exec"
	"sort"
	"strings"
)

// File-list scanning for the Project tab: the poll loop keeps each ready
// worktree's project files (tracked + untracked-but-not-ignored) synced to the
// server. git does the directory walking — it prunes ignored trees such as
// node_modules without descending, so a scan is a few tens of milliseconds even
// on large repos, cheap enough to run at poll cadence.

// maxFileEntries caps a single report so a pathological repo can't push a
// multi-megabyte list through the server on every change.
const maxFileEntries = 20000

// runGitStdout runs git capturing stdout only — unlike runGit's CombinedOutput,
// stderr warnings must never be parsed as path entries.
func runGitStdout(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git %s: %s: %w", strings.Join(args, " "), strings.TrimSpace(stderr.String()), err)
	}
	return stdout.String(), nil
}

// listWorktreeFiles returns the sorted project files of a worktree, relative to
// its root: tracked files still present on disk plus untracked files that are
// not gitignored. `.git` and ignored trees never appear.
func listWorktreeFiles(wtPath string) (paths []string, truncated bool, err error) {
	out, err := runGitStdout(wtPath, "ls-files", "-z", "--cached", "--others", "--exclude-standard")
	if err != nil {
		return nil, false, err
	}
	// Tracked files deleted from disk still show under --cached until the
	// deletion is staged; subtract them so the tree mirrors the filesystem.
	deleted := map[string]bool{}
	if delOut, delErr := runGitStdout(wtPath, "ls-files", "-z", "--deleted"); delErr == nil {
		for _, p := range strings.Split(delOut, "\x00") {
			if p != "" {
				deleted[p] = true
			}
		}
	}
	seen := map[string]bool{}
	paths = make([]string, 0, 256)
	for _, p := range strings.Split(out, "\x00") {
		if p == "" || deleted[p] || seen[p] {
			continue
		}
		seen[p] = true
		paths = append(paths, p)
	}
	sort.Strings(paths)
	if len(paths) > maxFileEntries {
		paths = paths[:maxFileEntries]
		truncated = true
	}
	return paths, truncated, nil
}

// filesDigest fingerprints a (sorted) file list for change detection.
func filesDigest(paths []string, truncated bool) string {
	h := sha256.New()
	for _, p := range paths {
		h.Write([]byte(p))
		h.Write([]byte{0})
	}
	if truncated {
		h.Write([]byte("truncated"))
	}
	return hex.EncodeToString(h.Sum(nil))
}

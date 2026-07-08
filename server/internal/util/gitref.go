package util

import (
	"errors"
	"fmt"
	"strings"
)

// MaxGitBranchNameLength caps user-supplied branch names well below any git
// limit but above every sane real-world name; deep paths + long names still
// fit, while pathological inputs are rejected before they reach a daemon.
const MaxGitBranchNameLength = 200

// ValidateGitBranchName reports whether name is a valid git branch name,
// mirroring the rules of `git check-ref-format --branch` (applied to
// refs/heads/<name>). Used at request boundaries so an invalid name fails
// with a clean 400 instead of a daemon-side `git worktree add` error.
//
// Unlike the worktree add-on's identifier sanitizer (which force-maps any
// identifier to a safe ref), user-supplied names are rejected rather than
// silently rewritten — the user asked for a specific branch.
func ValidateGitBranchName(name string) error {
	if name == "" {
		return errors.New("branch name is empty")
	}
	if len(name) > MaxGitBranchNameLength {
		return fmt.Errorf("branch name exceeds %d characters", MaxGitBranchNameLength)
	}
	if name == "@" {
		return errors.New(`branch name cannot be "@"`)
	}
	if strings.HasPrefix(name, "-") {
		return errors.New(`branch name cannot start with "-"`)
	}
	if strings.HasPrefix(name, "/") || strings.HasSuffix(name, "/") || strings.Contains(name, "//") {
		return errors.New("branch name cannot start or end with a slash or contain consecutive slashes")
	}
	if strings.HasSuffix(name, ".") {
		return errors.New(`branch name cannot end with "."`)
	}
	if strings.Contains(name, "..") {
		return errors.New(`branch name cannot contain ".."`)
	}
	if strings.Contains(name, "@{") {
		return errors.New(`branch name cannot contain "@{"`)
	}
	for _, r := range name {
		if r < 0x20 || r == 0x7f {
			return errors.New("branch name cannot contain control characters")
		}
		switch r {
		case ' ', '~', '^', ':', '?', '*', '[', '\\':
			return fmt.Errorf("branch name cannot contain %q", r)
		}
	}
	for _, seg := range strings.Split(name, "/") {
		if strings.HasPrefix(seg, ".") {
			return errors.New(`branch name components cannot start with "."`)
		}
		if strings.HasSuffix(seg, ".lock") {
			return errors.New(`branch name components cannot end with ".lock"`)
		}
	}
	return nil
}

package shared

import "strings"

// IssueBranch is the branch a repo's per-issue worktree is checked out on when
// the issue did not pin a custom branch_name: the human identifier (e.g.
// PRO-11), sanitized to a valid git ref. It is NOT under agent/*, so the
// daemon GC's agent-branch sweep never touches it. Falls back to a short issue
// id when no identifier is available.
//
// It lives in the shared wire package because BOTH sides must derive the same
// name independently: the daemon names the actual checkout, and the server
// stamps the resolved branch on agent task payloads (issue_branch) so the
// agent brief/env can announce the branch before the checkout is observed.
func IssueBranch(identifier, issueID string) string {
	if b := SafeRef(identifier); b != "" {
		return b
	}
	return "issue-" + ShortID(issueID)
}

// SafeRef maps an identifier to a valid, collision-free git branch ref. Keeps
// ref-safe characters and collapses the rest to '-', trimming stray separators.
func SafeRef(s string) string {
	s = strings.TrimSpace(s)
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	return strings.Trim(b.String(), "-.")
}

// ShortID returns a short, filesystem-safe prefix of an id.
func ShortID(id string) string {
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

package execenv

import (
	"strings"
	"testing"
)

// When the worktrees add-on pre-checked the task's repos out
// (ctx.CheckedOutBranch set), the brief must announce each repo's local
// directory and the issue branch, tell the agent to commit/push THAT branch,
// and forbid re-running `multica repo checkout` on those repos — the
// historical text steered agents into minting fresh agent/* branches and
// resetting the managed worktree. Without the field, the legacy
// checkout-on-demand text must be preserved verbatim. Runs both brief
// builders (legacy and slim).
func TestBriefAnnouncesPreCheckedOutBranch(t *testing.T) {
	checkedOut := TaskContextForEnv{
		IssueID:          "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee",
		CheckedOutBranch: "feature/login",
		Repos: []RepoContextForEnv{
			{URL: "https://github.com/acme/widget.git", Dir: "widget", Description: "main app"},
			{URL: "https://github.com/acme/gadget.git", Dir: "gadget"},
		},
	}
	onDemand := TaskContextForEnv{
		IssueID: "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee",
		Repos: []RepoContextForEnv{
			{URL: "https://github.com/acme/widget.git", Description: "main app"},
		},
	}

	run := func(t *testing.T, label string) {
		out := buildMetaSkillContent("claude", checkedOut)
		for _, want := range []string{
			"Already checked out in your working directory on branch `feature/login`",
			"`./widget` — https://github.com/acme/widget.git — main app",
			"`./gadget` — https://github.com/acme/gadget.git",
			"git push -u origin feature/login",
			"Do NOT create or switch to another branch",
			"do NOT run `multica repo checkout` for these repos",
			// The Available Commands bullet keeps the command discoverable for
			// OTHER repos but points back at the Repositories rule.
			"multica repo checkout <url>",
			"never re-checkout those",
		} {
			if !strings.Contains(out, want) {
				t.Errorf("%s: pre-checked-out brief missing %q", label, want)
			}
		}
		for _, banned := range []string{
			// The on-demand instructions must be fully replaced — any leftover
			// copy telling the agent to check out "on a dedicated branch" is
			// exactly the text that caused the agent/* branch churn.
			"to check out a repository into your working directory",
			"creates a git worktree with a dedicated branch",
			"creates a git worktree on a dedicated branch",
			"git worktree on a dedicated branch",
		} {
			if strings.Contains(out, banned) {
				t.Errorf("%s: pre-checked-out brief still contains on-demand copy %q", label, banned)
			}
		}

		// Without CheckedOutBranch the legacy on-demand text is unchanged.
		legacy := buildMetaSkillContent("claude", onDemand)
		if !strings.Contains(legacy, "multica repo checkout <url>") {
			t.Errorf("%s: on-demand brief lost the repo checkout command", label)
		}
		if strings.Contains(legacy, "Already checked out in your working directory") {
			t.Errorf("%s: on-demand brief must not claim repos are pre-checked out", label)
		}
	}

	// Not parallel: the slim subtest toggles a process-wide feature flag.
	t.Run("legacy", func(t *testing.T) { run(t, "legacy") })
	t.Run("slim", func(t *testing.T) {
		withSlimBrief(t)
		run(t, "slim")
	})
}

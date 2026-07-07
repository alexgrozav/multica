//go:build darwin

package daemonside

import (
	"testing"

	"github.com/multica-ai/multica/server/addons/worktrees/shared"
)

func TestOpenArgv(t *testing.T) {
	dir := "/tmp/worktrees/abc123"
	cases := []struct {
		target string
		want   []string
	}{
		{shared.OpenFinder, []string{"open", dir}},
		{shared.OpenVSCode, []string{"open", "-a", "Visual Studio Code", dir}},
		{shared.OpenZed, []string{"open", "-a", "Zed", dir}},
		{shared.OpenXcode, []string{"open", "-a", "Xcode", dir}},
		{shared.OpenGhostty, []string{"open", "-a", "Ghostty", dir}},
		{shared.OpenTerminal, []string{"open", "-a", "Terminal", dir}},
	}
	for _, c := range cases {
		got, ok := openArgv(c.target, dir)
		if !ok {
			t.Errorf("openArgv(%q): ok=false, want true", c.target)
			continue
		}
		if len(got) != len(c.want) {
			t.Errorf("openArgv(%q) = %v, want %v", c.target, got, c.want)
			continue
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("openArgv(%q)[%d] = %q, want %q", c.target, i, got[i], c.want[i])
			}
		}
	}

	if _, ok := openArgv("bogus", dir); ok {
		t.Error("openArgv(bogus): ok=true, want false")
	}
	// Every allowlisted target must have a command mapping (guards drift between
	// shared.ValidOpenTarget and the daemon's app map).
	for _, tgt := range []string{shared.OpenFinder, shared.OpenVSCode, shared.OpenZed, shared.OpenXcode, shared.OpenGhostty, shared.OpenTerminal} {
		if _, ok := openArgv(tgt, dir); !ok {
			t.Errorf("valid target %q has no openArgv mapping", tgt)
		}
	}
}

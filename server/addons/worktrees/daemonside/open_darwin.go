//go:build darwin

package daemonside

import (
	"context"
	"fmt"
	"os/exec"
	"strings"

	"github.com/multica-ai/multica/server/addons/worktrees/shared"
)

// macAppName maps an open-in target to the macOS application `open -a` launches.
// Finder is special-cased to a plain `open <dir>` (open the folder in Finder).
var macAppName = map[string]string{
	shared.OpenVSCode:   "Visual Studio Code",
	shared.OpenZed:      "Zed",
	shared.OpenXcode:    "Xcode",
	shared.OpenGhostty:  "Ghostty",
	shared.OpenTerminal: "Terminal",
}

// openArgv builds the macOS `open` command line for a target + directory. Pure
// (no exec) so the mapping is unit-testable; returns ok=false for an unknown
// target.
func openArgv(target, dir string) ([]string, bool) {
	if target == shared.OpenFinder {
		return []string{"open", dir}, true
	}
	if app, ok := macAppName[target]; ok {
		return []string{"open", "-a", app, dir}, true
	}
	return nil, false
}

// openInApp opens dir in the requested application via macOS `open`. `open`
// exits non-zero (surfaced as an error) when the app isn't installed.
func openInApp(ctx context.Context, target, dir string) error {
	if dir == "" {
		return fmt.Errorf("empty working directory")
	}
	argv, ok := openArgv(target, dir)
	if !ok {
		return fmt.Errorf("unknown open target %q", target)
	}
	if out, err := exec.CommandContext(ctx, argv[0], argv[1:]...).CombinedOutput(); err != nil {
		return fmt.Errorf("open %s: %s: %w", target, strings.TrimSpace(string(out)), err)
	}
	return nil
}

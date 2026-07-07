//go:build !darwin

package daemonside

import (
	"context"
	"fmt"
)

// openInApp is macOS-only: the open-in targets (Finder, Xcode, Ghostty, Zed, …)
// are macOS applications launched via `open`. On other platforms it is a no-op
// that reports the target as unsupported (the caller only logs the error).
func openInApp(_ context.Context, target, _ string) error {
	return fmt.Errorf("open-in target %q is only supported on macOS", target)
}

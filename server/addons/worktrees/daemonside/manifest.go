package daemonside

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/multica-ai/multica/server/addons/worktrees/shared"
)

// readManifest reads and parses <worktreePath>/multica.json — the per-repo,
// committed source of truth for the Setup / Run / Cleanup scripts. Only the
// daemon can read it (the file lives on the daemon's disk after checkout), so
// the server never sees script bodies.
//
// A missing file is not an error (returns an empty manifest); a malformed file
// returns an error so the UI can surface "invalid multica.json".
func readManifest(worktreePath string) (shared.Manifest, error) {
	data, err := os.ReadFile(filepath.Join(worktreePath, shared.ManifestFile))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return shared.Manifest{}, nil
		}
		return shared.Manifest{}, err
	}
	var mf shared.Manifest
	if err := json.Unmarshal(data, &mf); err != nil {
		return shared.Manifest{}, fmt.Errorf("parse %s: %w", shared.ManifestFile, err)
	}
	return mf, nil
}

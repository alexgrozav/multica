package daemonside

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/multica-ai/multica/server/addons/worktrees/shared"
)

func TestReadManifest(t *testing.T) {
	// missing file → empty manifest, no error
	mf, err := readManifest(t.TempDir())
	if err != nil || mf.HasSetup() || mf.HasCleanup() || len(mf.RunNames()) != 0 {
		t.Fatalf("missing manifest = %+v err %v, want empty/nil", mf, err)
	}

	// valid file: blank commands are dropped, names come back sorted
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, shared.ManifestFile),
		[]byte(`{"scripts":{"setup":"pnpm i","run":{"start":"pnpm start","dev":"pnpm dev","blank":""},"cleanup":"down"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	mf, err = readManifest(dir)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !mf.HasSetup() || !mf.HasCleanup() {
		t.Fatalf("want setup+cleanup, got %+v", mf)
	}
	names := mf.RunNames()
	if len(names) != 2 || names[0] != "dev" || names[1] != "start" {
		t.Fatalf("RunNames = %v, want [dev start] (sorted, blank dropped)", names)
	}
	if mf.RunCommand("dev") != "pnpm dev" || mf.RunCommand("blank") != "" {
		t.Fatalf("RunCommand mismatch: dev=%q blank=%q", mf.RunCommand("dev"), mf.RunCommand("blank"))
	}

	// malformed → error surfaced (UI shows "invalid multica.json")
	bad := t.TempDir()
	if err := os.WriteFile(filepath.Join(bad, shared.ManifestFile), []byte(`{not json`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := readManifest(bad); err == nil {
		t.Fatalf("malformed manifest should return an error")
	}
}

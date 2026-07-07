package execenv

import (
	"os"
	"path/filepath"
	"testing"
)

const testIssueID = "11111111-2222-3333-4444-555555555555"

// TestPrepareWorkDirOverrideKeepsRootClean covers the unified worktrees layout:
// the per-issue worktree IS the agent's cwd, so Prepare must keep its
// bookkeeping scratch (output/, logs/) out of the tree root — nested under a
// hidden .multica-env/ — and must report the env as prepared afterwards.
func TestPrepareWorkDirOverrideKeepsRootClean(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	issueDir := filepath.Join(root, "ws", "worktrees", "abc123")
	if err := os.MkdirAll(issueDir, 0o755); err != nil {
		t.Fatal(err)
	}

	env, err := Prepare(PrepareParams{
		WorkspacesRoot:  root,
		WorkspaceID:     "ws",
		TaskID:          testIssueID,
		WorkDirOverride: issueDir,
		Task:            TaskContextForEnv{IssueID: testIssueID},
	}, testLogger())
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}

	if env.WorkDir != issueDir || env.RootDir != issueDir {
		t.Fatalf("override: WorkDir=%q RootDir=%q, want both %q", env.WorkDir, env.RootDir, issueDir)
	}
	for _, d := range []string{"output", "logs"} {
		if _, err := os.Stat(filepath.Join(issueDir, d)); err == nil {
			t.Fatalf("scratch %q must not sit at the per-issue tree root", d)
		}
		if _, err := os.Stat(filepath.Join(issueDir, ".multica-env", d)); err != nil {
			t.Fatalf(".multica-env/%s should exist: %v", d, err)
		}
	}
	if !IsPreparedEnv(issueDir) {
		t.Fatal("IsPreparedEnv should be true after Prepare wrote the manifest")
	}
}

// TestReuseHonorsRootDirOverride guards the latent bug the unified flow hit:
// Reuse derives RootDir from filepath.Dir(WorkDir), which for a per-issue
// worktree is the SHARED worktrees/ parent. Passing RootDir explicitly
// must pin it to the per-issue tree so sidecar/skill cleanup targets it.
func TestReuseHonorsRootDirOverride(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	issueDir := filepath.Join(root, "ws", "worktrees", "abc123")
	if err := os.MkdirAll(issueDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := Prepare(PrepareParams{
		WorkspacesRoot:  root,
		WorkspaceID:     "ws",
		TaskID:          testIssueID,
		WorkDirOverride: issueDir,
		Task:            TaskContextForEnv{IssueID: testIssueID},
	}, testLogger()); err != nil {
		t.Fatalf("Prepare: %v", err)
	}

	env := Reuse(ReuseParams{
		WorkDir: issueDir,
		RootDir: issueDir,
		Task:    TaskContextForEnv{IssueID: testIssueID},
	}, testLogger())
	if env == nil {
		t.Fatal("Reuse returned nil for an existing prepared dir")
	}
	if env.RootDir != issueDir {
		t.Fatalf("Reuse RootDir=%q, want %q (must not be the shared worktrees parent %q)",
			env.RootDir, issueDir, filepath.Dir(issueDir))
	}
}

func TestIsPreparedEnv(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if IsPreparedEnv("") {
		t.Fatal("empty path must not be reported as prepared")
	}
	if IsPreparedEnv(dir) {
		t.Fatal("a bare dir (add-on checked out worktrees only) must not be prepared")
	}
	if err := os.WriteFile(filepath.Join(dir, sidecarManifestFile), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !IsPreparedEnv(dir) {
		t.Fatal("a dir with the sidecar manifest must be prepared")
	}
}

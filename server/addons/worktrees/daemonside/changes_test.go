//go:build !windows

package daemonside

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/multica-ai/multica/server/addons/worktrees/shared"
)

// newChangesRepo builds a repo with a pinned origin/main base ref, then a
// feature branch carrying committed work, uncommitted edits, and an untracked
// file — the shapes the Changes scan must classify.
func newChangesRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	dir := t.TempDir()
	mustGit(t, dir, "init", "-b", "main")
	mustGit(t, dir, "config", "user.email", "changes@test.local")
	mustGit(t, dir, "config", "user.name", "changes")

	write := func(rel, body string) {
		t.Helper()
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	// Base commit, pinned as the remote-tracking default branch so
	// resolveBaseRef finds it without a real remote.
	write("keep.txt", "same\n")
	write("mod.txt", "one\ntwo\n")
	write("gone.txt", "bye\n")
	mustGit(t, dir, "add", ".")
	mustGit(t, dir, "commit", "-m", "base")
	mustGit(t, dir, "update-ref", "refs/remotes/origin/main", "main")

	// Feature branch: committed modify + add + delete…
	mustGit(t, dir, "checkout", "-q", "-b", "PRO-11")
	write("mod.txt", "one\nTWO\nthree\n")
	write("added.txt", "fresh\n")
	if err := os.Remove(filepath.Join(dir, "gone.txt")); err != nil {
		t.Fatal(err)
	}
	mustGit(t, dir, "add", "-A")
	mustGit(t, dir, "commit", "-m", "work")
	// …then uncommitted work on top: an edit, an untracked file, and an
	// untracked file inside a brand-new (uppercase-named) directory — git
	// collapses the latter to "NEW/" without -uall, and byte-wise sorting
	// would put it first.
	write("mod.txt", "one\nTWO\nthree\nfour\n")
	write("untracked.txt", "u1\nu2\nu3\n")
	write("NEW/inner.txt", "n1\n")
	return dir
}

// TestListWorktreeChanges proves the scan reports the merge-base → working
// tree union with statuses, line counts, and uncommitted markers — against a
// real git repo.
func TestListWorktreeChanges(t *testing.T) {
	dir := newChangesRepo(t)

	entries, base, truncated, err := listWorktreeChanges(dir)
	if err != nil {
		t.Fatalf("listWorktreeChanges: %v", err)
	}
	if truncated {
		t.Fatal("unexpected truncation")
	}
	if base != "origin/main" {
		t.Fatalf("base = %q, want origin/main", base)
	}

	byPath := map[string]shared.ChangeEntry{}
	for _, e := range entries {
		byPath[e.Path] = e
	}
	if len(entries) != 5 {
		t.Fatalf("entries = %+v, want 5 (added, gone, mod, NEW/inner, untracked)", entries)
	}
	// Sorted alphabetically, case-insensitive: byte order would put
	// NEW/inner.txt first; a collapsed "NEW/" dir entry must not appear.
	for i, want := range []string{"added.txt", "gone.txt", "mod.txt", "NEW/inner.txt", "untracked.txt"} {
		if entries[i].Path != want {
			t.Fatalf("entries[%d].Path = %q, want %q", i, entries[i].Path, want)
		}
	}

	if e := byPath["added.txt"]; e.Status != shared.ChangeAdded || e.Additions != 1 || e.Deletions != 0 || e.Uncommitted {
		t.Fatalf("added.txt = %+v, want committed add +1", e)
	}
	if e := byPath["gone.txt"]; e.Status != shared.ChangeDeleted || e.Deletions != 1 || e.Uncommitted {
		t.Fatalf("gone.txt = %+v, want committed delete -1", e)
	}
	// mod.txt: base one\ntwo\n → working one\nTWO\nthree\nfour\n = +3 -1,
	// still dirty in the working tree.
	if e := byPath["mod.txt"]; e.Status != shared.ChangeModified || e.Additions != 3 || e.Deletions != 1 || !e.Uncommitted {
		t.Fatalf("mod.txt = %+v, want uncommitted modify +3 -1", e)
	}
	if e := byPath["untracked.txt"]; e.Status != shared.ChangeAdded || e.Additions != 3 || !e.Uncommitted || e.Binary {
		t.Fatalf("untracked.txt = %+v, want uncommitted add +3", e)
	}
	if e := byPath["NEW/inner.txt"]; e.Status != shared.ChangeAdded || e.Additions != 1 || !e.Uncommitted {
		t.Fatalf("NEW/inner.txt = %+v, want uncommitted add +1", e)
	}
}

// TestListWorktreeChangesCleanTree pins the empty result: a fresh checkout
// with no work reports zero entries (not an error), so the UI can show a
// clean "no changes" state.
func TestListWorktreeChangesCleanTree(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	dir := t.TempDir()
	mustGit(t, dir, "init", "-b", "main")
	mustGit(t, dir, "config", "user.email", "clean@test.local")
	mustGit(t, dir, "config", "user.name", "clean")
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustGit(t, dir, "add", ".")
	mustGit(t, dir, "commit", "-m", "base")
	mustGit(t, dir, "update-ref", "refs/remotes/origin/main", "main")
	mustGit(t, dir, "checkout", "-q", "-b", "PRO-12")

	entries, base, truncated, err := listWorktreeChanges(dir)
	if err != nil {
		t.Fatalf("listWorktreeChanges: %v", err)
	}
	if len(entries) != 0 || truncated || base != "origin/main" {
		t.Fatalf("clean tree = (%+v, %q, %t), want no entries", entries, base, truncated)
	}
}

// TestChangesDigest pins the change-detection contract: deterministic, and
// sensitive to every rendered field.
func TestChangesDigest(t *testing.T) {
	base := []shared.ChangeEntry{{Path: "a.txt", Status: shared.ChangeModified, Additions: 1, Deletions: 2}}
	same := []shared.ChangeEntry{{Path: "a.txt", Status: shared.ChangeModified, Additions: 1, Deletions: 2}}
	if changesDigest(base, "origin/main", false) != changesDigest(same, "origin/main", false) {
		t.Fatal("digest not deterministic")
	}
	mutations := []struct {
		name string
		e    shared.ChangeEntry
	}{
		{"status", shared.ChangeEntry{Path: "a.txt", Status: shared.ChangeAdded, Additions: 1, Deletions: 2}},
		{"additions", shared.ChangeEntry{Path: "a.txt", Status: shared.ChangeModified, Additions: 9, Deletions: 2}},
		{"uncommitted", shared.ChangeEntry{Path: "a.txt", Status: shared.ChangeModified, Additions: 1, Deletions: 2, Uncommitted: true}},
		{"binary", shared.ChangeEntry{Path: "a.txt", Status: shared.ChangeModified, Additions: 1, Deletions: 2, Binary: true}},
	}
	for _, mu := range mutations {
		if changesDigest(base, "origin/main", false) == changesDigest([]shared.ChangeEntry{mu.e}, "origin/main", false) {
			t.Fatalf("digest ignored %s", mu.name)
		}
	}
	if changesDigest(base, "origin/main", false) == changesDigest(base, "origin/main", true) {
		t.Fatal("digest ignored the truncated flag")
	}
	if changesDigest(base, "origin/main", false) == changesDigest(base, "origin/master", false) {
		t.Fatal("digest ignored the base")
	}
}

// TestDiffWorktreeFile covers the diff file-op against a real repo: modified
// files carry both sides, added files an empty old side, deleted files an
// empty new side, and unknown paths are NotFound.
func TestDiffWorktreeFile(t *testing.T) {
	dir := newChangesRepo(t)

	mod := diffWorktreeFile(dir, "mod.txt")
	if mod.Error != "" || mod.OldContent != "one\ntwo\n" || mod.Content != "one\nTWO\nthree\nfour\n" {
		t.Fatalf("mod.txt diff = %+v, want base vs working content", mod)
	}

	added := diffWorktreeFile(dir, "untracked.txt")
	if added.Error != "" || added.OldContent != "" || added.Content != "u1\nu2\nu3\n" {
		t.Fatalf("untracked.txt diff = %+v, want empty old side", added)
	}

	deleted := diffWorktreeFile(dir, "gone.txt")
	if deleted.Error != "" || deleted.OldContent != "bye\n" || deleted.Content != "" {
		t.Fatalf("gone.txt diff = %+v, want empty new side", deleted)
	}

	ghost := diffWorktreeFile(dir, "never-existed.txt")
	if !ghost.NotFound {
		t.Fatalf("ghost diff = %+v, want NotFound", ghost)
	}
}

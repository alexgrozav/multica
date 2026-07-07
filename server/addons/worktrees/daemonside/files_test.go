//go:build !windows

package daemonside

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/multica-ai/multica/server/addons/worktrees/shared"
)

// TestListWorktreeFiles proves the scanner returns exactly the project files:
// tracked + untracked-but-not-ignored, sorted, with ignored trees and on-disk
// deletions excluded — against a real git repo.
func TestListWorktreeFiles(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	dir := t.TempDir()
	mustGit(t, dir, "init")
	mustGit(t, dir, "config", "user.email", "files@test.local")
	mustGit(t, dir, "config", "user.name", "files")

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

	write(".gitignore", "dist/\n*.log\n")
	write("README.md", "hi\n")
	write("src/app/main.ts", "x\n")
	write("src/util.ts", "y\n")
	write("gone.txt", "bye\n")
	mustGit(t, dir, "add", ".")
	mustGit(t, dir, "commit", "-m", "init")

	// Untracked-but-not-ignored appears; ignored files/trees do not; a tracked
	// file deleted from disk (deletion unstaged) disappears.
	write("src/new-untracked.ts", "z\n")
	write("dist/bundle.js", "built\n")
	write("debug.log", "noise\n")
	if err := os.Remove(filepath.Join(dir, "gone.txt")); err != nil {
		t.Fatal(err)
	}

	paths, truncated, err := listWorktreeFiles(dir)
	if err != nil {
		t.Fatalf("listWorktreeFiles: %v", err)
	}
	if truncated {
		t.Fatal("unexpected truncation")
	}
	want := []string{
		".gitignore",
		"README.md",
		"src/app/main.ts",
		"src/new-untracked.ts",
		"src/util.ts",
	}
	if !reflect.DeepEqual(paths, want) {
		t.Fatalf("paths = %v, want %v", paths, want)
	}
}

// TestFilesDigest pins the change-detection contract: same list → same digest,
// any added path or the truncation flag → different digest.
func TestFilesDigest(t *testing.T) {
	base := []string{"a.txt", "b/c.txt"}
	if filesDigest(base, false) != filesDigest([]string{"a.txt", "b/c.txt"}, false) {
		t.Fatal("digest not deterministic")
	}
	if filesDigest(base, false) == filesDigest([]string{"a.txt", "b/c.txt", "d.txt"}, false) {
		t.Fatal("digest ignored an added path")
	}
	if filesDigest(base, false) == filesDigest(base, true) {
		t.Fatal("digest ignored the truncated flag")
	}
	// Path-boundary safety: ["ab"] must differ from ["a","b"].
	if filesDigest([]string{"ab"}, false) == filesDigest([]string{"a", "b"}, false) {
		t.Fatal("digest not boundary-safe")
	}
}

// TestScanAndReportFiles covers the poll-driven sync step: a digest mismatch
// posts the list to the server, a matching digest posts nothing, and a missing
// worktree is skipped silently.
func TestScanAndReportFiles(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	root := t.TempDir()
	const ws = "ws-files"
	const issue = "12121212-3434-5656-7878-909090909090"
	repoURL := "https://example.com/scan.git"

	// A plain repo at the exact per-issue path stands in for the worktree
	// (isWorktree only checks for a .git entry).
	wtPath := IssueWorktreePath(root, ws, issue, repoURL)
	if err := os.MkdirAll(wtPath, 0o755); err != nil {
		t.Fatal(err)
	}
	mustGit(t, wtPath, "init")
	mustGit(t, wtPath, "config", "user.email", "scan@test.local")
	mustGit(t, wtPath, "config", "user.name", "scan")
	if err := os.WriteFile(filepath.Join(wtPath, "hello.txt"), []byte("hi\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustGit(t, wtPath, "add", ".")
	mustGit(t, wtPath, "commit", "-m", "init")

	var mu sync.Mutex
	var paths []string
	var reports []shared.FileReport
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var rep shared.FileReport
		_ = json.NewDecoder(r.Body).Decode(&rep)
		mu.Lock()
		paths = append(paths, r.URL.Path)
		reports = append(reports, rep)
		mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	m := New(Deps{
		ServerBaseURL:  srv.URL,
		WorkspacesRoot: root,
		DaemonID:       "daemon-files",
		TokenProvider:  func() string { return "test-token" },
	})
	target := shared.FileScanTarget{WorktreeID: "wt-files", IssueID: issue, WorkspaceID: ws, RepoURL: repoURL}

	// Empty server digest → scan posts the list.
	m.scanAndReportFiles(context.Background(), target)
	mu.Lock()
	if len(reports) != 1 {
		mu.Unlock()
		t.Fatalf("got %d reports, want 1", len(reports))
	}
	if !strings.HasSuffix(paths[0], "/worktrees/wt-files/files") {
		t.Fatalf("posted to %q, want .../worktrees/wt-files/files", paths[0])
	}
	rep := reports[0]
	mu.Unlock()
	if !reflect.DeepEqual(rep.Paths, []string{"hello.txt"}) || rep.Digest == "" || rep.Truncated {
		t.Fatalf("report = %+v, want [hello.txt] with digest", rep)
	}

	// Matching digest → nothing new posted.
	target.Digest = rep.Digest
	m.scanAndReportFiles(context.Background(), target)
	mu.Lock()
	n := len(reports)
	mu.Unlock()
	if n != 1 {
		t.Fatalf("unchanged scan posted again (%d reports)", n)
	}

	// Missing worktree → skipped, no post, no panic.
	m.scanAndReportFiles(context.Background(), shared.FileScanTarget{
		WorktreeID: "wt-ghost", IssueID: "00000000-0000-0000-0000-000000000000", WorkspaceID: ws, RepoURL: repoURL,
	})
	mu.Lock()
	n = len(reports)
	mu.Unlock()
	if n != 1 {
		t.Fatalf("missing worktree still posted (%d reports)", n)
	}
}

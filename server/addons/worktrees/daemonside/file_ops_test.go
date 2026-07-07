//go:build !windows

package daemonside

import (
	"os"
	"path/filepath"
	"testing"
)

// TestReadWorktreeFile covers the read contract: content round trip, missing
// file → NotFound, directories rejected, binary and oversized files flagged
// without content.
func TestReadWorktreeFile(t *testing.T) {
	dir := t.TempDir()
	mustWrite := func(rel string, body []byte, mode os.FileMode) {
		t.Helper()
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, body, mode); err != nil {
			t.Fatal(err)
		}
	}

	mustWrite("src/main.ts", []byte("export const x = 1;\n"), 0o644)
	res := readWorktreeFile(dir, "src/main.ts")
	if res.Error != "" || res.Content != "export const x = 1;\n" || res.Binary || res.Truncated {
		t.Fatalf("read = %+v, want plain content", res)
	}
	if res.Size != int64(len("export const x = 1;\n")) {
		t.Fatalf("size = %d, want content length", res.Size)
	}

	res = readWorktreeFile(dir, "missing.txt")
	if !res.NotFound {
		t.Fatalf("read missing = %+v, want NotFound", res)
	}

	res = readWorktreeFile(dir, "src")
	if res.Error == "" || res.NotFound {
		t.Fatalf("read dir = %+v, want error", res)
	}

	mustWrite("blob.bin", []byte{0x89, 0x50, 0x4e, 0x47, 0x00, 0x01}, 0o644)
	res = readWorktreeFile(dir, "blob.bin")
	if !res.Binary || res.Content != "" {
		t.Fatalf("read binary = %+v, want Binary without content", res)
	}

	mustWrite("big.txt", make([]byte, maxFileContentBytes+1), 0o644)
	res = readWorktreeFile(dir, "big.txt")
	if !res.Truncated || res.Content != "" {
		t.Fatalf("read oversized = %+v, want Truncated without content", res)
	}
}

// TestReadWorktreeFileSymlinkEscape proves os.Root confinement: a symlink
// pointing outside the worktree is refused even though the rel path is valid.
func TestReadWorktreeFileSymlinkEscape(t *testing.T) {
	outer := t.TempDir()
	secret := filepath.Join(outer, "secret.txt")
	if err := os.WriteFile(secret, []byte("top secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	wt := filepath.Join(outer, "wt")
	if err := os.Mkdir(wt, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secret, filepath.Join(wt, "link.txt")); err != nil {
		t.Fatal(err)
	}

	res := readWorktreeFile(wt, "link.txt")
	if res.Error == "" || res.Content != "" {
		t.Fatalf("read escaping symlink = %+v, want error without content", res)
	}
}

// TestWriteWorktreeFile covers the write contract: replace content atomically,
// preserve the original mode, create new files, refuse oversized payloads.
func TestWriteWorktreeFile(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "run.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\necho old\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	res := writeWorktreeFile(dir, "run.sh", "#!/bin/sh\necho new\n")
	if res.Error != "" {
		t.Fatalf("write = %+v, want success", res)
	}
	b, err := os.ReadFile(script)
	if err != nil || string(b) != "#!/bin/sh\necho new\n" {
		t.Fatalf("content = %q err=%v, want the new body", b, err)
	}
	st, err := os.Stat(script)
	if err != nil || st.Mode().Perm() != 0o755 {
		t.Fatalf("mode = %v err=%v, want 0755 preserved", st.Mode().Perm(), err)
	}
	if _, err := os.Stat(script + ".multica-tmp"); !os.IsNotExist(err) {
		t.Fatalf("tmp file left behind: %v", err)
	}

	// New file (parent exists) is created 0644.
	res = writeWorktreeFile(dir, "notes.txt", "hello\n")
	if res.Error != "" {
		t.Fatalf("write new = %+v, want success", res)
	}
	st, err = os.Stat(filepath.Join(dir, "notes.txt"))
	if err != nil || st.Mode().Perm() != 0o644 {
		t.Fatalf("new file mode = %v err=%v, want 0644", st.Mode(), err)
	}

	res = writeWorktreeFile(dir, "big.txt", string(make([]byte, maxFileContentBytes+1)))
	if res.Error == "" {
		t.Fatalf("oversized write = %+v, want error", res)
	}
}

// TestIsBinary pins the "can the editor round-trip this?" heuristic.
func TestIsBinary(t *testing.T) {
	if isBinary([]byte("plain text\nwith lines\n")) {
		t.Fatal("text flagged binary")
	}
	if isBinary([]byte("unicode ✓ 中文")) {
		t.Fatal("valid UTF-8 flagged binary")
	}
	if !isBinary([]byte{'a', 0x00, 'b'}) {
		t.Fatal("NUL byte not flagged")
	}
	if !isBinary([]byte{0xff, 0xfe, 0xfd}) {
		t.Fatal("invalid UTF-8 not flagged")
	}
}

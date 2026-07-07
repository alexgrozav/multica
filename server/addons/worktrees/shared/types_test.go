package shared

import (
	"strings"
	"testing"
)

// TestValidRelFilePath pins the boundary contract for file-op paths: only
// clean, slash-separated, repo-confined file paths pass.
func TestValidRelFilePath(t *testing.T) {
	valid := []string{
		"README.md",
		"src/app/main.ts",
		".gitignore",
		"a b/c d.txt",
		"深/文件.md",
		".github/workflows/ci.yml",
		"dir.git/file", // ".git" only as an exact segment is blocked
	}
	for _, p := range valid {
		if !ValidRelFilePath(p) {
			t.Errorf("ValidRelFilePath(%q) = false, want true", p)
		}
	}

	invalid := []string{
		"",
		".",
		"..",
		"../escape",
		"a/../b",
		"src/..",
		"/abs/path",
		"a//b",
		"a/./b",
		"trailing/",
		"back\\slash",
		"nul\x00byte",
		".git/config",
		"sub/.git/config",
		strings.Repeat("a", 4097),
	}
	for _, p := range invalid {
		if ValidRelFilePath(p) {
			t.Errorf("ValidRelFilePath(%q) = true, want false", p)
		}
	}
}

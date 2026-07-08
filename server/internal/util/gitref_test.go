package util

import (
	"strings"
	"testing"
)

func TestValidateGitBranchNameAccepts(t *testing.T) {
	valid := []string{
		"main",
		"feature/login",
		"alex/MUL-123-fix",
		"v1.2.3",
		"hotfix_2024",
		"a",
		"UPPER-case.mixed/seg_ment",
		"release/2024.07",
		strings.Repeat("a", MaxGitBranchNameLength),
	}
	for _, name := range valid {
		if err := ValidateGitBranchName(name); err != nil {
			t.Errorf("ValidateGitBranchName(%q) = %v, want nil", name, err)
		}
	}
}

func TestValidateGitBranchNameRejects(t *testing.T) {
	invalid := []string{
		"",
		"@",
		"-leading-dash",
		"/leading-slash",
		"trailing-slash/",
		"double//slash",
		"dot..dot",
		"trailing-dot.",
		"has space",
		"has~tilde",
		"has^caret",
		"has:colon",
		"has?question",
		"has*star",
		"has[bracket",
		`has\backslash`,
		"ref@{log",
		".hidden",
		"seg/.hidden",
		"name.lock",
		"seg.lock/tail",
		"ctrl\x01char",
		"del\x7fchar",
		strings.Repeat("a", MaxGitBranchNameLength+1),
	}
	for _, name := range invalid {
		if err := ValidateGitBranchName(name); err == nil {
			t.Errorf("ValidateGitBranchName(%q) = nil, want error", name)
		}
	}
}

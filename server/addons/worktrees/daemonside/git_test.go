package daemonside

import "testing"

func TestRepoName(t *testing.T) {
	cases := map[string]string{
		"https://github.com/foo/bar.git": "bar",
		"git@github.com:foo/bar.git":     "bar",
		"https://github.com/foo/bar/":    "bar",
		"https://example.com/a/b/c":      "c",
		"weird name!@#":                  "weird-name---",
		"":                               "repo",
	}
	for in, want := range cases {
		if got := repoName(in); got != want {
			t.Errorf("repoName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestShortID(t *testing.T) {
	if got := shortID("01234567-89ab-cdef-0123-456789abcdef"); got != "0123456789ab" {
		t.Errorf("shortID = %q, want 12-char dash-stripped prefix", got)
	}
	if got := shortID(""); got != "issue" {
		t.Errorf("shortID(empty) = %q, want issue", got)
	}
}

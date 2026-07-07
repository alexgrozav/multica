package daemonside

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/multica-ai/multica/server/addons/worktrees/shared"
)

// Git-status scanning for the Changes tab: the poll loop keeps each ready
// worktree's changed files (vs the base the branch forked from) synced to the
// server. "Changed" is the union of the merge-base → working-tree diff
// (committed + uncommitted work on the issue branch) and untracked files, so
// the list mirrors what a PR against the default branch would contain. All
// git invocations use --no-renames so the -z output stays one-record-per-path
// (a rename shows as delete + add).

// maxChangeEntries caps a single report so a pathological worktree can't push
// a huge list through the server on every poll.
const maxChangeEntries = 5000

// maxUntrackedCountBytes bounds line counting of one untracked file; beyond it
// the count is the lines seen so far (approximate, like a capped numstat).
const maxUntrackedCountBytes = 8 << 20

// changesBase resolves the commit-ish the worktree's changes are diffed
// against: the merge-base of HEAD and the default branch (so upstream drift
// after forking never shows up as ours). Falls back to HEAD — an empty diff,
// uncommitted work only — when there is no usable base.
func changesBase(wtPath string) (mergeBase, baseName string) {
	base := resolveBaseRef(wtPath)
	if base == "HEAD" {
		return "HEAD", ""
	}
	out, err := runGitStdout(wtPath, "merge-base", "HEAD", base)
	if err != nil || strings.TrimSpace(out) == "" {
		return "HEAD", ""
	}
	return strings.TrimSpace(out), strings.TrimPrefix(base, "refs/remotes/")
}

// listWorktreeChanges returns the sorted changed files of a worktree vs its
// base, with per-file line counts and an uncommitted marker.
func listWorktreeChanges(wtPath string) (entries []shared.ChangeEntry, base string, truncated bool, err error) {
	mergeBase, baseName := changesBase(wtPath)

	statusOut, err := runGitStdout(wtPath, "diff", "--name-status", "--no-renames", "-z", mergeBase)
	if err != nil {
		return nil, "", false, err
	}
	numstatOut, err := runGitStdout(wtPath, "diff", "--numstat", "--no-renames", "-z", mergeBase)
	if err != nil {
		return nil, "", false, err
	}
	// -uall: list untracked files individually — by default git collapses a
	// fully-untracked directory to one "dir/" entry, which can't be counted or
	// diffed.
	porcelainOut, err := runGitStdout(wtPath, "status", "--porcelain=v1", "--untracked-files=all", "--no-renames", "-z")
	if err != nil {
		return nil, "", false, err
	}

	counts := parseNumstat(numstatOut)
	uncommitted, untracked := parsePorcelain(porcelainOut)

	byPath := map[string]*shared.ChangeEntry{}
	// `X\0path\0` records: the tracked changes (committed + uncommitted).
	fields := strings.Split(statusOut, "\x00")
	for i := 0; i+1 < len(fields); i += 2 {
		st, p := fields[i], fields[i+1]
		if p == "" {
			continue
		}
		e := &shared.ChangeEntry{Path: p, Status: changeStatus(st), Uncommitted: uncommitted[p]}
		if c, ok := counts[p]; ok {
			e.Additions, e.Deletions, e.Binary = c.add, c.del, c.binary
		}
		byPath[p] = e
	}
	// Untracked files are invisible to `git diff`; count their lines directly.
	for _, p := range untracked {
		if _, dup := byPath[p]; dup {
			continue
		}
		add, binary := countUntrackedLines(filepath.Join(wtPath, filepath.FromSlash(p)))
		byPath[p] = &shared.ChangeEntry{Path: p, Status: shared.ChangeAdded, Additions: add, Binary: binary, Uncommitted: true}
	}

	entries = make([]shared.ChangeEntry, 0, len(byPath))
	for _, e := range byPath {
		entries = append(entries, *e)
	}
	sort.Slice(entries, func(i, j int) bool { return pathLess(entries[i].Path, entries[j].Path) })
	if len(entries) > maxChangeEntries {
		entries = entries[:maxChangeEntries]
		truncated = true
	}
	return entries, baseName, truncated, nil
}

// pathLess orders paths alphabetically the way a human reads a file list:
// case-insensitive (plain byte order would put every uppercase path before
// every lowercase one), with a byte-wise tie-break so paths differing only by
// case still sort deterministically.
func pathLess(a, b string) bool {
	la, lb := strings.ToLower(a), strings.ToLower(b)
	if la != lb {
		return la < lb
	}
	return a < b
}

// changeStatus maps a git --name-status letter onto the wire status. Only the
// first byte matters (git may append a score); unknown letters degrade to
// modified so a new git status never drops a file from the list.
func changeStatus(st string) string {
	if st == "" {
		return shared.ChangeModified
	}
	switch st[0] {
	case 'A':
		return shared.ChangeAdded
	case 'D':
		return shared.ChangeDeleted
	default:
		return shared.ChangeModified
	}
}

type numstatCounts struct {
	add, del int
	binary   bool
}

// parseNumstat reads `added\tdeleted\tpath\0` records ("-" counts mean binary).
func parseNumstat(out string) map[string]numstatCounts {
	counts := map[string]numstatCounts{}
	for _, rec := range strings.Split(out, "\x00") {
		parts := strings.SplitN(rec, "\t", 3)
		if len(parts) != 3 || parts[2] == "" {
			continue
		}
		if parts[0] == "-" || parts[1] == "-" {
			counts[parts[2]] = numstatCounts{binary: true}
			continue
		}
		add, err1 := strconv.Atoi(parts[0])
		del, err2 := strconv.Atoi(parts[1])
		if err1 != nil || err2 != nil {
			continue
		}
		counts[parts[2]] = numstatCounts{add: add, del: del}
	}
	return counts
}

// parsePorcelain reads `XY path\0` records from git status --porcelain=v1 -z:
// every listed path has uncommitted work; `??` entries are the untracked files.
func parsePorcelain(out string) (uncommitted map[string]bool, untracked []string) {
	uncommitted = map[string]bool{}
	for _, rec := range strings.Split(out, "\x00") {
		if len(rec) < 4 || rec[2] != ' ' {
			continue
		}
		xy, p := rec[:2], rec[3:]
		uncommitted[p] = true
		if xy == "??" {
			untracked = append(untracked, p)
		}
	}
	return uncommitted, untracked
}

// countUntrackedLines counts an untracked file's lines the way numstat would
// (a trailing unterminated line counts). Binary files (NUL in the leading
// probe) carry no count, mirroring numstat's "-".
func countUntrackedLines(absPath string) (lines int, binary bool) {
	f, err := os.Open(absPath)
	if err != nil {
		return 0, false
	}
	defer f.Close()

	var (
		buf     = make([]byte, 64<<10)
		total   int64
		probed  bool
		partial bool
	)
	for total < maxUntrackedCountBytes {
		n, err := f.Read(buf)
		if n > 0 {
			chunk := buf[:n]
			if !probed {
				probed = true
				if bytes.IndexByte(chunk, 0) >= 0 {
					return 0, true
				}
			}
			lines += bytes.Count(chunk, []byte{'\n'})
			partial = chunk[n-1] != '\n'
			total += int64(n)
		}
		if err != nil {
			if err != io.EOF {
				return 0, false
			}
			break
		}
	}
	if partial {
		lines++
	}
	return lines, false
}

// changesDigest fingerprints a changes report for change detection.
func changesDigest(entries []shared.ChangeEntry, base string, truncated bool) string {
	h := sha256.New()
	fmt.Fprintf(h, "base:%s\x00", base)
	for _, e := range entries {
		fmt.Fprintf(h, "%s\x00%s\x00%d\x00%d\x00%t\x00%t\x00", e.Path, e.Status, e.Additions, e.Deletions, e.Binary, e.Uncommitted)
	}
	if truncated {
		h.Write([]byte("truncated"))
	}
	return hex.EncodeToString(h.Sum(nil))
}

package daemonside

import (
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"unicode/utf8"

	"github.com/multica-ai/multica/server/addons/worktrees/shared"
)

// On-demand file reads/writes for the issue view's file-tab editor. Ops arrive
// on the poll (a UI request is blocked on each), run here against the local
// worktree, and report back via postFileOpResult. All access is confined to
// the worktree with os.Root, so a hostile path can't traverse or symlink out.

// maxFileContentBytes caps a single read/write through the relay. Larger files
// come back truncated (content omitted, read-only in the UI); larger writes
// are refused — the editor is for source files, not blobs.
const maxFileContentBytes = 1 << 20

// handleFileOp executes one claimed op and posts the result. Errors land in
// the result payload (the waiting UI request surfaces them); only the report
// POST itself failing is logged here.
func (m *Module) handleFileOp(ctx context.Context, op shared.FileOp) {
	wtPath := IssueWorktreePath(m.deps.WorkspacesRoot, op.WorkspaceID, op.IssueID, op.RepoURL)
	var res shared.FileOpResult
	switch {
	case !shared.ValidRelFilePath(op.Path):
		res = shared.FileOpResult{Error: "invalid path"}
	case !isWorktree(wtPath):
		res = shared.FileOpResult{Error: "worktree missing on this machine"}
	case op.Kind == shared.FileOpRead:
		res = readWorktreeFile(wtPath, op.Path)
	case op.Kind == shared.FileOpWrite:
		res = writeWorktreeFile(wtPath, op.Path, op.Content)
	case op.Kind == shared.FileOpDiff:
		res = diffWorktreeFile(wtPath, op.Path)
	default:
		res = shared.FileOpResult{Error: "unknown file op kind"}
	}
	if err := m.cl.postFileOpResult(ctx, op.ID, res); err != nil {
		m.log().Debug("worktrees: post file-op result failed", "op", op.ID, "error", err)
	}
}

// readWorktreeFile reads one repo-relative file inside the worktree. Binary
// (NUL bytes / invalid UTF-8, which JSON can't carry) and oversized files are
// flagged instead of streamed.
func readWorktreeFile(wtPath, rel string) shared.FileOpResult {
	root, err := os.OpenRoot(wtPath)
	if err != nil {
		return shared.FileOpResult{Error: "open worktree: " + err.Error()}
	}
	defer root.Close()
	f, err := root.Open(filepath.FromSlash(rel))
	if err != nil {
		return fileOpError(err)
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return shared.FileOpResult{Error: err.Error()}
	}
	if st.IsDir() {
		return shared.FileOpResult{Error: "not a file"}
	}
	if st.Size() > maxFileContentBytes {
		return shared.FileOpResult{Size: st.Size(), Truncated: true}
	}
	b, err := io.ReadAll(io.LimitReader(f, maxFileContentBytes+1))
	if err != nil {
		return shared.FileOpResult{Error: err.Error()}
	}
	if len(b) > maxFileContentBytes { // grew between Stat and read
		return shared.FileOpResult{Size: int64(len(b)), Truncated: true}
	}
	if isBinary(b) {
		return shared.FileOpResult{Size: int64(len(b)), Binary: true}
	}
	return shared.FileOpResult{Content: string(b), Size: int64(len(b))}
}

// writeWorktreeFile replaces one repo-relative file's content, write-then-
// rename so a crash mid-write never leaves a half-truncated source file. The
// original file mode is preserved (a +x script stays executable).
func writeWorktreeFile(wtPath, rel, content string) shared.FileOpResult {
	if len(content) > maxFileContentBytes {
		return shared.FileOpResult{Error: "file too large"}
	}
	root, err := os.OpenRoot(wtPath)
	if err != nil {
		return shared.FileOpResult{Error: "open worktree: " + err.Error()}
	}
	defer root.Close()
	osRel := filepath.FromSlash(rel)

	mode := os.FileMode(0o644)
	if st, err := root.Stat(osRel); err == nil {
		if !st.Mode().IsRegular() {
			return shared.FileOpResult{Error: "not a file"}
		}
		mode = st.Mode().Perm()
	} else if !errors.Is(err, fs.ErrNotExist) {
		return fileOpError(err)
	}

	tmp := osRel + ".multica-tmp"
	f, err := root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode)
	if err != nil {
		return fileOpError(err)
	}
	if _, err := f.Write([]byte(content)); err != nil {
		_ = f.Close()
		_ = root.Remove(tmp)
		return shared.FileOpResult{Error: err.Error()}
	}
	if err := f.Close(); err != nil {
		_ = root.Remove(tmp)
		return shared.FileOpResult{Error: err.Error()}
	}
	if err := root.Rename(tmp, osRel); err != nil {
		_ = root.Remove(tmp)
		return shared.FileOpResult{Error: err.Error()}
	}
	return shared.FileOpResult{Size: int64(len(content))}
}

// diffWorktreeFile reads one repo-relative file's base content (at the
// worktree's merge-base with the default branch, via git show) and its current
// working-tree content, for the Changes tab's diff view. A file missing on one
// side is that side being empty (added / deleted file); missing on both sides
// is NotFound. Binary or oversized content on either side flags the whole diff.
func diffWorktreeFile(wtPath, rel string) shared.FileOpResult {
	newRes := readWorktreeFile(wtPath, rel)
	if newRes.Error != "" && !newRes.NotFound {
		return newRes
	}

	mergeBase, _ := changesBase(wtPath)
	oldContent, oldExists := "", false
	oldBinary, oldTruncated := false, false
	// git show fails when the path did not exist at the base — an added file.
	if out, err := runGitStdout(wtPath, "show", mergeBase+":"+rel); err == nil {
		oldExists = true
		switch {
		case len(out) > maxFileContentBytes:
			oldTruncated = true
		case isBinary([]byte(out)):
			oldBinary = true
		default:
			oldContent = out
		}
	}

	if newRes.NotFound && !oldExists {
		return shared.FileOpResult{NotFound: true, Error: "file not found"}
	}
	return shared.FileOpResult{
		Content:    newRes.Content,
		OldContent: oldContent,
		Size:       newRes.Size,
		Truncated:  newRes.Truncated || oldTruncated,
		Binary:     newRes.Binary || oldBinary,
	}
}

// isBinary reports content the editor can't round-trip: NUL bytes in the
// leading probe, or invalid UTF-8 anywhere (JSON strings must be UTF-8).
func isBinary(b []byte) bool {
	probe := b
	if len(probe) > 8000 {
		probe = probe[:8000]
	}
	if bytes.IndexByte(probe, 0) >= 0 {
		return true
	}
	return !utf8.Valid(b)
}

// fileOpError maps a filesystem error onto the result, distinguishing missing
// files (a clean 404 upstream) from real failures.
func fileOpError(err error) shared.FileOpResult {
	if errors.Is(err, fs.ErrNotExist) {
		return shared.FileOpResult{NotFound: true, Error: "file not found"}
	}
	return shared.FileOpResult{Error: err.Error()}
}

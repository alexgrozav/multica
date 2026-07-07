// Package shared holds the wire contract for the worktrees add-on: the WS
// event-type strings, the status enums, and the JSON DTOs exchanged between the
// server side and the daemon side (and mirrored by the frontend module).
//
// It imports nothing from the host application on purpose — the add-on is a
// self-contained module wired into the parent repo only through thin adapters.
package shared

import "strings"

// ManifestFile is the per-repo config file the daemon reads from a checked-out
// worktree to discover the Setup / Run / Cleanup scripts. It lives at the repo
// root and is the single source of truth for scripts (there is no server-stored
// script config).
const ManifestFile = "multica.json"

// WebSocket event-type strings. These must match the strings the frontend
// module (packages/worktrees-addon) subscribes to.
const (
	EventWorktreeUpdated = "worktree:updated"
	EventWorktreeRunLog  = "worktree_run:log"
	EventWorktreeFiles   = "worktree_files:updated"
	EventWorktreeChanges = "worktree_changes:updated"
)

// Worktree lifecycle status values (issue_worktree.status).
const (
	StatusPending      = "pending"
	StatusInitializing = "initializing"
	StatusReady        = "ready"
	StatusError        = "error"
	StatusCleaning     = "cleaning"
	StatusRemoved      = "removed"
)

// Setup script status values (issue_worktree.setup_status).
const (
	ScriptNone      = "none"
	ScriptRunning   = "running"
	ScriptSucceeded = "succeeded"
	ScriptFailed    = "failed"
)

// Run (on-demand) script status values (worktree_run.run_status).
const (
	RunIdle      = "idle"
	RunRunning   = "running"
	RunSucceeded = "succeeded"
	RunFailed    = "failed"
	RunStopped   = "stopped"
)

// Pending-action values — the durable daemon job queue carried on the row.
// Setup/Cleanup live on issue_worktree.pending_action; Run/Stop live on
// worktree_run.pending_action (one per named run script).
const (
	ActionNone    = ""
	ActionSetup   = "setup"
	ActionCleanup = "cleanup"
	ActionRun     = "run"
	ActionStop    = "stop"
)

// Job kinds the daemon polls for.
const (
	JobInit    = "init"
	JobSetup   = "setup"
	JobRun     = "run"
	JobStop    = "stop"
	JobCleanup = "cleanup"
	JobOpen    = "open"
)

// Open-in targets: the local applications the daemon can open an issue's working
// directory in. "copy" (copy path) is handled entirely on the client and never
// reaches the server, so it is intentionally absent.
const (
	OpenFinder   = "finder"
	OpenVSCode   = "vscode"
	OpenZed      = "zed"
	OpenXcode    = "xcode"
	OpenGhostty  = "ghostty"
	OpenTerminal = "terminal"
)

// ValidOpenTarget reports whether t is a known open-in target the daemon can act
// on — used to reject arbitrary values at the request boundary before they are
// queued for (and executed by) the daemon.
func ValidOpenTarget(t string) bool {
	switch t {
	case OpenFinder, OpenVSCode, OpenZed, OpenXcode, OpenGhostty, OpenTerminal:
		return true
	default:
		return false
	}
}

// Streams for a log line.
const (
	StreamStdout = "stdout"
	StreamStderr = "stderr"
)

// File-op kinds: on-demand file content reads/writes relayed to the owning
// daemon (the file-tab editor in the issue view). Diff fetches one file's
// base + working content in a single round trip (the Changes tab's diff view).
const (
	FileOpRead  = "read"
	FileOpWrite = "write"
	FileOpDiff  = "diff"
)

// Change statuses of one changed file vs the worktree's base (merge-base with
// the default branch), as reported by the daemon's git-status scan.
const (
	ChangeAdded    = "added"
	ChangeModified = "modified"
	ChangeDeleted  = "deleted"
)

// ValidRelFilePath reports whether p is a safe repo-relative file path as the
// file-list scan produces them: slash-separated, already clean, and confined to
// the repo. Rejects absolute paths, "..", ".", ".git" segments, backslashes and
// NULs — checked at the request boundary before an op is queued for (and
// executed by) the daemon. The daemon additionally confines all file access
// with os.Root, so this is the readable-error layer, not the only defense.
func ValidRelFilePath(p string) bool {
	if p == "" || len(p) > 4096 {
		return false
	}
	if strings.ContainsAny(p, "\x00\\") {
		return false
	}
	if strings.HasPrefix(p, "/") || strings.HasSuffix(p, "/") {
		return false
	}
	for _, seg := range strings.Split(p, "/") {
		switch seg {
		case "", ".", "..", ".git":
			return false
		}
	}
	return true
}

// Manifest mirrors the repo's multica.json. Scripts holds the Setup / Run /
// Cleanup commands, nested under a "scripts" key so the file has room for other
// top-level config later. The daemon reads this from the worktree — the server
// never sees script bodies.
type Manifest struct {
	Scripts Scripts `json:"scripts"`
}

// Scripts are the shell commands defined in multica.json. Setup and Cleanup are
// single commands; Run maps a name (e.g. "dev", "start") to a command.
type Scripts struct {
	Setup   string            `json:"setup"`
	Run     map[string]string `json:"run"`
	Cleanup string            `json:"cleanup"`
}

// HasSetup reports whether a runnable Setup command is defined.
func (m Manifest) HasSetup() bool { return strings.TrimSpace(m.Scripts.Setup) != "" }

// HasCleanup reports whether a runnable Cleanup command is defined.
func (m Manifest) HasCleanup() bool { return strings.TrimSpace(m.Scripts.Cleanup) != "" }

// RunCommand returns the command for a named run script (trimmed), or "".
func (m Manifest) RunCommand(name string) string { return strings.TrimSpace(m.Scripts.Run[name]) }

// RunNames returns the defined run-script names that have a non-empty command,
// sorted for stable display.
func (m Manifest) RunNames() []string {
	names := make([]string, 0, len(m.Scripts.Run))
	for name, cmd := range m.Scripts.Run {
		if strings.TrimSpace(name) != "" && strings.TrimSpace(cmd) != "" {
			names = append(names, name)
		}
	}
	sortStrings(names)
	return names
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j-1] > s[j]; j-- {
			s[j-1], s[j] = s[j], s[j-1]
		}
	}
}

// RunScript is one named run command's state on a worktree (worktree_run row),
// surfaced to the UI so it can render a Run/Stop control + log tab per script.
type RunScript struct {
	Name      string `json:"name"`
	Status    string `json:"status"` // idle|running|succeeded|failed|stopped
	RunTaskID string `json:"run_task_id,omitempty"`
	LastError string `json:"last_error,omitempty"`
}

// Worktree is the API representation of an issue_worktree row, returned to the
// UI and embedded in worktree:updated events.
type Worktree struct {
	ID            string `json:"id"`
	IssueID       string `json:"issue_id"`
	Identifier    string `json:"identifier"`
	WorkspaceID   string `json:"workspace_id"`
	RepoURL       string `json:"repo_url"`
	OwnerDaemonID string `json:"owner_daemon_id,omitempty"`
	Path          string `json:"path"`
	// WorkingDir is the per-issue parent that holds every repo checkout — the
	// unified agent workdir, and what "Open in" opens. Derived from Path.
	WorkingDir  string      `json:"working_dir,omitempty"`
	Branch      string      `json:"branch"`
	Status      string      `json:"status"`
	SetupStatus string      `json:"setup_status"`
	SetupTaskID string      `json:"setup_task_id,omitempty"`
	HasSetup    bool        `json:"has_setup"`
	HasCleanup  bool        `json:"has_cleanup"`
	Runs        []RunScript `json:"runs"`
	LastError   string      `json:"last_error,omitempty"`
	CreatedAt   string      `json:"created_at"`
	UpdatedAt   string      `json:"updated_at"`
}

// Job is one unit of work the daemon claims from the poll endpoint.
type Job struct {
	Kind        string `json:"kind"`
	WorktreeID  string `json:"worktree_id"`
	IssueID     string `json:"issue_id"`
	Identifier  string `json:"identifier,omitempty"` // human id (e.g. PRO-11) → branch name
	WorkspaceID string `json:"workspace_id"`
	RepoURL     string `json:"repo_url"`
	ScriptName  string `json:"script_name,omitempty"` // named run/stop target
	RunTaskID   string `json:"run_task_id,omitempty"`
	SetupTaskID string `json:"setup_task_id,omitempty"`
	OpenTarget  string `json:"open_target,omitempty"` // open-in target app (JobOpen)
}

// JobsResponse is the daemon poll response. FileScan lists the ready worktrees
// this daemon owns (with the server's current file-list digest), so the poll
// loop can rescan them and report only real changes. FileOps are pending
// on-demand file reads/writes whose UI callers are waiting on the result.
type JobsResponse struct {
	Jobs     []Job            `json:"jobs"`
	FileScan []FileScanTarget `json:"file_scan,omitempty"`
	FileOps  []FileOp         `json:"file_ops,omitempty"`
}

// FileOp is one on-demand file content request, relayed to the owning daemon on
// its next poll. Ephemeral by design: it exists only while a UI request waits
// for the daemon's result (there is no DB row).
type FileOp struct {
	ID          string `json:"id"`
	Kind        string `json:"kind"` // read | write
	WorktreeID  string `json:"worktree_id"`
	IssueID     string `json:"issue_id"`
	WorkspaceID string `json:"workspace_id"`
	RepoURL     string `json:"repo_url"`
	Path        string `json:"path"`              // repo-relative, slash-separated
	Content     string `json:"content,omitempty"` // write payload
}

// FileOpResult is what the daemon POSTs after executing a FileOp. Binary and
// Truncated flag files the editor must not round-trip (content is omitted);
// NotFound distinguishes a missing file from an execution error. For diff ops
// Content is the working-tree side and OldContent the base side.
type FileOpResult struct {
	Content    string `json:"content,omitempty"`
	OldContent string `json:"old_content,omitempty"`
	Size       int64  `json:"size"`
	Truncated  bool   `json:"truncated,omitempty"`
	Binary     bool   `json:"binary,omitempty"`
	NotFound   bool   `json:"not_found,omitempty"`
	Error      string `json:"error,omitempty"`
}

// FileContent is the UI response of the file read/write endpoints.
type FileContent struct {
	WorktreeID string `json:"worktree_id"`
	Path       string `json:"path"`
	Content    string `json:"content"`
	Size       int64  `json:"size"`
	Truncated  bool   `json:"truncated"`
	Binary     bool   `json:"binary"`
}

// FileDiff is the UI response of the diff endpoint: one file's content at the
// worktree's base (merge-base with the default branch) and in the working tree.
// An added file has an empty OldContent; a deleted file an empty NewContent.
type FileDiff struct {
	WorktreeID string `json:"worktree_id"`
	Path       string `json:"path"`
	OldContent string `json:"old_content"`
	NewContent string `json:"new_content"`
	Truncated  bool   `json:"truncated"`
	Binary     bool   `json:"binary"`
}

// WriteFileRequest is the body of the file write endpoint.
type WriteFileRequest struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

// FileScanTarget is one ready worktree whose file list + git status the daemon
// keeps synced. Digest / ChangesDigest are what the server currently stores
// ("" when none), making the daemon's change detection stateless across
// restarts.
type FileScanTarget struct {
	WorktreeID    string `json:"worktree_id"`
	IssueID       string `json:"issue_id"`
	WorkspaceID   string `json:"workspace_id"`
	RepoURL       string `json:"repo_url"`
	Digest        string `json:"digest"`
	ChangesDigest string `json:"changes_digest,omitempty"`
}

// FileReport is what the daemon POSTs when a worktree's file list changed: the
// project files (tracked + untracked-but-not-ignored, sorted, repo-relative)
// with a digest for cheap change detection.
type FileReport struct {
	Digest    string   `json:"digest"`
	Paths     []string `json:"paths"`
	Truncated bool     `json:"truncated"`
}

// WorktreeFiles is the UI representation of one repo worktree's stored file
// list, returned by the issue files endpoint.
type WorktreeFiles struct {
	WorktreeID string   `json:"worktree_id"`
	IssueID    string   `json:"issue_id"`
	RepoURL    string   `json:"repo_url"`
	Digest     string   `json:"digest"`
	Paths      []string `json:"paths"`
	Truncated  bool     `json:"truncated"`
	UpdatedAt  string   `json:"updated_at"`
}

// ChangeEntry is one changed file in a worktree's git status vs its base:
// committed + uncommitted work combined (a diff of merge-base → working tree,
// plus untracked files). Uncommitted marks files that still differ from HEAD.
// Binary files carry no line counts.
type ChangeEntry struct {
	Path        string `json:"path"`
	Status      string `json:"status"` // added|modified|deleted
	Additions   int    `json:"additions"`
	Deletions   int    `json:"deletions"`
	Binary      bool   `json:"binary,omitempty"`
	Uncommitted bool   `json:"uncommitted,omitempty"`
}

// ChangesReport is what the daemon POSTs when a worktree's git status changed:
// every changed file vs the base ref, with a digest for cheap change detection.
type ChangesReport struct {
	Digest    string        `json:"digest"`
	Base      string        `json:"base"` // base ref the diff was taken against (e.g. origin/main)
	Files     []ChangeEntry `json:"files"`
	Truncated bool          `json:"truncated"`
}

// WorktreeChanges is the UI representation of one repo worktree's stored git
// status, returned by the issue changes endpoint.
type WorktreeChanges struct {
	WorktreeID string        `json:"worktree_id"`
	IssueID    string        `json:"issue_id"`
	RepoURL    string        `json:"repo_url"`
	Digest     string        `json:"digest"`
	Base       string        `json:"base"`
	Files      []ChangeEntry `json:"files"`
	Truncated  bool          `json:"truncated"`
	UpdatedAt  string        `json:"updated_at"`
}

// ChangesUpdatedEvent is the worktree_changes:updated WS payload. It carries
// only ids + digest (not the list) — viewers refetch over HTTP.
type ChangesUpdatedEvent struct {
	IssueID    string `json:"issue_id"`
	WorktreeID string `json:"worktree_id"`
	Digest     string `json:"digest"`
}

// FilesUpdatedEvent is the worktree_files:updated WS payload. It carries only
// ids + digest (not the list) — viewers refetch the file list over HTTP.
type FilesUpdatedEvent struct {
	IssueID    string `json:"issue_id"`
	WorktreeID string `json:"worktree_id"`
	Digest     string `json:"digest"`
}

// StatusReport is what the daemon POSTs after working a job. For init it also
// carries the discovered manifest (has_setup / has_cleanup / run_scripts) so the
// server can populate the row + per-name run rows without seeing script bodies.
type StatusReport struct {
	Kind        string   `json:"kind"`
	Status      string   `json:"status,omitempty"` // worktree status to set
	Path        string   `json:"path,omitempty"`
	Branch      string   `json:"branch,omitempty"`
	SetupStatus string   `json:"setup_status,omitempty"`
	HasSetup    bool     `json:"has_setup,omitempty"`
	HasCleanup  bool     `json:"has_cleanup,omitempty"`
	RunScripts  []string `json:"run_scripts,omitempty"` // discovered run names (init)
	ScriptName  string   `json:"script_name,omitempty"` // run/stop target
	RunStatus   string   `json:"run_status,omitempty"`
	ExitCode    *int     `json:"exit_code,omitempty"`
	Error       string   `json:"error,omitempty"`
}

// LogLine is a single streamed script-output line.
type LogLine struct {
	Seq     int    `json:"seq"`
	Stream  string `json:"stream"`
	Content string `json:"content"`
}

// LogBatch is what the daemon POSTs to stream run output to the server.
type LogBatch struct {
	Lines []LogLine `json:"lines"`
}

// RunLogEvent is the worktree_run:log WS payload (mirrored by the frontend).
type RunLogEvent struct {
	RunTaskID  string `json:"run_task_id"`
	IssueID    string `json:"issue_id"`
	WorktreeID string `json:"worktree_id"`
	Seq        int    `json:"seq"`
	Stream     string `json:"stream"`
	Content    string `json:"content"`
}

// UpdatedEvent is the worktree:updated WS payload.
type UpdatedEvent struct {
	IssueID  string   `json:"issue_id"`
	Worktree Worktree `json:"worktree"`
}

// RunRequest is the body of the on-demand run / stop endpoints (names a script).
type RunRequest struct {
	Name string `json:"name"`
}

// OpenRequest is the body of the open-in endpoint (names a target app).
type OpenRequest struct {
	Target string `json:"target"`
}

// IssueWorktreeStatus is one repo's readiness in an IssueStatusResponse.
type IssueWorktreeStatus struct {
	RepoURL     string `json:"repo_url"`
	Status      string `json:"status"`
	SetupStatus string `json:"setup_status"`
	Path        string `json:"path"`
}

// IssueStatusResponse is the daemon-facing readiness snapshot for an issue's
// worktrees, used by the agent task's wait-and-adopt eager checkout. Managed is
// false when the add-on is not tracking the issue (no rows) — the caller then
// falls back to a legacy self-checkout.
type IssueStatusResponse struct {
	Managed   bool                  `json:"managed"`
	Worktrees []IssueWorktreeStatus `json:"worktrees"`
}

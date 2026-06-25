// Package shared holds the wire contract for the worktrees add-on: the WS
// event-type strings, the status enums, and the JSON DTOs exchanged between the
// server side and the daemon side (and mirrored by the frontend module).
//
// It imports nothing from the host application on purpose — the add-on is a
// self-contained module wired into the parent repo only through thin adapters.
package shared

// WebSocket event-type strings. These must match the strings the frontend
// module (packages/worktrees-addon) subscribes to.
const (
	EventWorktreeUpdated = "worktree:updated"
	EventWorktreeRunLog  = "worktree_run:log"
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

// Setup/Cleanup script status values.
const (
	ScriptNone      = "none"
	ScriptRunning   = "running"
	ScriptSucceeded = "succeeded"
	ScriptFailed    = "failed"
)

// Run (on-demand) script status values (issue_worktree.run_status).
const (
	RunIdle      = "idle"
	RunRunning   = "running"
	RunSucceeded = "succeeded"
	RunFailed    = "failed"
	RunStopped   = "stopped"
)

// Pending-action values (issue_worktree.pending_action) — the durable daemon
// job queue carried on the row itself.
const (
	ActionNone    = ""
	ActionSetup   = "setup"
	ActionRun     = "run"
	ActionStop    = "stop"
	ActionCleanup = "cleanup"
)

// Job kinds the daemon polls for.
const (
	JobInit    = "init"
	JobSetup   = "setup"
	JobRun     = "run"
	JobStop    = "stop"
	JobCleanup = "cleanup"
)

// Streams for a log line.
const (
	StreamStdout = "stdout"
	StreamStderr = "stderr"
)

// Worktree is the API representation of an issue_worktree row, returned to the
// UI and embedded in worktree:updated events.
type Worktree struct {
	ID             string `json:"id"`
	IssueID        string `json:"issue_id"`
	WorkspaceID    string `json:"workspace_id"`
	RepoURL        string `json:"repo_url"`
	OwnerDaemonID  string `json:"owner_daemon_id,omitempty"`
	Path           string `json:"path"`
	Branch         string `json:"branch"`
	Status         string `json:"status"`
	SetupStatus    string `json:"setup_status"`
	RunStatus      string `json:"run_status"`
	RunTaskID      string `json:"run_task_id,omitempty"`
	SetupTaskID    string `json:"setup_task_id,omitempty"`
	HasRunScript   bool   `json:"has_run_script"`
	HasSetupScript bool   `json:"has_setup_script"`
	LastError      string `json:"last_error,omitempty"`
	CreatedAt      string `json:"created_at"`
	UpdatedAt      string `json:"updated_at"`
}

// Job is one unit of work the daemon claims from the poll endpoint. The server
// includes only the scripts relevant to the job kind.
type Job struct {
	Kind        string `json:"kind"`
	WorktreeID  string `json:"worktree_id"`
	IssueID     string `json:"issue_id"`
	WorkspaceID string `json:"workspace_id"`
	RepoURL     string `json:"repo_url"`
	RunTaskID   string `json:"run_task_id,omitempty"`
	SetupTaskID string `json:"setup_task_id,omitempty"`
	Setup       string `json:"setup,omitempty"`
	Run         string `json:"run,omitempty"`
	Cleanup     string `json:"cleanup,omitempty"`
}

// JobsResponse is the daemon poll response.
type JobsResponse struct {
	Jobs []Job `json:"jobs"`
}

// StatusReport is what the daemon POSTs after working an init/run/cleanup job.
type StatusReport struct {
	Kind        string `json:"kind"`
	Status      string `json:"status,omitempty"`       // worktree status to set
	Path        string `json:"path,omitempty"`         // init result
	Branch      string `json:"branch,omitempty"`       // init result
	SetupStatus string `json:"setup_status,omitempty"` // init result
	RunStatus   string `json:"run_status,omitempty"`   // run result
	ExitCode    *int   `json:"exit_code,omitempty"`
	Error       string `json:"error,omitempty"`
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

// RepoScript holds the three scripts configured for one repository.
type RepoScript struct {
	RepoURL string `json:"repo_url"`
	Setup   string `json:"setup"`
	Run     string `json:"run"`
	Cleanup string `json:"cleanup"`
}

// Config is the per-workspace worktree configuration returned/accepted by the
// config endpoint and rendered in the settings tab.
type Config struct {
	AutoInit bool         `json:"auto_init"`
	Repos    []RepoScript `json:"repos"`
}

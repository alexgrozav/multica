// Frontend mirror of the worktrees add-on wire contract
// (server/addons/worktrees/shared). Enums are kept as plain strings so unknown
// server values still render (API-compatibility rule).

// One named run script's state on a worktree (dev, start, …). Discovered by the
// daemon from multica.json; each has its own status + streaming log channel.
export interface WorktreeRunScript {
  name: string;
  status: string; // idle | running | succeeded | failed | stopped
  run_task_id?: string;
  last_error?: string;
}

export interface IssueWorktree {
  id: string;
  issue_id: string;
  identifier: string; // human id (e.g. PRO-11) — also the worktree branch name
  workspace_id: string;
  repo_url: string;
  owner_daemon_id?: string;
  path: string;
  working_dir?: string; // per-issue parent dir holding every repo checkout (what "Open in" opens)
  branch: string;
  status: string; // pending | initializing | ready | error | cleaning | removed
  setup_status: string; // none | running | succeeded | failed
  setup_task_id?: string; // log channel for the latest Setup execution (boot or re-run)
  has_setup: boolean; // multica.json defines a setup script
  has_cleanup: boolean; // multica.json defines a cleanup script
  runs: WorktreeRunScript[]; // named run scripts from multica.json
  last_error?: string;
  created_at: string;
  updated_at: string;
}

export interface WorktreeRunLog {
  run_task_id: string;
  issue_id: string;
  worktree_id: string;
  seq: number;
  stream: "stdout" | "stderr";
  content: string;
}

export interface WorktreeUpdatedEvent {
  issue_id: string;
  worktree: IssueWorktree;
}

// One repo worktree's project file list (sorted paths relative to the repo
// root), as scanned by the daemon and stored server-side for the Project tab.
export interface WorktreeFileList {
  worktree_id: string;
  issue_id: string;
  repo_url: string;
  digest: string;
  paths: string[];
  truncated: boolean;
  updated_at: string;
}

// worktree_files:updated WS payload — ids + digest only; the file list itself
// is refetched over HTTP by mounted viewers.
export interface WorktreeFilesUpdatedEvent {
  issue_id: string;
  worktree_id: string;
  digest: string;
}

// One file's content read live from the worktree (the file-tab editor).
// binary / truncated content is withheld — those files render read-only hints.
export interface WorktreeFileContent {
  worktree_id: string;
  path: string;
  content: string;
  size: number;
  truncated: boolean;
  binary: boolean;
}

// One changed file in a worktree's git status vs its base: committed +
// uncommitted work combined. status stays a plain string (API-compat rule);
// binary files carry no line counts; uncommitted marks files still differing
// from HEAD.
export interface WorktreeChangeEntry {
  path: string;
  status: string; // added | modified | deleted
  additions: number;
  deletions: number;
  binary?: boolean;
  uncommitted?: boolean;
}

// One repo worktree's stored git status (the Changes tab), as scanned by the
// daemon against the branch base (merge-base with the default branch).
export interface WorktreeChanges {
  worktree_id: string;
  issue_id: string;
  repo_url: string;
  digest: string;
  base: string; // base ref the diff was taken against (e.g. origin/main)
  files: WorktreeChangeEntry[];
  truncated: boolean;
  updated_at: string;
}

// worktree_changes:updated WS payload — ids + digest only; the list itself is
// refetched over HTTP by mounted viewers.
export interface WorktreeChangesUpdatedEvent {
  issue_id: string;
  worktree_id: string;
  digest: string;
}

// One file's diff sides read live from the worktree (the Changes tab's diff
// view): content at the branch base and in the working tree. An added file has
// an empty old_content; a deleted file an empty new_content.
export interface WorktreeFileDiff {
  worktree_id: string;
  path: string;
  old_content: string;
  new_content: string;
  truncated: boolean;
  binary: boolean;
}

// One interactive terminal session in the issue's checked-out workspace.
// Ephemeral relay state: it lives in server memory + a daemon PTY, and dies
// with either process. index is the per-issue tab ordinal ("Terminal 2");
// title is the shell's current foreground process ("make"), "" at the prompt.
export interface IssueTerminal {
  id: string;
  issue_id: string;
  workspace_id: string;
  index: number;
  title: string;
  status: string; // pending | open | exited
  exit_code?: number;
  error?: string;
  created_at: string;
}

// worktree_terminal:updated WS payload — the session snapshot after any
// lifecycle change. removed marks a session that is gone entirely.
export interface WorktreeTerminalEvent {
  issue_id: string;
  terminal: IssueTerminal;
  removed?: boolean;
}

// JSON control frame on the terminal WebSocket (text frames; binary frames are
// raw PTY bytes). Mirrors shared.TermCtl.
export interface TerminalCtl {
  type: string; // resize | title | exit | state
  cols?: number;
  rows?: number;
  title?: string;
  status?: string;
  exit_code?: number;
  error?: string;
}

// WS event-type strings (must match server/addons/worktrees/shared).
export const WORKTREE_RUN_LOG_EVENT = "worktree_run:log";
export const WORKTREE_UPDATED_EVENT = "worktree:updated";
export const WORKTREE_FILES_EVENT = "worktree_files:updated";
export const WORKTREE_CHANGES_EVENT = "worktree_changes:updated";
export const WORKTREE_TERMINAL_EVENT = "worktree_terminal:updated";

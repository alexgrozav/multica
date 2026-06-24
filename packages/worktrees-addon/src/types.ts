// Frontend mirror of the worktrees add-on wire contract
// (server/addons/worktrees/shared). Enums are kept as plain strings so unknown
// server values still render (API-compatibility rule).

export interface IssueWorktree {
  id: string;
  issue_id: string;
  workspace_id: string;
  repo_url: string;
  owner_daemon_id?: string;
  path: string;
  branch: string;
  status: string; // pending | initializing | ready | error | cleaning | removed
  setup_status: string; // none | running | succeeded | failed
  run_status: string; // idle | running | succeeded | failed | stopped
  run_task_id?: string;
  has_run_script: boolean;
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

export interface WorktreeRepoScript {
  repo_url: string;
  setup: string;
  run: string;
  cleanup: string;
}

export interface WorktreeConfig {
  auto_init: boolean;
  repos: WorktreeRepoScript[];
}

// WS event-type strings (must match server/addons/worktrees/shared).
export const WORKTREE_RUN_LOG_EVENT = "worktree_run:log";
export const WORKTREE_UPDATED_EVENT = "worktree:updated";

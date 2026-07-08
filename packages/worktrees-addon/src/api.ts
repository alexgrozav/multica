import { z } from "zod";
import { api, parseWithFallback } from "@multica/core/api";
import type {
  IssueTerminal,
  IssueWorktree,
  WorktreeChanges,
  WorktreeFileContent,
  WorktreeFileDiff,
  WorktreeFileList,
} from "./types";

// Lenient schemas: enums stay z.string() so an unknown server value still
// parses; defaults keep the UI rendering on partial/legacy payloads.
const RunScriptSchema = z.object({
  name: z.string(),
  status: z.string().default("idle"),
  run_task_id: z.string().optional(),
  last_error: z.string().optional(),
});

const IssueWorktreeSchema = z.object({
  id: z.string(),
  issue_id: z.string().default(""),
  identifier: z.string().default(""),
  workspace_id: z.string().default(""),
  repo_url: z.string().default(""),
  owner_daemon_id: z.string().optional(),
  path: z.string().default(""),
  working_dir: z.string().optional(),
  branch: z.string().default(""),
  status: z.string().default("pending"),
  setup_status: z.string().default("none"),
  setup_task_id: z.string().optional(),
  has_setup: z.boolean().default(false),
  has_cleanup: z.boolean().default(false),
  runs: z.array(RunScriptSchema).default([]),
  last_error: z.string().optional(),
  created_at: z.string().default(""),
  updated_at: z.string().default(""),
});

function placeholderWorktree(id: string): IssueWorktree {
  return {
    id,
    issue_id: "",
    identifier: "",
    workspace_id: "",
    repo_url: "",
    path: "",
    branch: "",
    status: "error",
    setup_status: "none",
    has_setup: false,
    has_cleanup: false,
    runs: [],
    created_at: "",
    updated_at: "",
  };
}

const WorktreeFileListSchema = z.object({
  worktree_id: z.string(),
  issue_id: z.string().default(""),
  repo_url: z.string().default(""),
  digest: z.string().default(""),
  paths: z.array(z.string()).default([]),
  truncated: z.boolean().default(false),
  updated_at: z.string().default(""),
});

export async function listIssueWorktreeFiles(issueId: string): Promise<WorktreeFileList[]> {
  const raw = await api.fetch<unknown>(`/api/worktree/issues/${issueId}/files`);
  return parseWithFallback(raw, z.array(WorktreeFileListSchema), [] as WorktreeFileList[], {
    endpoint: "GET /api/worktree/issues/{id}/files",
  });
}

const WorktreeChangeEntrySchema = z.object({
  path: z.string(),
  status: z.string().default("modified"),
  additions: z.number().default(0),
  deletions: z.number().default(0),
  binary: z.boolean().optional(),
  uncommitted: z.boolean().optional(),
});

const WorktreeChangesSchema = z.object({
  worktree_id: z.string(),
  issue_id: z.string().default(""),
  repo_url: z.string().default(""),
  digest: z.string().default(""),
  base: z.string().default(""),
  files: z.array(WorktreeChangeEntrySchema).default([]),
  truncated: z.boolean().default(false),
  updated_at: z.string().default(""),
});

export async function listIssueWorktreeChanges(issueId: string): Promise<WorktreeChanges[]> {
  const raw = await api.fetch<unknown>(`/api/worktree/issues/${issueId}/changes`);
  return parseWithFallback(raw, z.array(WorktreeChangesSchema), [] as WorktreeChanges[], {
    endpoint: "GET /api/worktree/issues/{id}/changes",
  });
}

export async function listIssueWorktrees(issueId: string): Promise<IssueWorktree[]> {
  const raw = await api.fetch<unknown>(`/api/worktree/issues/${issueId}`);
  return parseWithFallback(raw, z.array(IssueWorktreeSchema), [] as IssueWorktree[], {
    endpoint: "GET /api/worktree/issues/{id}",
  });
}

export async function runWorktreeSetup(issueId: string, worktreeId: string): Promise<IssueWorktree> {
  const raw = await api.fetch<unknown>(`/api/worktree/issues/${issueId}/${worktreeId}/setup`, { method: "POST" });
  return parseWithFallback(raw, IssueWorktreeSchema, placeholderWorktree(worktreeId), {
    endpoint: "POST /api/worktree/issues/{id}/{worktreeId}/setup",
  });
}

export async function runWorktreeScript(issueId: string, worktreeId: string, name: string): Promise<IssueWorktree> {
  const raw = await api.fetch<unknown>(`/api/worktree/issues/${issueId}/${worktreeId}/run`, {
    method: "POST",
    body: JSON.stringify({ name }),
  });
  return parseWithFallback(raw, IssueWorktreeSchema, placeholderWorktree(worktreeId), {
    endpoint: "POST /api/worktree/issues/{id}/{worktreeId}/run",
  });
}

export async function stopWorktreeScript(issueId: string, worktreeId: string, name: string): Promise<IssueWorktree> {
  const raw = await api.fetch<unknown>(`/api/worktree/issues/${issueId}/${worktreeId}/run/stop`, {
    method: "POST",
    body: JSON.stringify({ name }),
  });
  return parseWithFallback(raw, IssueWorktreeSchema, placeholderWorktree(worktreeId), {
    endpoint: "POST /api/worktree/issues/{id}/{worktreeId}/run/stop",
  });
}

// Queue an "open the issue working directory in <target>" job for the owning
// daemon. Fire-and-forget: the response is ignored (the daemon opens the app
// out-of-band); a non-2xx (no ready worktree / daemon offline) throws so the
// caller can surface it.
export async function openIssueWorktree(issueId: string, target: string): Promise<void> {
  await api.fetch<unknown>(`/api/worktree/issues/${issueId}/open`, {
    method: "POST",
    body: JSON.stringify({ target }),
  });
}

const WorktreeFileContentSchema = z.object({
  worktree_id: z.string().default(""),
  path: z.string().default(""),
  content: z.string().default(""),
  size: z.number().default(0),
  truncated: z.boolean().default(false),
  binary: z.boolean().default(false),
});

// Reads one repo-relative file from the issue's checked-out worktree, relayed
// live through the owning daemon (takes up to one poll interval, ~2s). Non-2xx
// (not ready / machine offline / not found / timeout) throws for the caller's
// error state.
export async function getWorktreeFile(
  issueId: string,
  worktreeId: string,
  path: string,
): Promise<WorktreeFileContent> {
  const raw = await api.fetch<unknown>(
    `/api/worktree/issues/${issueId}/${worktreeId}/file?path=${encodeURIComponent(path)}`,
  );
  return parseWithFallback(
    raw,
    WorktreeFileContentSchema,
    { worktree_id: worktreeId, path, content: "", size: 0, truncated: false, binary: false },
    { endpoint: "GET /api/worktree/issues/{id}/{worktreeId}/file" },
  );
}

const WorktreeFileDiffSchema = z.object({
  worktree_id: z.string().default(""),
  path: z.string().default(""),
  old_content: z.string().default(""),
  new_content: z.string().default(""),
  truncated: z.boolean().default(false),
  binary: z.boolean().default(false),
});

// Reads one changed file's base + working-tree content from the issue's
// checked-out worktree, relayed live through the owning daemon (takes up to
// one poll interval, ~2s). Non-2xx throws for the caller's error state.
export async function getWorktreeFileDiff(
  issueId: string,
  worktreeId: string,
  path: string,
): Promise<WorktreeFileDiff> {
  const raw = await api.fetch<unknown>(
    `/api/worktree/issues/${issueId}/${worktreeId}/diff?path=${encodeURIComponent(path)}`,
  );
  return parseWithFallback(
    raw,
    WorktreeFileDiffSchema,
    { worktree_id: worktreeId, path, old_content: "", new_content: "", truncated: false, binary: false },
    { endpoint: "GET /api/worktree/issues/{id}/{worktreeId}/diff" },
  );
}

const IssueTerminalSchema = z.object({
  id: z.string(),
  issue_id: z.string().default(""),
  workspace_id: z.string().default(""),
  index: z.number().default(0),
  title: z.string().default(""),
  status: z.string().default("pending"),
  exit_code: z.number().optional(),
  error: z.string().optional(),
  created_at: z.string().default(""),
});

export async function listIssueTerminals(issueId: string): Promise<IssueTerminal[]> {
  const raw = await api.fetch<unknown>(`/api/worktree/issues/${issueId}/terminals`);
  return parseWithFallback(raw, z.array(IssueTerminalSchema), [] as IssueTerminal[], {
    endpoint: "GET /api/worktree/issues/{id}/terminals",
  });
}

// Opens a new terminal session on the issue's owning daemon. cols/rows size
// the PTY at spawn. Non-2xx (no ready worktree / daemon offline / too many
// terminals) throws so the caller can surface it.
export async function createIssueTerminal(
  issueId: string,
  size: { cols: number; rows: number },
): Promise<IssueTerminal> {
  const raw = await api.fetch<unknown>(`/api/worktree/issues/${issueId}/terminals`, {
    method: "POST",
    body: JSON.stringify(size),
  });
  return parseWithFallback(
    raw,
    IssueTerminalSchema,
    { id: "", issue_id: issueId, workspace_id: "", index: 0, title: "", status: "pending", created_at: "" },
    { endpoint: "POST /api/worktree/issues/{id}/terminals" },
  );
}

// Ends the terminal session (kills the shell) and removes its tab everywhere.
export async function closeIssueTerminal(issueId: string, terminalId: string): Promise<void> {
  await api.fetch<unknown>(`/api/worktree/issues/${issueId}/terminals/${terminalId}`, {
    method: "DELETE",
  });
}

const TerminalTicketSchema = z.object({ ticket: z.string().default("") });

// Mints the one-time credential for the terminal's viewer WebSocket (browsers
// cannot send auth headers on WS upgrades, so the socket authenticates with a
// short-lived ticket obtained over the normal authed API).
export async function createTerminalTicket(issueId: string, terminalId: string): Promise<string> {
  const raw = await api.fetch<unknown>(
    `/api/worktree/issues/${issueId}/terminals/${terminalId}/ticket`,
    { method: "POST" },
  );
  const parsed = parseWithFallback(raw, TerminalTicketSchema, { ticket: "" }, {
    endpoint: "POST /api/worktree/issues/{id}/terminals/{tid}/ticket",
  });
  if (!parsed.ticket) throw new Error("no terminal ticket issued");
  return parsed.ticket;
}

// The viewer WebSocket URL for a terminal session. Derived from the API base
// (absolute on desktop, possibly origin-relative on web).
export function terminalSocketUrl(ticket: string): string {
  const base = api.getBaseUrl();
  const abs =
    base && /^https?:\/\//.test(base)
      ? base
      : typeof window !== "undefined"
        ? window.location.origin + base
        : base;
  return `${abs.replace(/^http/, "ws")}/ws/worktree-terminal?ticket=${encodeURIComponent(ticket)}`;
}

// Writes editor content back into the worktree file through the same relay.
// Resolves once the daemon confirmed the write; throws on any failure so the
// editor keeps its dirty state.
export async function saveWorktreeFile(
  issueId: string,
  worktreeId: string,
  path: string,
  content: string,
): Promise<void> {
  await api.fetch<unknown>(`/api/worktree/issues/${issueId}/${worktreeId}/file`, {
    method: "PUT",
    body: JSON.stringify({ path, content }),
  });
}

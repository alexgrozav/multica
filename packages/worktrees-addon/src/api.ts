import { z } from "zod";
import { api, parseWithFallback } from "@multica/core/api";
import type { IssueWorktree, WorktreeConfig } from "./types";

// Lenient schemas: enums stay z.string() so an unknown server value still
// parses; defaults keep the UI rendering on partial/legacy payloads.
const IssueWorktreeSchema = z.object({
  id: z.string(),
  issue_id: z.string().default(""),
  workspace_id: z.string().default(""),
  repo_url: z.string().default(""),
  owner_daemon_id: z.string().optional(),
  path: z.string().default(""),
  branch: z.string().default(""),
  status: z.string().default("pending"),
  setup_status: z.string().default("none"),
  run_status: z.string().default("idle"),
  run_task_id: z.string().optional(),
  setup_task_id: z.string().optional(),
  has_run_script: z.boolean().default(false),
  has_setup_script: z.boolean().default(false),
  last_error: z.string().optional(),
  created_at: z.string().default(""),
  updated_at: z.string().default(""),
});

const RepoScriptSchema = z.object({
  repo_url: z.string(),
  setup: z.string().default(""),
  run: z.string().default(""),
  cleanup: z.string().default(""),
});

const ConfigSchema = z.object({
  auto_init: z.boolean().default(false),
  repos: z.array(RepoScriptSchema).default([]),
});

function placeholderWorktree(id: string): IssueWorktree {
  return {
    id,
    issue_id: "",
    workspace_id: "",
    repo_url: "",
    path: "",
    branch: "",
    status: "error",
    setup_status: "none",
    run_status: "idle",
    has_run_script: false,
    has_setup_script: false,
    created_at: "",
    updated_at: "",
  };
}

export async function runWorktreeSetup(issueId: string, worktreeId: string): Promise<IssueWorktree> {
  const raw = await api.fetch<unknown>(`/api/worktree/issues/${issueId}/${worktreeId}/setup`, { method: "POST" });
  return parseWithFallback(raw, IssueWorktreeSchema, placeholderWorktree(worktreeId), {
    endpoint: "POST /api/worktree/issues/{id}/{worktreeId}/setup",
  });
}

export async function listIssueWorktrees(issueId: string): Promise<IssueWorktree[]> {
  const raw = await api.fetch<unknown>(`/api/worktree/issues/${issueId}`);
  return parseWithFallback(raw, z.array(IssueWorktreeSchema), [] as IssueWorktree[], {
    endpoint: "GET /api/worktree/issues/{id}",
  });
}

export async function runWorktreeScript(issueId: string, worktreeId: string): Promise<IssueWorktree> {
  const raw = await api.fetch<unknown>(`/api/worktree/issues/${issueId}/${worktreeId}/run`, { method: "POST" });
  return parseWithFallback(raw, IssueWorktreeSchema, placeholderWorktree(worktreeId), {
    endpoint: "POST /api/worktree/issues/{id}/{worktreeId}/run",
  });
}

export async function stopWorktreeScript(issueId: string, worktreeId: string): Promise<IssueWorktree> {
  const raw = await api.fetch<unknown>(`/api/worktree/issues/${issueId}/${worktreeId}/run/stop`, { method: "POST" });
  return parseWithFallback(raw, IssueWorktreeSchema, placeholderWorktree(worktreeId), {
    endpoint: "POST /api/worktree/issues/{id}/{worktreeId}/run/stop",
  });
}

export async function getWorktreeConfig(): Promise<WorktreeConfig> {
  const raw = await api.fetch<unknown>(`/api/worktree/config`);
  return parseWithFallback(raw, ConfigSchema, { auto_init: false, repos: [] } as WorktreeConfig, {
    endpoint: "GET /api/worktree/config",
  });
}

export async function putWorktreeConfig(config: WorktreeConfig): Promise<WorktreeConfig> {
  const raw = await api.fetch<unknown>(`/api/worktree/config`, {
    method: "PUT",
    body: JSON.stringify(config),
  });
  return parseWithFallback(raw, ConfigSchema, config, { endpoint: "PUT /api/worktree/config" });
}

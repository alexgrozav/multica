import {
  queryOptions,
  useMutation,
  useQuery,
  useQueryClient,
} from "@tanstack/react-query";
import * as wapi from "./api";
import type { IssueWorktree, WorktreeConfig, WorktreeRunLog } from "./types";

export const worktreeKeys = {
  all: ["worktrees"] as const,
  list: (issueId: string) => ["worktrees", "issue", issueId] as const,
  runLog: (runTaskId: string) => ["worktrees", "run-log", runTaskId] as const,
  config: (wsId: string) => ["worktrees", "config", wsId] as const,
};

export function issueWorktreesOptions(issueId: string) {
  return queryOptions({
    queryKey: worktreeKeys.list(issueId),
    queryFn: () => wapi.listIssueWorktrees(issueId),
    staleTime: 15_000,
  });
}

export function useIssueWorktrees(issueId: string) {
  return useQuery(issueWorktreesOptions(issueId));
}

// The run-log cache is populated exclusively by the realtime hook (the WS
// handler) — the queryFn just seeds an empty array. staleTime Infinity keeps it
// from refetching (there is no backing GET; logs are ephemeral).
export function useWorktreeRunLog(runTaskId: string | undefined) {
  return useQuery<WorktreeRunLog[]>({
    queryKey: worktreeKeys.runLog(runTaskId ?? "__none__"),
    queryFn: () => [],
    enabled: !!runTaskId,
    staleTime: Infinity,
    gcTime: 5 * 60_000,
  });
}

export function useRunWorktreeScript(issueId: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (worktreeId: string) => wapi.runWorktreeScript(issueId, worktreeId),
    onMutate: async (worktreeId) => {
      await qc.cancelQueries({ queryKey: worktreeKeys.list(issueId) });
      const prev = qc.getQueryData<IssueWorktree[]>(worktreeKeys.list(issueId));
      qc.setQueryData<IssueWorktree[]>(worktreeKeys.list(issueId), (old = []) =>
        old.map((w) => (w.id === worktreeId ? { ...w, run_status: "running" } : w)),
      );
      return { prev };
    },
    onError: (_e, _v, ctx) => {
      if (ctx?.prev) qc.setQueryData(worktreeKeys.list(issueId), ctx.prev);
    },
    onSuccess: (updated) => {
      qc.setQueryData<IssueWorktree[]>(worktreeKeys.list(issueId), (old = []) =>
        old.map((w) => (w.id === updated.id ? updated : w)),
      );
    },
  });
}

export function useStopWorktreeScript(issueId: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (worktreeId: string) => wapi.stopWorktreeScript(issueId, worktreeId),
    onSuccess: (updated) => {
      qc.setQueryData<IssueWorktree[]>(worktreeKeys.list(issueId), (old = []) =>
        old.map((w) => (w.id === updated.id ? updated : w)),
      );
    },
  });
}

export function useWorktreeConfig(wsId: string) {
  return useQuery({
    queryKey: worktreeKeys.config(wsId),
    queryFn: () => wapi.getWorktreeConfig(),
    staleTime: 30_000,
  });
}

export function useSaveWorktreeConfig(wsId: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (config: WorktreeConfig) => wapi.putWorktreeConfig(config),
    onSuccess: (saved) => {
      qc.setQueryData(worktreeKeys.config(wsId), saved);
    },
  });
}

import {
  queryOptions,
  useMutation,
  useQuery,
  useQueryClient,
} from "@tanstack/react-query";
import * as wapi from "./api";
import type {
  IssueWorktree,
  WorktreeFileContent,
  WorktreeFileDiff,
  WorktreeRunLog,
} from "./types";

export const worktreeKeys = {
  all: ["worktrees"] as const,
  list: (issueId: string) => ["worktrees", "issue", issueId] as const,
  runLog: (runTaskId: string) => ["worktrees", "run-log", runTaskId] as const,
  files: (issueId: string) => ["worktrees", "files", issueId] as const,
  file: (worktreeId: string, path: string) => ["worktrees", "file", worktreeId, path] as const,
  changes: (issueId: string) => ["worktrees", "changes", issueId] as const,
  // Prefix for every diff of one worktree — what the changes WS event invalidates.
  diffs: (worktreeId: string) => ["worktrees", "diff", worktreeId] as const,
  diff: (worktreeId: string, path: string) => ["worktrees", "diff", worktreeId, path] as const,
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

// Per-repo project file lists for the issue's workspace. The WS files event
// invalidates this; staleTime keeps tab switches from refetching an unchanged
// list. `enabled` lets the Project tab defer the first fetch until activated.
export function issueWorktreeFilesOptions(issueId: string) {
  return queryOptions({
    queryKey: worktreeKeys.files(issueId),
    queryFn: () => wapi.listIssueWorktreeFiles(issueId),
    staleTime: 30_000,
  });
}

export function useIssueWorktreeFiles(issueId: string, enabled = true) {
  return useQuery({ ...issueWorktreeFilesOptions(issueId), enabled });
}

// Per-repo git status (changed files vs the branch base) for the issue's
// workspace. Mounted by the sidebar tab bar itself — the Changes tab shows a
// live count badge even before first activation. The WS changes event
// invalidates this; staleTime keeps tab switches from refetching.
export function issueWorktreeChangesOptions(issueId: string) {
  return queryOptions({
    queryKey: worktreeKeys.changes(issueId),
    queryFn: () => wapi.listIssueWorktreeChanges(issueId),
    staleTime: 15_000,
  });
}

export function useIssueWorktreeChanges(issueId: string, enabled = true) {
  return useQuery({ ...issueWorktreeChangesOptions(issueId), enabled });
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

function patchWorktree(
  qc: ReturnType<typeof useQueryClient>,
  issueId: string,
  worktreeId: string,
  fn: (w: IssueWorktree) => IssueWorktree,
) {
  qc.setQueryData<IssueWorktree[]>(worktreeKeys.list(issueId), (old = []) =>
    old.map((w) => (w.id === worktreeId ? fn(w) : w)),
  );
}

function replaceWorktree(qc: ReturnType<typeof useQueryClient>, issueId: string, updated: IssueWorktree) {
  qc.setQueryData<IssueWorktree[]>(worktreeKeys.list(issueId), (old = []) =>
    old.map((w) => (w.id === updated.id ? updated : w)),
  );
}

// A named-run target (which run script on which worktree).
interface RunTarget {
  worktreeId: string;
  name: string;
}

export function useRunWorktreeScript(issueId: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ worktreeId, name }: RunTarget) => wapi.runWorktreeScript(issueId, worktreeId, name),
    onMutate: async ({ worktreeId, name }) => {
      await qc.cancelQueries({ queryKey: worktreeKeys.list(issueId) });
      const prev = qc.getQueryData<IssueWorktree[]>(worktreeKeys.list(issueId));
      patchWorktree(qc, issueId, worktreeId, (w) => ({
        ...w,
        runs: w.runs.map((r) => (r.name === name ? { ...r, status: "running" } : r)),
      }));
      return { prev };
    },
    onError: (_e, _v, ctx) => {
      if (ctx?.prev) qc.setQueryData(worktreeKeys.list(issueId), ctx.prev);
    },
    onSuccess: (updated) => replaceWorktree(qc, issueId, updated),
  });
}

export function useStopWorktreeScript(issueId: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ worktreeId, name }: RunTarget) => wapi.stopWorktreeScript(issueId, worktreeId, name),
    onSuccess: (updated) => replaceWorktree(qc, issueId, updated),
  });
}

// Opens the issue working directory in a local app via the daemon. Fire-and-
// forget (no cache to update); the caller toasts optimistically and surfaces an
// error if the request is rejected (no ready worktree / owning machine offline).
export function useOpenIssueWorktree(issueId: string) {
  return useMutation({
    mutationFn: (target: string) => wapi.openIssueWorktree(issueId, target),
  });
}

// One file's content, relayed live through the owning daemon (a fetch takes up
// to one daemon poll, ~2s). No focus refetch — the editor stays mounted while
// its tab is open, so the only automatic refetch is on REOPENING a closed tab
// (staleTime elapsed → fresh read of a file an agent may have changed). The
// editor itself only applies refetched content to a clean buffer; unsaved
// edits are never swapped out from under the user.
export function useWorktreeFileContent(issueId: string, worktreeId: string, path: string) {
  return useQuery<WorktreeFileContent>({
    queryKey: worktreeKeys.file(worktreeId, path),
    queryFn: () => wapi.getWorktreeFile(issueId, worktreeId, path),
    staleTime: 10_000,
    gcTime: 5 * 60_000,
    refetchOnWindowFocus: false,
    retry: 1,
  });
}

// One changed file's diff sides (base + working tree), relayed live through
// the owning daemon like file content. The changes WS event invalidates every
// diff of a worktree whose git status moved, so an open diff tab tracks the
// agent's edits. Also feeds the editor's quick-diff gutter (`enabled` lets it
// skip the fetch for files without changes).
export function useWorktreeFileDiff(issueId: string, worktreeId: string, path: string, enabled = true) {
  return useQuery<WorktreeFileDiff>({
    queryKey: worktreeKeys.diff(worktreeId, path),
    queryFn: () => wapi.getWorktreeFileDiff(issueId, worktreeId, path),
    staleTime: 10_000,
    gcTime: 5 * 60_000,
    refetchOnWindowFocus: false,
    retry: 1,
    enabled,
  });
}

// Saves editor content back into the worktree file. On success the file cache
// is patched to the saved content so the editor's clean-state baseline (and a
// later remount) reflect what is on disk.
export function useSaveWorktreeFile(issueId: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ worktreeId, path, content }: { worktreeId: string; path: string; content: string }) =>
      wapi.saveWorktreeFile(issueId, worktreeId, path, content),
    onSuccess: (_res, { worktreeId, path, content }) => {
      qc.setQueryData<WorktreeFileContent>(worktreeKeys.file(worktreeId, path), {
        worktree_id: worktreeId,
        path,
        content,
        size: content.length,
        truncated: false,
        binary: false,
      });
    },
  });
}

export function useRunWorktreeSetup(issueId: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (worktreeId: string) => wapi.runWorktreeSetup(issueId, worktreeId),
    onMutate: async (worktreeId) => {
      await qc.cancelQueries({ queryKey: worktreeKeys.list(issueId) });
      const prev = qc.getQueryData<IssueWorktree[]>(worktreeKeys.list(issueId));
      patchWorktree(qc, issueId, worktreeId, (w) => ({ ...w, setup_status: "running" }));
      return { prev };
    },
    onError: (_e, _v, ctx) => {
      if (ctx?.prev) qc.setQueryData(worktreeKeys.list(issueId), ctx.prev);
    },
    onSuccess: (updated) => replaceWorktree(qc, issueId, updated),
  });
}

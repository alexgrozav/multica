"use client";

import { useEffect } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { useWS } from "@multica/core/realtime";
import { applyTerminalEvent, worktreeKeys } from "./queries";
import { mergeBySeq } from "./merge";
import {
  WORKTREE_CHANGES_EVENT,
  WORKTREE_FILES_EVENT,
  WORKTREE_RUN_LOG_EVENT,
  WORKTREE_TERMINAL_EVENT,
  WORKTREE_UPDATED_EVENT,
  type IssueTerminal,
  type WorktreeChangesUpdatedEvent,
  type WorktreeFilesUpdatedEvent,
  type WorktreeRunLog,
  type WorktreeTerminalEvent,
  type WorktreeUpdatedEvent,
} from "./types";

// The event-name strings aren't in the host's closed WSEventType union; cast
// through the subscribe param type so we add zero host event-type edits.
type WSEvent = Parameters<ReturnType<typeof useWS>["subscribe"]>[0];

// Subscribes to the add-on's WS events for one issue: run-log lines stream into
// the run-log cache (seq-deduped); worktree:updated invalidates the list so
// status pills refresh. Mounted by RunScriptsSection so it's scoped to the open
// issue and needs no edit to the host's central realtime sync.
export function useWorktreeRealtime(issueId: string) {
  const qc = useQueryClient();
  const { subscribe } = useWS();

  useEffect(() => {
    const unsubLog = subscribe(WORKTREE_RUN_LOG_EVENT as WSEvent, (payload) => {
      const p = payload as WorktreeRunLog;
      if (!p?.run_task_id) return;
      qc.setQueryData<WorktreeRunLog[]>(worktreeKeys.runLog(p.run_task_id), (old = []) =>
        mergeBySeq(old, [p]),
      );
    });

    const unsubUpdated = subscribe(WORKTREE_UPDATED_EVENT as WSEvent, (payload) => {
      const p = payload as WorktreeUpdatedEvent;
      if (!p || p.issue_id !== issueId) return;
      qc.invalidateQueries({ queryKey: worktreeKeys.list(issueId) });
    });

    // Terminal lifecycle events carry the full snapshot — patch the cache
    // directly (no refetch) so tab labels/status track titles live.
    const unsubTerminal = subscribe(WORKTREE_TERMINAL_EVENT as WSEvent, (payload) => {
      const p = payload as WorktreeTerminalEvent;
      if (!p?.terminal?.id || p.issue_id !== issueId) return;
      qc.setQueryData<IssueTerminal[]>(worktreeKeys.terminals(issueId), (old) =>
        applyTerminalEvent(old, p.terminal, p.removed === true),
      );
    });

    return () => {
      unsubLog();
      unsubUpdated();
      unsubTerminal();
    };
  }, [qc, subscribe, issueId]);
}

// Keeps the issue's worktree list + per-repo file lists + git status fresh for
// the sidebar tabs. Mounted by IssueSidebarTabs, so it works even when the
// log/tabs provider isn't mounted (issue without worktrees yet): a checkout
// appearing, a file-list change, and a cleanup all reach the Project and
// Changes tabs promptly. Invalidation is cheap — inactive queries just go
// stale; only mounted viewers actually refetch.
export function useWorktreeFilesRealtime(issueId: string) {
  const qc = useQueryClient();
  const { subscribe } = useWS();

  useEffect(() => {
    const unsubFiles = subscribe(WORKTREE_FILES_EVENT as WSEvent, (payload) => {
      const p = payload as WorktreeFilesUpdatedEvent;
      if (!p || p.issue_id !== issueId) return;
      qc.invalidateQueries({ queryKey: worktreeKeys.files(issueId) });
    });

    const unsubChanges = subscribe(WORKTREE_CHANGES_EVENT as WSEvent, (payload) => {
      const p = payload as WorktreeChangesUpdatedEvent;
      if (!p || p.issue_id !== issueId) return;
      qc.invalidateQueries({ queryKey: worktreeKeys.changes(issueId) });
      // Any open diff of that worktree refetches too — its file may be what
      // changed (mounted tabs only; closed diffs just go stale).
      if (p.worktree_id) {
        qc.invalidateQueries({ queryKey: worktreeKeys.diffs(p.worktree_id) });
      }
    });

    const unsubUpdated = subscribe(WORKTREE_UPDATED_EVENT as WSEvent, (payload) => {
      const p = payload as WorktreeUpdatedEvent;
      if (!p || p.issue_id !== issueId) return;
      qc.invalidateQueries({ queryKey: worktreeKeys.list(issueId) });
      qc.invalidateQueries({ queryKey: worktreeKeys.files(issueId) });
      qc.invalidateQueries({ queryKey: worktreeKeys.changes(issueId) });
    });

    return () => {
      unsubFiles();
      unsubChanges();
      unsubUpdated();
    };
  }, [qc, subscribe, issueId]);
}

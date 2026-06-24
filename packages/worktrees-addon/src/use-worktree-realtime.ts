"use client";

import { useEffect } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { useWS } from "@multica/core/realtime";
import { worktreeKeys } from "./queries";
import { mergeBySeq } from "./merge";
import {
  WORKTREE_RUN_LOG_EVENT,
  WORKTREE_UPDATED_EVENT,
  type WorktreeRunLog,
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

    return () => {
      unsubLog();
      unsubUpdated();
    };
  }, [qc, subscribe, issueId]);
}

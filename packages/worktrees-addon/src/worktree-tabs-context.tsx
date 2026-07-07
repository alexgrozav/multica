"use client";

import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useRef,
  useState,
  type ReactNode,
} from "react";
import { useStopWorktreeScript } from "./queries";
import { useWorktreeRealtime } from "./use-worktree-realtime";
import {
  addTab,
  nextActiveKey,
  reconcileSetupTabs,
  removeTab,
  tabKey,
  type OpenTab,
  type TabKind,
} from "./tab-state";
import type { IssueWorktree } from "./types";

interface WorktreeTabsContextValue {
  openTabs: OpenTab[];
  worktrees: IssueWorktree[];
  activeKey: string | null;
  setActive: (key: string) => void;
  // Open (or focus) the Setup / named-Run log tab for a worktree. Does not run anything.
  openSetup: (worktreeId: string) => void;
  openRun: (worktreeId: string, name: string) => void;
  // Close a tab. Closing a Run tab stops that named run if it is still running;
  // closing a Setup tab only hides the logs (re-openable from the scripts list).
  closeTab: (worktreeId: string, kind: TabKind, name?: string) => void;
}

// No-op default so consumers (e.g. RunScriptsSection) never throw when rendered
// outside a provider — which happens in the no-worktrees case where the provider
// isn't mounted, and in unit tests that render the scripts list directly.
const noop = () => {};
const WorktreeTabsContext = createContext<WorktreeTabsContextValue>({
  openTabs: [],
  worktrees: [],
  activeKey: null,
  setActive: noop,
  openSetup: noop,
  openRun: noop,
  closeTab: noop,
});

export function useWorktreeTabs(): WorktreeTabsContextValue {
  return useContext(WorktreeTabsContext);
}

export function WorktreeTabsProvider({
  issueId,
  worktrees,
  children,
}: {
  issueId: string;
  worktrees: IssueWorktree[];
  children: ReactNode;
}) {
  // Stream run/setup log lines into the cache for the whole issue, regardless of
  // which tab (if any) is currently mounted/visible.
  useWorktreeRealtime(issueId);
  const stop = useStopWorktreeScript(issueId);

  const [openTabs, setOpenTabs] = useState<OpenTab[]>([]);
  const [activeKey, setActiveKey] = useState<string | null>(null);
  // Tabs the user explicitly closed — suppresses auto-re-seeding Setup tabs.
  const closedRef = useRef<Set<string>>(new Set());

  // Auto-seed a Setup tab per repo + prune tabs for removed worktrees whenever
  // the live list changes.
  useEffect(() => {
    setOpenTabs((prev) => reconcileSetupTabs(prev, worktrees, closedRef.current));
  }, [worktrees]);

  // Effective active key is derived (not synced via effect) so it is never stale
  // or null while tabs exist — avoids a controlled/uncontrolled flash on the
  // Tabs root the first render a tab appears.
  const effectiveActiveKey = nextActiveKey(openTabs, activeKey);

  const setActive = useCallback((key: string) => setActiveKey(key), []);

  const openSetup = useCallback((worktreeId: string) => {
    const key = tabKey(worktreeId, "setup");
    closedRef.current.delete(key);
    setOpenTabs((prev) => addTab(prev, worktreeId, "setup"));
    setActiveKey(key);
  }, []);

  const openRun = useCallback((worktreeId: string, name: string) => {
    const key = tabKey(worktreeId, "run", name);
    closedRef.current.delete(key);
    setOpenTabs((prev) => addTab(prev, worktreeId, "run", name));
    setActiveKey(key);
  }, []);

  const stopMutate = stop.mutate;
  const closeTab = useCallback(
    (worktreeId: string, kind: TabKind, name?: string) => {
      closedRef.current.add(tabKey(worktreeId, kind, name));
      if (kind === "run" && name) {
        // Only stop a run that's actually still running — RequestStop no-ops
        // otherwise, but skipping the call avoids a needless round-trip.
        const wt = worktrees.find((w) => w.id === worktreeId);
        const run = wt?.runs.find((r) => r.name === name);
        if (run?.status === "running") stopMutate({ worktreeId, name });
      }
      setOpenTabs((prev) => removeTab(prev, worktreeId, kind, name));
    },
    [worktrees, stopMutate],
  );

  const value = useMemo<WorktreeTabsContextValue>(
    () => ({ openTabs, worktrees, activeKey: effectiveActiveKey, setActive, openSetup, openRun, closeTab }),
    [openTabs, worktrees, effectiveActiveKey, setActive, openSetup, openRun, closeTab],
  );

  return <WorktreeTabsContext.Provider value={value}>{children}</WorktreeTabsContext.Provider>;
}

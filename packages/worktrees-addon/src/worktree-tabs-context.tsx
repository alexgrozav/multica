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
import { toast } from "sonner";
import { useCloseTerminal, useCreateTerminal, useIssueTerminals, useStopWorktreeScript } from "./queries";
import { useWorktreeRealtime } from "./use-worktree-realtime";
import {
  addTab,
  nextActiveKey,
  reconcileSetupTabs,
  reconcileTerminalTabs,
  removeTab,
  tabKey,
  type OpenTab,
  type TabKind,
} from "./tab-state";
import type { IssueTerminal, IssueWorktree } from "./types";

interface WorktreeTabsContextValue {
  issueId: string;
  openTabs: OpenTab[];
  worktrees: IssueWorktree[];
  terminals: IssueTerminal[];
  activeKey: string | null;
  setActive: (key: string) => void;
  // Open (or focus) the Setup / named-Run log tab for a worktree. Does not run anything.
  openSetup: (worktreeId: string) => void;
  openRun: (worktreeId: string, name: string) => void;
  // Open a NEW terminal session on the issue's owning daemon (the "+" button).
  createTerminal: () => void;
  creatingTerminal: boolean;
  // Close a tab. Closing a Run tab stops that named run if it is still running;
  // closing a Setup tab only hides the logs (re-openable from the scripts list);
  // closing a terminal tab ends the session (kills the shell).
  closeTab: (worktreeId: string, kind: TabKind, name?: string) => void;
}

// No-op default so consumers (e.g. RunScriptsSection) never throw when rendered
// outside a provider — which happens in the no-worktrees case where the provider
// isn't mounted, and in unit tests that render the scripts list directly.
const noop = () => {};
const WorktreeTabsContext = createContext<WorktreeTabsContextValue>({
  issueId: "",
  openTabs: [],
  worktrees: [],
  terminals: [],
  activeKey: null,
  setActive: noop,
  openSetup: noop,
  openRun: noop,
  createTerminal: noop,
  creatingTerminal: false,
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
  // Stream run/setup log lines + terminal lifecycle events into the cache for
  // the whole issue, regardless of which tab (if any) is currently mounted.
  useWorktreeRealtime(issueId);
  const stop = useStopWorktreeScript(issueId);
  const { data: terminals = [] } = useIssueTerminals(issueId);
  const createTerminalMutation = useCreateTerminal(issueId);
  const closeTerminalMutation = useCloseTerminal(issueId);

  const [openTabs, setOpenTabs] = useState<OpenTab[]>([]);
  const [activeKey, setActiveKey] = useState<string | null>(null);
  // Tabs the user explicitly closed — suppresses auto-re-seeding Setup tabs and
  // re-opening a terminal tab in the gap before its removal event lands.
  const closedRef = useRef<Set<string>>(new Set());

  // Auto-seed a Setup tab per repo + prune tabs for removed worktrees whenever
  // the live list changes.
  useEffect(() => {
    setOpenTabs((prev) => reconcileSetupTabs(prev, worktrees, closedRef.current));
  }, [worktrees]);

  // Mirror live terminal sessions into tabs: restore after reload, adopt
  // sessions opened in another window, prune closed ones.
  useEffect(() => {
    setOpenTabs((prev) => reconcileTerminalTabs(prev, terminals, closedRef.current));
  }, [terminals]);

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

  const createTerminalMutate = createTerminalMutation.mutate;
  const createTerminal = useCallback(() => {
    // The PTY spawns at a default size; the view's first resize frame corrects
    // it as soon as the tab renders.
    createTerminalMutate(
      { cols: 80, rows: 24 },
      {
        onSuccess: (t) => {
          const key = tabKey("", "terminal", t.id);
          closedRef.current.delete(key);
          setOpenTabs((prev) => addTab(prev, "", "terminal", t.id));
          setActiveKey(key);
        },
        onError: (err) => {
          toast.error(err instanceof Error ? err.message : "Could not open a terminal");
        },
      },
    );
  }, [createTerminalMutate]);

  const stopMutate = stop.mutate;
  const closeTerminalMutate = closeTerminalMutation.mutate;
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
      if (kind === "terminal" && name) {
        // Closing the tab ends the session (kills the shell). Skip the call
        // when the session is already gone (closed from another window).
        if (terminals.some((t) => t.id === name)) closeTerminalMutate(name);
      }
      setOpenTabs((prev) => removeTab(prev, worktreeId, kind, name));
    },
    [worktrees, terminals, stopMutate, closeTerminalMutate],
  );

  const value = useMemo<WorktreeTabsContextValue>(
    () => ({
      issueId,
      openTabs,
      worktrees,
      terminals,
      activeKey: effectiveActiveKey,
      setActive,
      openSetup,
      openRun,
      createTerminal,
      creatingTerminal: createTerminalMutation.isPending,
      closeTab,
    }),
    [
      issueId,
      openTabs,
      worktrees,
      terminals,
      effectiveActiveKey,
      setActive,
      openSetup,
      openRun,
      createTerminal,
      createTerminalMutation.isPending,
      closeTab,
    ],
  );

  return <WorktreeTabsContext.Provider value={value}>{children}</WorktreeTabsContext.Provider>;
}

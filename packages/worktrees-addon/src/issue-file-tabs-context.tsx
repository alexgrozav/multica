"use client";

import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useState,
  type ReactNode,
} from "react";
import { useIssueWorktrees } from "./queries";
import {
  ISSUE_TAB_KEY,
  addFileTab,
  closeFileTab,
  effectiveActiveKey,
  fileTabKey,
  pruneFileTabs,
  setFileTabMode,
  type FileTab,
  type FileTabMode,
} from "./file-tab-state";

interface IssueFileTabsContextValue {
  fileTabs: FileTab[];
  // ISSUE_TAB_KEY or a tabKey — always valid (stale selections fall back).
  activeKey: string;
  // Tab keys with unsaved editor changes (drives the strip's dirty dots).
  dirtyKeys: ReadonlySet<string>;
  // Open (or re-focus) a file's tab in edit mode (Project tree clicks).
  openFile: (worktreeId: string, path: string) => void;
  // Open (or re-focus) a file's tab in diff mode (Changes list clicks).
  openDiff: (worktreeId: string, path: string) => void;
  // Switch an open tab between edit and diff (the toolbar's mode toggle).
  setTabMode: (worktreeId: string, path: string, mode: FileTabMode) => void;
  closeTab: (tab: FileTab) => void;
  setActive: (key: string) => void;
  setDirty: (key: string, dirty: boolean) => void;
}

// No-op default so consumers never throw outside a provider — ProjectTree and
// ChangesList render in unit tests (and any future mount point) without the
// tabs shell; clicking a file is then simply inert.
const noop = () => {};
const IssueFileTabsContext = createContext<IssueFileTabsContextValue>({
  fileTabs: [],
  activeKey: ISSUE_TAB_KEY,
  dirtyKeys: new Set<string>(),
  openFile: noop,
  openDiff: noop,
  setTabMode: noop,
  closeTab: noop,
  setActive: noop,
  setDirty: noop,
});

export function useIssueFileTabs(): IssueFileTabsContextValue {
  return useContext(IssueFileTabsContext);
}

// Tabs + selection live in ONE state object so close (which changes both) is a
// single pure functional update.
interface TabsState {
  tabs: FileTab[];
  active: string;
}

const initialState: TabsState = { tabs: [], active: ISSUE_TAB_KEY };

// IssueFileTabsProvider owns the file-tab state of one issue's detail view. It
// mounts ABOVE both panels (content + sidebar) so the Project tree and Changes
// list in the sidebar can open tabs in the content strip across the resizable
// split.
export function IssueFileTabsProvider({
  issueId,
  children,
}: {
  issueId: string;
  children: ReactNode;
}) {
  const { data: worktrees = [] } = useIssueWorktrees(issueId);
  const [state, setState] = useState<TabsState>(initialState);
  const [dirtyKeys, setDirtyKeys] = useState<ReadonlySet<string>>(() => new Set<string>());

  // Back to a clean slate when this view is reused for a different issue
  // (web's /issues/[id] route does not remount on issueId change).
  useEffect(() => {
    setState(initialState);
    setDirtyKeys(new Set<string>());
  }, [issueId]);

  // Prune tabs whose worktree was cleaned up (issue closed / repo removed);
  // a stale active selection falls back to the Issue tab via effectiveActiveKey.
  useEffect(() => {
    const live = new Set(worktrees.map((w) => w.id));
    setState((prev) => {
      const tabs = pruneFileTabs(prev.tabs, live);
      return tabs === prev.tabs ? prev : { tabs, active: prev.active };
    });
  }, [worktrees]);

  const openAs = useCallback((worktreeId: string, path: string, mode: FileTabMode) => {
    setState((prev) => ({
      tabs: addFileTab(prev.tabs, worktreeId, path, mode),
      active: fileTabKey(worktreeId, path),
    }));
  }, []);

  const openFile = useCallback(
    (worktreeId: string, path: string) => openAs(worktreeId, path, "edit"),
    [openAs],
  );

  const openDiff = useCallback(
    (worktreeId: string, path: string) => openAs(worktreeId, path, "diff"),
    [openAs],
  );

  const setTabMode = useCallback((worktreeId: string, path: string, mode: FileTabMode) => {
    setState((prev) => {
      const tabs = setFileTabMode(prev.tabs, worktreeId, path, mode);
      return tabs === prev.tabs ? prev : { tabs, active: prev.active };
    });
  }, []);

  const closeTab = useCallback((tab: FileTab) => {
    const key = fileTabKey(tab.worktreeId, tab.path);
    setDirtyKeys((prev) => {
      if (!prev.has(key)) return prev;
      const next = new Set(prev);
      next.delete(key);
      return next;
    });
    setState((prev) => {
      const out = closeFileTab(prev.tabs, prev.active, tab.worktreeId, tab.path);
      return out.tabs === prev.tabs && out.activeKey === prev.active
        ? prev
        : { tabs: out.tabs, active: out.activeKey };
    });
  }, []);

  const setActive = useCallback((key: string) => {
    setState((prev) => (prev.active === key ? prev : { tabs: prev.tabs, active: key }));
  }, []);

  const setDirty = useCallback((key: string, dirty: boolean) => {
    setDirtyKeys((prev) => {
      if (prev.has(key) === dirty) return prev;
      const next = new Set(prev);
      if (dirty) next.add(key);
      else next.delete(key);
      return next;
    });
  }, []);

  const resolvedActive = effectiveActiveKey(state.tabs, state.active);
  const value = useMemo<IssueFileTabsContextValue>(
    () => ({
      fileTabs: state.tabs,
      activeKey: resolvedActive,
      dirtyKeys,
      openFile,
      openDiff,
      setTabMode,
      closeTab,
      setActive,
      setDirty,
    }),
    [state.tabs, resolvedActive, dirtyKeys, openFile, openDiff, setTabMode, closeTab, setActive, setDirty],
  );

  return <IssueFileTabsContext.Provider value={value}>{children}</IssueFileTabsContext.Provider>;
}

// Pure tab-state helpers for the issue content file tabs (the strip under the
// page header: a pinned "Issue" tab plus one closable tab per opened file).
// Kept free of React so the open/close/activation logic is unit-testable.

// The pinned first tab (the issue conversation). Not closable, always present.
export const ISSUE_TAB_KEY = "issue";

// What a file tab currently shows: an editable buffer, or a read-only diff of
// the file against the branch base. Mode is mutable tab STATE, not identity —
// one tab per file, switched via the toolbar's Edit/Diff toggle (and forced by
// where the file was opened from: Project tree → edit, Changes list → diff).
export type FileTabMode = "edit" | "diff";

// One open tab. Identified by worktree + repo-relative path, so the same path
// in two repo worktrees of a multi-repo issue stays two distinct tabs.
export interface FileTab {
  worktreeId: string;
  path: string;
  mode: FileTabMode;
}

export function fileTabKey(worktreeId: string, path: string): string {
  return `file:${worktreeId}:${path}`;
}

export function tabKey(t: FileTab): string {
  return fileTabKey(t.worktreeId, t.path);
}

// fileTabLabel is the strip label: the basename (the full path lives in the
// tab's title attribute).
export function fileTabLabel(path: string): string {
  const i = path.lastIndexOf("/");
  return i >= 0 ? path.slice(i + 1) : path;
}

// addFileTab appends the tab, or — when the file is already open — switches
// the existing tab to the requested mode. Returns prev unchanged when the tab
// exists in that mode already.
export function addFileTab(
  prev: FileTab[],
  worktreeId: string,
  path: string,
  mode: FileTabMode,
): FileTab[] {
  const idx = prev.findIndex((t) => t.worktreeId === worktreeId && t.path === path);
  if (idx < 0) return [...prev, { worktreeId, path, mode }];
  return setFileTabMode(prev, worktreeId, path, mode);
}

// setFileTabMode switches an open tab's mode in place (same strip position).
// Returns prev unchanged when the tab is absent or already in that mode.
export function setFileTabMode(
  prev: FileTab[],
  worktreeId: string,
  path: string,
  mode: FileTabMode,
): FileTab[] {
  const idx = prev.findIndex((t) => t.worktreeId === worktreeId && t.path === path);
  if (idx < 0 || prev[idx]!.mode === mode) return prev;
  const next = [...prev];
  next[idx] = { worktreeId, path, mode };
  return next;
}

// closeFileTab drops a tab and picks the next active key: closing a background
// tab keeps the current selection; closing the active tab activates its left
// neighbor (right when it was first), falling back to the pinned Issue tab
// when it was the last file tab.
export function closeFileTab(
  prev: FileTab[],
  activeKey: string,
  worktreeId: string,
  path: string,
): { tabs: FileTab[]; activeKey: string } {
  const closedKey = fileTabKey(worktreeId, path);
  const idx = prev.findIndex((t) => t.worktreeId === worktreeId && t.path === path);
  if (idx < 0) return { tabs: prev, activeKey };
  const tabs = prev.filter((_, i) => i !== idx);
  if (activeKey !== closedKey) return { tabs, activeKey };
  const neighbor = tabs[idx - 1] ?? tabs[idx];
  return {
    tabs,
    activeKey: neighbor ? tabKey(neighbor) : ISSUE_TAB_KEY,
  };
}

// pruneFileTabs drops tabs whose worktree no longer exists (cleaned up on
// issue close). Returns the SAME array when nothing changed so React state
// doesn't churn on every worktree list invalidation.
export function pruneFileTabs(prev: FileTab[], liveWorktreeIds: ReadonlySet<string>): FileTab[] {
  const next = prev.filter((t) => liveWorktreeIds.has(t.worktreeId));
  return next.length === prev.length ? prev : next;
}

// effectiveActiveKey guards a stale selection (its tab pruned or never opened)
// by falling back to the pinned Issue tab.
export function effectiveActiveKey(tabs: FileTab[], activeKey: string): string {
  if (activeKey === ISSUE_TAB_KEY) return ISSUE_TAB_KEY;
  return tabs.some((t) => tabKey(t) === activeKey) ? activeKey : ISSUE_TAB_KEY;
}

// Pure tab-state helpers for the worktree log-tab panel. Kept free of React so
// the reconcile/selection logic can be unit-tested directly (node env).

export type TabKind = "setup" | "run";

export interface OpenTab {
  worktreeId: string;
  kind: TabKind;
  // For run tabs, which named run script (dev, start, …). Empty for setup tabs.
  name?: string;
}

// A tab is uniquely identified by its worktree + kind + name: one Setup tab per
// repo, and one Run tab per named run script. The channel id (run_task_id /
// setup_task_id) is NOT part of the identity — it is read live from the worktree
// so a tab follows a new run started from the same tab instead of remounting.
export function tabKey(worktreeId: string, kind: TabKind, name?: string): string {
  return `${worktreeId}:${kind}:${name ?? ""}`;
}

// Minimal shape the reconcile needs from a worktree.
interface WorktreeLike {
  id: string;
  has_setup: boolean;
}

// reconcileSetupTabs reconciles the open-tab list against the live worktree list:
//   - prune tabs whose worktree no longer exists (cleaned up / removed)
//   - auto-open a Setup tab for each worktree that has a Setup script, unless the
//     user explicitly closed it (`closed`) or it is already open
// Returns the SAME array reference when nothing changed so React state doesn't
// churn on every worktree:updated list invalidation.
export function reconcileSetupTabs(
  prev: OpenTab[],
  worktrees: WorktreeLike[],
  closed: ReadonlySet<string>,
): OpenTab[] {
  const liveIds = new Set(worktrees.map((w) => w.id));
  const openKeys = new Set(prev.map((t) => tabKey(t.worktreeId, t.kind, t.name)));
  let changed = false;

  const next = prev.filter((t) => {
    const keep = liveIds.has(t.worktreeId);
    if (!keep) changed = true;
    return keep;
  });

  for (const w of worktrees) {
    if (!w.has_setup) continue;
    const key = tabKey(w.id, "setup");
    if (!openKeys.has(key) && !closed.has(key)) {
      next.push({ worktreeId: w.id, kind: "setup" });
      changed = true;
    }
  }

  return changed ? next : prev;
}

// nextActiveKey keeps the active selection valid: keep the current key if it is
// still open, otherwise fall back to the first tab (or null when none remain).
export function nextActiveKey(openTabs: OpenTab[], current: string | null): string | null {
  if (openTabs.length === 0) return null;
  if (current && openTabs.some((t) => tabKey(t.worktreeId, t.kind, t.name) === current)) {
    return current;
  }
  const first = openTabs[0]!;
  return tabKey(first.worktreeId, first.kind, first.name);
}

// addTab returns prev unchanged when the tab is already open, else appends it.
export function addTab(prev: OpenTab[], worktreeId: string, kind: TabKind, name?: string): OpenTab[] {
  const key = tabKey(worktreeId, kind, name);
  if (prev.some((t) => tabKey(t.worktreeId, t.kind, t.name) === key)) return prev;
  return [...prev, { worktreeId, kind, name }];
}

// removeTab returns prev unchanged when the tab is not open, else drops it.
export function removeTab(prev: OpenTab[], worktreeId: string, kind: TabKind, name?: string): OpenTab[] {
  const key = tabKey(worktreeId, kind, name);
  if (!prev.some((t) => tabKey(t.worktreeId, t.kind, t.name) === key)) return prev;
  return prev.filter((t) => tabKey(t.worktreeId, t.kind, t.name) !== key);
}

"use client";

import { useEffect, useMemo, useState, type ReactNode } from "react";
import { Tabs, TabsList, TabsTrigger } from "@multica/ui/components/ui/tabs";
import { ChangesList } from "./changes-list";
import { ProjectTree } from "./project-tree";
import { useIssueWorktreeChanges } from "./queries";
import { useWorktreeFilesRealtime } from "./use-worktree-realtime";

type SidebarTab = "details" | "project" | "changes";

// Pill styling for the sidebar tab triggers: the ACTIVE tab is a filled
// accent pill; inactive tabs are plain text (no background, no border) that
// gain a soft rounded background on hover. Overrides the Tabs default
// variant's raised-segment actives (bg-background / shadow / dark input
// border); the base trigger's own border stays transparent throughout.
const PILL_TAB_CLASS =
  "h-auto flex-none rounded-full px-2.5 py-1 text-xs text-muted-foreground " +
  "hover:bg-accent/60 hover:text-foreground " +
  "data-active:bg-accent data-active:text-accent-foreground data-active:shadow-none " +
  "dark:data-active:border-transparent dark:data-active:bg-accent dark:data-active:text-accent-foreground";

// IssueSidebarTabs puts a tab bar at the top of the issue sidebar:
//   - Details (default): the host's sidebar content, passed as children.
//   - All files: a live file tree of the issue's checked-out workspace
//     (internal tab value stays "project").
//   - Changes: the live git status vs the branch base, with a count badge
//     that is visible without activating the tab.
//
// The bar is sticky inside the sidebar's scroll container; the -m/p offsets
// assume the host's p-4 content padding (both current mount points). Panels
// are toggled with CSS rather than unmounted: Details keeps its DOM, and the
// Project tree / Changes list — mounted on first activation — keep their
// expansion state and their queries live, so WS-driven updates keep them in
// sync even while the Details tab is shown.
export function IssueSidebarTabs({
  issueId,
  children,
}: {
  issueId: string;
  children: ReactNode;
}) {
  const [tab, setTab] = useState<SidebarTab>("details");
  const [mounted, setMounted] = useState<ReadonlySet<SidebarTab>>(() => new Set());
  useWorktreeFilesRealtime(issueId);
  // Fetched here (not in the panel) so the tab badge counts without a visit;
  // the WS changes event keeps it live.
  const { data: changeLists = [] } = useIssueWorktreeChanges(issueId);
  const changeCount = useMemo(
    () => changeLists.reduce((n, c) => n + c.files.length, 0),
    [changeLists],
  );

  // Back to the default tab when this panel is reused for a different issue
  // (web's /issues/[id] route does not remount on issueId change).
  useEffect(() => {
    setTab("details");
    setMounted(new Set());
  }, [issueId]);

  const handleTabChange = (value: unknown) => {
    const next: SidebarTab = value === "project" || value === "changes" ? value : "details";
    setTab(next);
    if (next !== "details") {
      setMounted((prev) => (prev.has(next) ? prev : new Set(prev).add(next)));
    }
  };

  return (
    <div>
      <div className="sticky top-0 z-10 -mx-4 -mt-4 mb-4 flex h-12 items-center border-b bg-background px-4">
        {/* Standalone button pills (PillButton idiom from views/common), not a
            segmented track: transparent list, each trigger a bordered
            rounded-full chip, the active one filled with accent. */}
        <Tabs value={tab} onValueChange={handleTabChange} className="w-full gap-0">
          <TabsList className="gap-1.5 bg-transparent p-0">
            <TabsTrigger value="details" className={PILL_TAB_CLASS}>
              Details
            </TabsTrigger>
            <TabsTrigger value="project" className={PILL_TAB_CLASS}>
              All files
            </TabsTrigger>
            <TabsTrigger value="changes" className={PILL_TAB_CLASS}>
              Changes
              {changeCount > 0 && (
                <span className="ml-1 font-mono text-[10px] tabular-nums opacity-70">
                  {changeCount}
                </span>
              )}
            </TabsTrigger>
          </TabsList>
        </Tabs>
      </div>
      <div className={tab === "details" ? undefined : "hidden"}>{children}</div>
      {mounted.has("project") && (
        <div className={tab === "project" ? undefined : "hidden"}>
          <ProjectTree issueId={issueId} />
        </div>
      )}
      {mounted.has("changes") && (
        <div className={tab === "changes" ? undefined : "hidden"}>
          <ChangesList issueId={issueId} />
        </div>
      )}
    </div>
  );
}

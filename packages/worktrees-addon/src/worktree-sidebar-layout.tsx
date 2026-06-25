"use client";

import type { ReactNode } from "react";
import {
  ResizableHandle,
  ResizablePanel,
  ResizablePanelGroup,
} from "@multica/ui/components/ui/resizable";
import { useIssueWorktrees } from "./queries";
import { WorktreeTabsProvider } from "./worktree-tabs-context";
import { WorktreeLogTabs } from "./worktree-log-tabs";

// WorktreeSidebarLayout wraps the issue side panel. When the issue has no
// worktrees it renders the sidebar content untouched (zero layout change). When
// it does, it splits the panel into a draggable top (the passed-in sidebar
// content) and bottom (the worktree log tabs), and provides the cross-panel tab
// state so the scripts list (top) can open tabs in the viewer (bottom).
//
// `stacked` (mobile / Sheet) drops the resizable split for a simple vertical
// stack, since the Sheet already scrolls and has no room for a drag handle.
export function WorktreeSidebarLayout({
  issueId,
  children,
  stacked = false,
}: {
  issueId: string;
  children: ReactNode;
  stacked?: boolean;
}) {
  const { data: worktrees = [] } = useIssueWorktrees(issueId);

  if (worktrees.length === 0) {
    return <>{children}</>;
  }

  if (stacked) {
    return (
      <WorktreeTabsProvider issueId={issueId} worktrees={worktrees}>
        {children}
        <div className="mt-4 h-72 border-t pt-2">
          <WorktreeLogTabs />
        </div>
      </WorktreeTabsProvider>
    );
  }

  return (
    <WorktreeTabsProvider issueId={issueId} worktrees={worktrees}>
      <ResizablePanelGroup orientation="vertical" className="h-full">
        <ResizablePanel id="worktree-sidebar-top" minSize="30%" defaultSize="58%">
          {children}
        </ResizablePanel>
        <ResizableHandle />
        <ResizablePanel id="worktree-sidebar-logs" minSize="20%" defaultSize="42%">
          <WorktreeLogTabs />
        </ResizablePanel>
      </ResizablePanelGroup>
    </WorktreeTabsProvider>
  );
}

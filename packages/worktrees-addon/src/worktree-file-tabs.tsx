"use client";

import { useEffect, useState, type ReactNode } from "react";
import { FileCode, FileDiff, MessageSquareText, X } from "lucide-react";
import { Tabs, TabsList, TabsTrigger } from "@multica/ui/components/ui/tabs";
import { cn } from "@multica/ui/lib/utils";
import { FileSearchPalette } from "./file-search-palette";
import { useIssueFileTabs } from "./issue-file-tabs-context";
import { ISSUE_TAB_KEY, fileTabLabel, tabKey, type FileTab, type FileTabMode } from "./file-tab-state";
import { useIssueWorktrees } from "./queries";
import { repoLabel } from "./repo-label";
import { WorktreeFileEditor } from "./worktree-file-editor";
import { WorktreeDiffView } from "./worktree-diff-view";

// WorktreeFileTabs renders the tab strip right under the issue page header:
//   - "Issue" (pinned, not closable): the host's issue content, passed as
//     children — the conversation view.
//   - one closable tab per file, opened from the Project tree (edit mode: a
//     CodeMirror editor) or the Changes list (diff mode: a read-only unified
//     diff vs the branch base). Mode is tab state — the toolbar's Edit/Diff
//     toggle switches it in place.
//
// The strip only appears once the issue has a checked-out workspace (or a tab
// is somehow still open) — without one there are no files to open, and the
// host layout stays byte-identical. Panels are toggled with CSS rather than
// unmounted: the issue view keeps its scroll/timeline state, and each editor
// keeps its buffer + undo history across tab switches AND mode switches.
export function WorktreeFileTabs({
  issueId,
  children,
}: {
  issueId: string;
  children: ReactNode;
}) {
  const { fileTabs, activeKey, dirtyKeys, setActive, closeTab } = useIssueFileTabs();
  const { data: worktrees = [] } = useIssueWorktrees(issueId);
  const showStrip = worktrees.length > 0 || fileTabs.length > 0;
  // Same basename in two places (repo roots, sibling dirs) → disambiguate the
  // label with the parent dir / repo.
  const multiRepo = worktrees.length > 1;

  return (
    <div className="flex min-h-0 min-w-0 flex-1 flex-col">
      {/* Cmd+P quick-open over the workspace's files (renders via portal). */}
      <FileSearchPalette issueId={issueId} />
      {showStrip && (
        <div className="shrink-0 border-b bg-background px-2 pt-1">
          <Tabs value={activeKey} onValueChange={(v) => setActive(v as string)} className="gap-0">
            <TabsList
              variant="line"
              className="h-8 w-full justify-start gap-1 overflow-x-auto rounded-none"
            >
              <TabsTrigger
                value={ISSUE_TAB_KEY}
                className="h-[26px] flex-none gap-1.5 whitespace-nowrap px-2 text-xs"
              >
                <MessageSquareText className="h-3.5 w-3.5" />
                Issue
              </TabsTrigger>
              {fileTabs.map((t) => {
                const key = tabKey(t);
                const wt = worktrees.find((w) => w.id === t.worktreeId);
                const dirty = dirtyKeys.has(key);
                const isDiff = t.mode === "diff";
                const TabIcon = isDiff ? FileDiff : FileCode;
                const location = wt ? `${repoLabel(wt.repo_url)}/${t.path}` : t.path;
                const title = isDiff ? `Diff: ${location}` : location;
                return (
                  <TabsTrigger
                    key={key}
                    value={key}
                    title={title}
                    className="h-[26px] flex-none gap-1.5 whitespace-nowrap px-2 text-xs"
                  >
                    <TabIcon className="h-3.5 w-3.5 shrink-0 text-muted-foreground" />
                    <span className="max-w-48 truncate">
                      {fileTabLabel(t.path)}
                      {multiRepo && wt && (
                        <span className="ml-1 text-[10px] text-muted-foreground/70">
                          {repoLabel(wt.repo_url)}
                        </span>
                      )}
                    </span>
                    {dirty && (
                      <span
                        aria-label="Unsaved changes"
                        className="h-1.5 w-1.5 shrink-0 rounded-full bg-primary"
                      />
                    )}
                    <span
                      role="button"
                      aria-label={`Close ${fileTabLabel(t.path)}`}
                      tabIndex={-1}
                      onPointerDown={(e) => e.stopPropagation()}
                      onClick={(e) => {
                        e.stopPropagation();
                        e.preventDefault();
                        closeTab(t);
                      }}
                      className="-mr-1 ml-0.5 inline-flex items-center justify-center rounded p-0.5 text-muted-foreground/60 transition-colors hover:bg-accent hover:text-foreground"
                    >
                      <X className="h-3 w-3" />
                    </span>
                  </TabsTrigger>
                );
              })}
            </TabsList>
          </Tabs>
        </div>
      )}

      <div
        className={cn(
          "flex min-h-0 min-w-0 flex-1 flex-col",
          activeKey !== ISSUE_TAB_KEY && "hidden",
        )}
      >
        {children}
      </div>
      {fileTabs.map((t) => {
        const key = tabKey(t);
        return (
          <div
            key={key}
            className={cn("min-h-0 min-w-0 flex-1", activeKey !== key && "hidden")}
          >
            <FileTabPanels issueId={issueId} tab={t} tabKey={key} />
          </div>
        );
      })}
    </div>
  );
}

// FileTabPanels hosts one tab's two mode views. Each mode mounts lazily on its
// first activation and then stays mounted (CSS-hidden), so toggling to Diff
// never tears down an editor buffer — unsaved edits survive the round trip.
function FileTabPanels({
  issueId,
  tab,
  tabKey: key,
}: {
  issueId: string;
  tab: FileTab;
  tabKey: string;
}) {
  const [mounted, setMounted] = useState<ReadonlySet<FileTabMode>>(() => new Set([tab.mode]));
  useEffect(() => {
    setMounted((prev) => (prev.has(tab.mode) ? prev : new Set(prev).add(tab.mode)));
  }, [tab.mode]);

  return (
    <>
      {mounted.has("edit") && (
        <div className={cn("h-full min-h-0", tab.mode !== "edit" && "hidden")}>
          <WorktreeFileEditor
            issueId={issueId}
            worktreeId={tab.worktreeId}
            path={tab.path}
            tabKey={key}
          />
        </div>
      )}
      {mounted.has("diff") && (
        <div className={cn("h-full min-h-0", tab.mode !== "diff" && "hidden")}>
          <WorktreeDiffView issueId={issueId} worktreeId={tab.worktreeId} path={tab.path} />
        </div>
      )}
    </>
  );
}

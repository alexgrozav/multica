"use client";

import { Loader2, Terminal, X } from "lucide-react";
import {
  Tabs,
  TabsContent,
  TabsList,
  TabsTrigger,
} from "@multica/ui/components/ui/tabs";
import { RunLog } from "./run-log";
import { useWorktreeTabs } from "./worktree-tabs-context";
import { tabKey, type OpenTab } from "./tab-state";
import type { IssueWorktree } from "./types";

function repoLabel(url: string): string {
  let u = url
    .trim()
    .replace(/\/$/, "")
    .replace(/\.git$/, "");
  const i = Math.max(u.lastIndexOf("/"), u.lastIndexOf(":"));
  if (i >= 0) u = u.slice(i + 1);
  return u || url;
}

function isTabRunning(wt: IssueWorktree | undefined, t: OpenTab): boolean {
  if (!wt) return false;
  if (t.kind === "setup") return wt.setup_status === "running";
  return wt.runs.find((r) => r.name === t.name)?.status === "running";
}

function channelId(
  wt: IssueWorktree | undefined,
  t: OpenTab,
): string | undefined {
  if (!wt) return undefined;
  if (t.kind === "setup") return wt.setup_task_id;
  return wt.runs.find((r) => r.name === t.name)?.run_task_id;
}

// WorktreeLogTabs is the bottom half of the issue side panel: one tab per open
// log channel. Setup tabs are seeded by default (one per repo with a Setup
// script); Run tabs are opened on demand per named run script. The tab content
// is the live streaming log for that channel.
export function WorktreeLogTabs() {
  const { openTabs, worktrees, activeKey, setActive, closeTab } =
    useWorktreeTabs();

  if (openTabs.length === 0) {
    return (
      <div className="flex h-full flex-col items-center justify-center gap-1.5 border-l p-4 text-center text-[11px] text-muted-foreground">
        <Terminal className="h-4 w-4 opacity-60" />
        <span>No logs open.</span>
        <span className="opacity-70">
          Run a script, or click Setup in the list above.
        </span>
      </div>
    );
  }

  return (
    <Tabs
      value={activeKey ?? undefined}
      onValueChange={(v) => setActive(v as string)}
      className="flex h-full min-h-0 flex-col gap-0 border-l"
    >
      <TabsList
        variant="line"
        className="h-8 w-full shrink-0 justify-start gap-1 overflow-x-auto rounded-none border-b border-t px-2"
      >
        {openTabs.map((t) => {
          const wt = worktrees.find((w) => w.id === t.worktreeId);
          const key = tabKey(t.worktreeId, t.kind, t.name);
          const running = isTabRunning(wt, t);
          return (
            <TabsTrigger
              key={key}
              value={key}
              className="h-[25px] flex-none gap-1.5 whitespace-nowrap px-2 text-[11px]"
            >
              <span className="truncate font-mono">
                {repoLabel(wt?.repo_url ?? "")}
              </span>
              <span className="text-muted-foreground/70">
                {t.kind === "setup" ? "setup" : t.name}
              </span>
              {running && (
                <Loader2 className="h-3 w-3 animate-spin text-info" />
              )}
              <span
                role="button"
                aria-label="Close tab"
                tabIndex={-1}
                onPointerDown={(e) => e.stopPropagation()}
                onClick={(e) => {
                  e.stopPropagation();
                  e.preventDefault();
                  closeTab(t.worktreeId, t.kind, t.name);
                }}
                className="-mr-1 ml-0.5 inline-flex items-center justify-center rounded p-0.5 text-muted-foreground/60 transition-colors hover:bg-accent hover:text-foreground"
              >
                <X className="h-3 w-3" />
              </span>
            </TabsTrigger>
          );
        })}
      </TabsList>

      <div className="min-h-0 flex-1 p-2">
        {openTabs.map((t) => {
          const wt = worktrees.find((w) => w.id === t.worktreeId);
          const key = tabKey(t.worktreeId, t.kind, t.name);
          return (
            <TabsContent key={key} value={key} className="h-full min-h-0">
              <RunLog channelId={channelId(wt, t)} />
            </TabsContent>
          );
        })}
      </div>
    </Tabs>
  );
}

"use client";

import { Loader2, Plus, Terminal as TerminalIcon, X } from "lucide-react";
import {
  Tabs,
  TabsContent,
  TabsList,
  TabsTrigger,
} from "@multica/ui/components/ui/tabs";
import { RunLog } from "./run-log";
import { TerminalView } from "./terminal-view";
import { terminalTabLabel } from "./terminal-protocol";
import { useWorktreeTabs } from "./worktree-tabs-context";
import { tabKey, type OpenTab } from "./tab-state";
import type { IssueTerminal, IssueWorktree } from "./types";

function repoLabel(url: string): string {
  let u = url
    .trim()
    .replace(/\/$/, "")
    .replace(/\.git$/, "");
  const i = Math.max(u.lastIndexOf("/"), u.lastIndexOf(":"));
  if (i >= 0) u = u.slice(i + 1);
  return u || url;
}

function isTabRunning(
  wt: IssueWorktree | undefined,
  term: IssueTerminal | undefined,
  t: OpenTab,
): boolean {
  if (t.kind === "terminal") return term?.status === "pending";
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
// channel. Setup tabs are seeded by default (one per repo with a Setup script);
// Run tabs open on demand per named run script; terminal tabs are interactive
// shells in the issue workspace, opened with the "+" button. Log tab content is
// the streamed log for that channel; terminal content is a live xterm view
// that stays mounted across tab switches (its buffer survives).
export function WorktreeLogTabs() {
  const {
    issueId,
    openTabs,
    worktrees,
    terminals,
    activeKey,
    setActive,
    createTerminal,
    creatingTerminal,
    closeTab,
  } = useWorktreeTabs();

  const newTerminalButton = (
    <button
      type="button"
      aria-label="New terminal"
      title="New terminal"
      disabled={creatingTerminal}
      onClick={createTerminal}
      className="inline-flex h-[25px] flex-none items-center justify-center rounded px-1.5 text-muted-foreground transition-colors hover:bg-accent hover:text-foreground disabled:pointer-events-none disabled:opacity-50"
    >
      {creatingTerminal ? (
        <Loader2 className="h-3.5 w-3.5 animate-spin" />
      ) : (
        <Plus className="h-3.5 w-3.5" />
      )}
    </button>
  );

  if (openTabs.length === 0) {
    return (
      <div className="flex h-full flex-col border-l">
        <div className="flex h-8 w-full shrink-0 items-center border-b border-t px-2">
          {newTerminalButton}
        </div>
        <div className="flex min-h-0 flex-1 flex-col items-center justify-center gap-1.5 p-4 text-center text-[11px] text-muted-foreground">
          <TerminalIcon className="h-4 w-4 opacity-60" />
          <span>No logs open.</span>
          <span className="opacity-70">
            Run a script, click Setup in the list above, or open a terminal
            with&nbsp;+.
          </span>
        </div>
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
          const term =
            t.kind === "terminal" ? terminals.find((x) => x.id === t.name) : undefined;
          const key = tabKey(t.worktreeId, t.kind, t.name);
          const running = isTabRunning(wt, term, t);
          return (
            <TabsTrigger
              key={key}
              value={key}
              className="h-[25px] flex-none gap-1.5 whitespace-nowrap px-2 text-[11px]"
            >
              {t.kind === "terminal" ? (
                <span className="truncate">{terminalTabLabel(term)}</span>
              ) : (
                <>
                  <span className="truncate font-mono">
                    {repoLabel(wt?.repo_url ?? "")}
                  </span>
                  <span className="text-muted-foreground/70">
                    {t.kind === "setup" ? "setup" : t.name}
                  </span>
                </>
              )}
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
        {newTerminalButton}
      </TabsList>

      <div className="min-h-0 flex-1 p-2">
        {openTabs.map((t) => {
          const wt = worktrees.find((w) => w.id === t.worktreeId);
          const key = tabKey(t.worktreeId, t.kind, t.name);
          if (t.kind === "terminal") {
            // keepMounted: the xterm buffer, scroll position and socket live
            // across tab switches.
            return (
              <TabsContent key={key} value={key} keepMounted className="h-full min-h-0">
                <TerminalView issueId={issueId} terminalId={t.name ?? ""} />
              </TabsContent>
            );
          }
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

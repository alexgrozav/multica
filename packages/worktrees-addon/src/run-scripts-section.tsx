"use client";

import { useEffect, useRef, useState } from "react";
import { AlertCircle, ChevronRight, Loader2, Play, Square, Terminal } from "lucide-react";
import { Button } from "@multica/ui/components/ui/button";
import { useIssueWorktrees, useRunWorktreeScript, useStopWorktreeScript, useWorktreeRunLog } from "./queries";
import { useWorktreeRealtime } from "./use-worktree-realtime";
import type { IssueWorktree } from "./types";

interface RunScriptsSectionProps {
  issueId: string;
}

function repoLabel(url: string): string {
  let u = url.trim().replace(/\/$/, "").replace(/\.git$/, "");
  const i = Math.max(u.lastIndexOf("/"), u.lastIndexOf(":"));
  if (i >= 0) u = u.slice(i + 1);
  return u || url;
}

function StatusPill({ worktree }: { worktree: IssueWorktree }) {
  const { status, setup_status } = worktree;
  let tone = "text-muted-foreground";
  let label: string = status;
  switch (status) {
    case "ready":
      tone = setup_status === "failed" ? "text-warning" : "text-success";
      label = setup_status === "failed" ? "ready (setup failed)" : "ready";
      break;
    case "error":
      tone = "text-destructive";
      break;
    case "pending":
    case "initializing":
      tone = "text-warning";
      break;
    case "cleaning":
      tone = "text-muted-foreground";
      break;
  }
  return <span className={`shrink-0 text-[11px] ${tone}`}>{label}</span>;
}

// Single repo worktree row: status, Run/Stop, and an expandable live log.
function WorktreeRow({ worktree, issueId }: { worktree: IssueWorktree; issueId: string }) {
  const run = useRunWorktreeScript(issueId);
  const stop = useStopWorktreeScript(issueId);
  const [logOpen, setLogOpen] = useState(false);

  const isRunning = worktree.run_status === "running";
  const canRun = worktree.status === "ready" && worktree.has_run_script && !isRunning;
  const hasLog = !!worktree.run_task_id;

  return (
    <div className="rounded px-1 py-1.5">
      <div className="flex items-center gap-2">
        <span className="min-w-0 flex-1 truncate font-mono text-xs text-muted-foreground" title={worktree.repo_url}>
          {repoLabel(worktree.repo_url)}
        </span>
        <StatusPill worktree={worktree} />
        {isRunning ? (
          <Button
            variant="ghost"
            size="sm"
            className="h-6 px-2 text-destructive hover:text-destructive"
            disabled={stop.isPending}
            onClick={() => stop.mutate(worktree.id)}
          >
            {stop.isPending ? <Loader2 className="h-3 w-3 animate-spin" /> : <Square className="h-3 w-3" />}
            Stop
          </Button>
        ) : (
          <Button
            variant="ghost"
            size="sm"
            className="h-6 px-2"
            disabled={!canRun || run.isPending}
            title={worktree.has_run_script ? undefined : "No run script configured"}
            onClick={() => run.mutate(worktree.id)}
          >
            {run.isPending ? <Loader2 className="h-3 w-3 animate-spin" /> : <Play className="h-3 w-3" />}
            Run
          </Button>
        )}
      </div>

      {worktree.last_error && worktree.status === "error" && (
        <div className="mt-1 flex items-start gap-1 pl-1 text-[11px] text-destructive">
          <AlertCircle className="mt-0.5 h-3 w-3 shrink-0" />
          <span className="min-w-0 break-words">{worktree.last_error}</span>
        </div>
      )}

      {hasLog && (
        <>
          <button
            type="button"
            onClick={() => setLogOpen((v) => !v)}
            className="mt-1 flex items-center gap-1 rounded px-1 py-0.5 text-[11px] text-muted-foreground transition-colors hover:text-foreground"
          >
            <ChevronRight className={`!size-3 stroke-[2.5] transition-transform ${logOpen ? "rotate-90" : ""}`} />
            <Terminal className="h-3 w-3" />
            {isRunning ? "Live output" : "Last run output"}
          </button>
          {logOpen && <RunLog runTaskId={worktree.run_task_id!} />}
        </>
      )}
    </div>
  );
}

function RunLog({ runTaskId }: { runTaskId: string }) {
  const { data: lines = [] } = useWorktreeRunLog(runTaskId);
  const ref = useRef<HTMLPreElement>(null);

  useEffect(() => {
    const el = ref.current;
    if (el) el.scrollTop = el.scrollHeight;
  }, [lines.length]);

  if (lines.length === 0) {
    return <div className="ml-4 mt-1 text-[11px] italic text-muted-foreground">No output yet.</div>;
  }
  return (
    <pre
      ref={ref}
      className="ml-4 mt-1 max-h-48 overflow-auto rounded bg-muted/60 p-2 font-mono text-[11px] leading-snug text-muted-foreground"
    >
      {lines.map((l) => (
        <div key={l.seq} className={l.stream === "stderr" ? "text-destructive/90" : undefined}>
          {l.content}
        </div>
      ))}
    </pre>
  );
}

// Sidebar section that lists this issue's repository worktrees with their
// Setup status and on-demand Run/Stop controls + streaming logs. Hidden when
// the issue has no worktrees (auto-init off, or none created yet).
export function RunScriptsSection({ issueId }: RunScriptsSectionProps) {
  const { data: worktrees = [] } = useIssueWorktrees(issueId);
  useWorktreeRealtime(issueId);
  const [open, setOpen] = useState(true);

  if (worktrees.length === 0) return null;

  const runningCount = worktrees.filter((w) => w.run_status === "running").length;

  return (
    <div>
      <button
        type="button"
        className={`mb-2 flex w-full items-center gap-1 rounded-md px-2 py-1 text-xs font-medium transition-colors hover:bg-accent/70 ${
          open ? "" : "text-muted-foreground hover:text-foreground"
        }`}
        onClick={() => setOpen(!open)}
      >
        Run / Scripts
        <ChevronRight
          className={`!size-3 shrink-0 stroke-[2.5] text-muted-foreground transition-transform ${open ? "rotate-90" : ""}`}
        />
        {runningCount > 0 && (
          <span className="ml-auto inline-flex items-center gap-1 text-info">
            <span className="h-1.5 w-1.5 rounded-full bg-info animate-pulse" />
            <span className="font-mono tabular-nums">{runningCount}</span>
          </span>
        )}
      </button>
      {open && (
        <div className="space-y-0.5 pl-2">
          {worktrees.map((w) => (
            <WorktreeRow key={w.id} worktree={w} issueId={issueId} />
          ))}
        </div>
      )}
    </div>
  );
}

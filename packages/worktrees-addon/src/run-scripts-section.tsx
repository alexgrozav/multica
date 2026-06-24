"use client";

import { useEffect, useRef, useState } from "react";
import {
  AlertCircle,
  CheckCircle2,
  ChevronRight,
  Loader2,
  Minus,
  Play,
  RotateCcw,
  Square,
  Terminal,
  XCircle,
} from "lucide-react";
import { Button } from "@multica/ui/components/ui/button";
import {
  useIssueWorktrees,
  useRunWorktreeScript,
  useRunWorktreeSetup,
  useStopWorktreeScript,
  useWorktreeRunLog,
} from "./queries";
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

function StatusPill({ status }: { status: string }) {
  let tone = "text-muted-foreground";
  if (status === "ready") tone = "text-success";
  else if (status === "error") tone = "text-destructive";
  else if (status === "pending" || status === "initializing") tone = "text-warning";
  return <span className={`shrink-0 text-[11px] ${tone}`}>{status}</span>;
}

// A small status glyph + label shared by the Setup and Run script lines.
function ScriptStatus({ status, busy }: { status: string; busy: boolean }) {
  if (busy || status === "running") {
    return (
      <span className="inline-flex items-center gap-1 text-[11px] text-info">
        <Loader2 className="h-3 w-3 animate-spin" /> running
      </span>
    );
  }
  switch (status) {
    case "succeeded":
      return (
        <span className="inline-flex items-center gap-1 text-[11px] text-success">
          <CheckCircle2 className="h-3 w-3" /> ok
        </span>
      );
    case "failed":
      return (
        <span className="inline-flex items-center gap-1 text-[11px] text-destructive">
          <XCircle className="h-3 w-3" /> failed
        </span>
      );
    case "stopped":
      return <span className="text-[11px] text-muted-foreground">stopped</span>;
    default:
      return (
        <span className="inline-flex items-center gap-1 text-[11px] text-muted-foreground/70">
          <Minus className="h-3 w-3" /> not run
        </span>
      );
  }
}

// One repo worktree: overall status, then Setup + Run as peer script lines
// (each with its own status + control), and a shared streaming log.
function WorktreeRow({ worktree, issueId }: { worktree: IssueWorktree; issueId: string }) {
  const setup = useRunWorktreeSetup(issueId);
  const run = useRunWorktreeScript(issueId);
  const stop = useStopWorktreeScript(issueId);
  const [logOpen, setLogOpen] = useState(false);

  const isRunning = worktree.run_status === "running";
  const isSettingUp = worktree.setup_status === "running";
  const busy = isRunning || isSettingUp;
  const ready = worktree.status === "ready" || worktree.status === "error";
  const canSetup = ready && worktree.has_setup_script && !busy;
  const canRun = worktree.status === "ready" && worktree.has_run_script && !busy;
  const hasLog = !!worktree.run_task_id;

  return (
    <div className="rounded px-1 py-1.5">
      <div className="flex items-center gap-2">
        <span className="min-w-0 flex-1 truncate font-mono text-xs text-muted-foreground" title={worktree.repo_url}>
          {repoLabel(worktree.repo_url)}
        </span>
        <StatusPill status={worktree.status} />
      </div>

      {worktree.has_setup_script && (
        <div className="mt-1 flex items-center gap-2 pl-1">
          <span className="w-10 shrink-0 text-[11px] font-medium text-muted-foreground">Setup</span>
          <ScriptStatus status={worktree.setup_status} busy={isSettingUp || setup.isPending} />
          <Button
            variant="ghost"
            size="sm"
            className="ml-auto h-6 px-2"
            disabled={!canSetup || setup.isPending}
            onClick={() => setup.mutate(worktree.id)}
          >
            {isSettingUp || setup.isPending ? <Loader2 className="h-3 w-3 animate-spin" /> : <RotateCcw className="h-3 w-3" />}
            Re-run
          </Button>
        </div>
      )}

      {worktree.has_run_script && (
        <div className="mt-1 flex items-center gap-2 pl-1">
          <span className="w-10 shrink-0 text-[11px] font-medium text-muted-foreground">Run</span>
          <ScriptStatus status={worktree.run_status} busy={isRunning} />
          {isRunning ? (
            <Button
              variant="ghost"
              size="sm"
              className="ml-auto h-6 px-2 text-destructive hover:text-destructive"
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
              className="ml-auto h-6 px-2"
              disabled={!canRun || run.isPending}
              onClick={() => run.mutate(worktree.id)}
            >
              {run.isPending ? <Loader2 className="h-3 w-3 animate-spin" /> : <Play className="h-3 w-3" />}
              Run
            </Button>
          )}
        </div>
      )}

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
            {busy ? "Live output" : "Last output"}
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

// Sidebar section: lists this issue's repository worktrees with Setup + Run
// status and controls + streaming logs. Hidden when the issue has no worktrees.
export function RunScriptsSection({ issueId }: RunScriptsSectionProps) {
  const { data: worktrees = [] } = useIssueWorktrees(issueId);
  useWorktreeRealtime(issueId);
  const [open, setOpen] = useState(true);

  if (worktrees.length === 0) return null;

  const activeCount = worktrees.filter(
    (w) => w.run_status === "running" || w.setup_status === "running",
  ).length;

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
        {activeCount > 0 && (
          <span className="ml-auto inline-flex items-center gap-1 text-info">
            <span className="h-1.5 w-1.5 rounded-full bg-info animate-pulse" />
            <span className="font-mono tabular-nums">{activeCount}</span>
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

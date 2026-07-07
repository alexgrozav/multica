"use client";

import { useState } from "react";
import {
  AlertCircle,
  CheckCircle2,
  ChevronRight,
  Loader2,
  Minus,
  Play,
  RotateCcw,
  Square,
  XCircle,
} from "lucide-react";
import { Button } from "@multica/ui/components/ui/button";
import {
  useIssueWorktrees,
  useRunWorktreeScript,
  useRunWorktreeSetup,
  useStopWorktreeScript,
} from "./queries";
import { useWorktreeTabs } from "./worktree-tabs-context";
import type { IssueWorktree, WorktreeRunScript } from "./types";

interface RunScriptsSectionProps {
  issueId: string;
}

function repoLabel(url: string): string {
  let u = url
    .trim()
    .replace(/\/$/, "")
    .replace(/\.git$/, "");
  const i = Math.max(u.lastIndexOf("/"), u.lastIndexOf(":"));
  if (i >= 0) u = u.slice(i + 1);
  return u || url;
}

function StatusPill({ status }: { status: string }) {
  let tone = "text-muted-foreground";
  if (status === "ready") tone = "text-success";
  else if (status === "error") tone = "text-destructive";
  else if (status === "pending" || status === "initializing")
    tone = "text-warning";
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

// One named run script line: status + Run/Stop control. Clicking the label opens
// its log tab; running/stopping opens (and focuses) it too.
function RunLine({
  run,
  worktree,
  issueId,
  disabled,
}: {
  run: WorktreeRunScript;
  worktree: IssueWorktree;
  issueId: string;
  disabled: boolean;
}) {
  const runMut = useRunWorktreeScript(issueId);
  const stopMut = useStopWorktreeScript(issueId);
  const { openRun } = useWorktreeTabs();
  const isRunning = run.status === "running";

  return (
    <div className="mt-1 flex items-center gap-2 pl-1">
      <button
        type="button"
        aria-label={`Open ${run.name} logs`}
        onClick={() => openRun(worktree.id, run.name)}
        className="min-w-10 shrink-0 text-left font-mono text-[11px] font-medium text-muted-foreground transition-colors hover:text-foreground"
      >
        {run.name}
      </button>
      <ScriptStatus status={run.status} busy={isRunning} />
      {isRunning ? (
        <Button
          variant="ghost"
          size="sm"
          aria-label="Stop"
          className="ml-auto h-6 px-2 text-destructive hover:text-destructive"
          onClick={() =>
            stopMut.mutate({ worktreeId: worktree.id, name: run.name })
          }
        >
          <Square className="h-3 w-3" />
        </Button>
      ) : (
        <Button
          variant="ghost"
          size="sm"
          aria-label="Run"
          className="ml-auto h-6 px-2"
          disabled={disabled}
          onClick={() => {
            runMut.mutate({ worktreeId: worktree.id, name: run.name });
            openRun(worktree.id, run.name);
          }}
        >
          <Play className="h-3 w-3" />
        </Button>
      )}
    </div>
  );
}

// One repo worktree: overall status, then Setup + each named Run script as peer
// lines (each with its own status + control). Logs live in the bottom log viewer,
// opened from the controls here.
function WorktreeRow({
  worktree,
  issueId,
}: {
  worktree: IssueWorktree;
  issueId: string;
}) {
  const setup = useRunWorktreeSetup(issueId);
  const { openSetup } = useWorktreeTabs();

  const isSettingUp = worktree.setup_status === "running";
  const anyRunActive = worktree.runs.some((r) => r.status === "running");
  const ready = worktree.status === "ready" || worktree.status === "error";
  const canSetup = ready && worktree.has_setup && !isSettingUp && !anyRunActive;
  const canRun = worktree.status === "ready" && !isSettingUp;

  return (
    <div className="rounded px-1 py-1.5">
      <div className="flex items-center gap-2">
        <span
          className="min-w-0 flex-1 truncate font-mono text-xs text-muted-foreground"
          title={worktree.repo_url}
        >
          {repoLabel(worktree.repo_url)}
        </span>
        {worktree.identifier && (
          <span className="shrink-0 font-mono text-[10px] text-muted-foreground/70">
            {worktree.identifier}
          </span>
        )}
        <StatusPill status={worktree.status} />
      </div>

      {worktree.has_setup && (
        <div className="mt-1 flex items-center gap-2 pl-1">
          <button
            type="button"
            aria-label="Open setup logs"
            onClick={() => openSetup(worktree.id)}
            className="w-10 shrink-0 text-left text-[11px] font-medium text-muted-foreground transition-colors hover:text-foreground"
          >
            setup
          </button>
          <ScriptStatus
            status={worktree.setup_status}
            busy={isSettingUp || setup.isPending}
          />
          <Button
            variant="ghost"
            size="sm"
            aria-label="Run setup"
            className="ml-auto h-6 px-2"
            disabled={!canSetup || setup.isPending}
            onClick={() => {
              setup.mutate(worktree.id);
              openSetup(worktree.id);
            }}
          >
            {isSettingUp || setup.isPending ? (
              <Loader2 className="h-3 w-3 animate-spin" />
            ) : (
              <RotateCcw className="h-3 w-3" />
            )}
          </Button>
        </div>
      )}

      {worktree.runs.map((run) => (
        <RunLine
          key={run.name}
          run={run}
          worktree={worktree}
          issueId={issueId}
          disabled={!canRun}
        />
      ))}

      {worktree.last_error && worktree.status === "error" && (
        <div className="mt-1 flex items-start gap-1 pl-1 text-[11px] text-destructive">
          <AlertCircle className="mt-0.5 h-3 w-3 shrink-0" />
          <span className="min-w-0 break-words">{worktree.last_error}</span>
        </div>
      )}
    </div>
  );
}

// Sidebar section: lists this issue's repository worktrees with Setup + Run
// status and controls. Streaming logs live in the bottom log-tab viewer
// (WorktreeLogTabs), opened from the controls here. Hidden when the issue has no
// worktrees. Realtime + tab state are owned by WorktreeTabsProvider (mounted by
// WorktreeSidebarLayout), so this component is purely the list.
export function RunScriptsSection({ issueId }: RunScriptsSectionProps) {
  const { data: worktrees = [] } = useIssueWorktrees(issueId);
  const [open, setOpen] = useState(true);

  if (worktrees.length === 0) return null;

  const activeCount = worktrees.filter(
    (w) =>
      w.setup_status === "running" ||
      w.runs.some((r) => r.status === "running"),
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

"use client";

import { ChevronRight, GitCompareArrows, Loader2, SquareDot, SquareMinus, SquarePlus } from "lucide-react";
import { useMemo, useState } from "react";
import { Skeleton } from "@multica/ui/components/ui/skeleton";
import { cn } from "@multica/ui/lib/utils";
import { useIssueFileTabs } from "./issue-file-tabs-context";
import { useIssueWorktreeChanges, useIssueWorktrees } from "./queries";
import { repoLabel } from "./repo-label";
import type { IssueWorktree, WorktreeChangeEntry, WorktreeChanges } from "./types";

// ChangesList is the Changes tab's content: the live git status of each repo
// worktree of the issue's workspace — every file the branch changed vs its
// base (committed + uncommitted), fed by the daemon's status scans. Clicking a
// row opens that file's diff in the content area's tabs (a no-op outside the
// IssueFileTabsProvider). With multiple repos each gets a collapsible section;
// a single repo renders its rows flat, like the screenshot.

// sortChangeEntries orders entries alphabetically by path — case-insensitive
// (byte order would put every uppercase path before every lowercase one), with
// a plain-compare tie-break so paths differing only by case stay deterministic.
// The daemon reports sorted lists, but the UI re-sorts defensively so the
// presentation never depends on the backend version (API-compat rule).
export function sortChangeEntries(files: WorktreeChangeEntry[]): WorktreeChangeEntry[] {
  return [...files].sort((a, b) => {
    const la = a.path.toLowerCase();
    const lb = b.path.toLowerCase();
    if (la !== lb) return la < lb ? -1 : 1;
    return a.path < b.path ? -1 : a.path > b.path ? 1 : 0;
  });
}

// statusIcon maps a change status onto its marker (shape + color), VS Code
// style: added → green plus, deleted → red minus, anything else → amber dot.
function StatusIcon({ status }: { status: string }) {
  switch (status) {
    case "added":
      return <SquarePlus aria-label="Added" className="h-3.5 w-3.5 shrink-0 text-success" />;
    case "deleted":
      return <SquareMinus aria-label="Deleted" className="h-3.5 w-3.5 shrink-0 text-destructive" />;
    default:
      return <SquareDot aria-label="Modified" className="h-3.5 w-3.5 shrink-0 text-warning" />;
  }
}

function ChangeRow({
  entry,
  onClick,
}: {
  entry: WorktreeChangeEntry;
  onClick: () => void;
}) {
  const i = entry.path.lastIndexOf("/");
  const dir = i >= 0 ? entry.path.slice(0, i + 1) : "";
  const name = i >= 0 ? entry.path.slice(i + 1) : entry.path;
  return (
    <button
      type="button"
      title={entry.path}
      onClick={onClick}
      className="flex w-full items-center gap-1.5 rounded px-1 py-[3px] text-xs transition-colors hover:bg-accent/50"
    >
      <span className="min-w-0 flex-1 truncate text-left" dir="rtl">
        {/* rtl truncation keeps the filename end visible; bdi restores LTR text order. */}
        <bdi>
          {dir && <span className="text-muted-foreground">{dir}</span>}
          <span
            className={cn(
              "text-foreground",
              entry.status === "deleted" && "line-through opacity-70",
            )}
          >
            {name}
          </span>
        </bdi>
      </span>
      {entry.uncommitted === true && (
        <span
          title="Has uncommitted changes"
          className="shrink-0 text-[10px] font-semibold text-muted-foreground"
        >
          U
        </span>
      )}
      {!entry.binary && entry.additions > 0 && (
        <span className="shrink-0 font-mono text-[10px] tabular-nums text-success">
          +{entry.additions}
        </span>
      )}
      {!entry.binary && entry.deletions > 0 && (
        <span className="shrink-0 font-mono text-[10px] tabular-nums text-destructive">
          −{entry.deletions}
        </span>
      )}
      <StatusIcon status={entry.status} />
    </button>
  );
}

// waitingLabel describes why a worktree has no git status yet.
function waitingLabel(status: string): string {
  switch (status) {
    case "pending":
    case "initializing":
      return "Checking out…";
    case "cleaning":
      return "Cleaning up…";
    case "error":
      return "Checkout failed";
    default:
      return "Scanning for changes…";
  }
}

function WaitingHint({ worktree }: { worktree: IssueWorktree }) {
  return (
    <span className="inline-flex items-center gap-1 px-1 py-0.5 text-[11px] text-muted-foreground">
      {worktree.status !== "error" && <Loader2 className="h-3 w-3 animate-spin" />}
      {waitingLabel(worktree.status)}
    </span>
  );
}

function ChangeRows({
  worktree,
  changes,
}: {
  worktree: IssueWorktree;
  changes: WorktreeChanges | undefined;
}) {
  const { openDiff } = useIssueFileTabs();
  // TanStack's structural sharing keeps `files` referentially stable across
  // refetches that carry the same list, so an unchanged list is not re-sorted.
  const files = changes?.files;
  const sorted = useMemo(() => sortChangeEntries(files ?? []), [files]);
  if (!changes) return <WaitingHint worktree={worktree} />;
  return (
    <>
      {changes.truncated && (
        <div className="px-1 py-0.5 text-[10px] text-warning">
          Large change set — showing the first {sorted.length.toLocaleString()} files.
        </div>
      )}
      {sorted.length === 0 ? (
        <div className="px-1 py-1 text-[11px] text-muted-foreground">No changes yet.</div>
      ) : (
        sorted.map((f) => (
          <ChangeRow key={f.path} entry={f} onClick={() => openDiff(worktree.id, f.path)} />
        ))
      )}
    </>
  );
}

// RepoChanges is one repo's collapsible section (multi-repo issues only).
function RepoChanges({
  worktree,
  changes,
}: {
  worktree: IssueWorktree;
  changes: WorktreeChanges | undefined;
}) {
  const [open, setOpen] = useState(true);
  return (
    <div>
      <button
        type="button"
        onClick={() => setOpen(!open)}
        className="flex w-full items-center gap-1.5 rounded-md px-1 py-1 text-xs font-medium transition-colors hover:bg-accent/70"
        title={changes?.base ? `${worktree.repo_url} — diff vs ${changes.base}` : worktree.repo_url}
      >
        <ChevronRight
          className={`h-3 w-3 shrink-0 stroke-[2.5] text-muted-foreground transition-transform ${open ? "rotate-90" : ""}`}
        />
        <span className="truncate font-mono">{repoLabel(worktree.repo_url)}</span>
        {changes && (
          <span className="ml-auto shrink-0 text-[10px] tabular-nums text-muted-foreground">
            {changes.files.length}
          </span>
        )}
      </button>
      {open && (
        <div className="pl-1">
          <ChangeRows worktree={worktree} changes={changes} />
        </div>
      )}
    </div>
  );
}

export function ChangesList({ issueId }: { issueId: string }) {
  const { data: worktrees = [], isLoading: worktreesLoading } = useIssueWorktrees(issueId);
  const { data: changeLists = [] } = useIssueWorktreeChanges(issueId);

  if (worktreesLoading && worktrees.length === 0) {
    return (
      <div className="space-y-2 px-1 py-2">
        <Skeleton className="h-4 w-2/3" />
        <Skeleton className="h-4 w-1/2" />
        <Skeleton className="h-4 w-3/5" />
      </div>
    );
  }

  if (worktrees.length === 0) {
    return (
      <div className="flex flex-col items-center gap-1.5 px-4 py-10 text-center text-xs text-muted-foreground">
        <GitCompareArrows className="h-4 w-4 opacity-60" />
        <span>No workspace for this issue.</span>
        <span className="opacity-70">
          Changes appear here once the issue is checked out on a connected machine.
        </span>
      </div>
    );
  }

  if (worktrees.length === 1) {
    const wt = worktrees[0]!;
    return (
      <div>
        <ChangeRows worktree={wt} changes={changeLists.find((c) => c.worktree_id === wt.id)} />
      </div>
    );
  }

  return (
    <div className="space-y-2">
      {worktrees.map((wt) => (
        <RepoChanges
          key={wt.id}
          worktree={wt}
          changes={changeLists.find((c) => c.worktree_id === wt.id)}
        />
      ))}
    </div>
  );
}

"use client";

import { memo, useMemo, useState } from "react";
import {
  ChevronRight,
  File,
  Folder,
  FolderOpen,
  FolderTree,
  Loader2,
} from "lucide-react";
import { Skeleton } from "@multica/ui/components/ui/skeleton";
import { useIssueFileTabs } from "./issue-file-tabs-context";
import { useIssueWorktreeFiles, useIssueWorktrees } from "./queries";
import { repoLabel } from "./repo-label";
import { buildTree, countFiles, type TreeDir } from "./tree-model";
import type { IssueWorktree, WorktreeFileList } from "./types";

// ProjectTree is the Project tab's content: one file tree per repo worktree of
// the issue's workspace, fed by the daemon's file-list scans. Rendering is
// lazy — a directory's children mount only while it is expanded — so even a
// 20k-file list stays cheap. Expansion state lives here (per mount) and
// survives tab switches because IssueSidebarTabs keeps the tab mounted once
// activated. Clicking a file opens it in the content area's file tabs (a
// no-op outside the IssueFileTabsProvider).

const INDENT_PX = 14;

function DirRow({
  dir,
  depth,
  expanded,
  onToggle,
  onFileClick,
}: {
  dir: TreeDir;
  depth: number;
  expanded: ReadonlySet<string>;
  onToggle: (path: string) => void;
  onFileClick: (path: string) => void;
}) {
  const isOpen = expanded.has(dir.path);
  const FolderIcon = isOpen ? FolderOpen : Folder;
  return (
    <>
      <button
        type="button"
        onClick={() => onToggle(dir.path)}
        title={dir.path}
        className="flex w-full items-center gap-1.5 rounded px-1 py-[3px] text-xs text-foreground/90 transition-colors hover:bg-accent/50"
        style={{ paddingLeft: depth * INDENT_PX + 4 }}
      >
        <ChevronRight
          className={`h-3 w-3 shrink-0 text-muted-foreground transition-transform ${isOpen ? "rotate-90" : ""}`}
        />
        <FolderIcon className="h-3.5 w-3.5 shrink-0 text-muted-foreground" />
        <span className="truncate">{dir.name}</span>
      </button>
      {isOpen && (
        <DirChildren
          dir={dir}
          depth={depth + 1}
          expanded={expanded}
          onToggle={onToggle}
          onFileClick={onFileClick}
        />
      )}
    </>
  );
}

const DirChildren = memo(function DirChildren({
  dir,
  depth,
  expanded,
  onToggle,
  onFileClick,
}: {
  dir: TreeDir;
  depth: number;
  expanded: ReadonlySet<string>;
  onToggle: (path: string) => void;
  onFileClick: (path: string) => void;
}) {
  return (
    <>
      {dir.dirs.map((d) => (
        <DirRow
          key={d.path}
          dir={d}
          depth={depth}
          expanded={expanded}
          onToggle={onToggle}
          onFileClick={onFileClick}
        />
      ))}
      {dir.files.map((f) => {
        const full = dir.path === "" ? f : `${dir.path}/${f}`;
        return (
          <button
            key={f}
            type="button"
            title={full}
            onClick={() => onFileClick(full)}
            className="flex w-full items-center gap-1.5 rounded px-1 py-[3px] text-xs text-muted-foreground transition-colors hover:bg-accent/50 hover:text-foreground"
            style={{ paddingLeft: depth * INDENT_PX + 4 + 18 }}
          >
            <File className="h-3.5 w-3.5 shrink-0 opacity-70" />
            <span className="truncate">{f}</span>
          </button>
        );
      })}
    </>
  );
});

// waitingLabel describes why a worktree has no file list yet.
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
      return "Indexing files…";
  }
}

function RepoTree({
  worktree,
  files,
}: {
  worktree: IssueWorktree;
  files: WorktreeFileList | undefined;
}) {
  const { openFile } = useIssueFileTabs();
  const [rootOpen, setRootOpen] = useState(true);
  const [expanded, setExpanded] = useState<ReadonlySet<string>>(() => new Set<string>());
  const toggle = (path: string) =>
    setExpanded((prev) => {
      const next = new Set(prev);
      if (next.has(path)) next.delete(path);
      else next.add(path);
      return next;
    });
  const handleFileClick = (path: string) => openFile(worktree.id, path);

  // TanStack's structural sharing keeps `paths` referentially stable across
  // refetches that carry the same list, so an unchanged tree is not rebuilt.
  const paths = files?.paths;
  const tree = useMemo(() => buildTree(paths ?? []), [paths]);
  const total = useMemo(() => countFiles(tree), [tree]);

  return (
    <div>
      <button
        type="button"
        onClick={() => setRootOpen(!rootOpen)}
        className="flex w-full items-center gap-1.5 rounded-md px-1 py-1 text-xs font-medium transition-colors hover:bg-accent/70"
        title={worktree.repo_url}
      >
        <ChevronRight
          className={`h-3 w-3 shrink-0 stroke-[2.5] text-muted-foreground transition-transform ${rootOpen ? "rotate-90" : ""}`}
        />
        <span className="truncate font-mono">{repoLabel(worktree.repo_url)}</span>
        {files ? (
          <span className="ml-auto shrink-0 text-[10px] tabular-nums text-muted-foreground">
            {total} files
          </span>
        ) : (
          <span className="ml-auto inline-flex shrink-0 items-center gap-1 text-[10px] text-muted-foreground">
            {worktree.status !== "error" && <Loader2 className="h-3 w-3 animate-spin" />}
            {waitingLabel(worktree.status)}
          </span>
        )}
      </button>
      {rootOpen && files && (
        <div className="pl-1">
          {files.truncated && (
            <div className="px-1 py-0.5 text-[10px] text-warning">
              Large workspace — showing the first {files.paths.length.toLocaleString()} files.
            </div>
          )}
          {total === 0 ? (
            <div className="px-1 py-1 text-[11px] text-muted-foreground">No files.</div>
          ) : (
            <DirChildren
              dir={tree}
              depth={1}
              expanded={expanded}
              onToggle={toggle}
              onFileClick={handleFileClick}
            />
          )}
        </div>
      )}
    </div>
  );
}

export function ProjectTree({ issueId }: { issueId: string }) {
  const { data: worktrees = [], isLoading: worktreesLoading } = useIssueWorktrees(issueId);
  const { data: fileLists = [] } = useIssueWorktreeFiles(issueId);

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
        <FolderTree className="h-4 w-4 opacity-60" />
        <span>No workspace for this issue.</span>
        <span className="opacity-70">
          Files appear here once the issue is checked out on a connected machine.
        </span>
      </div>
    );
  }

  return (
    <div className="space-y-2">
      {worktrees.map((wt) => (
        <RepoTree
          key={wt.id}
          worktree={wt}
          files={fileLists.find((f) => f.worktree_id === wt.id)}
        />
      ))}
    </div>
  );
}

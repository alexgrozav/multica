"use client";

import {
  useDeferredValue,
  useEffect,
  useMemo,
  useState,
  type ReactNode,
} from "react";
import { Command as CommandPrimitive } from "cmdk";
import { File, FileCode, Loader2, SearchIcon } from "lucide-react";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@multica/ui/components/ui/dialog";
import { useIssueFileTabs } from "./issue-file-tabs-context";
import { fuzzyMatch, sliceRanges } from "./fuzzy";
import { useIssueWorktreeFiles, useIssueWorktrees } from "./queries";
import { repoLabel } from "./repo-label";
import type { IssueWorktree } from "./types";

// FileSearchPalette is the issue view's Cmd+P / Ctrl+P quick-open: fuzzy
// search over every file of the issue's workspace (all repo worktrees), with
// the matched character groups highlighted; picking a file opens it in the
// content file tabs. Mounted by WorktreeFileTabs, so it exists exactly while
// an issue detail view does.
//
// The shortcut only engages once the issue has a workspace — without one the
// browser keeps its native Cmd+P. The dialog subtree (and its file-list
// fetch) mounts lazily on first open. The keydown listener lives in an
// effect on purpose: the desktop app keeps hidden tabs mounted inside
// <Activity mode="hidden">, which tears effects down while hidden, so only
// the visible issue view ever handles the shortcut.

const MAX_RESULTS = 50;

const ITEM_CLASS =
  "flex cursor-default select-none items-center gap-2.5 rounded-lg px-3 py-2 text-sm outline-none data-[disabled=true]:pointer-events-none data-[disabled=true]:opacity-50 data-selected:bg-accent";

interface Candidate {
  worktreeId: string;
  path: string;
  repoUrl: string;
}

interface ScoredCandidate extends Candidate {
  score: number;
  ranges: Array<[number, number]>;
}

// Matched-range renderer, mirroring the host search's HighlightText marks.
function Highlighted({
  text,
  ranges,
}: {
  text: string;
  ranges: ReadonlyArray<readonly [number, number]>;
}) {
  if (ranges.length === 0) return <>{text}</>;
  const parts: ReactNode[] = [];
  let last = 0;
  for (const [s, e] of ranges) {
    if (s > last) parts.push(text.slice(last, s));
    parts.push(
      <mark key={s} className="rounded-sm bg-yellow-200 text-inherit dark:bg-yellow-900/60">
        {text.slice(s, e)}
      </mark>,
    );
    last = e;
  }
  if (last < text.length) parts.push(text.slice(last));
  return <>{parts}</>;
}

// One result row: basename first (like an editor quick-open), directory after
// in muted text, repo label when the issue spans several repos. The highlight
// ranges are over the full path and get split at the basename boundary.
function FileRow({
  path,
  ranges,
  repo,
  onSelect,
  value,
  icon,
}: {
  path: string;
  ranges: ReadonlyArray<readonly [number, number]>;
  repo: string | null;
  onSelect: () => void;
  value: string;
  icon: "file" | "open";
}) {
  const slash = path.lastIndexOf("/");
  const base = path.slice(slash + 1);
  const dir = slash > 0 ? path.slice(0, slash) : "";
  const baseRanges = sliceRanges(ranges, slash + 1, path.length);
  const dirRanges = sliceRanges(ranges, 0, slash > 0 ? slash : 0);
  const Icon = icon === "open" ? FileCode : File;
  return (
    <CommandPrimitive.Item value={value} onSelect={onSelect} title={path} className={ITEM_CLASS}>
      <Icon className="size-4 shrink-0 text-muted-foreground" />
      <span className="min-w-0 flex-1 truncate">
        <Highlighted text={base} ranges={baseRanges} />
        {dir && (
          <span className="ml-2 text-xs text-muted-foreground">
            <Highlighted text={dir} ranges={dirRanges} />
          </span>
        )}
      </span>
      {repo && (
        <span className="ml-2 shrink-0 font-mono text-[10px] text-muted-foreground">{repo}</span>
      )}
    </CommandPrimitive.Item>
  );
}

function FileSearchDialog({
  issueId,
  worktrees,
  open,
  onOpenChange,
}: {
  issueId: string;
  worktrees: IssueWorktree[];
  open: boolean;
  onOpenChange: (open: boolean) => void;
}) {
  const { data: fileLists = [], isLoading } = useIssueWorktreeFiles(issueId, open);
  const { fileTabs, openFile } = useIssueFileTabs();
  const [query, setQuery] = useState("");
  const deferredQuery = useDeferredValue(query);

  // Close on single ESC — capture phase fires before base-ui Dialog's handlers.
  useEffect(() => {
    if (!open) return;
    const handleEsc = (e: KeyboardEvent) => {
      if (e.key === "Escape") {
        e.preventDefault();
        e.stopPropagation();
        onOpenChange(false);
      }
    };
    document.addEventListener("keydown", handleEsc, true);
    return () => document.removeEventListener("keydown", handleEsc, true);
  }, [open, onOpenChange]);

  // Fresh query on reopen (the dialog stays mounted after its first open).
  useEffect(() => {
    if (!open) setQuery("");
  }, [open]);

  const candidates = useMemo<Candidate[]>(() => {
    const live = new Set(worktrees.map((w) => w.id));
    const out: Candidate[] = [];
    for (const list of fileLists) {
      if (!live.has(list.worktree_id)) continue;
      for (const path of list.paths) {
        out.push({ worktreeId: list.worktree_id, path, repoUrl: list.repo_url });
      }
    }
    return out;
  }, [fileLists, worktrees]);

  const results = useMemo<ScoredCandidate[]>(() => {
    const q = deferredQuery.trim();
    if (!q) return [];
    const scored: ScoredCandidate[] = [];
    for (const c of candidates) {
      const m = fuzzyMatch(q, c.path);
      if (m) scored.push({ ...c, score: m.score, ranges: m.ranges });
    }
    scored.sort(
      (a, b) =>
        b.score - a.score ||
        a.path.length - b.path.length ||
        (a.path < b.path ? -1 : a.path > b.path ? 1 : 0),
    );
    return scored.slice(0, MAX_RESULTS);
  }, [candidates, deferredQuery]);

  const multiRepo = worktrees.length > 1;
  const repoOf = (c: Candidate) => (multiRepo ? repoLabel(c.repoUrl) : null);
  const pick = (worktreeId: string, path: string) => {
    openFile(worktreeId, path);
    onOpenChange(false);
  };

  // Empty-query state surfaces the already-open file tabs (quick switching),
  // like an editor's quick-open shows recent files.
  const openFileTabs = useMemo(
    () =>
      fileTabs.map((t) => {
        const wt = worktrees.find((w) => w.id === t.worktreeId);
        return { worktreeId: t.worktreeId, path: t.path, repoUrl: wt?.repo_url ?? "" };
      }),
    [fileTabs, worktrees],
  );

  const showEmptyQuery = !deferredQuery.trim();
  // "Indexing" only while some non-failed worktree's list hasn't arrived yet;
  // arrived-but-empty lists mean the workspace really has no files.
  const listArrived = new Set(fileLists.map((f) => f.worktree_id));
  const stillIndexing =
    candidates.length === 0 &&
    (isLoading || worktrees.some((w) => w.status !== "error" && !listArrived.has(w.id)));
  const truncated = fileLists.some((f) => f.truncated === true);

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent
        finalFocus={false}
        className="top-[20%] translate-y-0 overflow-hidden rounded-xl! p-0 sm:max-w-xl!"
        showCloseButton={false}
      >
        <DialogHeader className="sr-only">
          <DialogTitle>Search files</DialogTitle>
          <DialogDescription>
            Fuzzy-search the files of this issue&apos;s workspace by name.
          </DialogDescription>
        </DialogHeader>
        <CommandPrimitive
          shouldFilter={false}
          className="flex size-full flex-col overflow-hidden rounded-xl bg-popover text-popover-foreground"
        >
          <div className="flex items-center gap-3 border-b px-4 py-3">
            <SearchIcon className="size-5 shrink-0 text-muted-foreground" />
            <CommandPrimitive.Input
              placeholder="Search files by name…"
              value={query}
              onValueChange={setQuery}
              className="flex-1 bg-transparent text-sm outline-none placeholder:text-muted-foreground"
            />
            <kbd className="hidden shrink-0 rounded bg-muted px-1.5 py-0.5 text-[10px] font-medium text-muted-foreground sm:inline">
              ESC
            </kbd>
          </div>

          <CommandPrimitive.List className="max-h-[min(400px,50vh)] overflow-x-hidden overflow-y-auto">
            {candidates.length === 0 ? (
              <div className="flex items-center justify-center gap-2 py-10 text-sm text-muted-foreground">
                {stillIndexing ? (
                  <>
                    <Loader2 className="size-4 animate-spin" />
                    <span>Indexing workspace files…</span>
                  </>
                ) : (
                  <span>No files in this workspace.</span>
                )}
              </div>
            ) : showEmptyQuery ? (
              openFileTabs.length > 0 ? (
                <CommandPrimitive.Group className="p-2">
                  <div className="px-3 py-1.5 text-xs font-medium text-muted-foreground">
                    Open files
                  </div>
                  {openFileTabs.map((t) => (
                    <FileRow
                      key={`open:${t.worktreeId}:${t.path}`}
                      value={`open:${t.worktreeId}:${t.path}`}
                      path={t.path}
                      ranges={[]}
                      repo={multiRepo ? repoLabel(t.repoUrl) : null}
                      icon="open"
                      onSelect={() => pick(t.worktreeId, t.path)}
                    />
                  ))}
                </CommandPrimitive.Group>
              ) : (
                <div className="px-5 py-8 text-center text-xs text-muted-foreground">
                  Type to search {candidates.length.toLocaleString()} files…
                </div>
              )
            ) : results.length === 0 ? (
              <CommandPrimitive.Empty className="py-10 text-center text-sm text-muted-foreground">
                No files match “{deferredQuery.trim()}”.
              </CommandPrimitive.Empty>
            ) : (
              <CommandPrimitive.Group className="p-2">
                {results.map((r) => (
                  <FileRow
                    key={`${r.worktreeId}:${r.path}`}
                    value={`${r.worktreeId}:${r.path}`}
                    path={r.path}
                    ranges={r.ranges}
                    repo={repoOf(r)}
                    icon="file"
                    onSelect={() => pick(r.worktreeId, r.path)}
                  />
                ))}
              </CommandPrimitive.Group>
            )}
          </CommandPrimitive.List>

          {truncated && (
            <div className="border-t px-4 py-1.5 text-[10px] text-warning">
              Large workspace — search covers the first {candidates.length.toLocaleString()} files.
            </div>
          )}
        </CommandPrimitive>
      </DialogContent>
    </Dialog>
  );
}

export function FileSearchPalette({ issueId }: { issueId: string }) {
  const { data: worktrees = [] } = useIssueWorktrees(issueId);
  const [open, setOpen] = useState(false);
  const [everOpened, setEverOpened] = useState(false);
  const hasWorkspace = worktrees.length > 0;

  useEffect(() => {
    if (!hasWorkspace) return;
    const onKeyDown = (e: KeyboardEvent) => {
      if (e.key.toLowerCase() !== "p" || !(e.metaKey || e.ctrlKey) || e.shiftKey || e.altKey) {
        return;
      }
      e.preventDefault();
      setOpen((o) => !o);
      setEverOpened(true);
    };
    document.addEventListener("keydown", onKeyDown);
    return () => document.removeEventListener("keydown", onKeyDown);
  }, [hasWorkspace]);

  // Close when the issue view is hidden (desktop Activity tab switch) or
  // unmounted — the dialog portals outside the hidden subtree and would
  // otherwise linger over the newly visible tab.
  useEffect(() => () => setOpen(false), []);

  if (!everOpened) return null;
  return (
    <FileSearchDialog
      issueId={issueId}
      worktrees={worktrees}
      open={open}
      onOpenChange={setOpen}
    />
  );
}

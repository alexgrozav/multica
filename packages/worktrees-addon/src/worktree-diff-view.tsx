"use client";

import { useCallback, useEffect, useMemo, useRef } from "react";
import { EditorState, Compartment } from "@codemirror/state";
import { EditorView } from "@codemirror/view";
import { FileWarning, Loader2, RefreshCw } from "lucide-react";
import { Button } from "@multica/ui/components/ui/button";
import { cn } from "@multica/ui/lib/utils";
import { useIssueWorktreeChanges, useWorktreeFileDiff } from "./queries";
import { useIssueFileTabs } from "./issue-file-tabs-context";
import { buildDiffExtensions, languageExtensionFor } from "./codemirror-setup";
import { FileModeToggle } from "./file-mode-toggle";
import { fileTabLabel } from "./file-tab-state";

// WorktreeDiffView is one diff tab's content: a toolbar (path, +/- counts,
// Reload) over a read-only CodeMirror unified diff of the file against the
// branch base. Both sides are fetched live through the owning daemon in one
// round trip (~2s). The view is read-only, so unlike the editor it can always
// follow the data: a WS-driven refetch (the agent kept working) rebuilds the
// diff in place, keeping the scroll position.
export function WorktreeDiffView({
  issueId,
  worktreeId,
  path,
}: {
  issueId: string;
  worktreeId: string;
  path: string;
}) {
  const { setTabMode } = useIssueFileTabs();
  const { data, isLoading, isError, error, refetch, isRefetching } = useWorktreeFileDiff(
    issueId,
    worktreeId,
    path,
  );
  // Line counts + base label come from the sidebar's changes list (kept fresh
  // by the same WS event); missing entries just hide the summary.
  const { data: changes = [] } = useIssueWorktreeChanges(issueId);
  const { entry, base } = useMemo(() => {
    const wt = changes.find((c) => c.worktree_id === worktreeId);
    return { entry: wt?.files.find((f) => f.path === path), base: wt?.base ?? "" };
  }, [changes, worktreeId, path]);
  // A deleted file has nothing to edit — the toggle disappears rather than
  // offering a dead Edit mode. A missing entry (file went clean while the diff
  // was open) keeps the toggle as the way back to the editor.
  const canEdit = entry ? entry.status !== "deleted" : true;

  const containerRef = useRef<HTMLDivElement | null>(null);
  // Scroll position carried across rebuilds; the previous effect's CLEANUP
  // captures it (React runs cleanup before the next body, so reading the old
  // view inside the body would always see a torn-down ref).
  const scrollTopRef = useRef(0);

  const viewable = !isError && !!data && !data.binary && !data.truncated;

  // (Re)build the diff whenever fresh sides arrive. CodeMirror's unified merge
  // view takes `original` at creation, so an update is a rebuild — scroll is
  // carried over to keep WS-driven refreshes from yanking the reader to the top.
  useEffect(() => {
    const parent = containerRef.current;
    if (!parent || !viewable) return;
    let cancelled = false;
    const language = new Compartment();
    const view = new EditorView({
      parent,
      state: EditorState.create({
        doc: data.new_content,
        extensions: buildDiffExtensions({ language, original: data.old_content }),
      }),
    });
    view.scrollDOM.scrollTop = scrollTopRef.current;
    void languageExtensionFor(fileTabLabel(path)).then((ext) => {
      if (!cancelled && ext) view.dispatch({ effects: language.reconfigure(ext) });
    });
    return () => {
      cancelled = true;
      scrollTopRef.current = view.scrollDOM.scrollTop;
      view.destroy();
    };
  }, [viewable, data, path]);

  const handleReload = useCallback(() => {
    void refetch();
  }, [refetch]);

  let body: React.ReactNode;
  if (isLoading) {
    body = (
      <DiffHint icon={<Loader2 className="h-4 w-4 animate-spin" />}>
        Loading diff of {fileTabLabel(path)}…
      </DiffHint>
    );
  } else if (isError) {
    body = (
      <DiffHint icon={<FileWarning className="h-4 w-4" />}>
        <span>{error instanceof Error && error.message ? error.message : "Failed to load diff."}</span>
        <Button variant="outline" size="sm" className="mt-2 h-6 px-2 text-[11px]" onClick={handleReload}>
          Retry
        </Button>
      </DiffHint>
    );
  } else if (data?.binary) {
    body = (
      <DiffHint icon={<FileWarning className="h-4 w-4" />}>
        Binary file — no diff to show.
      </DiffHint>
    );
  } else if (data?.truncated) {
    body = (
      <DiffHint icon={<FileWarning className="h-4 w-4" />}>
        File is larger than 1&nbsp;MB — open it locally instead.
      </DiffHint>
    );
  } else {
    body = <div ref={containerRef} className="min-h-0 flex-1 overflow-hidden" />;
  }

  return (
    <div className="flex h-full min-h-0 flex-col">
      <div className="flex h-8 shrink-0 items-center gap-2 border-b bg-background px-3">
        <span
          className="min-w-0 flex-1 truncate font-mono text-[11px] text-muted-foreground"
          title={base ? `${path} — diff vs ${base}` : path}
        >
          {path}
        </span>
        {entry && !entry.binary && (
          <span className="shrink-0 font-mono text-[11px] tabular-nums">
            {entry.additions > 0 && <span className="text-success">+{entry.additions}</span>}
            {entry.additions > 0 && entry.deletions > 0 && " "}
            {entry.deletions > 0 && <span className="text-destructive">−{entry.deletions}</span>}
          </span>
        )}
        <Button
          variant="ghost"
          size="sm"
          className="h-6 gap-1 px-2 text-[11px] text-muted-foreground"
          onClick={handleReload}
          disabled={isLoading || isRefetching}
          title="Reload diff"
        >
          <RefreshCw className={cn("h-3 w-3", isRefetching && "animate-spin")} />
          Reload
        </Button>
        {canEdit && (
          <FileModeToggle mode="diff" onSwitch={(m) => setTabMode(worktreeId, path, m)} />
        )}
      </div>
      {body}
    </div>
  );
}

function DiffHint({ icon, children }: { icon: React.ReactNode; children: React.ReactNode }) {
  return (
    <div className="flex min-h-0 flex-1 flex-col items-center justify-center gap-1.5 p-6 text-center text-xs text-muted-foreground">
      <span className="opacity-70">{icon}</span>
      {children}
    </div>
  );
}

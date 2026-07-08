"use client";

import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { createPortal } from "react-dom";
import { EditorState, Compartment } from "@codemirror/state";
import { EditorView } from "@codemirror/view";
import { FileWarning, Loader2, RefreshCw } from "lucide-react";
import { Button } from "@multica/ui/components/ui/button";
import { cn } from "@multica/ui/lib/utils";
import { useCreateComment } from "@multica/core/issues/mutations";
import { useIssueWorktreeChanges, useIssueWorktrees, useWorktreeFileDiff } from "./queries";
import { useIssueFileTabs } from "./issue-file-tabs-context";
import { buildDiffExtensions, languageExtensionFor } from "./codemirror-setup";
import { diffCommentExtension, fenceLangFor, formatDiffComment } from "./diff-comment";
import { DiffCommentForm } from "./diff-comment-form";
import { FileModeToggle } from "./file-mode-toggle";
import { fileTabLabel } from "./file-tab-state";
import { repoLabel } from "./repo-label";

// WorktreeDiffView is one diff tab's content: a toolbar (path, +/- counts,
// Reload) over a read-only CodeMirror unified diff of the file against the
// branch base. Both sides are fetched live through the owning daemon in one
// round trip (~2s). The view is read-only, so unlike the editor it can always
// follow the data: a WS-driven refetch (the agent kept working) rebuilds the
// diff in place, keeping the scroll position.
//
// Hovering a line shows a "+" gutter button that opens an inline composer
// (diff-comment.ts); submitting posts an issue comment quoting file, line,
// and code above the body, addressed to the issue's conversation.
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
  const viewRef = useRef<EditorView | null>(null);

  const viewable = !isError && !!data && !data.binary && !data.truncated;

  // --- Line comments ---
  // The composer target (line + the code the user saw when clicking "+") and
  // the draft live here rather than in editor state, so both survive the
  // WS-driven editor rebuilds below.
  const { mutateAsync: createComment, isPending: commentPending } = useCreateComment(issueId);
  const { data: worktrees = [] } = useIssueWorktrees(issueId);
  const [composerTarget, setComposerTarget] = useState<{ line: number; code: string } | null>(
    null,
  );
  const [composerSlot, setComposerSlot] = useState<HTMLElement | null>(null);
  const [draft, setDraft] = useState("");
  const [commentError, setCommentError] = useState<string | null>(null);

  const diffComment = useMemo(
    () =>
      diffCommentExtension({
        onOpen: (line, code) => {
          setCommentError(null);
          setComposerTarget({ line, code });
        },
        onSlotMount: (el) => setComposerSlot(el),
        // Guarded: when the composer moves lines the new slot can mount
        // before the old one is destroyed.
        onSlotUnmount: (el) => setComposerSlot((prev) => (prev === el ? null : prev)),
      }),
    [],
  );

  // Ref mirror for the (re)build effect below — the composer opening must not
  // trigger a full editor rebuild. Declared before the build effect so the
  // ref is current when a rebuild re-opens the composer.
  const composerLineRef = useRef<number | null>(null);
  useEffect(() => {
    composerLineRef.current = composerTarget?.line ?? null;
  }, [composerTarget]);

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
        extensions: [
          buildDiffExtensions({ language, original: data.old_content }),
          diffComment.extension,
        ],
      }),
    });
    viewRef.current = view;
    // Re-open the composer after a rebuild (fresh sides arrived while the
    // user was typing); the draft lives in React state so nothing is lost.
    if (composerLineRef.current != null) {
      diffComment.setLine(view, composerLineRef.current, { scroll: false });
    }
    view.scrollDOM.scrollTop = scrollTopRef.current;
    void languageExtensionFor(fileTabLabel(path)).then((ext) => {
      if (!cancelled && ext) view.dispatch({ effects: language.reconfigure(ext) });
    });
    return () => {
      cancelled = true;
      scrollTopRef.current = view.scrollDOM.scrollTop;
      viewRef.current = null;
      view.destroy();
    };
  }, [viewable, data, path, diffComment]);

  // Push composer open/close into the live view (rebuilds re-open above).
  // Moving the composer to another line makes CodeMirror relocate the widget
  // DOM, which drops focus from the textarea — restore it right after the
  // dispatch (the slot node survives the move, so it can be queried here).
  useEffect(() => {
    const view = viewRef.current;
    if (view) diffComment.setLine(view, composerTarget?.line ?? null);
    if (composerTarget) composerSlot?.querySelector("textarea")?.focus();
  }, [composerTarget, composerSlot, diffComment]);

  const handleCancelComment = useCallback(() => {
    setComposerTarget(null);
    setDraft("");
    setCommentError(null);
  }, []);

  const handleSubmitComment = useCallback(async () => {
    const body = draft.trim();
    if (!composerTarget || !body) return;
    // Same repo-qualification convention as the tab titles: only multi-repo
    // issues need the prefix to disambiguate the path.
    const wt = worktrees.find((w) => w.id === worktreeId);
    const location = worktrees.length > 1 && wt ? `${repoLabel(wt.repo_url)}/${path}` : path;
    const content = formatDiffComment({
      location,
      line: composerTarget.line,
      code: composerTarget.code,
      lang: fenceLangFor(path),
      body,
    });
    setCommentError(null);
    try {
      await createComment({ content });
      setComposerTarget(null);
      setDraft("");
    } catch (err) {
      setCommentError(
        err instanceof Error && err.message ? err.message : "Failed to post the comment.",
      );
    }
  }, [composerTarget, draft, worktrees, worktreeId, path, createComment]);

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
      {composerSlot &&
        composerTarget &&
        createPortal(
          <DiffCommentForm
            value={draft}
            onChange={(v) => {
              setDraft(v);
              if (commentError) setCommentError(null);
            }}
            onSubmit={() => void handleSubmitComment()}
            onCancel={handleCancelComment}
            submitting={commentPending}
            error={commentError}
          />,
          composerSlot,
        )}
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

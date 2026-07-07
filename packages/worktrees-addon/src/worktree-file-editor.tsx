"use client";

import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { EditorState, Compartment } from "@codemirror/state";
import { EditorView } from "@codemirror/view";
import { toast } from "sonner";
import { FileWarning, Loader2, RefreshCw, Save } from "lucide-react";
import { Button } from "@multica/ui/components/ui/button";
import { cn } from "@multica/ui/lib/utils";
import {
  useIssueWorktreeChanges,
  useSaveWorktreeFile,
  useWorktreeFileContent,
  useWorktreeFileDiff,
} from "./queries";
import { useIssueFileTabs } from "./issue-file-tabs-context";
import { buildEditorExtensions, languageExtensionFor, quickDiffExtension } from "./codemirror-setup";
import { FileModeToggle } from "./file-mode-toggle";
import { fileTabLabel } from "./file-tab-state";

// WorktreeFileEditor is one file tab's content: a toolbar (path, Reload, Save)
// over a CodeMirror buffer. Content is read/written live through the owning
// daemon, so open and save each take up to a poll tick (~2s).
//
// Buffer ownership: the editor's doc is driven by (1) the first load, (2) the
// user typing, (3) an explicit Reload. Background query updates only sync into
// a CLEAN buffer — unsaved edits are never clobbered. Saving writes the buffer
// to disk and re-baselines the dirty tracking.
export function WorktreeFileEditor({
  issueId,
  worktreeId,
  path,
  tabKey,
}: {
  issueId: string;
  worktreeId: string;
  path: string;
  tabKey: string;
}) {
  const { setDirty: setTabDirty, setTabMode } = useIssueFileTabs();
  const { data, isLoading, isError, error, refetch, isRefetching } = useWorktreeFileContent(
    issueId,
    worktreeId,
    path,
  );
  const save = useSaveWorktreeFile(issueId);
  // The Edit/Diff toggle appears once the file has changes vs the branch base
  // (it is in the worktree's live changes list — kept fresh by the WS event).
  const { data: changes = [] } = useIssueWorktreeChanges(issueId);
  const hasChanges = useMemo(
    () =>
      changes.some((c) => c.worktree_id === worktreeId && c.files.some((f) => f.path === path)),
    [changes, worktreeId, path],
  );
  // Base content for the quick-diff gutter (VS Code-style change markers).
  // Same query the Diff mode uses, so the fetch is shared; skipped entirely
  // for files without changes.
  const { data: diffData } = useWorktreeFileDiff(issueId, worktreeId, path, hasChanges);

  const containerRef = useRef<HTMLDivElement | null>(null);
  const viewRef = useRef<EditorView | null>(null);
  // The buffer's clean baseline: what the file looked like at load / last save
  // / last reload. Dirty === (doc !== baseline).
  const baselineRef = useRef<string>("");
  const [dirty, setLocalDirty] = useState(false);

  // isError included: an error body replaces the editor container, so the view
  // must be torn down with it (and rebuilt when a retry succeeds).
  const editable = !isError && !!data && !data.binary && !data.truncated;
  // Latest editable content for the mount effect (which must not re-run on
  // every data change — the view is created exactly once per mounted tab).
  const contentRef = useRef<string>("");
  if (editable) contentRef.current = data.content;

  const syncDirty = useCallback(
    (view: EditorView) => {
      const d = view.state.doc.toString() !== baselineRef.current;
      setLocalDirty(d);
      setTabDirty(tabKey, d);
    },
    [setTabDirty, tabKey],
  );

  const handleSave = useCallback(() => {
    const view = viewRef.current;
    if (!view || save.isPending) return;
    const content = view.state.doc.toString();
    if (content === baselineRef.current) return; // nothing to save
    save.mutate(
      { worktreeId, path, content },
      {
        onSuccess: () => {
          baselineRef.current = content;
          const v = viewRef.current;
          if (v) syncDirty(v);
        },
        onError: (err) => {
          toast.error(
            err instanceof Error && err.message ? err.message : "Failed to save file",
          );
        },
      },
    );
  }, [save, worktreeId, path, syncDirty]);

  // Stable indirection for the Mod-S keymap so extensions are built once.
  const saveRef = useRef(handleSave);
  saveRef.current = handleSave;

  // The quick-diff compartment of the mounted view, for the effect below that
  // loads/refreshes the gutter when the base content arrives.
  const quickDiffCompRef = useRef<Compartment | null>(null);

  // Mount the editor once the file is loaded and editable; destroy on unmount
  // (tab close). Tab switches only CSS-hide the panel, so the buffer and its
  // undo history survive them.
  useEffect(() => {
    const parent = containerRef.current;
    if (!parent || !editable) return;
    let cancelled = false;
    const language = new Compartment();
    const quickDiff = new Compartment();
    quickDiffCompRef.current = quickDiff;
    baselineRef.current = contentRef.current;
    const view = new EditorView({
      parent,
      state: EditorState.create({
        doc: contentRef.current,
        extensions: buildEditorExtensions({
          language,
          quickDiff,
          onSave: () => saveRef.current(),
          onDocChanged: () => {
            const v = viewRef.current;
            if (v) syncDirty(v);
          },
        }),
      }),
    });
    viewRef.current = view;
    // A fresh buffer starts clean; this also clears a dirty dot left over from
    // a torn-down editor (error → retry recreates the view).
    syncDirty(view);
    void languageExtensionFor(fileTabLabel(path)).then((ext) => {
      if (!cancelled && ext) view.dispatch({ effects: language.reconfigure(ext) });
    });
    return () => {
      cancelled = true;
      view.destroy();
      viewRef.current = null;
      quickDiffCompRef.current = null;
    };
    // syncDirty is stable per tab; path/tabKey are a tab's identity (fixed for
    // a mounted editor).
  }, [editable, path, syncDirty]);

  // Load the quick-diff gutter once the file's base content arrives (and
  // refresh it when the WS changes event refetches the diff — the base only
  // really moves when the branch is rebased). The gutter itself tracks buffer
  // edits live; without changes (or for binary/oversized bases) it stays off.
  useEffect(() => {
    const view = viewRef.current;
    const cmp = quickDiffCompRef.current;
    if (!view || !cmp) return;
    const ext =
      hasChanges && diffData && !diffData.binary && !diffData.truncated
        ? quickDiffExtension(diffData.old_content)
        : [];
    view.dispatch({ effects: cmp.reconfigure(ext) });
  }, [diffData, hasChanges, editable]);

  // Background data updates (a refetch after remount, the save-success cache
  // patch) sync into a clean buffer only — user edits always win.
  useEffect(() => {
    const view = viewRef.current;
    if (!view || !editable) return;
    const content = data.content;
    if (content === baselineRef.current) return;
    if (view.state.doc.toString() !== baselineRef.current) return; // dirty
    view.dispatch({ changes: { from: 0, to: view.state.doc.length, insert: content } });
    baselineRef.current = content;
    syncDirty(view);
  }, [data, editable, syncDirty]);

  // Explicit Reload: refresh from disk and (unlike background updates) replace
  // the buffer even when dirty — that is what reloading means.
  const handleReload = useCallback(() => {
    void refetch().then((res) => {
      const view = viewRef.current;
      const fresh = res.data;
      if (!view || !fresh || fresh.binary || fresh.truncated) return;
      if (view.state.doc.toString() !== fresh.content) {
        view.dispatch({ changes: { from: 0, to: view.state.doc.length, insert: fresh.content } });
      }
      baselineRef.current = fresh.content;
      syncDirty(view);
    });
  }, [refetch, syncDirty]);

  let body: React.ReactNode;
  if (isLoading) {
    body = (
      <EditorHint icon={<Loader2 className="h-4 w-4 animate-spin" />}>
        Opening {fileTabLabel(path)}…
      </EditorHint>
    );
  } else if (isError) {
    body = (
      <EditorHint icon={<FileWarning className="h-4 w-4" />}>
        <span>{error instanceof Error && error.message ? error.message : "Failed to load file."}</span>
        <Button variant="outline" size="sm" className="mt-2 h-6 px-2 text-[11px]" onClick={handleReload}>
          Retry
        </Button>
      </EditorHint>
    );
  } else if (data?.binary) {
    body = (
      <EditorHint icon={<FileWarning className="h-4 w-4" />}>
        Binary file — it can’t be edited here.
      </EditorHint>
    );
  } else if (data?.truncated) {
    body = (
      <EditorHint icon={<FileWarning className="h-4 w-4" />}>
        File is larger than 1&nbsp;MB — open it locally instead.
      </EditorHint>
    );
  } else {
    body = <div ref={containerRef} className="min-h-0 flex-1 overflow-hidden" />;
  }

  return (
    <div className="flex h-full min-h-0 flex-col">
      <div className="flex h-8 shrink-0 items-center gap-2 border-b bg-background px-3">
        <span className="min-w-0 flex-1 truncate font-mono text-[11px] text-muted-foreground" title={path}>
          {path}
        </span>
        <Button
          variant="ghost"
          size="sm"
          className="h-6 gap-1 px-2 text-[11px] text-muted-foreground"
          onClick={handleReload}
          disabled={isLoading || isRefetching}
          title="Reload from disk"
        >
          <RefreshCw className={cn("h-3 w-3", isRefetching && "animate-spin")} />
          Reload
        </Button>
        <Button
          variant="secondary"
          size="sm"
          className="h-6 gap-1 px-2 text-[11px]"
          onClick={handleSave}
          disabled={!dirty || !editable || save.isPending}
          title="Save (⌘S / Ctrl+S)"
        >
          {save.isPending ? <Loader2 className="h-3 w-3 animate-spin" /> : <Save className="h-3 w-3" />}
          {save.isPending ? "Saving…" : "Save"}
        </Button>
        {hasChanges && (
          <FileModeToggle mode="edit" onSwitch={(m) => setTabMode(worktreeId, path, m)} />
        )}
      </div>
      {body}
    </div>
  );
}

function EditorHint({ icon, children }: { icon: React.ReactNode; children: React.ReactNode }) {
  return (
    <div className="flex min-h-0 flex-1 flex-col items-center justify-center gap-1.5 p-6 text-center text-xs text-muted-foreground">
      <span className="opacity-70">{icon}</span>
      {children}
    </div>
  );
}

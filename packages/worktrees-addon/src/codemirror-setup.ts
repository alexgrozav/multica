// CodeMirror 6 building blocks for the worktree file editor: app-token theme,
// a syntax palette legible on both light and dark backgrounds, and lazy
// per-language loading keyed by filename.

import { basicSetup } from "codemirror";
import { EditorView, GutterMarker, gutter, keymap } from "@codemirror/view";
import {
  Compartment,
  EditorState,
  RangeSet,
  RangeSetBuilder,
  StateField,
  Text,
  type Extension,
} from "@codemirror/state";
import { indentWithTab } from "@codemirror/commands";
import { unifiedMergeView } from "@codemirror/merge";
import { computeQuickDiff } from "./quick-diff";
import {
  HighlightStyle,
  LanguageDescription,
  syntaxHighlighting,
} from "@codemirror/language";
import { languages } from "@codemirror/language-data";
import { tags } from "@lezer/highlight";

// Chrome colors come from the app's semantic tokens (full oklch values in
// tokens.css), so the editor follows light/dark automatically with no JS
// theme detection.
const editorTheme = EditorView.theme({
  "&": {
    height: "100%",
    fontSize: "12.5px",
    backgroundColor: "transparent",
    color: "var(--foreground)",
  },
  "&.cm-focused": { outline: "none" },
  ".cm-scroller": {
    fontFamily: "var(--font-mono, ui-monospace, SFMono-Regular, Menlo, monospace)",
    lineHeight: "1.55",
    overflow: "auto",
  },
  ".cm-content": { padding: "10px 0", caretColor: "var(--foreground)" },
  ".cm-cursor, .cm-dropCursor": { borderLeftColor: "var(--foreground)" },
  ".cm-gutters": {
    backgroundColor: "transparent",
    color: "var(--muted-foreground)",
    border: "none",
    paddingLeft: "6px",
  },
  ".cm-activeLine": {
    backgroundColor: "color-mix(in oklab, var(--accent) 45%, transparent)",
  },
  ".cm-activeLineGutter": {
    backgroundColor: "transparent",
    color: "var(--foreground)",
  },
  ".cm-selectionBackground, &.cm-focused .cm-selectionBackground, ::selection": {
    backgroundColor: "color-mix(in oklab, var(--primary) 22%, transparent) !important",
  },
  ".cm-selectionMatch": {
    backgroundColor: "color-mix(in oklab, var(--primary) 14%, transparent)",
  },
  ".cm-panels": {
    backgroundColor: "var(--background)",
    color: "var(--foreground)",
    borderColor: "var(--border)",
  },
  ".cm-tooltip": {
    backgroundColor: "var(--popover)",
    color: "var(--popover-foreground)",
    border: "1px solid var(--border)",
    borderRadius: "6px",
  },
});

// Mid-tone syntax palette chosen to stay legible on white and near-black
// (comments track the app's muted token so they adapt with the theme).
const highlight = HighlightStyle.define([
  { tag: [tags.comment, tags.lineComment, tags.blockComment], color: "var(--muted-foreground)", fontStyle: "italic" },
  { tag: [tags.keyword, tags.moduleKeyword, tags.operatorKeyword, tags.controlKeyword, tags.definitionKeyword], color: "#a855f7" },
  { tag: [tags.string, tags.special(tags.string), tags.regexp], color: "#10b981" },
  { tag: [tags.number, tags.bool, tags.atom, tags.null, tags.unit], color: "#f59e0b" },
  { tag: [tags.typeName, tags.className, tags.namespace], color: "#0ea5e9" },
  { tag: [tags.function(tags.variableName), tags.function(tags.propertyName), tags.macroName], color: "#3b82f6" },
  { tag: [tags.definition(tags.variableName), tags.definition(tags.propertyName)], color: "#0284c7" },
  { tag: [tags.tagName], color: "#ef4444" },
  { tag: [tags.attributeName], color: "#d97706" },
  { tag: [tags.propertyName], color: "#0284c7" },
  { tag: [tags.meta, tags.processingInstruction], color: "var(--muted-foreground)" },
  { tag: tags.heading, fontWeight: "600" },
  { tag: tags.strong, fontWeight: "600" },
  { tag: tags.emphasis, fontStyle: "italic" },
  { tag: tags.link, color: "#3b82f6", textDecoration: "underline" },
  { tag: tags.invalid, color: "#ef4444" },
]);

// buildEditorExtensions assembles the editor config. `language` is a
// Compartment slot filled asynchronously once the filename's language package
// loads; `quickDiff` is a Compartment slot filled with quickDiffExtension once
// the file's base content arrives; `onSave` handles Mod-S (through a
// caller-owned ref so the keymap never needs rebuilding); `onDocChanged` feeds
// dirty tracking.
export function buildEditorExtensions(opts: {
  language: Compartment;
  quickDiff: Compartment;
  onDocChanged: () => void;
  onSave: () => void;
}): Extension[] {
  return [
    basicSetup,
    keymap.of([
      {
        key: "Mod-s",
        preventDefault: true,
        run: () => {
          opts.onSave();
          return true;
        },
      },
      indentWithTab,
    ]),
    editorTheme,
    syntaxHighlighting(highlight, { fallback: true }),
    opts.language.of([]),
    opts.quickDiff.of([]),
    EditorView.updateListener.of((update) => {
      if (update.docChanged) opts.onDocChanged();
    }),
  ];
}

// ---- Quick-diff gutter (VS Code-style change markers in edit mode) ----

class QuickDiffMarker extends GutterMarker {
  constructor(cls: string) {
    super();
    this.elementClass = cls;
  }
}

const addedMarker = new QuickDiffMarker("cm-quickDiffAdded");
const modifiedMarker = new QuickDiffMarker("cm-quickDiffModified");
const deletedMarker = new QuickDiffMarker("cm-quickDiffDeleted");

// quickDiffMarkers renders the classification as gutter markers. On the rare
// collision (a deletion boundary on a line that is itself changed) the
// stronger line classification wins.
function quickDiffMarkers(aLines: readonly string[], doc: Text): RangeSet<GutterMarker> {
  const bLines: string[] = new Array(doc.lines);
  for (let i = 1; i <= doc.lines; i++) bLines[i - 1] = doc.line(i).text;
  const { added, modified, deletedAt } = computeQuickDiff(aLines, bLines);

  const byLine = new Map<number, QuickDiffMarker>();
  for (const n of deletedAt) byLine.set(n, deletedMarker);
  for (const n of added) byLine.set(n, addedMarker);
  for (const n of modified) byLine.set(n, modifiedMarker);

  const builder = new RangeSetBuilder<GutterMarker>();
  for (const n of [...byLine.keys()].sort((a, b) => a - b)) {
    const pos = doc.line(n).from;
    builder.add(pos, pos, byLine.get(n)!);
  }
  return builder.finish();
}

// Marker colors are faded token mixes (full-strength tokens shout next to the
// code), and the selectors carry the gutter class so highlightActiveLineGutter
// — whose theme rule repaints the active line's gutter elements — can't blank
// out the marker under the cursor.
const quickDiffTheme = EditorView.theme({
  ".cm-gutter.cm-quickDiffGutter": { width: "3px" },
  ".cm-quickDiffGutter .cm-gutterElement": { padding: "0" },
  ".cm-quickDiffGutter .cm-quickDiffAdded": {
    background: "color-mix(in oklab, var(--success) 45%, transparent)",
  },
  ".cm-quickDiffGutter .cm-quickDiffModified": {
    background: "color-mix(in oklab, var(--warning) 55%, transparent)",
  },
  ".cm-quickDiffGutter .cm-quickDiffDeleted": {
    position: "relative",
    overflow: "visible",
    "&:after": {
      content: '""',
      position: "absolute",
      left: "-1px",
      top: "-4px",
      width: "0",
      height: "0",
      borderLeft: "6px solid color-mix(in oklab, var(--destructive) 60%, transparent)",
      borderTop: "4px solid transparent",
      borderBottom: "4px solid transparent",
    },
  },
});

// quickDiffExtension adds VS Code-style change markers to an EDITABLE buffer:
// a slim gutter tracking how the current doc differs from `original` (the
// file's content at the branch base), recomputed on every edit. The common
// prefix/suffix trim keeps the per-keystroke line diff at the size of the
// changed region. Loaded into the editor's quickDiff compartment once the base
// content is fetched; a reconfigure rebuilds the field against the current doc.
export function quickDiffExtension(original: string): Extension {
  const aLines = original.split("\n");
  const field = StateField.define<RangeSet<GutterMarker>>({
    create: (state) => quickDiffMarkers(aLines, state.doc),
    update: (value, tr) => (tr.docChanged ? quickDiffMarkers(aLines, tr.newDoc) : value),
  });
  return [
    field,
    gutter({
      class: "cm-quickDiffGutter",
      markers: (view) => view.state.field(field),
    }),
    quickDiffTheme,
  ];
}

// Unified-diff colors over the app tokens: additions track --success, the
// deleted chunks --destructive, so the view follows light/dark automatically.
// The merge package's default theme is light-mode-tuned; every class it colors
// is overridden here. Selector structure matters: the unified view's root
// carries .cm-merge-b, and the base theme styles inserted text through
// `&light.cm-merge-b .cm-changedText` (a bottom-gradient "underline") — a
// 3-class selector a bare `.cm-changedText` theme rule loses to. Rules below
// mirror that structure; at equal specificity the theme wins (themes are
// mounted after base themes).
const diffTheme = EditorView.theme({
  "&.cm-merge-b .cm-changedLine": {
    backgroundColor: "color-mix(in oklab, var(--success) 11%, transparent)",
  },
  "&.cm-merge-b .cm-changedText": {
    background: "color-mix(in oklab, var(--success) 26%, transparent)",
    borderRadius: "2px",
  },
  ".cm-deletedChunk": {
    backgroundColor: "color-mix(in oklab, var(--destructive) 9%, transparent)",
    padding: "0 2px 0 6px",
  },
  ".cm-deletedLine": { textDecoration: "none" },
  "&.cm-merge-b .cm-deletedText": {
    background: "color-mix(in oklab, var(--destructive) 24%, transparent)",
    borderRadius: "2px",
    textDecoration: "none",
  },
  ".cm-changeGutter": { width: "3px", paddingLeft: "1px" },
  "&.cm-merge-b .cm-changedLineGutter": {
    background: "color-mix(in oklab, var(--success) 45%, transparent)",
  },
  ".cm-deletedLineGutter": {
    background: "color-mix(in oklab, var(--destructive) 45%, transparent)",
  },
  ".cm-collapsedLines": {
    color: "var(--muted-foreground)",
    background: "color-mix(in oklab, var(--accent) 60%, transparent)",
    padding: "3px 8px",
    fontSize: "11px",
    "&:before, &:after": { display: "none" },
  },
});

// buildDiffExtensions assembles the read-only unified diff view for one
// changed file: the buffer holds the working-tree side, `original` the base
// side; long unchanged stretches collapse to a click-to-expand bar.
export function buildDiffExtensions(opts: {
  language: Compartment;
  original: string;
}): Extension[] {
  return [
    basicSetup,
    EditorState.readOnly.of(true),
    EditorView.editable.of(false),
    editorTheme,
    diffTheme,
    syntaxHighlighting(highlight, { fallback: true }),
    opts.language.of([]),
    unifiedMergeView({
      original: opts.original,
      mergeControls: false,
      gutter: true,
      highlightChanges: true,
      syntaxHighlightDeletions: true,
      collapseUnchanged: { margin: 3, minSize: 6 },
    }),
  ];
}

// languageExtensionFor resolves the CodeMirror language support for a filename
// (null when unknown). Loading is dynamic — each language package is fetched
// on first use only.
export async function languageExtensionFor(filename: string): Promise<Extension | null> {
  const desc = LanguageDescription.matchFilename(languages, filename);
  if (!desc) return null;
  try {
    return await desc.load();
  } catch {
    return null; // fall back to plain text on a failed chunk load
  }
}

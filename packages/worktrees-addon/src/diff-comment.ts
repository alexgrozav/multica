// Line comments for the read-only unified diff view: hovering a line shows a
// "+" button over the gutter; clicking it opens an inline composer below the
// line (a block widget the React form is portaled into). Submitting posts a
// plain issue comment whose markdown quotes the file, line number, and the
// line's code above the body — context both teammates and the issue's agent
// can act on. Comments anchor to the working-tree side of the diff (the doc);
// deleted-chunk widgets are not commentable.

import {
  Decoration,
  EditorView,
  GutterMarker,
  ViewPlugin,
  WidgetType,
  gutter,
  type DecorationSet,
} from "@codemirror/view";
import {
  StateEffect,
  StateField,
  type EditorState,
  type Extension,
} from "@codemirror/state";

// ---------------------------------------------------------------------------
// Comment formatting (pure)
// ---------------------------------------------------------------------------

// fenceLangFor picks a code-fence language from the filename extension, or ""
// when there is none worth claiming (dotfiles, extensionless files).
export function fenceLangFor(path: string): string {
  const base = path.slice(path.lastIndexOf("/") + 1);
  const dot = base.lastIndexOf(".");
  if (dot <= 0) return "";
  const ext = base.slice(dot + 1).toLowerCase();
  return /^[a-z0-9+#-]{1,12}$/.test(ext) ? ext : "";
}

// formatDiffComment renders the posted comment body:
//
//   **`src/badge.ts:9`**
//
//   ```ts
//   export type BadgeProps = { … };
//   ```
//
//   Split this into named unions.
//
// A blank quoted line drops the code block (an empty fence reads as broken).
// The fence grows past any backtick run inside the code so quoting markdown
// sources cannot terminate the block early.
export function formatDiffComment(opts: {
  location: string;
  line: number;
  code: string;
  lang: string;
  body: string;
}): string {
  const parts = [`**\`${opts.location}:${opts.line}\`**`];
  if (opts.code.trim().length > 0) {
    const longestRun = (opts.code.match(/`+/g) ?? []).reduce(
      (max, run) => Math.max(max, run.length),
      0,
    );
    const fence = "`".repeat(Math.max(3, longestRun + 1));
    parts.push(`${fence}${opts.lang}\n${opts.code}\n${fence}`);
  }
  parts.push(opts.body);
  return parts.join("\n\n");
}

// ---------------------------------------------------------------------------
// CodeMirror extension
// ---------------------------------------------------------------------------

const setHoveredLine = StateEffect.define<number | null>();
const setComposerLine = StateEffect.define<number | null>();

const hoveredLineField = StateField.define<number | null>({
  create: () => null,
  update(value, tr) {
    for (const e of tr.effects) if (e.is(setHoveredLine)) value = e.value;
    return value;
  },
});

// Tracks which line the pointer is over. The listener sits on scrollDOM so it
// covers the gutters as well as the code; it dispatches only when the line
// actually changes, keeping mousemove cheap.
const hoverTracker = ViewPlugin.define((view) => {
  const move = (event: MouseEvent) => {
    const docY = event.clientY - view.documentTop;
    const line =
      docY < 0 || docY > view.contentHeight
        ? null
        : view.state.doc.lineAt(view.lineBlockAtHeight(docY).from).number;
    if (line !== view.state.field(hoveredLineField)) {
      view.dispatch({ effects: setHoveredLine.of(line) });
    }
  };
  const leave = () => {
    if (view.state.field(hoveredLineField) != null) {
      view.dispatch({ effects: setHoveredLine.of(null) });
    }
  };
  view.scrollDOM.addEventListener("mousemove", move);
  view.scrollDOM.addEventListener("mouseleave", leave);
  return {
    destroy() {
      view.scrollDOM.removeEventListener("mousemove", move);
      view.scrollDOM.removeEventListener("mouseleave", leave);
    },
  };
});

class PlusMarker extends GutterMarker {
  constructor(
    private readonly line: number,
    private readonly text: string,
    private readonly onOpen: (line: number, code: string) => void,
  ) {
    super();
  }
  override eq(other: PlusMarker) {
    return other.line === this.line && other.text === this.text;
  }
  override toDOM() {
    const btn = document.createElement("button");
    btn.type = "button";
    btn.className = "cm-diffCommentAdd";
    btn.title = "Comment on this line";
    btn.setAttribute("aria-label", `Comment on line ${this.line}`);
    btn.textContent = "+";
    btn.addEventListener("mousedown", (event) => {
      // Keep the mousedown from grabbing editor focus — the composer's
      // textarea autofocuses right after opening.
      event.preventDefault();
      event.stopPropagation();
      this.onOpen(this.line, this.text);
    });
    return btn;
  }
}

// ComposerWidget is an empty slot below the target line. The React form is
// portaled into it by the diff view, so the widget itself carries no state —
// a rebuild (fresh diff sides) recreates the slot and the portal re-targets.
class ComposerWidget extends WidgetType {
  constructor(
    private readonly onMount: (el: HTMLElement) => void,
    private readonly onUnmount: (el: HTMLElement) => void,
  ) {
    super();
  }
  override eq() {
    return true; // a single composer exists per view
  }
  override ignoreEvent() {
    return true; // events inside the form belong to the form
  }
  override toDOM() {
    const el = document.createElement("div");
    el.className = "cm-diffCommentHost";
    this.onMount(el);
    return el;
  }
  override destroy(dom: HTMLElement) {
    this.onUnmount(dom);
  }
}

// The gutter is a 1px anchor column on the code side of the gutter cluster;
// the button overflows left over the fold/change gutters so no width is
// reserved — matching the design, where the "+" overlaps the line number's
// edge on hover.
const diffCommentTheme = EditorView.theme({
  // The base .cm-gutter style clips (overflow: hidden); the override lets the
  // button escape the 1px column. Themes mount after base themes, so this
  // wins at equal specificity.
  ".cm-diffCommentGutter": { width: "1px", overflow: "visible" },
  ".cm-diffCommentGutter .cm-gutterElement": {
    position: "relative",
    overflow: "visible",
  },
  ".cm-diffCommentAdd": {
    position: "absolute",
    left: "-19px",
    top: "50%",
    transform: "translateY(-50%)",
    zIndex: "20",
    display: "flex",
    alignItems: "center",
    justifyContent: "center",
    width: "16px",
    height: "16px",
    padding: "0",
    borderRadius: "4px",
    border: "1px solid var(--border)",
    background: "var(--background)",
    color: "var(--foreground)",
    boxShadow: "0 1px 2px rgb(0 0 0 / 0.2)",
    cursor: "pointer",
    font: "600 13px/1 var(--font-sans, ui-sans-serif, system-ui)",
  },
  ".cm-diffCommentAdd:hover": {
    background: "var(--accent)",
  },
  ".cm-diffCommentTargetLine": {
    // Wins over the merge view's changed-line tint while the composer is open.
    backgroundColor: "color-mix(in oklab, var(--warning) 16%, transparent) !important",
  },
  ".cm-diffCommentHost": {
    // Sticky keeps the form visible when long lines force horizontal scroll.
    position: "sticky",
    left: "0",
    boxSizing: "border-box",
    width: "min(640px, 100%)",
    padding: "8px 10px 12px",
    fontFamily: "var(--font-sans, ui-sans-serif, system-ui)",
  },
});

export interface DiffCommentCallbacks {
  /** "+" clicked: open the composer for a 1-based line and its current text. */
  onOpen: (line: number, code: string) => void;
  /** The composer slot entered the DOM — portal the form into it. */
  onSlotMount: (el: HTMLElement) => void;
  /** The slot left the DOM (composer closed or the view was rebuilt). */
  onSlotUnmount: (el: HTMLElement) => void;
}

export interface DiffCommentHandle {
  extension: Extension;
  /**
   * Open the composer under a 1-based line (null closes it). `scroll: false`
   * skips the scroll-into-view nudge — used when re-opening after a rebuild,
   * where the reader's scroll position must not move.
   */
  setLine: (view: EditorView, line: number | null, opts?: { scroll?: boolean }) => void;
}

export function diffCommentExtension(cb: DiffCommentCallbacks): DiffCommentHandle {
  const widget = new ComposerWidget(cb.onSlotMount, cb.onSlotUnmount);

  const buildDeco = (state: EditorState, lineNo: number): DecorationSet => {
    // Clamp for placement only — a rebuild may have shortened the doc while
    // the composer was open; the referenced line/code were captured at click.
    const line = state.doc.line(Math.max(1, Math.min(lineNo, state.doc.lines)));
    return Decoration.set([
      Decoration.line({ class: "cm-diffCommentTargetLine" }).range(line.from),
      Decoration.widget({ widget, block: true, side: 1 }).range(line.to),
    ]);
  };

  const composerField = StateField.define<{ line: number; deco: DecorationSet } | null>({
    create: () => null,
    update(value, tr) {
      if (value && tr.docChanged) {
        value = { line: value.line, deco: value.deco.map(tr.changes) };
      }
      for (const e of tr.effects) {
        if (e.is(setComposerLine)) {
          value = e.value == null ? null : { line: e.value, deco: buildDeco(tr.state, e.value) };
        }
      }
      return value;
    },
    provide: (f) => EditorView.decorations.from(f, (v) => v?.deco ?? Decoration.none),
  });

  const commentGutter = gutter({
    class: "cm-diffCommentGutter",
    lineMarker(view, block) {
      const hovered = view.state.field(hoveredLineField);
      if (hovered == null) return null;
      const line = view.state.doc.lineAt(block.from);
      return line.number === hovered ? new PlusMarker(line.number, line.text, cb.onOpen) : null;
    },
    lineMarkerChange: (update) =>
      update.state.field(hoveredLineField) !== update.startState.field(hoveredLineField),
  });

  const setLine: DiffCommentHandle["setLine"] = (view, line, opts) => {
    const current = view.state.field(composerField)?.line ?? null;
    if (current === line) return;
    const effects: StateEffect<unknown>[] = [setComposerLine.of(line)];
    if (line != null && opts?.scroll !== false) {
      // Keep the target line and the form below it in view. Anchored at the
      // line START so a long line never yanks the view horizontally.
      const anchor = view.state.doc.line(Math.max(1, Math.min(line, view.state.doc.lines))).from;
      effects.push(EditorView.scrollIntoView(anchor, { y: "nearest", yMargin: 170 }));
    }
    view.dispatch({ effects });
  };

  return {
    extension: [hoveredLineField, hoverTracker, commentGutter, composerField, diffCommentTheme],
    setLine,
  };
}

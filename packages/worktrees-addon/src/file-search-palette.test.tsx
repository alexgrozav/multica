// @vitest-environment jsdom
import "@testing-library/jest-dom/vitest";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import type { IssueWorktree, WorktreeFileList } from "./types";

// jsdom gaps: cmdk scrolls the selected item into view; base-ui measures.
if (typeof Element.prototype.scrollIntoView !== "function") {
  Element.prototype.scrollIntoView = () => {};
}
if (typeof globalThis.ResizeObserver === "undefined") {
  globalThis.ResizeObserver = class {
    observe() {}
    unobserve() {}
    disconnect() {}
  } as unknown as typeof ResizeObserver;
}
if (typeof window.matchMedia !== "function") {
  window.matchMedia = (query: string) =>
    ({
      matches: false,
      media: query,
      onchange: null,
      addListener: () => {},
      removeListener: () => {},
      addEventListener: () => {},
      removeEventListener: () => {},
      dispatchEvent: () => false,
    }) as MediaQueryList;
}

// Mutable fixtures the mocked hooks read from, reset per test.
let worktrees: IssueWorktree[] = [];
let fileLists: WorktreeFileList[] = [];
let filesLoading = false;

vi.mock("./queries", () => ({
  useIssueWorktrees: () => ({ data: worktrees, isLoading: false }),
  useIssueWorktreeFiles: () => ({ data: fileLists, isLoading: filesLoading }),
}));

import { FileSearchPalette } from "./file-search-palette";
import { IssueFileTabsProvider, useIssueFileTabs } from "./issue-file-tabs-context";

function makeWorktree(over: Partial<IssueWorktree> = {}): IssueWorktree {
  return {
    id: "w1",
    issue_id: "i1",
    identifier: "PRO-11",
    workspace_id: "ws1",
    repo_url: "https://example.com/repoA.git",
    path: "/wt/a",
    branch: "PRO-11",
    status: "ready",
    setup_status: "succeeded",
    has_setup: false,
    has_cleanup: false,
    runs: [],
    created_at: "",
    updated_at: "",
    ...over,
  };
}

function makeFiles(over: Partial<WorktreeFileList> = {}): WorktreeFileList {
  return {
    worktree_id: "w1",
    issue_id: "i1",
    repo_url: "https://example.com/repoA.git",
    digest: "d1",
    paths: ["README.md", "docs/setup.md", "src/app/main.ts", "src/tab-state.ts"],
    truncated: false,
    updated_at: "",
    ...over,
  };
}

// Probe standing in for the rest of the issue view: reports which tabs the
// palette opened, and can pre-open one for the empty-query state.
function Probe() {
  const { fileTabs, openFile } = useIssueFileTabs();
  return (
    <>
      <div data-testid="open-tabs">
        {fileTabs.map((t) => `${t.worktreeId}:${t.path}`).join(",")}
      </div>
      <button onClick={() => openFile("w1", "src/tab-state.ts")}>probe-open</button>
    </>
  );
}

function renderPalette() {
  return render(
    <IssueFileTabsProvider issueId="i1">
      <FileSearchPalette issueId="i1" />
      <Probe />
    </IssueFileTabsProvider>,
  );
}

function pressCmdP(init: KeyboardEventInit = {}) {
  return fireEvent.keyDown(document, { key: "p", metaKey: true, ...init });
}

function paletteInput() {
  return screen.getByPlaceholderText("Search files by name…");
}

beforeEach(() => {
  worktrees = [];
  fileLists = [];
  filesLoading = false;
});

afterEach(() => cleanup());

describe("FileSearchPalette", () => {
  it("leaves Cmd+P to the browser while the issue has no workspace", () => {
    renderPalette();
    const notPrevented = pressCmdP();
    expect(notPrevented).toBe(true);
    expect(screen.queryByPlaceholderText("Search files by name…")).not.toBeInTheDocument();
  });

  it("opens on Cmd+P and again toggles closed", () => {
    worktrees = [makeWorktree()];
    fileLists = [makeFiles()];
    renderPalette();

    expect(pressCmdP()).toBe(false); // preventDefault: shortcut consumed
    expect(paletteInput()).toBeInTheDocument();

    pressCmdP();
    expect(screen.queryByPlaceholderText("Search files by name…")).not.toBeInTheDocument();
  });

  it("does not react to Cmd+Shift+P or a plain P", () => {
    worktrees = [makeWorktree()];
    fileLists = [makeFiles()];
    renderPalette();

    pressCmdP({ shiftKey: true });
    fireEvent.keyDown(document, { key: "p" });
    expect(screen.queryByPlaceholderText("Search files by name…")).not.toBeInTheDocument();
  });

  it("fuzzy-matches on the query and highlights the matched groups", () => {
    worktrees = [makeWorktree()];
    fileLists = [makeFiles()];
    renderPalette();
    pressCmdP({ ctrlKey: true, metaKey: false }); // Ctrl+P works too

    fireEvent.change(paletteInput(), { target: { value: "tabst" } });

    const row = screen.getByTitle("src/tab-state.ts");
    expect(row).toBeInTheDocument();
    // "tab" + "st" groups, split across basename marks.
    const marks = [...row.querySelectorAll("mark")].map((m) => m.textContent);
    expect(marks).toEqual(["tab", "st"]);
    // Non-matching files are filtered out.
    expect(screen.queryByTitle("README.md")).not.toBeInTheDocument();
  });

  it("opens the picked file in the content tabs and closes", () => {
    worktrees = [makeWorktree()];
    fileLists = [makeFiles()];
    renderPalette();
    pressCmdP();

    fireEvent.change(paletteInput(), { target: { value: "main" } });
    fireEvent.click(screen.getByTitle("src/app/main.ts"));

    expect(screen.getByTestId("open-tabs")).toHaveTextContent("w1:src/app/main.ts");
    expect(screen.queryByPlaceholderText("Search files by name…")).not.toBeInTheDocument();
  });

  it("opens the top match on Enter", () => {
    worktrees = [makeWorktree()];
    fileLists = [makeFiles()];
    renderPalette();
    pressCmdP();

    const input = paletteInput();
    fireEvent.change(input, { target: { value: "main" } });
    fireEvent.keyDown(input, { key: "Enter" });

    expect(screen.getByTestId("open-tabs")).toHaveTextContent("w1:src/app/main.ts");
  });

  it("closes on Escape and reopens with a fresh query", () => {
    worktrees = [makeWorktree()];
    fileLists = [makeFiles()];
    renderPalette();
    pressCmdP();
    fireEvent.change(paletteInput(), { target: { value: "main" } });

    fireEvent.keyDown(document, { key: "Escape" });
    expect(screen.queryByPlaceholderText("Search files by name…")).not.toBeInTheDocument();

    pressCmdP();
    expect(paletteInput()).toHaveValue("");
  });

  it("lists already-open files under an Open files group when the query is empty", () => {
    worktrees = [makeWorktree()];
    fileLists = [makeFiles()];
    renderPalette();

    fireEvent.click(screen.getByText("probe-open"));
    pressCmdP();

    expect(screen.getByText("Open files")).toBeInTheDocument();
    expect(screen.getByTitle("src/tab-state.ts")).toBeInTheDocument();
  });

  it("shows repo labels when the issue spans several repos", () => {
    worktrees = [
      makeWorktree(),
      makeWorktree({ id: "w2", repo_url: "https://example.com/repoB.git" }),
    ];
    fileLists = [
      makeFiles(),
      makeFiles({
        worktree_id: "w2",
        repo_url: "https://example.com/repoB.git",
        paths: ["lib/beta.go"],
      }),
    ];
    renderPalette();
    pressCmdP();

    fireEvent.change(paletteInput(), { target: { value: "beta" } });
    const row = screen.getByTitle("lib/beta.go");
    expect(row).toHaveTextContent("repoB");
  });

  it("shows an indexing hint while the workspace has no file list yet", () => {
    worktrees = [makeWorktree()];
    filesLoading = true;
    renderPalette();
    pressCmdP();

    expect(screen.getByText(/Indexing workspace files/)).toBeInTheDocument();
  });

  it("shows the no-files state once an empty file list has arrived", () => {
    worktrees = [makeWorktree()];
    fileLists = [makeFiles({ paths: [] })];
    renderPalette();
    pressCmdP();

    expect(screen.getByText("No files in this workspace.")).toBeInTheDocument();
  });

  it("notes truncation on capped file lists", () => {
    worktrees = [makeWorktree()];
    fileLists = [makeFiles({ truncated: true })];
    renderPalette();
    pressCmdP();

    expect(screen.getByText(/search covers the first/i)).toBeInTheDocument();
  });
});

// @vitest-environment jsdom
import "@testing-library/jest-dom/vitest";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import type { IssueWorktree } from "./types";

// Mutable fixture the mocked worktrees query reads from, reset per test.
let worktrees: IssueWorktree[] = [];

vi.mock("./queries", () => ({
  useIssueWorktrees: () => ({ data: worktrees, isLoading: false }),
}));

// The editor and diff view drag in CodeMirror (jsdom-hostile measurement
// APIs); the strip tests only care that the right panel mounts for the right
// tab.
vi.mock("./worktree-file-editor", () => ({
  WorktreeFileEditor: ({ worktreeId, path }: { worktreeId: string; path: string }) => (
    <div data-testid={`editor:${worktreeId}:${path}`}>editor for {path}</div>
  ),
}));
vi.mock("./worktree-diff-view", () => ({
  WorktreeDiffView: ({ worktreeId, path }: { worktreeId: string; path: string }) => (
    <div data-testid={`diff:${worktreeId}:${path}`}>diff for {path}</div>
  ),
}));

import { IssueFileTabsProvider, useIssueFileTabs } from "./issue-file-tabs-context";
import { WorktreeFileTabs } from "./worktree-file-tabs";
import { fileTabKey } from "./file-tab-state";

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

// Probe standing in for the Project tree / Changes list / mode toggle: opens
// files and diffs, switches mode, and flags dirty state through the same
// context they use.
function Probe() {
  const { openFile, openDiff, setTabMode, setDirty } = useIssueFileTabs();
  return (
    <>
      <button onClick={() => openFile("w1", "src/main.ts")}>probe-open-main</button>
      <button onClick={() => openFile("w1", "README.md")}>probe-open-readme</button>
      <button onClick={() => openDiff("w1", "src/main.ts")}>probe-open-diff-main</button>
      <button onClick={() => setTabMode("w1", "src/main.ts", "edit")}>probe-mode-edit</button>
      <button onClick={() => setDirty(fileTabKey("w1", "src/main.ts"), true)}>probe-dirty</button>
    </>
  );
}

function renderTabs(issueId = "i1") {
  return render(
    <IssueFileTabsProvider issueId={issueId}>
      <WorktreeFileTabs issueId={issueId}>
        <div data-testid="issue-body">conversation</div>
      </WorktreeFileTabs>
      <Probe />
    </IssueFileTabsProvider>,
  );
}

beforeEach(() => {
  worktrees = [];
});

afterEach(() => cleanup());

describe("WorktreeFileTabs", () => {
  it("renders no strip when the issue has no workspace", () => {
    renderTabs();
    expect(screen.queryByRole("tablist")).not.toBeInTheDocument();
    expect(screen.getByTestId("issue-body")).toBeInTheDocument();
  });

  it("shows the pinned Issue tab (no close affordance) once a workspace exists", () => {
    worktrees = [makeWorktree()];
    renderTabs();

    const issueTab = screen.getByRole("tab", { name: "Issue" });
    expect(issueTab).toHaveAttribute("aria-selected", "true");
    expect(screen.queryByLabelText(/^Close/)).not.toBeInTheDocument();
  });

  it("opens a file in a new active tab and keeps the issue view mounted but hidden", () => {
    worktrees = [makeWorktree()];
    renderTabs();

    fireEvent.click(screen.getByText("probe-open-main"));

    expect(screen.getByRole("tab", { name: /main\.ts/ })).toHaveAttribute("aria-selected", "true");
    expect(screen.getByTestId("editor:w1:src/main.ts")).toBeInTheDocument();
    // Issue content stays mounted (CSS-hidden) so its state survives.
    const body = screen.getByTestId("issue-body");
    expect(body).toBeInTheDocument();
    expect(body.parentElement).toHaveClass("hidden");

    // Re-opening the same file focuses the existing tab instead of duplicating.
    fireEvent.click(screen.getByText("probe-open-main"));
    expect(screen.getAllByRole("tab")).toHaveLength(2); // Issue + main.ts
  });

  it("switches back to the conversation via the pinned Issue tab", () => {
    worktrees = [makeWorktree()];
    renderTabs();

    fireEvent.click(screen.getByText("probe-open-main"));
    fireEvent.click(screen.getByRole("tab", { name: "Issue" }));

    expect(screen.getByRole("tab", { name: "Issue" })).toHaveAttribute("aria-selected", "true");
    expect(screen.getByTestId("issue-body").parentElement).not.toHaveClass("hidden");
    // The editor stays mounted (hidden) so its buffer survives the switch.
    expect(screen.getByTestId("editor:w1:src/main.ts")).toBeInTheDocument();
  });

  it("closes a file tab and falls back to the neighbor, then the Issue tab", () => {
    worktrees = [makeWorktree()];
    renderTabs();

    fireEvent.click(screen.getByText("probe-open-main"));
    fireEvent.click(screen.getByText("probe-open-readme"));
    expect(screen.getByRole("tab", { name: /README\.md/ })).toHaveAttribute("aria-selected", "true");

    fireEvent.click(screen.getByLabelText("Close README.md"));
    // Left neighbor becomes active.
    expect(screen.getByRole("tab", { name: /main\.ts/ })).toHaveAttribute("aria-selected", "true");
    expect(screen.queryByTestId("editor:w1:README.md")).not.toBeInTheDocument();

    fireEvent.click(screen.getByLabelText("Close main.ts"));
    expect(screen.getByRole("tab", { name: "Issue" })).toHaveAttribute("aria-selected", "true");
    expect(screen.getByTestId("issue-body").parentElement).not.toHaveClass("hidden");
  });

  it("switches an open file tab to diff mode in place, keeping the editor mounted", () => {
    worktrees = [makeWorktree()];
    renderTabs();

    fireEvent.click(screen.getByText("probe-open-main"));
    expect(screen.getByTestId("editor:w1:src/main.ts")).toBeInTheDocument();

    // Opening the same file's diff converts the tab — no second tab appears.
    fireEvent.click(screen.getByText("probe-open-diff-main"));
    expect(screen.getAllByRole("tab")).toHaveLength(2); // Issue + main.ts
    expect(screen.getByRole("tab", { name: /main\.ts/ })).toHaveAttribute("title", expect.stringMatching(/^Diff: /));
    expect(screen.getByTestId("diff:w1:src/main.ts")).toBeInTheDocument();
    // The editor stays mounted (CSS-hidden) so its buffer survives the switch.
    const editor = screen.getByTestId("editor:w1:src/main.ts");
    expect(editor).toBeInTheDocument();
    expect(editor.parentElement).toHaveClass("hidden");

    // Toggling back to edit mode reveals the same editor again.
    fireEvent.click(screen.getByText("probe-mode-edit"));
    expect(screen.getByTestId("editor:w1:src/main.ts").parentElement).not.toHaveClass("hidden");
    expect(screen.getByTestId("diff:w1:src/main.ts").parentElement).toHaveClass("hidden");

    // Closing the tab removes both mode panels.
    fireEvent.click(screen.getByLabelText("Close main.ts"));
    expect(screen.queryByTestId("editor:w1:src/main.ts")).not.toBeInTheDocument();
    expect(screen.queryByTestId("diff:w1:src/main.ts")).not.toBeInTheDocument();
  });

  it("marks a tab with a dirty dot when its editor reports unsaved changes", () => {
    worktrees = [makeWorktree()];
    renderTabs();

    fireEvent.click(screen.getByText("probe-open-main"));
    expect(screen.queryByLabelText("Unsaved changes")).not.toBeInTheDocument();

    fireEvent.click(screen.getByText("probe-dirty"));
    expect(screen.getByLabelText("Unsaved changes")).toBeInTheDocument();
  });

  it("drops tabs whose worktree disappeared (workspace cleaned up)", () => {
    worktrees = [makeWorktree()];
    const view = renderTabs();

    fireEvent.click(screen.getByText("probe-open-main"));
    expect(screen.getByTestId("editor:w1:src/main.ts")).toBeInTheDocument();

    worktrees = [];
    view.rerender(
      <IssueFileTabsProvider issueId="i1">
        <WorktreeFileTabs issueId="i1">
          <div data-testid="issue-body">conversation</div>
        </WorktreeFileTabs>
        <Probe />
      </IssueFileTabsProvider>,
    );

    expect(screen.queryByTestId("editor:w1:src/main.ts")).not.toBeInTheDocument();
    expect(screen.getByTestId("issue-body").parentElement).not.toHaveClass("hidden");
  });
});

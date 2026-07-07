// @vitest-environment jsdom
import "@testing-library/jest-dom/vitest";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import type { IssueWorktree, WorktreeChanges, WorktreeFileList } from "./types";

// Mutable fixtures the mocked hooks read from, reset per test.
let worktrees: IssueWorktree[] = [];
let fileLists: WorktreeFileList[] = [];
let changeLists: WorktreeChanges[] = [];

vi.mock("./queries", () => ({
  useIssueWorktrees: () => ({ data: worktrees, isLoading: false }),
  useIssueWorktreeFiles: () => ({ data: fileLists, isLoading: false }),
  useIssueWorktreeChanges: () => ({ data: changeLists, isLoading: false }),
}));
vi.mock("./use-worktree-realtime", () => ({
  useWorktreeRealtime: () => {},
  useWorktreeFilesRealtime: () => {},
}));

import { IssueSidebarTabs } from "./issue-sidebar-tabs";

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
    paths: ["README.md", "src/main.ts"],
    truncated: false,
    updated_at: "",
    ...over,
  };
}

function makeChanges(over: Partial<WorktreeChanges> = {}): WorktreeChanges {
  return {
    worktree_id: "w1",
    issue_id: "i1",
    repo_url: "https://example.com/repoA.git",
    digest: "c1",
    base: "origin/main",
    files: [
      { path: "src/main.ts", status: "modified", additions: 3, deletions: 1, uncommitted: true },
      { path: "src/new.ts", status: "added", additions: 10, deletions: 0 },
    ],
    truncated: false,
    updated_at: "",
    ...over,
  };
}

beforeEach(() => {
  worktrees = [];
  fileLists = [];
  changeLists = [];
});

afterEach(() => cleanup());

describe("IssueSidebarTabs", () => {
  it("shows Details by default and does not mount the Project tree", () => {
    render(
      <IssueSidebarTabs issueId="i1">
        <div data-testid="details-body">details content</div>
      </IssueSidebarTabs>,
    );

    expect(screen.getByRole("tab", { name: "Details" })).toHaveAttribute("aria-selected", "true");
    expect(screen.getByRole("tab", { name: "All files" })).toHaveAttribute("aria-selected", "false");
    expect(screen.getByTestId("details-body")).toBeInTheDocument();
    // Lazy: no Project content (not even its empty state) before first activation.
    expect(screen.queryByText(/No workspace for this issue/)).not.toBeInTheDocument();
  });

  it("switches to the Project tab and shows the workspace empty state when no worktrees", () => {
    render(
      <IssueSidebarTabs issueId="i1">
        <div data-testid="details-body">details content</div>
      </IssueSidebarTabs>,
    );

    fireEvent.click(screen.getByRole("tab", { name: "All files" }));

    expect(screen.getByRole("tab", { name: "All files" })).toHaveAttribute("aria-selected", "true");
    expect(screen.getByText(/No workspace for this issue/)).toBeInTheDocument();
    // Details stays mounted (hidden), so its state/DOM survive the switch.
    const details = screen.getByTestId("details-body");
    expect(details).toBeInTheDocument();
    expect(details.parentElement).toHaveClass("hidden");
  });

  it("renders the file tree on the Project tab and keeps it mounted after switching back", () => {
    worktrees = [makeWorktree()];
    fileLists = [makeFiles()];
    render(
      <IssueSidebarTabs issueId="i1">
        <div data-testid="details-body">details content</div>
      </IssueSidebarTabs>,
    );

    fireEvent.click(screen.getByRole("tab", { name: "All files" }));
    expect(screen.getByText("repoA")).toBeInTheDocument();
    expect(screen.getByText("README.md")).toBeInTheDocument();

    fireEvent.click(screen.getByRole("tab", { name: "Details" }));
    expect(screen.getByTestId("details-body").parentElement).not.toHaveClass("hidden");
    // Tree stays mounted (hidden) so expansion + live updates survive.
    expect(screen.getByText("README.md")).toBeInTheDocument();
  });

  it("shows the change count on the Changes tab without activating it", () => {
    worktrees = [makeWorktree()];
    changeLists = [makeChanges()];
    render(
      <IssueSidebarTabs issueId="i1">
        <div>details</div>
      </IssueSidebarTabs>,
    );

    const changesTab = screen.getByRole("tab", { name: /Changes/ });
    expect(changesTab).toHaveTextContent("2");
    // Lazy: the list itself is not mounted before first activation.
    expect(screen.queryByText("main.ts")).not.toBeInTheDocument();
  });

  it("hides the count badge when there are no changes", () => {
    worktrees = [makeWorktree()];
    changeLists = [makeChanges({ files: [] })];
    render(
      <IssueSidebarTabs issueId="i1">
        <div>details</div>
      </IssueSidebarTabs>,
    );
    expect(screen.getByRole("tab", { name: "Changes" })).toHaveTextContent(/^Changes$/);
  });

  it("renders the changes list on the Changes tab and keeps it mounted after switching back", () => {
    worktrees = [makeWorktree()];
    changeLists = [makeChanges()];
    render(
      <IssueSidebarTabs issueId="i1">
        <div data-testid="details-body">details content</div>
      </IssueSidebarTabs>,
    );

    fireEvent.click(screen.getByRole("tab", { name: /Changes/ }));
    expect(screen.getByText("main.ts")).toBeInTheDocument();
    expect(screen.getByText("new.ts")).toBeInTheDocument();

    fireEvent.click(screen.getByRole("tab", { name: "Details" }));
    // List stays mounted (hidden) so live updates survive tab switches.
    expect(screen.getByText("main.ts")).toBeInTheDocument();
  });
});

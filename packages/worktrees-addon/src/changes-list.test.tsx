// @vitest-environment jsdom
import "@testing-library/jest-dom/vitest";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import type { IssueWorktree, WorktreeChanges } from "./types";

// Mutable fixtures the mocked hooks read from, reset per test.
let worktrees: IssueWorktree[] = [];
let changeLists: WorktreeChanges[] = [];

vi.mock("./queries", () => ({
  useIssueWorktrees: () => ({ data: worktrees, isLoading: false }),
  useIssueWorktreeChanges: () => ({ data: changeLists, isLoading: false }),
}));

const openDiff = vi.fn();
vi.mock("./issue-file-tabs-context", () => ({
  useIssueFileTabs: () => ({ openDiff }),
}));

import { ChangesList } from "./changes-list";

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

function makeChanges(over: Partial<WorktreeChanges> = {}): WorktreeChanges {
  return {
    worktree_id: "w1",
    issue_id: "i1",
    repo_url: "https://example.com/repoA.git",
    digest: "c1",
    base: "origin/main",
    files: [
      { path: "src/app/main.ts", status: "modified", additions: 53, deletions: 30, uncommitted: true },
      { path: "src/new.ts", status: "added", additions: 130, deletions: 0 },
      { path: "old.txt", status: "deleted", additions: 0, deletions: 12 },
      { path: "logo.png", status: "added", additions: 0, deletions: 0, binary: true },
    ],
    truncated: false,
    updated_at: "",
    ...over,
  };
}

beforeEach(() => {
  worktrees = [];
  changeLists = [];
  openDiff.mockClear();
});

afterEach(() => cleanup());

describe("ChangesList", () => {
  it("renders one row per changed file with counts, status markers, and the U flag", () => {
    worktrees = [makeWorktree()];
    changeLists = [makeChanges()];
    render(<ChangesList issueId="i1" />);

    // Dimmed dir prefix + bright basename.
    expect(screen.getByText("src/app/")).toBeInTheDocument();
    expect(screen.getByText("main.ts")).toBeInTheDocument();
    expect(screen.getByText("+53")).toBeInTheDocument();
    expect(screen.getByText("−30")).toBeInTheDocument();
    // Uncommitted marker only on the file that still differs from HEAD.
    expect(screen.getAllByTitle("Has uncommitted changes")).toHaveLength(1);
    // Status markers by kind.
    expect(screen.getByLabelText("Modified")).toBeInTheDocument();
    expect(screen.getAllByLabelText("Added")).toHaveLength(2);
    expect(screen.getByLabelText("Deleted")).toBeInTheDocument();
    // Binary files carry no line counts.
    expect(screen.queryByText("+0")).not.toBeInTheDocument();
  });

  it("opens a clicked file's diff with its worktree + full path", () => {
    worktrees = [makeWorktree()];
    changeLists = [makeChanges()];
    render(<ChangesList issueId="i1" />);

    fireEvent.click(screen.getByRole("button", { name: /main\.ts/ }));
    expect(openDiff).toHaveBeenCalledWith("w1", "src/app/main.ts");

    fireEvent.click(screen.getByRole("button", { name: /old\.txt/ }));
    expect(openDiff).toHaveBeenCalledWith("w1", "old.txt");
  });

  it("shows a scanning hint for a ready worktree whose status has not arrived", () => {
    worktrees = [makeWorktree()];
    render(<ChangesList issueId="i1" />);
    expect(screen.getByText(/Scanning for changes/)).toBeInTheDocument();
  });

  it("shows checkout progress for a not-yet-ready worktree", () => {
    worktrees = [makeWorktree({ status: "initializing" })];
    render(<ChangesList issueId="i1" />);
    expect(screen.getByText(/Checking out/)).toBeInTheDocument();
  });

  it("shows an empty state for a clean workspace", () => {
    worktrees = [makeWorktree()];
    changeLists = [makeChanges({ files: [] })];
    render(<ChangesList issueId="i1" />);
    expect(screen.getByText("No changes yet.")).toBeInTheDocument();
  });

  it("shows the workspace empty state when there are no worktrees", () => {
    render(<ChangesList issueId="i1" />);
    expect(screen.getByText(/No workspace for this issue/)).toBeInTheDocument();
  });

  it("notes truncation on capped lists", () => {
    worktrees = [makeWorktree()];
    changeLists = [makeChanges({ truncated: true })];
    render(<ChangesList issueId="i1" />);
    expect(screen.getByText(/showing the first/i)).toBeInTheDocument();
  });

  it("sorts rows alphabetically (case-insensitive) regardless of report order", () => {
    worktrees = [makeWorktree()];
    changeLists = [
      makeChanges({
        files: [
          { path: "src/zeta.ts", status: "modified", additions: 1, deletions: 1 },
          { path: "README.md", status: "modified", additions: 2, deletions: 0 },
          { path: "apps/web/page.tsx", status: "added", additions: 5, deletions: 0 },
          { path: "NEW/inner.txt", status: "added", additions: 1, deletions: 0 },
        ],
      }),
    ];
    render(<ChangesList issueId="i1" />);

    // Single-repo mode renders rows flat — every button is a change row.
    const titles = screen.getAllByRole("button").map((b) => b.getAttribute("title"));
    expect(titles).toEqual([
      "apps/web/page.tsx",
      "NEW/inner.txt",
      "README.md",
      "src/zeta.ts",
    ]);
  });

  it("groups rows per repo section on multi-repo issues", () => {
    worktrees = [
      makeWorktree(),
      makeWorktree({ id: "w2", repo_url: "https://example.com/repoB.git" }),
    ];
    changeLists = [
      makeChanges(),
      makeChanges({
        worktree_id: "w2",
        repo_url: "https://example.com/repoB.git",
        files: [{ path: "b.txt", status: "modified", additions: 1, deletions: 1 }],
      }),
    ];
    render(<ChangesList issueId="i1" />);
    expect(screen.getByText("repoA")).toBeInTheDocument();
    expect(screen.getByText("repoB")).toBeInTheDocument();
    expect(screen.getByText("b.txt")).toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: /b\.txt/ }));
    expect(openDiff).toHaveBeenCalledWith("w2", "b.txt");
  });
});

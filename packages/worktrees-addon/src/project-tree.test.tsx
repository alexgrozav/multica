// @vitest-environment jsdom
import "@testing-library/jest-dom/vitest";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import type { IssueWorktree, WorktreeFileList } from "./types";

// Mutable fixtures the mocked hooks read from, reset per test.
let worktrees: IssueWorktree[] = [];
let fileLists: WorktreeFileList[] = [];

vi.mock("./queries", () => ({
  useIssueWorktrees: () => ({ data: worktrees, isLoading: false }),
  useIssueWorktreeFiles: () => ({ data: fileLists, isLoading: false }),
}));

const openFile = vi.fn();
vi.mock("./issue-file-tabs-context", () => ({
  useIssueFileTabs: () => ({ openFile }),
}));

import { ProjectTree } from "./project-tree";

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
    paths: ["README.md", "src/app/main.ts", "src/util.ts"],
    truncated: false,
    updated_at: "",
    ...over,
  };
}

beforeEach(() => {
  worktrees = [];
  fileLists = [];
  openFile.mockClear();
});

afterEach(() => cleanup());

describe("ProjectTree", () => {
  it("renders root entries and lazily expands directories on click", () => {
    worktrees = [makeWorktree()];
    fileLists = [makeFiles()];
    render(<ProjectTree issueId="i1" />);

    // Repo header + root-level entries.
    expect(screen.getByText("repoA")).toBeInTheDocument();
    expect(screen.getByText("3 files")).toBeInTheDocument();
    expect(screen.getByText("README.md")).toBeInTheDocument();
    expect(screen.getByText("src")).toBeInTheDocument();
    // Children of a collapsed dir are not mounted.
    expect(screen.queryByText("util.ts")).not.toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: /^src$/ }));
    expect(screen.getByText("util.ts")).toBeInTheDocument();
    expect(screen.getByText("app")).toBeInTheDocument();
    expect(screen.queryByText("main.ts")).not.toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: /^app$/ }));
    expect(screen.getByText("main.ts")).toBeInTheDocument();

    // Collapse unmounts the subtree again.
    fireEvent.click(screen.getByRole("button", { name: /^src/ }));
    expect(screen.queryByText("main.ts")).not.toBeInTheDocument();
  });

  it("shows an indexing hint for a ready worktree whose list has not arrived", () => {
    worktrees = [makeWorktree()];
    render(<ProjectTree issueId="i1" />);
    expect(screen.getByText(/Indexing files/)).toBeInTheDocument();
  });

  it("shows checkout progress for a not-yet-ready worktree", () => {
    worktrees = [makeWorktree({ status: "initializing" })];
    render(<ProjectTree issueId="i1" />);
    expect(screen.getByText(/Checking out/)).toBeInTheDocument();
  });

  it("notes truncation on capped lists", () => {
    worktrees = [makeWorktree()];
    fileLists = [makeFiles({ truncated: true })];
    render(<ProjectTree issueId="i1" />);
    expect(screen.getByText(/showing the first/i)).toBeInTheDocument();
  });

  it("opens a clicked file in the content tabs with its worktree + full path", () => {
    worktrees = [makeWorktree()];
    fileLists = [makeFiles()];
    render(<ProjectTree issueId="i1" />);

    fireEvent.click(screen.getByRole("button", { name: /^src$/ }));
    fireEvent.click(screen.getByRole("button", { name: /^app$/ }));
    fireEvent.click(screen.getByRole("button", { name: /main\.ts/ }));

    expect(openFile).toHaveBeenCalledWith("w1", "src/app/main.ts");

    fireEvent.click(screen.getByRole("button", { name: /README\.md/ }));
    expect(openFile).toHaveBeenCalledWith("w1", "README.md");
  });

  it("renders one tree per repo worktree", () => {
    worktrees = [
      makeWorktree(),
      makeWorktree({ id: "w2", repo_url: "https://example.com/repoB.git" }),
    ];
    fileLists = [
      makeFiles(),
      makeFiles({ worktree_id: "w2", repo_url: "https://example.com/repoB.git", paths: ["b.txt"] }),
    ];
    render(<ProjectTree issueId="i1" />);
    expect(screen.getByText("repoA")).toBeInTheDocument();
    expect(screen.getByText("repoB")).toBeInTheDocument();
    expect(screen.getByText("b.txt")).toBeInTheDocument();
  });
});

// @vitest-environment jsdom
import "@testing-library/jest-dom/vitest";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";
import type { WorktreeChanges, WorktreeFileDiff } from "./types";

// Mutable fixtures the mocked queries read from, reset per test. These tests
// cover the non-CodeMirror bodies (loading / error / binary / huge) and the
// toolbar; the merge-view mount itself is exercised in the real app, not jsdom.
let diffState: {
  data?: WorktreeFileDiff;
  isLoading?: boolean;
  isError?: boolean;
  error?: Error;
} = {};
let changeLists: WorktreeChanges[] = [];

vi.mock("./queries", () => ({
  useWorktreeFileDiff: () => ({
    data: diffState.data,
    isLoading: diffState.isLoading ?? false,
    isError: diffState.isError ?? false,
    error: diffState.error,
    refetch: vi.fn().mockResolvedValue({ data: diffState.data }),
    isRefetching: false,
  }),
  useIssueWorktreeChanges: () => ({ data: changeLists, isLoading: false }),
  useIssueWorktrees: () => ({ data: [], isLoading: false }),
}));

// The line-comment composer posts through the host's comment mutation; the
// hook needs a QueryClient, so it is stubbed out here.
vi.mock("@multica/core/issues/mutations", () => ({
  useCreateComment: () => ({ mutateAsync: vi.fn(), isPending: false }),
}));

import { WorktreeDiffView } from "./worktree-diff-view";

function renderDiff() {
  return render(<WorktreeDiffView issueId="i1" worktreeId="w1" path="src/main.ts" />);
}

function makeDiff(over: Partial<WorktreeFileDiff> = {}): WorktreeFileDiff {
  return {
    worktree_id: "w1",
    path: "src/main.ts",
    old_content: "a\n",
    new_content: "b\n",
    truncated: false,
    binary: false,
    ...over,
  };
}

beforeEach(() => {
  diffState = {};
  changeLists = [];
});

afterEach(() => cleanup());

describe("WorktreeDiffView states", () => {
  it("shows a loading hint with the path in the toolbar", () => {
    diffState = { isLoading: true };
    renderDiff();
    expect(screen.getByText(/Loading diff of main\.ts/)).toBeInTheDocument();
    expect(screen.getByTitle("src/main.ts")).toBeInTheDocument();
  });

  it("shows the load error with a Retry action", () => {
    diffState = { isError: true, error: new Error("owning machine did not respond") };
    renderDiff();
    expect(screen.getByText("owning machine did not respond")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Retry" })).toBeInTheDocument();
  });

  it("refuses binary files", () => {
    diffState = { data: makeDiff({ binary: true }) };
    renderDiff();
    expect(screen.getByText(/Binary file/)).toBeInTheDocument();
  });

  it("refuses oversized files", () => {
    diffState = { data: makeDiff({ truncated: true }) };
    renderDiff();
    expect(screen.getByText(/larger than 1 MB/)).toBeInTheDocument();
  });

  it("shows the file's counts and base in the toolbar from the changes list", () => {
    diffState = { isLoading: true };
    changeLists = [
      {
        worktree_id: "w1",
        issue_id: "i1",
        repo_url: "https://example.com/repoA.git",
        digest: "c1",
        base: "origin/main",
        files: [{ path: "src/main.ts", status: "modified", additions: 5, deletions: 2 }],
        truncated: false,
        updated_at: "",
      },
    ];
    renderDiff();
    expect(screen.getByText("+5")).toBeInTheDocument();
    expect(screen.getByText("−2")).toBeInTheDocument();
    expect(screen.getByTitle("src/main.ts — diff vs origin/main")).toBeInTheDocument();
    // The Edit/Diff toggle renders with Diff active.
    expect(screen.getByRole("button", { name: "Diff" })).toHaveAttribute("aria-pressed", "true");
    expect(screen.getByRole("button", { name: "Edit" })).toHaveAttribute("aria-pressed", "false");
  });

  it("hides the mode toggle for a deleted file (nothing to edit)", () => {
    diffState = { isLoading: true };
    changeLists = [
      {
        worktree_id: "w1",
        issue_id: "i1",
        repo_url: "https://example.com/repoA.git",
        digest: "c1",
        base: "origin/main",
        files: [{ path: "src/main.ts", status: "deleted", additions: 0, deletions: 9 }],
        truncated: false,
        updated_at: "",
      },
    ];
    renderDiff();
    expect(screen.queryByRole("group", { name: "View mode" })).not.toBeInTheDocument();
  });
});

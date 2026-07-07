// @vitest-environment jsdom
import "@testing-library/jest-dom/vitest";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";
import type { WorktreeChanges, WorktreeFileContent } from "./types";

// Mutable fixtures the mocked queries read from, reset per test. These tests
// cover the non-editor bodies (loading / error / binary / huge) and the
// toolbar; the CodeMirror mount itself is exercised in the real app, not jsdom.
let fileState: {
  data?: WorktreeFileContent;
  isLoading?: boolean;
  isError?: boolean;
  error?: Error;
} = {};
let changeLists: WorktreeChanges[] = [];

const useWorktreeFileDiffMock = vi.fn(() => ({ data: undefined }));

vi.mock("./queries", () => ({
  useWorktreeFileContent: () => ({
    data: fileState.data,
    isLoading: fileState.isLoading ?? false,
    isError: fileState.isError ?? false,
    error: fileState.error,
    refetch: vi.fn().mockResolvedValue({ data: fileState.data }),
    isRefetching: false,
  }),
  useSaveWorktreeFile: () => ({ mutate: vi.fn(), isPending: false }),
  useIssueWorktreeChanges: () => ({ data: changeLists, isLoading: false }),
  useWorktreeFileDiff: (...args: unknown[]) => useWorktreeFileDiffMock(...(args as [])),
}));

import { WorktreeFileEditor } from "./worktree-file-editor";

function renderEditor() {
  return render(
    <WorktreeFileEditor issueId="i1" worktreeId="w1" path="src/main.ts" tabKey="file:w1:src/main.ts" />,
  );
}

function makeContent(over: Partial<WorktreeFileContent> = {}): WorktreeFileContent {
  return {
    worktree_id: "w1",
    path: "src/main.ts",
    content: "",
    size: 0,
    truncated: false,
    binary: false,
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
    files: [{ path: "src/main.ts", status: "modified", additions: 1, deletions: 1 }],
    truncated: false,
    updated_at: "",
    ...over,
  };
}

beforeEach(() => {
  fileState = {};
  changeLists = [];
  useWorktreeFileDiffMock.mockClear();
});

afterEach(() => cleanup());

describe("WorktreeFileEditor states", () => {
  it("shows a loading hint (path in the toolbar, Save disabled)", () => {
    fileState = { isLoading: true };
    renderEditor();
    expect(screen.getByText(/Opening main\.ts/)).toBeInTheDocument();
    expect(screen.getByTitle("src/main.ts")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /Save/ })).toBeDisabled();
  });

  it("shows the load error with a Retry action", () => {
    fileState = { isError: true, error: new Error("owning machine did not respond") };
    renderEditor();
    expect(screen.getByText("owning machine did not respond")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Retry" })).toBeInTheDocument();
  });

  it("refuses binary files", () => {
    fileState = { data: makeContent({ binary: true, size: 5120 }) };
    renderEditor();
    expect(screen.getByText(/Binary file/)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /Save/ })).toBeDisabled();
  });

  it("refuses oversized files", () => {
    fileState = { data: makeContent({ truncated: true, size: 5 << 20 }) };
    renderEditor();
    expect(screen.getByText(/larger than 1 MB/)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /Save/ })).toBeDisabled();
  });

  it("shows the Edit/Diff toggle only when the file has changes", () => {
    fileState = { isLoading: true };
    renderEditor();
    expect(screen.queryByRole("group", { name: "View mode" })).not.toBeInTheDocument();
    cleanup();

    changeLists = [makeChanges()];
    renderEditor();
    expect(screen.getByRole("group", { name: "View mode" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Edit" })).toHaveAttribute("aria-pressed", "true");
    expect(screen.getByRole("button", { name: "Diff" })).toHaveAttribute("aria-pressed", "false");
  });

  it("hides the toggle when only OTHER files have changes", () => {
    fileState = { isLoading: true };
    changeLists = [
      makeChanges({ files: [{ path: "src/other.ts", status: "added", additions: 1, deletions: 0 }] }),
    ];
    renderEditor();
    expect(screen.queryByRole("group", { name: "View mode" })).not.toBeInTheDocument();
  });

  it("fetches the quick-diff base only when the file has changes", () => {
    fileState = { isLoading: true };
    renderEditor();
    expect(useWorktreeFileDiffMock).toHaveBeenLastCalledWith("i1", "w1", "src/main.ts", false);
    cleanup();

    changeLists = [makeChanges()];
    renderEditor();
    expect(useWorktreeFileDiffMock).toHaveBeenLastCalledWith("i1", "w1", "src/main.ts", true);
  });
});

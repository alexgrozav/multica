// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import "@testing-library/jest-dom/vitest";
import type { IssueWorktree } from "./types";

// Control the worktree list + capture the open mutation without a real backend.
const mockMutate = vi.fn();
let worktrees: IssueWorktree[] = [];
vi.mock("./queries", () => ({
  useIssueWorktrees: () => ({ data: worktrees }),
  useOpenIssueWorktree: () => ({ mutate: mockMutate }),
}));
vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn(), message: vi.fn() } }));

import { OpenInMenu } from "./open-in-menu";

afterEach(() => {
  cleanup();
  mockMutate.mockReset();
  worktrees = [];
  localStorage.clear();
});

function ready(over: Partial<IssueWorktree> = {}): IssueWorktree {
  return {
    id: "w1",
    issue_id: "i1",
    identifier: "ABC-1",
    workspace_id: "ws1",
    repo_url: "https://example.com/repo.git",
    path: "/root/ws/worktrees/abc/repo",
    working_dir: "/root/ws/worktrees/abc",
    branch: "ABC-1",
    status: "ready",
    setup_status: "none",
    has_setup: false,
    has_cleanup: false,
    runs: [],
    created_at: "",
    updated_at: "",
    ...over,
  };
}

describe("OpenInMenu", () => {
  it("renders nothing until a worktree is checked out (ready)", () => {
    worktrees = [];
    const { container } = render(<OpenInMenu issueId="i1" />);
    expect(container).toBeEmptyDOMElement();
  });

  it("stays hidden while the only worktree is still pending", () => {
    worktrees = [ready({ status: "pending", path: "", working_dir: "" })];
    const { container } = render(<OpenInMenu issueId="i1" />);
    expect(container).toBeEmptyDOMElement();
  });

  it("opens the default target (Finder) from the main split button", async () => {
    worktrees = [ready()];
    render(<OpenInMenu issueId="i1" />);
    await userEvent.click(screen.getByLabelText("Open in Finder"));
    expect(mockMutate).toHaveBeenCalledWith("finder", expect.anything());
  });

  it("still shows when only a repo path is present (derives the parent dir)", () => {
    worktrees = [ready({ working_dir: undefined })];
    render(<OpenInMenu issueId="i1" />);
    expect(screen.getByLabelText("Open in Finder")).toBeInTheDocument();
  });
});

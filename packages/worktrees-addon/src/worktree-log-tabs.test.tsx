// @vitest-environment jsdom
import "@testing-library/jest-dom/vitest";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, within } from "@testing-library/react";
import type { IssueWorktree, WorktreeRunLog } from "./types";

// Mutable fixtures the mocked hooks read from, reset per test.
let worktrees: IssueWorktree[] = [];
let logLines: Record<string, WorktreeRunLog[]> = {};
const runMutate = vi.fn();
const setupMutate = vi.fn();
const stopMutate = vi.fn();

vi.mock("./queries", () => ({
  useIssueWorktrees: () => ({ data: worktrees }),
  useRunWorktreeScript: () => ({ mutate: runMutate, isPending: false }),
  useRunWorktreeSetup: () => ({ mutate: setupMutate, isPending: false }),
  useStopWorktreeScript: () => ({ mutate: stopMutate, isPending: false }),
  useWorktreeRunLog: (id?: string) => ({ data: id ? (logLines[id] ?? []) : [] }),
}));
vi.mock("./use-worktree-realtime", () => ({ useWorktreeRealtime: () => {} }));

import { WorktreeSidebarLayout } from "./worktree-sidebar-layout";
import { RunScriptsSection } from "./run-scripts-section";

function makeWorktree(over: Partial<IssueWorktree> = {}): IssueWorktree {
  return {
    id: "w1",
    issue_id: "i1",
    workspace_id: "ws1",
    repo_url: "https://example.com/repoA.git",
    path: "/wt/a",
    branch: "issue/x/a",
    status: "ready",
    setup_status: "succeeded",
    run_status: "idle",
    setup_task_id: "setup-1",
    run_task_id: undefined,
    has_run_script: true,
    has_setup_script: true,
    created_at: "",
    updated_at: "",
    ...over,
  };
}

// stacked=true avoids the resizable split (no ResizeObserver needs in jsdom);
// it still mounts the provider + RunScriptsSection (top) + WorktreeLogTabs (bottom).
function renderPanel() {
  return render(
    <WorktreeSidebarLayout issueId="i1" stacked>
      <RunScriptsSection issueId="i1" />
    </WorktreeSidebarLayout>,
  );
}

beforeEach(() => {
  worktrees = [];
  logLines = {};
  runMutate.mockClear();
  setupMutate.mockClear();
  stopMutate.mockClear();
});

// No vitest config in this package → no auto-cleanup; unmount between tests so
// renders don't accumulate in the shared jsdom document.
afterEach(() => cleanup());

describe("WorktreeLogTabs + RunScriptsSection integration", () => {
  it("auto-opens a Setup tab per repo and shows its streamed logs", () => {
    worktrees = [makeWorktree()];
    logLines = { "setup-1": [{ run_task_id: "setup-1", issue_id: "i1", worktree_id: "w1", seq: 1, stream: "stdout", content: "booting setup" }] };
    renderPanel();

    // The default Setup tab is present (labelled with the repo + "setup")...
    expect(screen.getByRole("tab", { name: /repoA\s*setup/i })).toBeInTheDocument();
    // ...and its panel shows the streamed setup log line.
    expect(screen.getByText("booting setup")).toBeInTheDocument();
  });

  it("opens a Run tab (and runs the script) when Run is clicked in the list", () => {
    worktrees = [makeWorktree()];
    renderPanel();

    // No run tab yet.
    expect(screen.queryByRole("tab", { name: /repoA\s*run/i })).not.toBeInTheDocument();

    // The list's Run action button runs the script and opens its tab.
    fireEvent.click(screen.getByRole("button", { name: "Run" }));
    expect(runMutate).toHaveBeenCalledWith("w1");
    expect(screen.getByRole("tab", { name: /repoA\s*run/i })).toBeInTheDocument();
  });

  it("closing a running Run tab stops the process and removes the tab", () => {
    worktrees = [makeWorktree({ run_status: "running", run_task_id: "run-1" })];
    renderPanel();

    // Open the run tab via its label (the action button shows Stop while running,
    // but the "Open run logs" label still opens the log tab).
    fireEvent.click(screen.getByRole("button", { name: "Open run logs" }));
    const runTab = screen.getByRole("tab", { name: /repoA\s*run/i });
    expect(runTab).toBeInTheDocument();

    // Closing the run tab calls stop and drops the tab.
    fireEvent.click(within(runTab).getByLabelText("Close tab"));
    expect(stopMutate).toHaveBeenCalledWith("w1");
    expect(screen.queryByRole("tab", { name: /repoA\s*run/i })).not.toBeInTheDocument();
  });

  it("closing a Setup tab does NOT stop anything and can be reopened from the list", () => {
    worktrees = [makeWorktree()];
    renderPanel();

    const setupTab = screen.getByRole("tab", { name: /repoA\s*setup/i });
    fireEvent.click(within(setupTab).getByLabelText("Close tab"));
    expect(stopMutate).not.toHaveBeenCalled();
    expect(screen.queryByRole("tab", { name: /repoA\s*setup/i })).not.toBeInTheDocument();

    // Clicking the "Setup" label in the list reopens the tab without re-running.
    fireEvent.click(screen.getByRole("button", { name: "Open setup logs" }));
    expect(setupMutate).not.toHaveBeenCalled();
    expect(screen.getByRole("tab", { name: /repoA\s*setup/i })).toBeInTheDocument();
  });

  it("renders the sidebar content unchanged when the issue has no worktrees", () => {
    worktrees = [];
    const { container } = render(
      <WorktreeSidebarLayout issueId="i1" stacked>
        <div data-testid="sidebar-body">content</div>
      </WorktreeSidebarLayout>,
    );
    expect(screen.getByTestId("sidebar-body")).toBeInTheDocument();
    // No tab UI mounted.
    expect(container.querySelector('[role="tab"]')).toBeNull();
  });
});

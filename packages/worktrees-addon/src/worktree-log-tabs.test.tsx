// @vitest-environment jsdom
import "@testing-library/jest-dom/vitest";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, within } from "@testing-library/react";
import type { IssueTerminal, IssueWorktree, WorktreeRunLog } from "./types";

// Mutable fixtures the mocked hooks read from, reset per test.
let worktrees: IssueWorktree[] = [];
let terminals: IssueTerminal[] = [];
let logLines: Record<string, WorktreeRunLog[]> = {};
const runMutate = vi.fn();
const setupMutate = vi.fn();
const stopMutate = vi.fn();
const closeTerminalMutate = vi.fn();
// The context passes per-call callbacks; the mock mimics a successful create.
const createTerminalMutate = vi.fn(
  (_vars: unknown, opts?: { onSuccess?: (t: IssueTerminal) => void }) => {
    const t = makeTerminal({ id: `t${terminals.length + 1}`, index: terminals.length + 1 });
    terminals = [...terminals, t];
    opts?.onSuccess?.(t);
  },
);

vi.mock("./queries", () => ({
  useIssueWorktrees: () => ({ data: worktrees }),
  useIssueTerminals: () => ({ data: terminals }),
  useRunWorktreeScript: () => ({ mutate: runMutate, isPending: false }),
  useRunWorktreeSetup: () => ({ mutate: setupMutate, isPending: false }),
  useStopWorktreeScript: () => ({ mutate: stopMutate, isPending: false }),
  useCreateTerminal: () => ({ mutate: createTerminalMutate, isPending: false }),
  useCloseTerminal: () => ({ mutate: closeTerminalMutate, isPending: false }),
  useWorktreeRunLog: (id?: string) => ({ data: id ? (logLines[id] ?? []) : [] }),
}));
vi.mock("./use-worktree-realtime", () => ({ useWorktreeRealtime: () => {} }));
// The real view needs xterm + a WebSocket; the tab integration only cares that
// the right session mounts.
vi.mock("./terminal-view", () => ({
  TerminalView: ({ terminalId }: { terminalId: string }) => (
    <div data-testid={`terminal-view-${terminalId}`} />
  ),
}));

import { WorktreeSidebarLayout } from "./worktree-sidebar-layout";
import { RunScriptsSection } from "./run-scripts-section";

function makeTerminal(over: Partial<IssueTerminal> = {}): IssueTerminal {
  return {
    id: "t1",
    issue_id: "i1",
    workspace_id: "ws1",
    index: 1,
    title: "",
    status: "open",
    created_at: "",
    ...over,
  };
}

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
    setup_task_id: "setup-1",
    has_setup: true,
    has_cleanup: true,
    runs: [{ name: "dev", status: "idle" }],
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
  terminals = [];
  logLines = {};
  runMutate.mockClear();
  setupMutate.mockClear();
  stopMutate.mockClear();
  createTerminalMutate.mockClear();
  closeTerminalMutate.mockClear();
});

// No vitest config in this package → no auto-cleanup; unmount between tests so
// renders don't accumulate in the shared jsdom document.
afterEach(() => cleanup());

describe("WorktreeLogTabs + RunScriptsSection integration", () => {
  it("auto-opens a Setup tab per repo and shows its streamed logs", () => {
    worktrees = [makeWorktree()];
    logLines = { "setup-1": [{ run_task_id: "setup-1", issue_id: "i1", worktree_id: "w1", seq: 1, stream: "stdout", content: "booting setup" }] };
    renderPanel();

    expect(screen.getByRole("tab", { name: /repoA\s*setup/i })).toBeInTheDocument();
    expect(screen.getByText("booting setup")).toBeInTheDocument();
  });

  it("opens a named Run tab (and runs that script) when Run is clicked in the list", () => {
    worktrees = [makeWorktree()];
    renderPanel();

    expect(screen.queryByRole("tab", { name: /repoA\s*dev/i })).not.toBeInTheDocument();

    // The list's Run action button runs the named script and opens its tab.
    fireEvent.click(screen.getByRole("button", { name: "Run" }));
    expect(runMutate).toHaveBeenCalledWith({ worktreeId: "w1", name: "dev" });
    expect(screen.getByRole("tab", { name: /repoA\s*dev/i })).toBeInTheDocument();
  });

  it("closing a running Run tab stops that named run and removes the tab", () => {
    worktrees = [makeWorktree({ runs: [{ name: "dev", status: "running", run_task_id: "run-1" }] })];
    renderPanel();

    // Open the run tab via its label (the action shows Stop while running, but
    // the "Open dev logs" label still opens the log tab).
    fireEvent.click(screen.getByRole("button", { name: "Open dev logs" }));
    const runTab = screen.getByRole("tab", { name: /repoA\s*dev/i });
    expect(runTab).toBeInTheDocument();

    fireEvent.click(within(runTab).getByLabelText("Close tab"));
    expect(stopMutate).toHaveBeenCalledWith({ worktreeId: "w1", name: "dev" });
    expect(screen.queryByRole("tab", { name: /repoA\s*dev/i })).not.toBeInTheDocument();
  });

  it("closing a Setup tab does NOT stop anything and can be reopened from the list", () => {
    worktrees = [makeWorktree()];
    renderPanel();

    const setupTab = screen.getByRole("tab", { name: /repoA\s*setup/i });
    fireEvent.click(within(setupTab).getByLabelText("Close tab"));
    expect(stopMutate).not.toHaveBeenCalled();
    expect(screen.queryByRole("tab", { name: /repoA\s*setup/i })).not.toBeInTheDocument();

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
    expect(container.querySelector('[role="tab"]')).toBeNull();
  });
});

describe("terminal tabs", () => {
  it("the + button opens a new terminal session as the active tab", () => {
    worktrees = [makeWorktree({ has_setup: false })];
    renderPanel();

    fireEvent.click(screen.getByRole("button", { name: "New terminal" }));
    expect(createTerminalMutate).toHaveBeenCalledTimes(1);
    const tab = screen.getByRole("tab", { name: /Terminal 1/ });
    expect(tab).toHaveAttribute("data-active");
    expect(screen.getByTestId("terminal-view-t1")).toBeInTheDocument();
  });

  it("offers the + button even when no tabs are open", () => {
    worktrees = [makeWorktree({ has_setup: false })];
    renderPanel();

    expect(screen.queryByRole("tab")).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "New terminal" })).toBeInTheDocument();
  });

  it("auto-opens tabs for live sessions and labels them with the foreground process", () => {
    worktrees = [makeWorktree({ has_setup: false })];
    terminals = [
      makeTerminal({ id: "t1", index: 1, title: "make" }),
      makeTerminal({ id: "t2", index: 2 }),
    ];
    renderPanel();

    expect(screen.getByRole("tab", { name: /Terminal 1 \(make\)/ })).toBeInTheDocument();
    expect(screen.getByRole("tab", { name: /Terminal 2/ })).toBeInTheDocument();
    expect(screen.getByTestId("terminal-view-t1")).toBeInTheDocument();
    expect(screen.getByTestId("terminal-view-t2")).toBeInTheDocument();
  });

  it("closing a terminal tab ends the session", () => {
    worktrees = [makeWorktree({ has_setup: false })];
    terminals = [makeTerminal({ id: "t1", index: 1 })];
    renderPanel();

    const tab = screen.getByRole("tab", { name: /Terminal 1/ });
    fireEvent.click(within(tab).getByLabelText("Close tab"));
    expect(closeTerminalMutate).toHaveBeenCalledWith("t1");
    expect(screen.queryByRole("tab", { name: /Terminal 1/ })).not.toBeInTheDocument();
  });

  it("keeps terminal panels mounted while other tabs are active", () => {
    worktrees = [makeWorktree()]; // has_setup → a Setup tab exists too
    terminals = [makeTerminal({ id: "t1", index: 1 })];
    renderPanel();

    // Activate the Setup tab; the terminal view must stay in the DOM.
    fireEvent.click(screen.getByRole("tab", { name: /repoA\s*setup/i }));
    expect(screen.getByTestId("terminal-view-t1")).toBeInTheDocument();
  });
});

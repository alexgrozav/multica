import { describe, expect, it } from "vitest";
import {
  addTab,
  nextActiveKey,
  reconcileSetupTabs,
  reconcileTerminalTabs,
  removeTab,
  tabKey,
  type OpenTab,
} from "./tab-state";

const wt = (id: string, has_setup = true) => ({ id, has_setup });

describe("reconcileSetupTabs", () => {
  it("seeds one setup tab per worktree with a setup script", () => {
    const next = reconcileSetupTabs([], [wt("a"), wt("b", false), wt("c")], new Set());
    expect(next.map((t) => tabKey(t.worktreeId, t.kind, t.name))).toEqual(["a:setup:", "c:setup:"]);
  });

  it("does not re-seed a setup tab the user closed", () => {
    const next = reconcileSetupTabs([], [wt("a")], new Set(["a:setup:"]));
    expect(next).toEqual([]);
  });

  it("keeps existing tabs (incl. named run tabs) and preserves identity when unchanged", () => {
    const prev: OpenTab[] = [
      { worktreeId: "a", kind: "setup" },
      { worktreeId: "a", kind: "run", name: "dev" },
    ];
    // 'a' already has its setup tab open, so nothing to add -> same reference.
    expect(reconcileSetupTabs(prev, [wt("a")], new Set())).toBe(prev);
  });

  it("prunes tabs whose worktree no longer exists", () => {
    const prev: OpenTab[] = [
      { worktreeId: "a", kind: "setup" },
      { worktreeId: "gone", kind: "run", name: "dev" },
    ];
    const next = reconcileSetupTabs(prev, [wt("a")], new Set());
    expect(next.map((t) => tabKey(t.worktreeId, t.kind, t.name))).toEqual(["a:setup:"]);
  });
});

describe("reconcileTerminalTabs", () => {
  const term = (id: string) => ({ id });

  it("auto-opens a tab per live session, after existing tabs", () => {
    const prev: OpenTab[] = [{ worktreeId: "a", kind: "setup" }];
    const next = reconcileTerminalTabs(prev, [term("t1"), term("t2")], new Set());
    expect(next.map((t) => tabKey(t.worktreeId, t.kind, t.name))).toEqual([
      "a:setup:",
      ":terminal:t1",
      ":terminal:t2",
    ]);
  });

  it("does not re-open a terminal tab the user just closed", () => {
    const next = reconcileTerminalTabs([], [term("t1")], new Set([":terminal:t1"]));
    expect(next).toEqual([]);
  });

  it("prunes tabs whose session vanished, leaving other kinds alone", () => {
    const prev: OpenTab[] = [
      { worktreeId: "a", kind: "run", name: "dev" },
      { worktreeId: "", kind: "terminal", name: "gone" },
      { worktreeId: "", kind: "terminal", name: "t1" },
    ];
    const next = reconcileTerminalTabs(prev, [term("t1")], new Set());
    expect(next.map((t) => tabKey(t.worktreeId, t.kind, t.name))).toEqual([
      "a:run:dev",
      ":terminal:t1",
    ]);
  });

  it("returns the same reference when nothing changed", () => {
    const prev: OpenTab[] = [{ worktreeId: "", kind: "terminal", name: "t1" }];
    expect(reconcileTerminalTabs(prev, [term("t1")], new Set())).toBe(prev);
  });
});

describe("nextActiveKey", () => {
  it("returns null when no tabs are open", () => {
    expect(nextActiveKey([], "a:setup:")).toBeNull();
  });

  it("keeps the current key when still open", () => {
    const tabs: OpenTab[] = [
      { worktreeId: "a", kind: "setup" },
      { worktreeId: "b", kind: "run", name: "dev" },
    ];
    expect(nextActiveKey(tabs, "b:run:dev")).toBe("b:run:dev");
  });

  it("falls back to the first tab when the current key is gone", () => {
    const tabs: OpenTab[] = [{ worktreeId: "a", kind: "setup" }];
    expect(nextActiveKey(tabs, "b:run:dev")).toBe("a:setup:");
  });
});

describe("addTab / removeTab", () => {
  it("addTab is idempotent per name (same reference when already open)", () => {
    const prev: OpenTab[] = [{ worktreeId: "a", kind: "run", name: "dev" }];
    expect(addTab(prev, "a", "run", "dev")).toBe(prev);
    // a different named run is a distinct tab
    expect(addTab(prev, "a", "run", "start").map((t) => tabKey(t.worktreeId, t.kind, t.name))).toEqual([
      "a:run:dev",
      "a:run:start",
    ]);
  });

  it("removeTab drops the named tab and is a no-op (same reference) when absent", () => {
    const prev: OpenTab[] = [{ worktreeId: "a", kind: "run", name: "dev" }];
    expect(removeTab(prev, "a", "run", "dev")).toEqual([]);
    expect(removeTab(prev, "a", "run", "start")).toBe(prev);
  });
});

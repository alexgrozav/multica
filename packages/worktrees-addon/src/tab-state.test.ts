import { describe, expect, it } from "vitest";
import {
  addTab,
  nextActiveKey,
  reconcileSetupTabs,
  removeTab,
  tabKey,
  type OpenTab,
} from "./tab-state";

const wt = (id: string, has_setup_script = true) => ({ id, has_setup_script });

describe("reconcileSetupTabs", () => {
  it("seeds one setup tab per worktree with a setup script", () => {
    const next = reconcileSetupTabs([], [wt("a"), wt("b", false), wt("c")], new Set());
    expect(next.map((t) => tabKey(t.worktreeId, t.kind))).toEqual(["a:setup", "c:setup"]);
  });

  it("does not re-seed a setup tab the user closed", () => {
    const next = reconcileSetupTabs([], [wt("a")], new Set(["a:setup"]));
    expect(next).toEqual([]);
  });

  it("keeps existing tabs (incl. run tabs) and preserves identity when unchanged", () => {
    const prev: OpenTab[] = [
      { worktreeId: "a", kind: "setup" },
      { worktreeId: "a", kind: "run" },
    ];
    // 'a' already has its setup tab open, so nothing to add -> same reference.
    expect(reconcileSetupTabs(prev, [wt("a")], new Set())).toBe(prev);
  });

  it("prunes tabs whose worktree no longer exists", () => {
    const prev: OpenTab[] = [
      { worktreeId: "a", kind: "setup" },
      { worktreeId: "gone", kind: "run" },
    ];
    const next = reconcileSetupTabs(prev, [wt("a")], new Set());
    expect(next.map((t) => tabKey(t.worktreeId, t.kind))).toEqual(["a:setup"]);
  });
});

describe("nextActiveKey", () => {
  it("returns null when no tabs are open", () => {
    expect(nextActiveKey([], "a:setup")).toBeNull();
  });

  it("keeps the current key when still open", () => {
    const tabs: OpenTab[] = [{ worktreeId: "a", kind: "setup" }, { worktreeId: "b", kind: "run" }];
    expect(nextActiveKey(tabs, "b:run")).toBe("b:run");
  });

  it("falls back to the first tab when the current key is gone", () => {
    const tabs: OpenTab[] = [{ worktreeId: "a", kind: "setup" }];
    expect(nextActiveKey(tabs, "b:run")).toBe("a:setup");
  });
});

describe("addTab / removeTab", () => {
  it("addTab is idempotent (same reference when already open)", () => {
    const prev: OpenTab[] = [{ worktreeId: "a", kind: "run" }];
    expect(addTab(prev, "a", "run")).toBe(prev);
    expect(addTab(prev, "a", "setup").map((t) => tabKey(t.worktreeId, t.kind))).toEqual([
      "a:run",
      "a:setup",
    ]);
  });

  it("removeTab drops the tab and is a no-op (same reference) when absent", () => {
    const prev: OpenTab[] = [{ worktreeId: "a", kind: "run" }];
    expect(removeTab(prev, "a", "run")).toEqual([]);
    expect(removeTab(prev, "b", "setup")).toBe(prev);
  });
});

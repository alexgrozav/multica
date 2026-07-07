import { describe, expect, it } from "vitest";
import {
  ISSUE_TAB_KEY,
  addFileTab,
  closeFileTab,
  effectiveActiveKey,
  fileTabKey,
  fileTabLabel,
  pruneFileTabs,
  setFileTabMode,
  tabKey,
  type FileTab,
  type FileTabMode,
} from "./file-tab-state";

const tab = (worktreeId: string, path: string, mode: FileTabMode = "edit"): FileTab => ({
  worktreeId,
  path,
  mode,
});

describe("fileTabLabel", () => {
  it("returns the basename", () => {
    expect(fileTabLabel("src/app/main.ts")).toBe("main.ts");
    expect(fileTabLabel("README.md")).toBe("README.md");
  });
});

describe("tab keys", () => {
  it("identify a tab by worktree + path, independent of mode", () => {
    expect(tabKey(tab("w1", "a.ts"))).toBe(fileTabKey("w1", "a.ts"));
    expect(tabKey(tab("w1", "a.ts", "diff"))).toBe(fileTabKey("w1", "a.ts"));
  });
});

describe("addFileTab", () => {
  it("appends new tabs and dedupes already-open files by (worktree, path)", () => {
    let tabs: FileTab[] = [];
    tabs = addFileTab(tabs, "w1", "a.ts", "edit");
    tabs = addFileTab(tabs, "w2", "a.ts", "edit"); // same path, other repo → distinct tab
    expect(tabs).toHaveLength(2);
    const same = addFileTab(tabs, "w1", "a.ts", "edit");
    expect(same).toBe(tabs); // unchanged reference when already open in that mode
  });

  it("switches an already-open tab's mode in place instead of duplicating", () => {
    let tabs = [tab("w1", "a.ts"), tab("w1", "b.ts")];
    tabs = addFileTab(tabs, "w1", "a.ts", "diff");
    expect(tabs).toHaveLength(2);
    expect(tabs[0]).toEqual(tab("w1", "a.ts", "diff")); // same strip position
    expect(tabs[1]).toEqual(tab("w1", "b.ts"));
  });
});

describe("setFileTabMode", () => {
  it("flips a tab's mode, keeping the reference when nothing changes", () => {
    const tabs = [tab("w1", "a.ts"), tab("w1", "b.ts", "diff")];
    const flipped = setFileTabMode(tabs, "w1", "a.ts", "diff");
    expect(flipped[0]!.mode).toBe("diff");
    expect(flipped[1]).toBe(tabs[1]);
    expect(setFileTabMode(tabs, "w1", "a.ts", "edit")).toBe(tabs); // already edit
    expect(setFileTabMode(tabs, "w9", "zzz.ts", "diff")).toBe(tabs); // unknown tab
  });
});

describe("closeFileTab", () => {
  const three = [tab("w1", "a.ts"), tab("w1", "b.ts", "diff"), tab("w1", "c.ts")];

  it("keeps the current selection when closing a background tab", () => {
    const { tabs, activeKey } = closeFileTab(three, fileTabKey("w1", "c.ts"), "w1", "a.ts");
    expect(tabs.map((t) => t.path)).toEqual(["b.ts", "c.ts"]);
    expect(activeKey).toBe(fileTabKey("w1", "c.ts"));
  });

  it("activates the left neighbor when closing the active tab", () => {
    const { activeKey } = closeFileTab(three, fileTabKey("w1", "b.ts"), "w1", "b.ts");
    expect(activeKey).toBe(fileTabKey("w1", "a.ts"));
  });

  it("activates the right neighbor when the first tab was active and closed", () => {
    const { activeKey } = closeFileTab(three, fileTabKey("w1", "a.ts"), "w1", "a.ts");
    expect(activeKey).toBe(fileTabKey("w1", "b.ts"));
  });

  it("falls back to the Issue tab when the last file tab closes", () => {
    const { tabs, activeKey } = closeFileTab(
      [tab("w1", "a.ts")],
      fileTabKey("w1", "a.ts"),
      "w1",
      "a.ts",
    );
    expect(tabs).toHaveLength(0);
    expect(activeKey).toBe(ISSUE_TAB_KEY);
  });

  it("is a no-op for an unknown tab", () => {
    const out = closeFileTab(three, ISSUE_TAB_KEY, "w9", "zzz.ts");
    expect(out.tabs).toBe(three);
    expect(out.activeKey).toBe(ISSUE_TAB_KEY);
  });
});

describe("pruneFileTabs", () => {
  it("drops tabs of removed worktrees, keeping the reference when unchanged", () => {
    const tabs = [tab("w1", "a.ts"), tab("w2", "b.ts", "diff")];
    expect(pruneFileTabs(tabs, new Set(["w1", "w2"]))).toBe(tabs);
    expect(pruneFileTabs(tabs, new Set(["w2"]))).toEqual([tab("w2", "b.ts", "diff")]);
  });
});

describe("effectiveActiveKey", () => {
  it("falls back to the Issue tab when the active file tab is gone", () => {
    const tabs = [tab("w1", "a.ts")];
    expect(effectiveActiveKey(tabs, fileTabKey("w1", "a.ts"))).toBe(fileTabKey("w1", "a.ts"));
    expect(effectiveActiveKey(tabs, fileTabKey("w1", "gone.ts"))).toBe(ISSUE_TAB_KEY);
    expect(effectiveActiveKey([], ISSUE_TAB_KEY)).toBe(ISSUE_TAB_KEY);
  });
});

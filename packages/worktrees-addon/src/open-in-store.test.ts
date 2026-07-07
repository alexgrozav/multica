// @vitest-environment jsdom
import { afterEach, describe, expect, it } from "vitest";
import { isOpenTarget, OPEN_TARGETS, setOpenInTarget } from "./open-in-store";

const KEY = "multica.worktree.open-in-target";

afterEach(() => {
  localStorage.clear();
});

describe("open-in-store", () => {
  it("recognizes the app targets and rejects copy / unknown", () => {
    for (const t of OPEN_TARGETS) expect(isOpenTarget(t)).toBe(true);
    expect(isOpenTarget("copy")).toBe(false);
    expect(isOpenTarget("bogus")).toBe(false);
  });

  it("persists a chosen app target as the default (last opened stays default)", () => {
    setOpenInTarget("vscode");
    expect(localStorage.getItem(KEY)).toBe("vscode");
  });

  it("ignores non-app targets so Copy path never changes the default", () => {
    setOpenInTarget("zed");
    expect(localStorage.getItem(KEY)).toBe("zed");
    setOpenInTarget("copy"); // client-only utility, not persistable
    setOpenInTarget("bogus"); // unknown value
    expect(localStorage.getItem(KEY)).toBe("zed");
  });
});

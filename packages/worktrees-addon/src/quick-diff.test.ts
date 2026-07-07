import { describe, expect, it } from "vitest";
import { computeQuickDiff } from "./quick-diff";

const sorted = (s: Set<number>) => [...s].sort((a, b) => a - b);

describe("computeQuickDiff", () => {
  it("returns nothing for identical documents", () => {
    const lines = ["a", "b", "c"];
    const out = computeQuickDiff(lines, [...lines]);
    expect(out.added.size + out.modified.size + out.deletedAt.size).toBe(0);
  });

  it("marks pure insertions as added", () => {
    const out = computeQuickDiff(["a", "b"], ["a", "new1", "new2", "b"]);
    expect(sorted(out.added)).toEqual([2, 3]);
    expect(out.modified.size).toBe(0);
    expect(out.deletedAt.size).toBe(0);
  });

  it("marks replaced lines as modified", () => {
    const out = computeQuickDiff(["a", "old", "c"], ["a", "new", "c"]);
    expect(sorted(out.modified)).toEqual([2]);
    expect(out.added.size).toBe(0);
    expect(out.deletedAt.size).toBe(0);
  });

  it("marks a pure deletion on the line below the removed block", () => {
    const out = computeQuickDiff(["a", "gone", "b"], ["a", "b"]);
    expect(sorted(out.deletedAt)).toEqual([2]);
    expect(out.added.size + out.modified.size).toBe(0);
  });

  it("clamps a deletion at the end of the document to the last line", () => {
    const out = computeQuickDiff(["a", "b", "gone"], ["a", "b"]);
    expect(sorted(out.deletedAt)).toEqual([2]);
  });

  it("keeps an unchanged line between an insertion and a deletion unmarked", () => {
    // The scenario a char-level diff gets wrong: two new lines inserted, one
    // line deleted, separated by the unchanged "line four".
    const a = ["line one", "line two", "line three", "line four", "line five", "line six", ""];
    const b = [
      "line one",
      "line TWO modified",
      "line three",
      "brand new line",
      "another new line",
      "line four",
      "line six",
      "",
    ];
    const out = computeQuickDiff(a, b);
    expect(sorted(out.modified)).toEqual([2]);
    expect(sorted(out.added)).toEqual([4, 5]);
    expect(sorted(out.deletedAt)).toEqual([7]); // "line six" sits below the deleted "line five"
  });

  it("handles insertions at the start and end of the document", () => {
    const out = computeQuickDiff(["a"], ["first", "a", "last"]);
    expect(sorted(out.added)).toEqual([1, 3]);
  });

  it("marks an unbalanced replacement block as modified", () => {
    const out = computeQuickDiff(["a", "x", "b"], ["a", "y1", "y2", "y3", "b"]);
    expect(sorted(out.modified)).toEqual([2, 3, 4]);
    expect(out.added.size).toBe(0);
  });

  it("degrades to a modified region when the diff exceeds the complexity cap", () => {
    const a = Array.from({ length: 400 }, (_, i) => `a${i}`);
    const b = Array.from({ length: 400 }, (_, i) => `b${i}`);
    const out = computeQuickDiff(a, b);
    expect(out.modified.size).toBe(400);
    expect(out.added.size + out.deletedAt.size).toBe(0);
  });

  // Cross-check the bounded Myers pass against a reference DP LCS: every
  // random edit script must classify each buffer line exactly once, and the
  // set of unmarked lines must be a common subsequence of both documents.
  it("agrees with a reference LCS on random edits", () => {
    // Deterministic PRNG so failures reproduce.
    let seed = 42;
    const rand = (n: number) => {
      seed = (seed * 1103515245 + 12345) & 0x7fffffff;
      return seed % n;
    };
    const lcsLength = (a: string[], b: string[]) => {
      const dp = Array.from({ length: a.length + 1 }, () => new Array<number>(b.length + 1).fill(0));
      for (let i = 1; i <= a.length; i++)
        for (let j = 1; j <= b.length; j++)
          dp[i]![j] = a[i - 1] === b[j - 1] ? dp[i - 1]![j - 1]! + 1 : Math.max(dp[i - 1]![j]!, dp[i]![j - 1]!);
      return dp[a.length]![b.length]!;
    };

    for (let round = 0; round < 60; round++) {
      const a = Array.from({ length: 5 + rand(30) }, (_, i) => `l${rand(12)}-${i % 4}`);
      const b = [...a];
      for (let edits = 1 + rand(6); edits > 0; edits--) {
        const op = rand(3);
        const at = rand(Math.max(1, b.length));
        if (op === 0) b.splice(at, 0, `ins${rand(10)}`);
        else if (op === 1 && b.length > 1) b.splice(at, 1);
        else if (b.length > 0) b[Math.min(at, b.length - 1)] = `mod${rand(10)}`;
      }
      const out = computeQuickDiff(a, b);
      // No line is both added and modified.
      for (const n of out.added) expect(out.modified.has(n)).toBe(false);
      // Unmarked lines form a common subsequence, so there are at least
      // as many of them as... exactly: unmarked count <= LCS length, and the
      // classification covers all non-LCS lines: b.length - marked <= LCS.
      const marked = out.added.size + out.modified.size;
      expect(b.length - marked).toBeLessThanOrEqual(lcsLength(a, b));
      // All markers point at real lines.
      for (const n of [...out.added, ...out.modified, ...out.deletedAt]) {
        expect(n).toBeGreaterThanOrEqual(1);
        expect(n).toBeLessThanOrEqual(b.length);
      }
      // And the unmarked lines really are matchable: replaying the diff must
      // reconstruct identical documents when unmarked lines are kept.
      const unmarkedB = b.filter((_, i) => !out.added.has(i + 1) && !out.modified.has(i + 1));
      expect(lcsLength(a, unmarkedB)).toBe(unmarkedB.length); // subsequence of a
    }
  });
});

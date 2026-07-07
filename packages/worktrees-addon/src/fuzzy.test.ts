import { describe, expect, it } from "vitest";
import { fuzzyMatch, sliceRanges } from "./fuzzy";

// Highlighted substrings, in order — the shape the palette renders.
function highlighted(query: string, text: string): string[] | null {
  const m = fuzzyMatch(query, text);
  if (!m) return null;
  return m.ranges.map(([s, e]) => text.slice(s, e));
}

describe("fuzzyMatch", () => {
  it("matches subsequences case-insensitively", () => {
    expect(fuzzyMatch("READ", "docs/readme.md")).not.toBeNull();
    expect(fuzzyMatch("rdme", "docs/readme.md")).not.toBeNull();
    expect(fuzzyMatch("xyz", "docs/readme.md")).toBeNull();
  });

  it("returns null for an empty or whitespace query", () => {
    expect(fuzzyMatch("", "a.ts")).toBeNull();
    expect(fuzzyMatch("   ", "a.ts")).toBeNull();
  });

  it("groups consecutive matched characters into ranges", () => {
    // "is" + "det" should land on the word starts, not scatter.
    expect(highlighted("isdet", "issue-detail.tsx")).toEqual(["is", "det"]);
  });

  it("prefers word-boundary alignments over earlier scattered ones", () => {
    // A greedy left-to-right match would grab the "m" of "components"; the
    // DP alignment finds the contiguous basename run instead.
    expect(highlighted("main", "components/main.ts")).toEqual(["main"]);
  });

  it("matches camelCase initials", () => {
    expect(highlighted("fsp", "src/FileSearchPalette.tsx")).toEqual(["F", "S", "P"]);
  });

  it("requires every whitespace-separated term and merges their highlights", () => {
    expect(highlighted("detail issue", "views/issue-detail.tsx")).toEqual(["issue", "detail"]);
    expect(fuzzyMatch("issue nomatch", "views/issue-detail.tsx")).toBeNull();
  });

  it("merges overlapping term highlights", () => {
    expect(highlighted("issue sue", "issue.ts")).toEqual(["issue"]);
  });

  it("ranks basename matches above directory matches", () => {
    const inBasename = fuzzyMatch("issue", "packages/views/issue-detail.tsx")!;
    const inDir = fuzzyMatch("issue", "packages/views/issues/foo-bar.tsx")!;
    expect(inBasename.score).toBeGreaterThan(inDir.score);
  });

  it("ranks an exact basename above a fuzzy one", () => {
    const exact = fuzzyMatch("main.ts", "src/main.ts")!;
    const fuzzy = fuzzyMatch("main.ts", "src/main-suite.ts")!;
    expect(exact.score).toBeGreaterThan(fuzzy.score);
  });

  it("ranks consecutive runs above scattered characters", () => {
    const run = fuzzyMatch("query", "src/query.ts")!;
    const scattered = fuzzyMatch("query", "quantum-electric-relay.ts")!;
    expect(run.score).toBeGreaterThan(scattered.score);
  });

  it("keeps ranges within text bounds and ascending", () => {
    const m = fuzzyMatch("st", "src/store/state.ts")!;
    let prevEnd = 0;
    for (const [s, e] of m.ranges) {
      expect(s).toBeGreaterThanOrEqual(prevEnd);
      expect(e).toBeGreaterThan(s);
      expect(e).toBeLessThanOrEqual("src/store/state.ts".length);
      prevEnd = e;
    }
  });

  it("survives very long paths via the greedy fallback", () => {
    const long = `${"a/".repeat(600)}target.ts`;
    const m = fuzzyMatch("target", long);
    expect(m).not.toBeNull();
    expect(m!.ranges.length).toBeGreaterThan(0);
  });
});

describe("sliceRanges", () => {
  it("clips to the window and rebases to its start", () => {
    // Ranges over "src/main.ts"; basename window is [4, 11).
    expect(sliceRanges([[0, 3], [4, 8]], 4, 11)).toEqual([[0, 4]]);
    expect(sliceRanges([[2, 6]], 4, 11)).toEqual([[0, 2]]);
    expect(sliceRanges([[0, 3]], 4, 11)).toEqual([]);
  });

  it("returns an empty list for an empty window", () => {
    expect(sliceRanges([[0, 5]], 3, 3)).toEqual([]);
  });
});

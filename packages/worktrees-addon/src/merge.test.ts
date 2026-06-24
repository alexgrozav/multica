import { describe, expect, it } from "vitest";
import { mergeBySeq } from "./merge";

describe("mergeBySeq", () => {
  it("dedupes by seq and orders ascending", () => {
    const existing = [
      { seq: 1, content: "a" },
      { seq: 3, content: "c" },
    ];
    const incoming = [
      { seq: 2, content: "b" },
      { seq: 1, content: "dup" },
    ];
    const merged = mergeBySeq(existing, incoming);
    expect(merged.map((m) => m.seq)).toEqual([1, 2, 3]);
    expect(merged.map((m) => m.content)).toEqual(["a", "b", "c"]);
  });

  it("preserves array identity when nothing new arrives", () => {
    const existing = [{ seq: 1 }, { seq: 2 }];
    expect(mergeBySeq(existing, [{ seq: 1 }])).toBe(existing);
  });

  it("appends fresh lines onto an empty buffer", () => {
    expect(mergeBySeq([], [{ seq: 5 }, { seq: 4 }]).map((m) => m.seq)).toEqual([4, 5]);
  });
});

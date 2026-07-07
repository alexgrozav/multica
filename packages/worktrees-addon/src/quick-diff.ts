// Pure line-level diff for the editor's quick-diff gutter (VS Code-style
// change markers). Kept free of CodeMirror so the classification logic is
// unit-testable. Line-level diffing matters: a char-level diff happily pairs
// unrelated lines through scattered common substrings, mislabeling inserted
// lines as modified.

// Classification of the buffer's lines against the base: 1-based buffer line
// numbers. deletedAt marks lines that sit directly below a deleted block.
export interface QuickDiffLines {
  added: Set<number>;
  modified: Set<number>;
  deletedAt: Set<number>;
}

// myersLineMatches runs a bounded Myers diff over the (already trimmed) line
// ranges and returns the matched line pairs, or null when the diff exceeds the
// complexity cap (the caller then degrades to "whole region modified").
function myersLineMatches(
  a: readonly string[],
  b: readonly string[],
  start: number,
  endA: number,
  endB: number,
): Array<[number, number]> | null {
  const n = endA - start;
  const m = endB - start;
  const maxD = Math.min(n + m, 700);
  const off = maxD + 1;
  const v = new Int32Array(2 * maxD + 3);
  const trace: Int32Array[] = [];
  let found = -1;
  for (let d = 0; d <= maxD && found < 0; d++) {
    for (let k = -d; k <= d; k += 2) {
      let x =
        k === -d || (k !== d && v[off + k - 1]! < v[off + k + 1]!)
          ? v[off + k + 1]!
          : v[off + k - 1]! + 1;
      let y = x - k;
      while (x < n && y < m && a[start + x] === b[start + y]) {
        x++;
        y++;
      }
      v[off + k] = x;
      if (x >= n && y >= m) {
        found = d;
        break;
      }
    }
    trace.push(v.slice());
  }
  if (found < 0) return null;

  const matches: Array<[number, number]> = [];
  let x = n;
  let y = m;
  for (let d = found; d > 0; d--) {
    const prev = trace[d - 1]!;
    const k = x - y;
    const down = k === -d || (k !== d && prev[off + k - 1]! < prev[off + k + 1]!);
    const prevK = down ? k + 1 : k - 1;
    const prevX = prev[off + prevK]!;
    const prevY = prevX - prevK;
    // Diagonal snake back to just after the edit step — every step a match.
    const midX = down ? prevX : prevX + 1;
    while (x > midX) {
      x--;
      y--;
      matches.push([start + x, start + y]);
    }
    x = prevX;
    y = prevY;
  }
  // The d = 0 leading snake.
  while (x > 0 && y > 0) {
    x--;
    y--;
    matches.push([start + x, start + y]);
  }
  return matches.reverse();
}

// computeQuickDiff classifies the buffer's lines against the base with VS Code
// quick-diff semantics: pure insertions green (added), replaced blocks amber
// (modified), pure deletions a marker on the line below.
export function computeQuickDiff(
  aLines: readonly string[],
  bLines: readonly string[],
): QuickDiffLines {
  const added = new Set<number>();
  const modified = new Set<number>();
  const deletedAt = new Set<number>();
  const aLen = aLines.length;
  const bLen = bLines.length;

  // Trim the common prefix/suffix — after a typical edit the interesting
  // region is a handful of lines, keeping the Myers pass trivial.
  let start = 0;
  while (start < aLen && start < bLen && aLines[start] === bLines[start]) start++;
  let endA = aLen;
  let endB = bLen;
  while (endA > start && endB > start && aLines[endA - 1] === bLines[endB - 1]) {
    endA--;
    endB--;
  }
  if (endA === start && endB === start) return { added, modified, deletedAt };
  if (endA === start) {
    for (let i = start; i < endB; i++) added.add(i + 1);
    return { added, modified, deletedAt };
  }
  if (endB === start) {
    deletedAt.add(Math.min(start + 1, bLen));
    return { added, modified, deletedAt };
  }

  const matches = myersLineMatches(aLines, bLines, start, endA, endB);
  if (!matches) {
    // Diff too complex for the cap — degrade to one modified region.
    for (let i = start; i < endB; i++) modified.add(i + 1);
    return { added, modified, deletedAt };
  }

  // Walk the runs between matched lines: insert-only → added, delete-only →
  // boundary marker on the matched line below, mixed → modified.
  let ai = start;
  let bi = start;
  const walk = (ma: number, mb: number) => {
    const del = ma - ai;
    const ins = mb - bi;
    if (ins > 0 && del > 0) for (let i = bi; i < mb; i++) modified.add(i + 1);
    else if (ins > 0) for (let i = bi; i < mb; i++) added.add(i + 1);
    else if (del > 0) deletedAt.add(Math.min(mb + 1, bLen));
  };
  for (const [ma, mb] of matches) {
    walk(ma, mb);
    ai = ma + 1;
    bi = mb + 1;
  }
  walk(endA, endB);
  return { added, modified, deletedAt };
}

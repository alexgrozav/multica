// Pure fuzzy path matching for the file search palette (Cmd+P). fzy-style
// scoring: each whitespace-separated query term must match the path as a
// case-insensitive subsequence, aligned optimally via dynamic programming so
// consecutive runs and word-boundary starts (after "/", "-", "_", ".",
// camelCase) beat scattered characters. The optimal alignment is also what
// gets highlighted — matched positions merge into contiguous ranges.

export interface FuzzyMatch {
  /** Higher is better; comparable only across matches of the same query. */
  score: number;
  /** Merged [start, end) index ranges of matched characters in the text. */
  ranges: Array<[number, number]>;
}

const SCORE_GAP_LEADING = -0.005;
const SCORE_GAP_TRAILING = -0.005;
const SCORE_GAP_INNER = -0.01;
const SCORE_MATCH_CONSECUTIVE = 1.0;
const SCORE_MATCH_SLASH = 0.9;
const SCORE_MATCH_WORD = 0.8;
const SCORE_MATCH_CAPITAL = 0.7;
const SCORE_MATCH_DOT = 0.6;
/** Extra score per term whose match starts in the basename — file-name hits
 * outrank equally-good directory hits. */
const SCORE_BASENAME = 0.4;
/** A term equal to the whole text can't be beaten. */
const SCORE_EXACT = 1e9;
/** DP is O(term × text); beyond this the greedy alignment is good enough. */
const MAX_DP_TEXT = 1024;
const SCORE_MIN = -Infinity;

// Boundary bonus for a match starting at each position. Start-of-string
// counts as "after a slash" (a repo-relative path's root).
function computeBonus(text: string): number[] {
  const n = text.length;
  const bonus = new Array<number>(n);
  let prev = "/";
  for (let i = 0; i < n; i++) {
    const ch = text[i]!;
    let b = 0;
    if (prev === "/") b = SCORE_MATCH_SLASH;
    else if (prev === "-" || prev === "_" || prev === " ") b = SCORE_MATCH_WORD;
    else if (prev === ".") b = SCORE_MATCH_DOT;
    else if (prev >= "a" && prev <= "z" && ch >= "A" && ch <= "Z") b = SCORE_MATCH_CAPITAL;
    bonus[i] = b;
    prev = ch;
  }
  return bonus;
}

interface TermMatch {
  score: number;
  /** Ascending matched indices, one per term character. */
  positions: number[];
}

// One term against the text: greedy subsequence pre-check (fast reject), then
// fzy's DP for the best-scoring alignment. D[j][i] = best score of term[0..j]
// with term[j] matched exactly at text[i]; M[j][i] = best of term[0..j]
// matched at or before i. Traceback recovers the aligned positions.
function matchTerm(term: string, lowerText: string, bonus: readonly number[]): TermMatch | null {
  const m = term.length;
  const n = lowerText.length;
  if (m === 0 || m > n) return null;

  const greedy: number[] = [];
  for (let i = 0, j = 0; i < n && j < m; i++) {
    if (lowerText[i] === term[j]) {
      greedy.push(i);
      j++;
    }
  }
  if (greedy.length < m) return null;
  if (m === n) return { score: SCORE_EXACT, positions: greedy };
  if (n > MAX_DP_TEXT) return { score: 0, positions: greedy };

  const D = new Float64Array(m * n);
  const M = new Float64Array(m * n);
  for (let j = 0; j < m; j++) {
    const gap = j === m - 1 ? SCORE_GAP_TRAILING : SCORE_GAP_INNER;
    const tj = term[j]!;
    let prevM = SCORE_MIN;
    for (let i = 0; i < n; i++) {
      const idx = j * n + i;
      let d = SCORE_MIN;
      if (lowerText[i] === tj) {
        if (j === 0) {
          d = i * SCORE_GAP_LEADING + bonus[i]!;
        } else if (i > 0) {
          const pidx = idx - n - 1; // (j-1, i-1)
          const viaGap = M[pidx]! + bonus[i]!;
          const viaRun = D[pidx]! + SCORE_MATCH_CONSECUTIVE;
          d = viaGap > viaRun ? viaGap : viaRun;
        }
      }
      D[idx] = d;
      const gapped = prevM === SCORE_MIN ? SCORE_MIN : prevM + gap;
      M[idx] = prevM = d > gapped ? d : gapped;
    }
  }

  const score = M[m * n - 1]!;
  if (score === SCORE_MIN) return { score: 0, positions: greedy };

  const positions = new Array<number>(m);
  let matchRequired = false;
  for (let j = m - 1, i = n - 1; j >= 0; j--) {
    for (; i >= 0; i--) {
      const idx = j * n + i;
      const d = D[idx]!;
      // Take text[i] for term[j] when it's where the best alignment ends —
      // or when the char after it was matched as a consecutive run.
      if (d !== SCORE_MIN && (matchRequired || d === M[idx])) {
        matchRequired =
          j > 0 && i > 0 && M[idx] === D[idx - n - 1]! + SCORE_MATCH_CONSECUTIVE;
        positions[j] = i;
        i--;
        break;
      }
    }
  }
  return { score, positions };
}

// Ascending, possibly overlapping positions (multi-term) → merged [start, end)
// ranges of consecutive characters.
function toRanges(positions: readonly number[]): Array<[number, number]> {
  const ranges: Array<[number, number]> = [];
  for (const p of positions) {
    const last = ranges[ranges.length - 1];
    if (last && p < last[1]) continue;
    if (last && p === last[1]) last[1] = p + 1;
    else ranges.push([p, p + 1]);
  }
  return ranges;
}

// fuzzyMatch matches every whitespace-separated term of `query` against
// `text` (all must match) and returns the summed score plus the merged
// highlight ranges, or null when any term fails.
export function fuzzyMatch(query: string, text: string): FuzzyMatch | null {
  const terms = query.trim().toLowerCase().split(/\s+/).filter(Boolean);
  if (terms.length === 0 || text.length === 0) return null;
  // Rare unicode lowercasings change string length; positions would then
  // misalign with the original text, so match case-sensitively instead.
  let lowerText = text.toLowerCase();
  if (lowerText.length !== text.length) lowerText = text;
  const bonus = computeBonus(text);
  const basenameStart = text.lastIndexOf("/") + 1;

  let score = 0;
  const positionSet = new Set<number>();
  for (const term of terms) {
    const match = matchTerm(term, lowerText, bonus);
    if (!match) return null;
    score += match.score;
    if (match.positions[0]! >= basenameStart) score += SCORE_BASENAME;
    for (const p of match.positions) positionSet.add(p);
  }
  const positions = [...positionSet].sort((a, b) => a - b);
  return { score, ranges: toRanges(positions) };
}

// sliceRanges clips ranges to the [start, end) window and rebases them to
// start — used to split a full-path highlight into basename + directory parts.
export function sliceRanges(
  ranges: ReadonlyArray<readonly [number, number]>,
  start: number,
  end: number,
): Array<[number, number]> {
  const out: Array<[number, number]> = [];
  for (const [s, e] of ranges) {
    const cs = Math.max(s, start);
    const ce = Math.min(e, end);
    if (cs < ce) out.push([cs - start, ce - start]);
  }
  return out;
}

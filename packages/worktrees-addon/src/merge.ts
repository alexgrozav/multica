// mergeBySeq deduplicates and orders streamed items by their sequence number,
// preserving array identity when nothing new arrived (so React Query skips a
// re-render). Generalizes the host's mergeTaskMessagesBySeq.
export function mergeBySeq<T extends { seq: number }>(
  existing: readonly T[],
  incoming: readonly T[],
): T[] {
  const known = new Set(existing.map((m) => m.seq));
  const fresh = incoming.filter((m) => !known.has(m.seq));
  if (fresh.length === 0) return existing as T[];
  return [...existing, ...fresh].sort((a, b) => a.seq - b.seq);
}

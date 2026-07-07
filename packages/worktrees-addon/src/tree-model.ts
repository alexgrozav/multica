// Pure tree building for the Project tab: fold the daemon's flat, sorted path
// list into a nested directory model the tree view can render lazily.

export interface TreeDir {
  name: string;
  /** Full relative path of the directory ("" for the repo root). */
  path: string;
  dirs: TreeDir[];
  /** File basenames directly in this directory. */
  files: string[];
}

interface MutableDir {
  name: string;
  path: string;
  dirs: Map<string, MutableDir>;
  files: string[];
}

function compareNames(a: string, b: string): number {
  return a.localeCompare(b, undefined, { sensitivity: "base", numeric: true }) || (a < b ? -1 : a > b ? 1 : 0);
}

function finalize(dir: MutableDir): TreeDir {
  const dirs = [...dir.dirs.values()].map(finalize);
  dirs.sort((a, b) => compareNames(a.name, b.name));
  const files = [...dir.files];
  files.sort(compareNames);
  return { name: dir.name, path: dir.path, dirs, files };
}

// buildTree folds relative file paths ("src/app/main.ts") into a directory
// tree. Directories exist only where files establish them (matching git, which
// does not track empty directories). Malformed segments ("", ".") are skipped
// defensively — the daemon never emits them.
export function buildTree(paths: string[]): TreeDir {
  const root: MutableDir = { name: "", path: "", dirs: new Map(), files: [] };
  for (const p of paths) {
    const segments = p.split("/").filter((s) => s !== "" && s !== ".");
    if (segments.length === 0) continue;
    let cur = root;
    for (let i = 0; i < segments.length - 1; i++) {
      const seg = segments[i]!;
      let next = cur.dirs.get(seg);
      if (!next) {
        next = {
          name: seg,
          path: cur.path === "" ? seg : `${cur.path}/${seg}`,
          dirs: new Map(),
          files: [],
        };
        cur.dirs.set(seg, next);
      }
      cur = next;
    }
    cur.files.push(segments[segments.length - 1]!);
  }
  return finalize(root);
}

// countFiles returns the total file count of a built tree (for the repo header).
export function countFiles(dir: TreeDir): number {
  let n = dir.files.length;
  for (const d of dir.dirs) n += countFiles(d);
  return n;
}

import { describe, expect, it } from "vitest";
import { buildTree, countFiles } from "./tree-model";

describe("buildTree", () => {
  it("folds flat paths into nested dirs with files split per level", () => {
    const tree = buildTree([
      "README.md",
      "src/app/main.ts",
      "src/util.ts",
      "src/app/api/route.ts",
    ]);

    expect(tree.files).toEqual(["README.md"]);
    expect(tree.dirs.map((d) => d.name)).toEqual(["src"]);

    const src = tree.dirs[0]!;
    expect(src.path).toBe("src");
    expect(src.files).toEqual(["util.ts"]);
    expect(src.dirs.map((d) => d.name)).toEqual(["app"]);

    const app = src.dirs[0]!;
    expect(app.path).toBe("src/app");
    expect(app.files).toEqual(["main.ts"]);
    expect(app.dirs[0]!.path).toBe("src/app/api");
    expect(app.dirs[0]!.files).toEqual(["route.ts"]);
  });

  it("sorts dirs and files case-insensitively", () => {
    const tree = buildTree(["Zebra.txt", "apple.txt", "beta/x.ts", "Alpha/y.ts"]);
    expect(tree.dirs.map((d) => d.name)).toEqual(["Alpha", "beta"]);
    expect(tree.files).toEqual(["apple.txt", "Zebra.txt"]);
  });

  it("skips empty and dot segments defensively", () => {
    const tree = buildTree(["", "./a.txt", "b//c.txt"]);
    expect(tree.files).toEqual(["a.txt"]);
    expect(tree.dirs.map((d) => d.name)).toEqual(["b"]);
    expect(tree.dirs[0]!.files).toEqual(["c.txt"]);
  });

  it("counts every file across the tree", () => {
    const tree = buildTree(["a.txt", "x/b.txt", "x/y/c.txt", "x/y/d.txt"]);
    expect(countFiles(tree)).toBe(4);
  });
});

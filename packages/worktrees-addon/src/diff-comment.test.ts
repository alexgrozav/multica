import { describe, expect, it } from "vitest";
import { fenceLangFor, formatDiffComment } from "./diff-comment";

describe("formatDiffComment", () => {
  it("quotes file, line, and code above the body", () => {
    expect(
      formatDiffComment({
        location: "src/badge.ts",
        line: 9,
        code: "export type BadgeProps = { color?: string };",
        lang: "ts",
        body: "Split this into named unions.",
      }),
    ).toBe(
      "**`src/badge.ts:9`**\n\n" +
        "```ts\nexport type BadgeProps = { color?: string };\n```\n\n" +
        "Split this into named unions.",
    );
  });

  it("omits the code block when the quoted line is blank", () => {
    expect(
      formatDiffComment({ location: "src/a.ts", line: 3, code: "   ", lang: "ts", body: "Why the gap?" }),
    ).toBe("**`src/a.ts:3`**\n\nWhy the gap?");
  });

  it("grows the fence past backtick runs inside the code", () => {
    const out = formatDiffComment({
      location: "README.md",
      line: 5,
      code: "usage: ```bash example```",
      lang: "md",
      body: "Fix the fence.",
    });
    expect(out).toContain("````md\nusage: ```bash example```\n````");
  });

  it("keeps the repo-qualified location intact", () => {
    expect(
      formatDiffComment({ location: "repoA/src/main.go", line: 12, code: "x := 1", lang: "go", body: "b" }),
    ).toContain("**`repoA/src/main.go:12`**");
  });
});

describe("fenceLangFor", () => {
  it("uses the lowercased filename extension", () => {
    expect(fenceLangFor("packages/ui/src/badge.test.TSX")).toBe("tsx");
    expect(fenceLangFor("main.go")).toBe("go");
  });

  it("returns empty for extensionless files and dotfiles", () => {
    expect(fenceLangFor("Makefile")).toBe("");
    expect(fenceLangFor("config/.env")).toBe("");
  });
});

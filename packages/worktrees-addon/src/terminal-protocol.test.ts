import { describe, expect, it } from "vitest";
import {
  encodeBinaryInput,
  encodeInput,
  parseTerminalCtl,
  resizeFrame,
  terminalTabLabel,
} from "./terminal-protocol";

describe("parseTerminalCtl", () => {
  it("decodes a well-formed control frame", () => {
    expect(parseTerminalCtl('{"type":"state","status":"open","title":"make"}')).toEqual({
      type: "state",
      status: "open",
      title: "make",
    });
  });

  it("returns null for binary data, malformed JSON, and untyped objects", () => {
    expect(parseTerminalCtl(new ArrayBuffer(4))).toBeNull();
    expect(parseTerminalCtl("not json")).toBeNull();
    expect(parseTerminalCtl('"a string"')).toBeNull();
    expect(parseTerminalCtl("{}")).toBeNull();
    expect(parseTerminalCtl("null")).toBeNull();
  });
});

describe("frames + encoding", () => {
  it("resizeFrame round-trips through the parser", () => {
    expect(parseTerminalCtl(resizeFrame(120, 32))).toEqual({ type: "resize", cols: 120, rows: 32 });
  });

  it("encodeInput produces UTF-8 (escape sequences + multibyte survive)", () => {
    expect(Array.from(encodeInput("\x1b[A"))).toEqual([0x1b, 0x5b, 0x41]);
    expect(Array.from(encodeInput("é"))).toEqual([0xc3, 0xa9]);
  });

  it("encodeBinaryInput maps char codes to raw bytes", () => {
    expect(Array.from(encodeBinaryInput("\x00\xff"))).toEqual([0x00, 0xff]);
  });
});

describe("terminalTabLabel", () => {
  it("renders the ordinal, the foreground process when present, and a fallback", () => {
    expect(terminalTabLabel({ index: 1, title: "" })).toBe("Terminal 1");
    expect(terminalTabLabel({ index: 2, title: "make" })).toBe("Terminal 2 (make)");
    expect(terminalTabLabel(undefined)).toBe("Terminal");
  });
});

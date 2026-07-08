// Pure helpers for the terminal WebSocket protocol (mirrors shared.TermCtl):
// binary frames carry raw PTY bytes; text frames carry one JSON control frame.
// Kept free of React/xterm so the framing logic is unit-testable in node.

import type { IssueTerminal, TerminalCtl } from "./types";

// parseTerminalCtl decodes one text frame, returning null for anything that
// is not a well-formed control frame (defensive: the server owns the protocol
// but a viewer must never throw on a frame).
export function parseTerminalCtl(data: unknown): TerminalCtl | null {
  if (typeof data !== "string") return null;
  try {
    const obj = JSON.parse(data) as TerminalCtl;
    if (!obj || typeof obj !== "object" || typeof obj.type !== "string") return null;
    return obj;
  } catch {
    return null;
  }
}

// resizeFrame is the viewer → PTY size announcement.
export function resizeFrame(cols: number, rows: number): string {
  return JSON.stringify({ type: "resize", cols, rows });
}

// encodeInput turns xterm onData output (a UTF-16 string of what the user
// typed, escape sequences included) into UTF-8 bytes for the wire.
export function encodeInput(data: string): Uint8Array {
  return new TextEncoder().encode(data);
}

// encodeBinaryInput turns xterm onBinary output (a "binary string" whose char
// codes are raw byte values, e.g. binary paste) into bytes.
export function encodeBinaryInput(data: string): Uint8Array {
  const out = new Uint8Array(data.length);
  for (let i = 0; i < data.length; i++) out[i] = data.charCodeAt(i) & 0xff;
  return out;
}

// terminalTabLabel renders the tab text: "Terminal 2", plus the shell's
// foreground process when one is running — "Terminal 1 (make)".
export function terminalTabLabel(t: Pick<IssueTerminal, "index" | "title"> | undefined): string {
  if (!t) return "Terminal";
  const base = `Terminal ${t.index}`;
  return t.title ? `${base} (${t.title})` : base;
}

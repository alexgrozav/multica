// @vitest-environment jsdom
import "@testing-library/jest-dom/vitest";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, cleanup, render, screen, waitFor } from "@testing-library/react";

// Mock xterm (jsdom can't lay a real terminal out) and the API layer; the test
// drives the component's protocol wiring end to end over a fake WebSocket.
const xterm = vi.hoisted(() => {
  class MockTerminal {
    static instances: MockTerminal[] = [];
    cols = 80;
    rows = 24;
    dataCb: ((d: string) => void) | undefined;
    open = vi.fn();
    loadAddon = vi.fn();
    reset = vi.fn();
    dispose = vi.fn();
    write = vi.fn();
    onData = vi.fn((cb: (d: string) => void) => {
      this.dataCb = cb;
      return { dispose: vi.fn() };
    });
    onBinary = vi.fn(() => ({ dispose: vi.fn() }));
    constructor() {
      MockTerminal.instances.push(this);
    }
  }
  return { MockTerminal };
});

vi.mock("@xterm/xterm", () => ({ Terminal: xterm.MockTerminal }));
vi.mock("@xterm/addon-fit", () => ({
  FitAddon: class {
    fit = vi.fn();
  },
}));
vi.mock("./api", () => ({
  createTerminalTicket: vi.fn(async () => "tick-1"),
  terminalSocketUrl: (t: string) => `ws://test/ws/worktree-terminal?ticket=${t}`,
}));

import { TerminalView } from "./terminal-view";

class FakeWebSocket {
  static OPEN = 1;
  static instances: FakeWebSocket[] = [];
  url: string;
  binaryType = "";
  readyState = 0;
  sent: unknown[] = [];
  onopen: (() => void) | null = null;
  onmessage: ((ev: { data: unknown }) => void) | null = null;
  onclose: (() => void) | null = null;
  constructor(url: string) {
    this.url = url;
    FakeWebSocket.instances.push(this);
  }
  send(data: unknown) {
    this.sent.push(data);
  }
  close() {
    this.readyState = 3;
    this.onclose?.();
  }
  emitOpen() {
    this.readyState = FakeWebSocket.OPEN;
    this.onopen?.();
  }
  emitMessage(data: unknown) {
    this.onmessage?.({ data });
  }
}

class FakeResizeObserver {
  observe() {}
  disconnect() {}
}

beforeEach(() => {
  xterm.MockTerminal.instances.length = 0;
  FakeWebSocket.instances.length = 0;
  vi.stubGlobal("WebSocket", FakeWebSocket);
  vi.stubGlobal("ResizeObserver", FakeResizeObserver);
});

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

async function renderConnected() {
  render(<TerminalView issueId="i1" terminalId="t1" />);
  await waitFor(() => expect(FakeWebSocket.instances.length).toBe(1));
  const ws = FakeWebSocket.instances[0]!;
  const term = xterm.MockTerminal.instances[0]!;
  act(() => ws.emitOpen());
  return { ws, term };
}

describe("TerminalView", () => {
  it("connects with a ticket, resets for the replay, and writes PTY bytes into xterm", async () => {
    const { ws, term } = await renderConnected();
    expect(ws.url).toContain("ticket=tick-1");
    expect(ws.binaryType).toBe("arraybuffer");
    // Fresh socket → fresh buffer (the server replays scrollback).
    expect(term.reset).toHaveBeenCalled();

    const bytes = new TextEncoder().encode("hello").buffer;
    act(() => ws.emitMessage(bytes));
    expect(term.write).toHaveBeenCalledWith(new Uint8Array([104, 101, 108, 108, 111]));
  });

  it("sends keystrokes as binary and announces the size on open", async () => {
    const { ws, term } = await renderConnected();
    // The open handler announces cols/rows.
    expect(ws.sent.some((f) => typeof f === "string" && (f as string).includes('"resize"'))).toBe(true);

    act(() => term.dataCb?.("ls\r"));
    // ArrayBuffer.isView instead of instanceof: the bytes come from the jsdom
    // realm's TextEncoder, whose Uint8Array is a different constructor.
    const bin = ws.sent.find((f) => ArrayBuffer.isView(f)) as Uint8Array | undefined;
    expect(bin && Array.from(bin)).toEqual([108, 115, 13]);
  });

  it("shows the exit banner when the session ends", async () => {
    const { ws } = await renderConnected();
    act(() => ws.emitMessage(JSON.stringify({ type: "state", status: "exited", exit_code: 0 })));
    expect(await screen.findByText("Process exited (code 0)")).toBeInTheDocument();
  });

  it("stops reconnecting once exited (a closed socket schedules nothing)", async () => {
    vi.useFakeTimers();
    try {
      render(<TerminalView issueId="i1" terminalId="t1" />);
      await vi.waitFor(() => expect(FakeWebSocket.instances.length).toBe(1));
      const ws = FakeWebSocket.instances[0]!;
      act(() => {
        ws.emitOpen();
        ws.emitMessage(JSON.stringify({ type: "state", status: "exited" }));
        ws.close();
      });
      await act(() => vi.advanceTimersByTimeAsync(30_000));
      expect(FakeWebSocket.instances.length).toBe(1);
    } finally {
      vi.useRealTimers();
    }
  });
});

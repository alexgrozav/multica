"use client";

import { useEffect, useRef, useState } from "react";
import { Loader2 } from "lucide-react";
import { Terminal } from "@xterm/xterm";
import { FitAddon } from "@xterm/addon-fit";
import { ApiError } from "@multica/core/api";
import { createTerminalTicket, terminalSocketUrl } from "./api";
import { ensureXtermStyles } from "./xterm-styles";
import {
  encodeBinaryInput,
  encodeInput,
  parseTerminalCtl,
  resizeFrame,
} from "./terminal-protocol";

// cssVar reads a design token off the root element ("" when undefined).
function cssVar(name: string): string {
  if (typeof document === "undefined") return "";
  return getComputedStyle(document.documentElement).getPropertyValue(name).trim();
}

// resolveColor normalizes any CSS color (the app's tokens are oklch, which
// xterm's own parser can't take) to a concrete value via a canvas fillStyle
// round trip. Falls back when canvas or the color is unavailable (jsdom).
function resolveColor(color: string, fallback: string): string {
  if (!color || typeof document === "undefined") return fallback;
  try {
    const ctx = document.createElement("canvas").getContext("2d");
    if (!ctx) return fallback;
    ctx.fillStyle = fallback;
    ctx.fillStyle = color;
    return typeof ctx.fillStyle === "string" && ctx.fillStyle ? ctx.fillStyle : fallback;
  } catch {
    return fallback;
  }
}

// withAlpha turns a #rrggbb hex into rgba() — used for the selection color so
// no color-mix() support is needed.
function withAlpha(hex: string, alpha: number, fallback: string): string {
  const m = /^#([0-9a-f]{6})$/i.exec(hex);
  if (!m) return fallback;
  const n = parseInt(m[1]!, 16);
  return `rgba(${(n >> 16) & 0xff}, ${(n >> 8) & 0xff}, ${n & 0xff}, ${alpha})`;
}

function buildTheme() {
  const background = resolveColor(cssVar("--background"), "#111113");
  const foreground = resolveColor(cssVar("--foreground"), "#e6e6e9");
  return {
    background,
    foreground,
    cursor: foreground,
    cursorAccent: background,
    selectionBackground: withAlpha(foreground, 0.28, "rgba(128,128,140,0.35)"),
  };
}

function monoFontFamily(): string {
  const v = cssVar("--font-mono");
  return v || 'ui-monospace, SFMono-Regular, Menlo, Monaco, "Cascadia Mono", monospace';
}

type ConnState = "connecting" | "open" | "exited" | "lost";

interface ExitInfo {
  code?: number;
  error?: string;
}

// TerminalView renders one interactive terminal session: an xterm.js instance
// wired to the session's viewer WebSocket (ticket-authenticated). The server
// replays recent scrollback on every (re)connect, so the component can always
// rebuild its buffer from a fresh socket. Keep it mounted across tab switches
// — reconnecting is cheap but resets scroll position and selection.
export function TerminalView({
  issueId,
  terminalId,
}: {
  issueId: string;
  terminalId: string;
}) {
  const hostRef = useRef<HTMLDivElement>(null);
  const [conn, setConn] = useState<ConnState>("connecting");
  const [exitInfo, setExitInfo] = useState<ExitInfo>({});

  useEffect(() => {
    const host = hostRef.current;
    if (!host) return;
    ensureXtermStyles();

    const term = new Terminal({
      fontFamily: monoFontFamily(),
      fontSize: 12,
      lineHeight: 1.2,
      cursorBlink: true,
      scrollback: 5000,
      theme: buildTheme(),
    });
    const fit = new FitAddon();
    term.loadAddon(fit);
    term.open(host);

    let ws: WebSocket | null = null;
    let disposed = false;
    let exited = false;
    let retries = 0;
    let retryTimer: ReturnType<typeof setTimeout> | null = null;

    // A hidden tab panel is 0×0 — fitting there would corrupt the geometry.
    const safeFit = () => {
      if (host.clientWidth < 20 || host.clientHeight < 20) return;
      try {
        fit.fit();
      } catch {
        // fit() throws when the renderer isn't measurable yet; a later resize
        // observation retries.
      }
    };

    const sendResize = () => {
      if (ws?.readyState === WebSocket.OPEN) ws.send(resizeFrame(term.cols, term.rows));
    };

    const scheduleRetry = () => {
      if (disposed || exited) return;
      setConn("lost");
      const delay = Math.min(10_000, 1000 * 2 ** retries++);
      retryTimer = setTimeout(() => void connect(), delay);
    };

    const connect = async () => {
      if (disposed || exited) return;
      setConn("connecting");
      let url: string;
      try {
        url = terminalSocketUrl(await createTerminalTicket(issueId, terminalId));
      } catch (err) {
        // A 404 means the session is gone (closed elsewhere / server restart):
        // stop retrying — the tab reconcile will prune this tab shortly.
        if (err instanceof ApiError && err.status === 404) {
          exited = true;
          setExitInfo({ error: "Terminal session ended" });
          setConn("exited");
        } else {
          scheduleRetry();
        }
        return;
      }
      if (disposed) return;

      const socket = new WebSocket(url);
      socket.binaryType = "arraybuffer";
      ws = socket;

      socket.onopen = () => {
        retries = 0;
        // The server replays scrollback on this fresh socket; drop any stale
        // buffer so a reconnect doesn't duplicate history.
        term.reset();
        safeFit();
        sendResize();
      };
      socket.onmessage = (ev) => {
        if (typeof ev.data === "string") {
          const ctl = parseTerminalCtl(ev.data);
          if (ctl?.type === "state") {
            if (ctl.status === "exited") {
              exited = true;
              setExitInfo({ code: ctl.exit_code, error: ctl.error });
              setConn("exited");
            } else {
              setConn("open");
            }
          }
          return;
        }
        term.write(new Uint8Array(ev.data as ArrayBuffer));
      };
      socket.onclose = () => {
        if (ws === socket) ws = null;
        scheduleRetry();
      };
    };

    // A paste arrives as ONE onData event; the relay caps frames at 1MB, so
    // large inputs are chunked (the PTY reassembles the stream).
    const sendInput = (bytes: Uint8Array) => {
      if (ws?.readyState !== WebSocket.OPEN) return;
      const chunk = 64 << 10;
      for (let i = 0; i < bytes.length; i += chunk) {
        ws.send(bytes.subarray(i, i + chunk));
      }
    };
    const dataSub = term.onData((d) => sendInput(encodeInput(d)));
    const binarySub = term.onBinary((d) => sendInput(encodeBinaryInput(d)));

    // Refit on real size changes — including hidden → shown (0×0 → real), so a
    // terminal opened in a background tab lays out when first revealed.
    let lastCols = 0;
    let lastRows = 0;
    const ro = new ResizeObserver(() => {
      safeFit();
      if (term.cols !== lastCols || term.rows !== lastRows) {
        lastCols = term.cols;
        lastRows = term.rows;
        sendResize();
      }
    });
    ro.observe(host);

    safeFit();
    void connect();

    return () => {
      disposed = true;
      if (retryTimer) clearTimeout(retryTimer);
      ro.disconnect();
      dataSub.dispose();
      binarySub.dispose();
      ws?.close();
      term.dispose();
    };
  }, [issueId, terminalId]);

  return (
    <div className="relative h-full min-h-0 overflow-hidden rounded bg-background">
      <div ref={hostRef} className="h-full w-full p-1.5" />
      {(conn === "connecting" || conn === "lost") && (
        <div className="pointer-events-none absolute inset-0 flex items-center justify-center gap-1.5 text-[11px] text-muted-foreground">
          <Loader2 className="h-3.5 w-3.5 animate-spin" />
          {conn === "lost" ? "Reconnecting…" : "Connecting…"}
        </div>
      )}
      {conn === "exited" && (
        <div className="absolute inset-x-0 bottom-0 flex items-center justify-center gap-1 border-t bg-background/95 px-2 py-1 text-[11px] text-muted-foreground">
          {exitInfo.error
            ? exitInfo.error
            : exitInfo.code !== undefined
              ? `Process exited (code ${exitInfo.code})`
              : "Process exited"}
        </div>
      )}
    </div>
  );
}

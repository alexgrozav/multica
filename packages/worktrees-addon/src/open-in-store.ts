import { useSyncExternalStore } from "react";

// The local apps the daemon can open an issue's working directory in, in menu
// order. Must match the server allowlist (server/addons/worktrees/shared:
// ValidOpenTarget). "copy" (copy path) is a client-only action and is NOT a
// persistable default, so it is intentionally excluded here.
export const OPEN_TARGETS = ["finder", "vscode", "zed", "xcode", "ghostty", "terminal"] as const;
export type OpenTarget = (typeof OPEN_TARGETS)[number];

export function isOpenTarget(v: string): v is OpenTarget {
  return (OPEN_TARGETS as readonly string[]).includes(v);
}

// The default that opens on the main split-button click until the user picks a
// target — Finder is guaranteed present on macOS, so it never silently no-ops.
const DEFAULT_TARGET: OpenTarget = "finder";
const STORAGE_KEY = "multica.worktree.open-in-target";

// A tiny external store so the persisted "last opened target" is shared across
// every OpenInMenu instance and survives reloads. Kept dependency-free (no
// Zustand) and SSR-safe (getServerSnapshot returns the default; localStorage is
// read lazily and guarded) — this add-on renders under both web (Next SSR) and
// desktop.
let cached: OpenTarget | null = null;
const listeners = new Set<() => void>();

function read(): OpenTarget {
  if (typeof window === "undefined") return DEFAULT_TARGET;
  try {
    const v = window.localStorage.getItem(STORAGE_KEY);
    return v && isOpenTarget(v) ? v : DEFAULT_TARGET;
  } catch {
    return DEFAULT_TARGET;
  }
}

function getSnapshot(): OpenTarget {
  if (cached === null) cached = read();
  return cached;
}

function getServerSnapshot(): OpenTarget {
  return DEFAULT_TARGET;
}

function subscribe(cb: () => void): () => void {
  listeners.add(cb);
  return () => listeners.delete(cb);
}

// Persist a newly-opened target as the default. Ignores non-app targets (e.g.
// "copy"), so copying the path never changes the split button's default action.
export function setOpenInTarget(target: string): void {
  if (!isOpenTarget(target) || target === cached) return;
  cached = target;
  try {
    window.localStorage.setItem(STORAGE_KEY, target);
  } catch {
    // best-effort; the in-memory value still updates for this session
  }
  listeners.forEach((l) => l());
}

// Reads the persisted default target (last opened) and re-renders on change.
export function useOpenInTarget(): OpenTarget {
  return useSyncExternalStore(subscribe, getSnapshot, getServerSnapshot);
}

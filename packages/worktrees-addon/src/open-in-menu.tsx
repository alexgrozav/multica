"use client";

import { Fragment, useCallback, useEffect, useMemo, useRef, useState } from "react";
import {
  ChevronDown,
  Code,
  Copy,
  Folder,
  Ghost,
  Hammer,
  SquareCode,
  SquareTerminal,
  type LucideIcon,
} from "lucide-react";
import { toast } from "sonner";
import { Button } from "@multica/ui/components/ui/button";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuShortcut,
  DropdownMenuTrigger,
} from "@multica/ui/components/ui/dropdown-menu";
import { Tooltip, TooltipContent, TooltipTrigger } from "@multica/ui/components/ui/tooltip";
import { useIssueWorktrees, useOpenIssueWorktree } from "./queries";
import {
  OPEN_TARGETS,
  setOpenInTarget,
  useOpenInTarget,
  type OpenTarget,
} from "./open-in-store";
import type { IssueWorktree } from "./types";

// Copy-path is a client-only utility, appended after the app targets. It never
// reaches the server and never becomes the persisted default.
const COPY_KEY = "copy";

interface Row {
  key: OpenTarget | typeof COPY_KEY;
  label: string;
  icon: LucideIcon;
}

// App targets in menu order, matched to OPEN_TARGETS. lucide stand-ins keep the
// icon set consistent with the rest of the app (no bundled brand assets).
const APP_ROWS: Record<OpenTarget, { label: string; icon: LucideIcon }> = {
  finder: { label: "Finder", icon: Folder },
  vscode: { label: "VS Code", icon: Code },
  zed: { label: "Zed", icon: SquareCode },
  xcode: { label: "Xcode", icon: Hammer },
  ghostty: { label: "Ghostty", icon: Ghost },
  terminal: { label: "Terminal", icon: SquareTerminal },
};

const ROWS: Row[] = [
  ...OPEN_TARGETS.map((key) => ({ key, ...APP_ROWS[key] })),
  { key: COPY_KEY, label: "Copy path", icon: Copy },
];

// The issue working directory is the per-issue parent that holds every repo
// checkout — identical across all of the issue's worktree rows. Prefer the
// server-derived working_dir; fall back to the repo path's parent for older
// payloads.
function issueWorkingDir(worktrees: IssueWorktree[]): string {
  const w = worktrees.find((w) => w.status === "ready" && (w.working_dir || w.path));
  if (!w) return "";
  return w.working_dir || parentDir(w.path);
}

function parentDir(p: string): string {
  const i = p.replace(/\/+$/, "").lastIndexOf("/");
  return i > 0 ? p.slice(0, i) : p;
}

function isEditable(target: EventTarget | null): boolean {
  const el = target as HTMLElement | null;
  if (!el) return false;
  return el.tagName === "INPUT" || el.tagName === "TEXTAREA" || el.isContentEditable === true;
}

// A split "Open in <app>" button for an issue's working directory: the main
// button opens the last-used (default) target; the chevron opens a menu of all
// targets + Copy path. Picking an app opens it AND makes it the new default.
// Renders nothing until the issue has a checked-out (ready) worktree.
export function OpenInMenu({ issueId }: { issueId: string }) {
  const { data: worktrees = [] } = useIssueWorktrees(issueId);
  const openMut = useOpenIssueWorktree(issueId);
  const defaultTarget = useOpenInTarget();
  const [menuOpen, setMenuOpen] = useState(false);

  const workingDir = useMemo(() => issueWorkingDir(worktrees), [worktrees]);

  const openTarget = useCallback(
    (key: string) => {
      if (key === COPY_KEY) {
        if (!workingDir) return;
        void navigator.clipboard?.writeText(workingDir);
        toast.success("Copied working directory path");
        return;
      }
      const meta = APP_ROWS[key as OpenTarget];
      if (!meta) return;
      setOpenInTarget(key); // last opened becomes the default action
      toast.message(`Opening in ${meta.label}…`);
      openMut.mutate(key, { onError: () => toast.error(`Couldn't open in ${meta.label}`) });
    },
    [workingDir, openMut],
  );

  // ⌘O / Ctrl+O opens the working dir in the current default target (the menu's
  // ⌘O hint). Read through refs so the always-mounted listener never restages on
  // every render. Ignored while typing in a field.
  const openRef = useRef(openTarget);
  openRef.current = openTarget;
  const defaultRef = useRef(defaultTarget);
  defaultRef.current = defaultTarget;
  useEffect(() => {
    if (!workingDir) return;
    const onKey = (e: KeyboardEvent) => {
      if (e.key.toLowerCase() !== "o" || !(e.metaKey || e.ctrlKey) || e.altKey || e.shiftKey) return;
      if (isEditable(e.target)) return;
      e.preventDefault();
      openRef.current(defaultRef.current);
    };
    document.addEventListener("keydown", onKey);
    return () => document.removeEventListener("keydown", onKey);
  }, [workingDir]);

  if (!workingDir) return null;

  const defaultMeta = APP_ROWS[defaultTarget] ?? APP_ROWS.finder;
  const DefaultIcon = defaultMeta.icon;

  return (
    <div className="flex items-center">
      <Tooltip>
        <TooltipTrigger
          render={
            <Button
              variant="ghost"
              size="icon-sm"
              aria-label={`Open in ${defaultMeta.label}`}
              className="rounded-r-none text-muted-foreground"
              onClick={() => openTarget(defaultTarget)}
            >
              <DefaultIcon />
            </Button>
          }
        />
        <TooltipContent side="bottom">Open in {defaultMeta.label}</TooltipContent>
      </Tooltip>
      <DropdownMenu open={menuOpen} onOpenChange={setMenuOpen}>
        <DropdownMenuTrigger
          render={
            <Button
              variant="ghost"
              size="icon-sm"
              aria-label="Open in…"
              className="ml-px w-5 rounded-l-none px-0 text-muted-foreground"
            >
              <ChevronDown className="!size-3" />
            </Button>
          }
        />
        <DropdownMenuContent
          align="end"
          className="min-w-52"
          onKeyDown={(e) => {
            const row = Number.isInteger(Number(e.key)) ? ROWS[Number(e.key) - 1] : undefined;
            if (row) {
              e.preventDefault();
              setMenuOpen(false);
              openTarget(row.key);
            }
          }}
        >
          {ROWS.map((row, i) => {
            const Icon = row.icon;
            const isDefault = row.key === defaultTarget;
            return (
              <Fragment key={row.key}>
                {row.key === COPY_KEY && <DropdownMenuSeparator />}
                <DropdownMenuItem onClick={() => openTarget(row.key)}>
                  <Icon className="text-muted-foreground" />
                  <span>{row.label}</span>
                  <span className="ml-auto flex items-center gap-2">
                    {isDefault && <DropdownMenuShortcut className="ml-0">⌘O</DropdownMenuShortcut>}
                    <DropdownMenuShortcut className="ml-0 tabular-nums">{i + 1}</DropdownMenuShortcut>
                  </span>
                </DropdownMenuItem>
              </Fragment>
            );
          })}
        </DropdownMenuContent>
      </DropdownMenu>
    </div>
  );
}

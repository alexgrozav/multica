"use client";

import { cn } from "@multica/ui/lib/utils";
import type { FileTabMode } from "./file-tab-state";

// FileModeToggle is the toolbar's Edit/Diff segmented pill, shown in both the
// file editor and the diff view when the file has changes. Switching mode
// converts the tab in place — the hidden editor buffer stays mounted, so
// unsaved edits survive a round trip through the diff.
export function FileModeToggle({
  mode,
  onSwitch,
}: {
  mode: FileTabMode;
  onSwitch: (mode: FileTabMode) => void;
}) {
  return (
    <div
      role="group"
      aria-label="View mode"
      className="flex h-6 shrink-0 items-center gap-0.5 rounded-md bg-muted p-0.5"
    >
      <ModeButton active={mode === "edit"} onClick={() => onSwitch("edit")}>
        Edit
      </ModeButton>
      <ModeButton active={mode === "diff"} onClick={() => onSwitch("diff")}>
        Diff
      </ModeButton>
    </div>
  );
}

function ModeButton({
  active,
  onClick,
  children,
}: {
  active: boolean;
  onClick: () => void;
  children: string;
}) {
  return (
    <button
      type="button"
      aria-pressed={active}
      onClick={onClick}
      className={cn(
        "h-5 rounded px-2 text-[11px] leading-none transition-colors",
        active
          ? "bg-background text-foreground shadow-sm"
          : "text-muted-foreground hover:text-foreground",
      )}
    >
      {children}
    </button>
  );
}

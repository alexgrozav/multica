"use client";

import { useState, useRef } from "react";
import { GitBranch } from "lucide-react";
import {
  Popover,
  PopoverTrigger,
  PopoverContent,
} from "@multica/ui/components/ui/popover";
import { Button } from "@multica/ui/components/ui/button";
import { isImeComposing } from "@multica/core/utils";
import { useT } from "../../../i18n";

/**
 * Free-text picker for the issue's custom git branch (create-time only — the
 * branch is immutable once the issue exists, so this is used by the create
 * dialog, not the detail sidebar). The typed value commits on ANY close
 * (Enter, click-away, Esc) — one predictable path, no lost input.
 */
export function BranchPicker({
  branchName,
  onBranchNameChange,
  trigger: customTrigger,
  triggerRender,
  open: controlledOpen,
  onOpenChange: controlledOnOpenChange,
  align = "start",
  defaultOpen = false,
}: {
  /** Current branch name ("" = default, derived from the issue identifier). */
  branchName: string;
  onBranchNameChange: (value: string) => void;
  trigger?: React.ReactNode;
  triggerRender?: React.ReactElement;
  open?: boolean;
  onOpenChange?: (v: boolean) => void;
  align?: "start" | "center" | "end";
  /** Open the popover on first mount. Used by the create dialog so the field
   *  revealed from the ⋯ menu immediately enters edit state. */
  defaultOpen?: boolean;
}) {
  const { t } = useT("issues");
  const [internalOpen, setInternalOpen] = useState(defaultOpen);
  const open = controlledOpen ?? internalOpen;
  const setOpen = controlledOnOpenChange ?? setInternalOpen;
  const [draft, setDraft] = useState(branchName);
  const draftRef = useRef(draft);
  draftRef.current = draft;

  const commit = (value: string) => {
    onBranchNameChange(value.trim());
  };

  const handleOpenChange = (v: boolean) => {
    if (v) {
      setDraft(branchName);
    } else {
      commit(draftRef.current);
    }
    setOpen(v);
  };

  return (
    <Popover open={open} onOpenChange={handleOpenChange}>
      <PopoverTrigger
        className={triggerRender ? undefined : "flex items-center gap-1.5 cursor-pointer rounded px-1 -mx-1 hover:bg-accent/30 transition-colors"}
        render={triggerRender}
      >
        {customTrigger ?? (
          <>
            <GitBranch className="h-3.5 w-3.5 text-muted-foreground" />
            {branchName ? (
              <span className="truncate font-mono text-[0.6875rem]">{branchName}</span>
            ) : (
              <span className="text-muted-foreground">{t(($) => $.pickers.branch.trigger_label)}</span>
            )}
          </>
        )}
      </PopoverTrigger>
      <PopoverContent className="w-72 p-0" align={align}>
        <div className="px-3 py-2.5">
          <input
            type="text"
            autoFocus
            value={draft}
            onChange={(e) => setDraft(e.target.value)}
            onKeyDown={(e) => {
              if (isImeComposing(e)) return;
              if (e.key === "Enter") {
                e.preventDefault();
                commit(draft);
                setOpen(false);
              }
            }}
            placeholder={t(($) => $.pickers.branch.placeholder)}
            aria-label={t(($) => $.pickers.branch.trigger_label)}
            spellCheck={false}
            autoCapitalize="off"
            autoCorrect="off"
            className="w-full bg-transparent font-mono text-sm placeholder:font-sans placeholder:text-muted-foreground outline-none"
          />
          <p className="mt-1.5 text-xs text-muted-foreground">
            {t(($) => $.pickers.branch.hint)}
          </p>
        </div>
        {branchName && (
          <div className="border-t px-3 py-2">
            <Button
              variant="ghost"
              size="xs"
              onClick={() => {
                setDraft("");
                commit("");
                setOpen(false);
              }}
              className="text-muted-foreground hover:text-foreground"
            >
              {t(($) => $.pickers.branch.clear_action)}
            </Button>
          </div>
        )}
      </PopoverContent>
    </Popover>
  );
}

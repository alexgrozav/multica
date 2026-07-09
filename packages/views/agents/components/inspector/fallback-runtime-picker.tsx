"use client";

import { useMemo, useState } from "react";
import { Layers } from "lucide-react";
import type { AgentRuntime } from "@multica/core/types";
import {
  PickerItem,
  PropertyPicker,
} from "../../../issues/components/pickers";
import { ProviderLogo } from "../../../runtimes/components/provider-logo";
import { CHIP_CLASS } from "./chip";
import { useT } from "../../../i18n";

/**
 * Inline ordered multi-select for the agent's fallback runtimes. Candidates
 * are the workspace runtimes with the SAME provider as the main runtime
 * (cross-provider fallback is rejected server-side — a different provider is
 * a different agent). Click order defines dispatch priority; each selected
 * entry shows its position. The popover stays open across toggles so users
 * can compose the list in one visit.
 */
export function FallbackRuntimePicker({
  mainRuntimeId,
  value,
  runtimes,
  currentUserId,
  canEdit = true,
  onChange,
}: {
  mainRuntimeId: string;
  /** Ordered fallback runtime ids as stored on the agent. */
  value: string[];
  runtimes: AgentRuntime[];
  currentUserId: string | null;
  /** When false, render a static read-only display and skip the popover. */
  canEdit?: boolean;
  onChange: (runtimeIds: string[]) => Promise<void> | void;
}) {
  const { t } = useT("agents");
  const [open, setOpen] = useState(false);

  const main = runtimes.find((r) => r.id === mainRuntimeId) ?? null;
  const candidates = useMemo(
    () =>
      main
        ? runtimes.filter(
            (r) => r.id !== main.id && r.provider === main.provider,
          )
        : [],
    [runtimes, main],
  );

  // Same visibility rule as the main runtime picker: private runtimes are
  // selectable only by their owner (the backend re-checks with the full
  // owner/admin rule, so an admin can still manage via API).
  const isDisabled = (r: AgentRuntime): boolean => {
    if (!currentUserId) return false;
    if (r.owner_id === currentUserId) return false;
    return r.visibility !== "public";
  };

  // Entries whose runtime vanished from the list (deleted machine, stale
  // cache) still count toward the label but render nothing selectable —
  // the server prunes them on the next save.
  const selectedNames = value.map(
    (id) => runtimes.find((r) => r.id === id)?.name ?? "?",
  );
  const label =
    selectedNames.length === 0
      ? t(($) => $.pickers.fallbacks_none)
      : selectedNames.join(" → ");
  const tooltip = t(($) => $.pickers.fallbacks_tooltip);

  if (!canEdit) {
    return (
      <span className="inline-flex min-w-0 items-center gap-1.5 px-1.5 py-0.5 text-xs text-muted-foreground">
        <Layers className="h-3 w-3 shrink-0" />
        <span className="min-w-0 truncate font-mono">{label}</span>
      </span>
    );
  }

  const toggle = (id: string) => {
    const next = value.includes(id)
      ? value.filter((v) => v !== id)
      : [...value, id];
    void onChange(next);
  };

  return (
    <PropertyPicker
      open={open}
      onOpenChange={setOpen}
      width="w-auto min-w-[18rem] max-w-md"
      align="start"
      tooltip={tooltip}
      triggerRender={
        <button type="button" className={CHIP_CLASS} aria-label={tooltip} />
      }
      trigger={
        <>
          <Layers className="h-3 w-3 shrink-0 text-muted-foreground" />
          <span className="min-w-0 truncate font-mono">{label}</span>
        </>
      }
      header={
        <p className="px-3 pb-1 pt-2 text-xs text-muted-foreground">
          {t(($) => $.pickers.fallbacks_hint)}
        </p>
      }
    >
      {candidates.length === 0 ? (
        <p className="px-2 py-3 text-center text-xs text-muted-foreground">
          {t(($) => $.pickers.fallbacks_empty, {
            provider: main?.provider ?? "",
          })}
        </p>
      ) : (
        candidates.map((rt) => {
          const position = value.indexOf(rt.id);
          const selected = position >= 0;
          const rtOnline = rt.status === "online";
          const locked = !selected && isDisabled(rt);
          return (
            <PickerItem
              key={rt.id}
              selected={selected}
              disabled={locked}
              onClick={() => {
                if (locked) return;
                toggle(rt.id);
              }}
              tooltip={
                rtOnline
                  ? t(($) => $.pickers.runtime_online)
                  : t(($) => $.pickers.runtime_offline)
              }
            >
              <ProviderLogo provider={rt.provider} className="h-4 w-4 shrink-0" />
              <div className="min-w-0 flex-1">
                <div className="flex items-center gap-1.5">
                  <span className="truncate text-sm font-medium">{rt.name}</span>
                  {selected && (
                    <span className="shrink-0 rounded bg-primary/10 px-1 text-[10px] font-medium text-primary">
                      {t(($) => $.pickers.fallbacks_position, {
                        position: position + 1,
                      })}
                    </span>
                  )}
                </div>
                {rt.device_info && (
                  <div className="mt-0.5 truncate font-mono text-[10px] text-muted-foreground">
                    {rt.device_info}
                  </div>
                )}
              </div>
              <span
                className={`h-1.5 w-1.5 shrink-0 rounded-full ${
                  rtOnline ? "bg-success" : "bg-muted-foreground/40"
                }`}
                aria-label={
                  rtOnline
                    ? t(($) => $.pickers.runtime_online)
                    : t(($) => $.pickers.runtime_offline)
                }
              />
            </PickerItem>
          );
        })
      )}
    </PropertyPicker>
  );
}

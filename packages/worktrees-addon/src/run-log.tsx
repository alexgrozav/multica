"use client";

import { useEffect, useRef } from "react";
import { useWorktreeRunLog } from "./queries";

// RunLog renders the streamed output for one log channel (a run_task_id or a
// setup_task_id). Lines arrive over the WS into a cache keyed by that id; this
// component only reads them and auto-scrolls to the bottom as they grow. It
// fills its container's height, so the parent (a tab panel) bounds it.
export function RunLog({ channelId }: { channelId: string | undefined }) {
  const { data: lines = [] } = useWorktreeRunLog(channelId);
  const ref = useRef<HTMLPreElement>(null);

  useEffect(() => {
    const el = ref.current;
    if (el) el.scrollTop = el.scrollHeight;
  }, [lines.length]);

  if (lines.length === 0) {
    return (
      <div className="flex h-full items-center justify-center p-4 text-[11px] italic text-muted-foreground">
        No output yet.
      </div>
    );
  }
  return (
    <pre
      ref={ref}
      className="h-full w-full overflow-auto rounded bg-muted/60 p-2 font-mono text-[11px] leading-snug text-muted-foreground"
    >
      {lines.map((l) => (
        <div key={l.seq} className={l.stream === "stderr" ? "text-destructive/90" : undefined}>
          {l.content}
        </div>
      ))}
    </pre>
  );
}

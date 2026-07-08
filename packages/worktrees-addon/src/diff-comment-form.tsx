"use client";

import { useCallback, useEffect, useRef } from "react";
import { Loader2 } from "lucide-react";
import { Button } from "@multica/ui/components/ui/button";
import { Textarea } from "@multica/ui/components/ui/textarea";

// DiffCommentForm is the inline composer portaled into the diff view's
// composer slot (the block widget under the target line). It is fully
// controlled: the draft lives in the diff view's React state, so it survives
// CodeMirror rebuilds when fresh diff sides arrive mid-composition.
export function DiffCommentForm({
  value,
  onChange,
  onSubmit,
  onCancel,
  submitting,
  error,
}: {
  value: string;
  onChange: (value: string) => void;
  onSubmit: () => void;
  onCancel: () => void;
  submitting: boolean;
  error: string | null;
}) {
  const canSubmit = value.trim().length > 0 && !submitting;

  // Submitting disables the textarea, which drops focus to <body>; when the
  // submit fails, put the caret back so retrying with Enter just works. (The
  // diff view handles the other focus-loss case — the composer moving lines.)
  const textareaRef = useRef<HTMLTextAreaElement>(null);
  useEffect(() => {
    if (error) textareaRef.current?.focus();
  }, [error]);

  const handleKeyDown = useCallback(
    (event: React.KeyboardEvent<HTMLTextAreaElement>) => {
      if (event.key === "Enter" && !event.shiftKey && !event.nativeEvent.isComposing) {
        event.preventDefault();
        if (canSubmit) onSubmit();
      } else if (event.key === "Escape") {
        event.preventDefault();
        event.stopPropagation();
        if (!submitting) onCancel();
      }
    },
    [canSubmit, submitting, onSubmit, onCancel],
  );

  return (
    <div className="flex flex-col gap-2">
      <Textarea
        ref={textareaRef}
        autoFocus
        value={value}
        onChange={(event) => onChange(event.target.value)}
        onKeyDown={handleKeyDown}
        placeholder="Add a comment for the AI"
        aria-label="Add a comment for the AI"
        disabled={submitting}
        className="min-h-16 bg-background text-sm shadow-sm"
      />
      {error && <p className="text-xs text-destructive">{error}</p>}
      <div className="flex items-center justify-end gap-1.5">
        <Button
          type="button"
          variant="ghost"
          size="sm"
          className="h-7 px-2.5 text-xs"
          onClick={onCancel}
          disabled={submitting}
        >
          Cancel
        </Button>
        <Button
          type="button"
          variant="secondary"
          size="sm"
          className="h-7 gap-1.5 px-2.5 text-xs"
          onClick={onSubmit}
          disabled={!canSubmit}
        >
          {submitting && <Loader2 className="h-3 w-3 animate-spin" />}
          Comment
          <span aria-hidden className="text-muted-foreground">
            ⏎
          </span>
        </Button>
      </div>
    </div>
  );
}

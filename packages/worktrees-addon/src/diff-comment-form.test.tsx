// @vitest-environment jsdom
import "@testing-library/jest-dom/vitest";
import { useState } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { DiffCommentForm } from "./diff-comment-form";

// The form is controlled by the diff view; this harness plays that role.
function Harness({
  onSubmit = vi.fn(),
  onCancel = vi.fn(),
  submitting = false,
  error = null as string | null,
}) {
  const [value, setValue] = useState("");
  return (
    <DiffCommentForm
      value={value}
      onChange={setValue}
      onSubmit={onSubmit}
      onCancel={onCancel}
      submitting={submitting}
      error={error}
    />
  );
}

afterEach(() => cleanup());

describe("DiffCommentForm", () => {
  it("disables Comment until there is non-blank text", async () => {
    render(<Harness />);
    const button = screen.getByRole("button", { name: /Comment/ });
    expect(button).toBeDisabled();
    await userEvent.type(screen.getByPlaceholderText("Add a comment for the AI"), "  ");
    expect(button).toBeDisabled();
    await userEvent.type(screen.getByPlaceholderText("Add a comment for the AI"), "fix");
    expect(button).toBeEnabled();
  });

  it("submits on Enter and keeps Shift+Enter as a newline", async () => {
    const onSubmit = vi.fn();
    render(<Harness onSubmit={onSubmit} />);
    const box = screen.getByPlaceholderText("Add a comment for the AI");
    await userEvent.type(box, "line one{Shift>}{Enter}{/Shift}line two");
    expect(onSubmit).not.toHaveBeenCalled();
    expect(box).toHaveValue("line one\nline two");
    await userEvent.type(box, "{Enter}");
    expect(onSubmit).toHaveBeenCalledTimes(1);
  });

  it("does not submit an empty draft on Enter", async () => {
    const onSubmit = vi.fn();
    render(<Harness onSubmit={onSubmit} />);
    await userEvent.type(screen.getByPlaceholderText("Add a comment for the AI"), "{Enter}");
    expect(onSubmit).not.toHaveBeenCalled();
  });

  it("cancels on Escape and via the Cancel button", async () => {
    const onCancel = vi.fn();
    render(<Harness onCancel={onCancel} />);
    await userEvent.type(screen.getByPlaceholderText("Add a comment for the AI"), "{Escape}");
    expect(onCancel).toHaveBeenCalledTimes(1);
    await userEvent.click(screen.getByRole("button", { name: "Cancel" }));
    expect(onCancel).toHaveBeenCalledTimes(2);
  });

  it("locks the form and shows the error while submitting / after failure", () => {
    render(<Harness submitting error="Failed to post the comment." />);
    expect(screen.getByPlaceholderText("Add a comment for the AI")).toBeDisabled();
    expect(screen.getByRole("button", { name: "Cancel" })).toBeDisabled();
    expect(screen.getByRole("button", { name: /Comment/ })).toBeDisabled();
    expect(screen.getByText("Failed to post the comment.")).toBeInTheDocument();
  });

  it("refocuses the textarea when a submit failure surfaces", () => {
    const { rerender } = render(<Harness />);
    const box = screen.getByPlaceholderText("Add a comment for the AI");
    box.blur();
    rerender(<Harness error="nope" />);
    expect(box).toHaveFocus();
  });
});

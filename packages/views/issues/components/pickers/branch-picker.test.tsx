import { describe, it, expect } from "vitest";
import { useState } from "react";
import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { renderWithI18n } from "../../../test/i18n";
import { BranchPicker } from "./branch-picker";

// Controlled harness mirroring how the create dialog mounts the picker: open
// state lifted, committed value observable.
function Harness({ initial = "" }: { initial?: string }) {
  const [branch, setBranch] = useState(initial);
  const [open, setOpen] = useState(true);
  return (
    <>
      <BranchPicker
        branchName={branch}
        onBranchNameChange={setBranch}
        open={open}
        onOpenChange={setOpen}
      />
      <output data-testid="committed">{branch}</output>
    </>
  );
}

describe("BranchPicker", () => {
  it("commits the trimmed value on Enter and closes", async () => {
    const user = userEvent.setup();
    renderWithI18n(<Harness />);

    const input = await screen.findByLabelText("Branch");
    await user.type(input, "  feature/login  ");
    await user.keyboard("{Enter}");

    expect(screen.getByTestId("committed")).toHaveTextContent("feature/login");
    await waitFor(() => {
      expect(screen.queryByLabelText("Branch")).not.toBeInTheDocument();
    });
  });

  it("commits the typed value when the popover closes without Enter", async () => {
    const user = userEvent.setup();
    renderWithI18n(<Harness />);

    const input = await screen.findByLabelText("Branch");
    await user.type(input, "feature/escape-close");
    await user.keyboard("{Escape}");

    await waitFor(() => {
      expect(screen.getByTestId("committed")).toHaveTextContent("feature/escape-close");
    });
  });

  it("clears the branch via the clear action", async () => {
    const user = userEvent.setup();
    renderWithI18n(<Harness initial="feature/old" />);

    await user.click(await screen.findByRole("button", { name: "Clear branch" }));

    expect(screen.getByTestId("committed")).toHaveTextContent("");
  });
});

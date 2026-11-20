import React from "react";
import { test, expect, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import PermissionPicker from "../src/PermissionPicker.jsx";

const options = [
  { id: "pi", label: "Pi", detail: "Pi", icon: "terminal" },
  { id: "codex", label: "Codex", detail: "Codex", icon: "terminal" },
];

// Regression for the locked-CLI selector (#1118): a picker that is disabled
// while its menu is open must close the menu immediately and never allow a
// choice from the closing menu. These cover the open -> disabled transition,
// not just an initially-disabled picker.

test("a picker disabled while open immediately hides and inerts its closing menu", () => {
  const props = { value: "pi", options, open: true, onOpenChange: vi.fn(), onSelect: vi.fn(), label: "CLI" };
  const view = render(<PermissionPicker {...props} />);
  const menu = screen.getByRole("menu");
  view.rerender(<PermissionPicker {...props} disabled />);
  expect(menu).toHaveAttribute("aria-hidden", "true");
  expect(menu).toHaveAttribute("inert");
});

test("disabling an open picker tells the parent to close it", () => {
  const onOpenChange = vi.fn();
  const props = { value: "pi", options, open: true, onOpenChange, onSelect: vi.fn(), label: "CLI" };
  const view = render(<PermissionPicker {...props} />);
  view.rerender(<PermissionPicker {...props} disabled />);
  expect(onOpenChange).toHaveBeenCalledWith(false);
});

test("a focused closing menu cannot choose another executor after being disabled", async () => {
  const user = userEvent.setup();
  const onSelect = vi.fn();
  const props = { value: "pi", options, open: true, onOpenChange: vi.fn(), onSelect, label: "CLI" };
  const view = render(<PermissionPicker {...props} />);
  const item = screen.getByRole("menuitemradio", { name: "Codex" });
  item.focus();
  view.rerender(<PermissionPicker {...props} disabled />);
  await user.keyboard("{Enter}");
  expect(onSelect).not.toHaveBeenCalled();
});

test("a disabled picker never opens its menu from the trigger", async () => {
  const user = userEvent.setup();
  const onOpenChange = vi.fn();
  const view = render(
    <PermissionPicker value="pi" options={options} disabled onOpenChange={onOpenChange} onSelect={vi.fn()} label="CLI" />,
  );
  await user.click(screen.getByRole("button", { name: "CLI" }));
  expect(onOpenChange).not.toHaveBeenCalled();
  expect(screen.queryByRole("menu")).not.toBeInTheDocument();
  view.unmount();
});

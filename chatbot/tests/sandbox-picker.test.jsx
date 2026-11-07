import React from "react";
import { render, screen } from "@testing-library/react";
import { describe, expect, test, vi } from "vitest";
import userEvent from "@testing-library/user-event";
import SandboxPicker, { conversationSandboxOptions } from "../src/SandboxPicker";

const project = { id: "p" };
const sandboxes = [
  { id: "business", name: "PHP 业务环境", projectId: "p", driver: "docker" },
  { id: "shared", name: "共享环境", shared: true, driver: "docker" },
  { id: "private", name: "其他业务私有环境", projectId: "other", driver: "docker" },
  { id: "local", name: "宿主机", shared: true, driver: "local" },
];

describe("conversation environment selection", () => {
  test("uses the project menu and opens sandbox creation without changing the choice", async () => {
    const create = vi.fn(), select = vi.fn();
    render(<SandboxPicker chat={{ projectId: "p", projects: [project], sandboxes,
      sandboxProfileId: "", selectSandbox: select }} onCreate={create} />);
    const trigger = screen.getByRole("button", { name: /^运行环境/ });
    trigger.getBoundingClientRect = () => ({top:500,bottom:528,left:200,right:450,width:250,height:28});
    const user = userEvent.setup();
    await user.click(trigger);
    const menu = screen.getByRole("listbox", { name: "沙箱" });
    expect(menu.closest(".select-menu")).toBeInTheDocument();
    await user.click(screen.getByRole("option", { name: "新增沙箱" }));
    expect(create).toHaveBeenCalledOnce();
    expect(select).not.toHaveBeenCalled();
  });

  test("defaults to the agent's choice and lists only accessible containers", () => {
    const options = conversationSandboxOptions(sandboxes, project);
    expect(options.map((o) => o.value)).toEqual(["", "business", "shared"]);
    expect(options[0].label).toBe("由 Agent 选择");
    const missing = conversationSandboxOptions(sandboxes, project, "deleted").at(-1);
    expect(missing).toMatchObject({ value: "deleted", disabled: true });
  });

  test("a task conversation shows the task environment", () => {
    render(<SandboxPicker chat={{
      projectId: "p", projects: [project],
      sandboxes, sandboxLocked: true, workspaceSandbox: sandboxes[0], selectSandbox: vi.fn(),
    }} />);
    expect(screen.getByText("PHP 业务环境")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /^运行环境/ })).not.toBeInTheDocument();
  });

  test("selection is unavailable while saving or generating", () => {
    render(<SandboxPicker chat={{ projectId: "p", projects: [project], sandboxes,
      sandboxProfileId: "", sandboxBusy: true, selectSandbox: vi.fn(),
    }} />);
    expect(screen.getByRole("button", { name: /^运行环境/ })).toBeDisabled();
  });

  test("a project participant can change another creator's conversation environment", async () => {
    const select = vi.fn();
    render(<SandboxPicker chat={{ projectId: "p", projects: [project], sandboxes,
      user: { id: 2 }, session: { userId: 1 }, sandboxProfileId: "", selectSandbox: select,
    }} />);
    const trigger = screen.getByRole("button", { name: /^运行环境/ });
    expect(trigger).toBeEnabled();
    trigger.getBoundingClientRect = () => ({top:500,bottom:528,left:200,right:450,width:250,height:28});
    const user = userEvent.setup();
    await user.click(trigger);
    await user.click(screen.getByRole("option", { name: "PHP 业务环境" }));
    expect(select).toHaveBeenCalledWith("business");
  });
});

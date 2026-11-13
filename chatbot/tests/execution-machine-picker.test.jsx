import React from "react";
import { render, screen } from "@testing-library/react";
import { describe, expect, test, vi } from "vitest";
import userEvent from "@testing-library/user-event";
import ExecutionMachinePicker from "../src/ExecutionMachinePicker";

function fixture(overrides = {}) {
  return {
    user: { id: 1 },
    ready: true,
    machinesReady: true,
    machines: [
      { id: "mac", name: "开发 Mac", status: "online" },
      { id: "linux", name: "测试 Linux", status: "offline" },
    ],
    defaultRunnerId: "mac",
    selectMachine: vi.fn(),
    refreshMachines: vi.fn(),
    ...overrides,
  };
}

async function openPicker() {
  const trigger = screen.getByRole("button", { name: /^执行机器：/ });
  trigger.getBoundingClientRect = () => ({
    top: 500,
    bottom: 528,
    left: 200,
    right: 420,
    width: 220,
    height: 28,
  });
  const user = userEvent.setup();
  await user.click(trigger);
  return { trigger, user };
}

describe("execution machine picker", () => {
  test("an existing empty cloud draft still offers machine selection", async () => {
    render(<ExecutionMachinePicker chat={fixture({session:{id:"draft",messages:[],executionTarget:{pool:"cloud"}}})} />);
    const { user } = await openPicker();
    await user.click(screen.getByRole("option", { name: /测试 Linux 离线/ }));
  });
  test("shows the real personal default, searches bound machines and persists the choice", async () => {
    const chat = fixture();
    render(<ExecutionMachinePicker chat={chat} />);
    const { trigger, user } = await openPicker();
    expect(trigger).toHaveTextContent("开发 Mac");
    expect(trigger.querySelector(".connection-dot")).toHaveClass("online");
    expect(chat.refreshMachines).toHaveBeenCalledOnce();
    expect(screen.getByRole("option", { name: /开发 Mac/ })).toHaveAttribute(
      "aria-selected",
      "true",
    );
    await user.type(screen.getByRole("combobox"), "Linux");
    expect(
      screen.queryByRole("option", { name: /LoopForge 云端/ }),
    ).not.toBeInTheDocument();
    await user.clear(screen.getByRole("combobox"));
    await user.type(screen.getByRole("combobox"), "没有这台机器");
    expect(screen.getByText("没有匹配的执行机器")).toBeInTheDocument();
    await user.clear(screen.getByRole("combobox"));
    await user.type(screen.getByRole("combobox"), "Linux");
    await user.click(screen.getByRole("option", { name: /测试 Linux 离线/ }));
    expect(chat.selectMachine).toHaveBeenCalledWith("linux");
    expect(trigger).toHaveAttribute("aria-expanded", "false");
  });

  test("switches back to cloud and exposes machine management", async () => {
    const chat = fixture(),
      manage = vi.fn();
    render(<ExecutionMachinePicker chat={chat} onManage={manage} />);
    const { user } = await openPicker();
    await user.click(screen.getByRole("option", { name: /LoopForge 云端/ }));
    expect(chat.selectMachine).toHaveBeenCalledWith("");
    await openPicker();
    await user.click(screen.getByRole("option", { name: "管理执行机器" }));
    expect(manage).toHaveBeenCalledOnce();
  });

  test("keeps a workspace on its actual machine after the personal default changes", () => {
    render(
      <ExecutionMachinePicker
        chat={fixture({
          defaultRunnerId: "linux",
          session: { workspaceRunId: "workspace-1", executionTarget:{pool:"runner",runnerId:"mac"} },
          workspaceExecution: {
            executionMode: "runner",
            executionRunnerId: "mac",
          },
        })}
      />,
    );
    expect(screen.getByText("开发 Mac")).toBeInTheDocument();
    expect(screen.queryByText("测试 Linux")).not.toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: /^执行机器：/ }),
    ).not.toBeInTheDocument();
  });

  test("shows a cloud workspace independently of the selected personal machine", () => {
    render(
      <ExecutionMachinePicker
        chat={fixture({
          session: { runId: "run-1",executionTarget:{pool:"cloud",runnerId:"platform-1"} },
          workspaceExecution: {
            executionMode: "runner",
            executionPool: "cloud",
            executionRunnerId: "platform-1",
          },
        })}
      />,
    );
    expect(screen.getByText("LoopForge 云端")).toBeInTheDocument();
    expect(screen.queryByText("开发 Mac")).not.toBeInTheDocument();
  });

  test.each(["busy", "machineSaving", "readOnly", "local"])(
    "disables changes when %s",
    (flag) => {
      render(<ExecutionMachinePicker chat={fixture({ [flag]: true })} />);
      expect(
        screen.getByRole("button", { name: /^执行机器：/ }),
      ).toBeDisabled();
    },
  );

  test("reports a failed load and offers retry without pretending cloud is selected", async () => {
    const chat = fixture({ machinesReady: false, machineError: "连接失败" });
    render(<ExecutionMachinePicker chat={chat} />);
    const { user, trigger } = await openPicker();
    expect(trigger).toHaveTextContent("执行机器加载失败");
    expect(
      screen.queryByRole("option", { name: /LoopForge 云端/ }),
    ).not.toBeInTheDocument();
    await user.click(screen.getByRole("option", { name: "重新加载" }));
    expect(chat.refreshMachines).toHaveBeenCalledTimes(2);
  });

  test("Escape closes the menu and returns focus to the trigger", async () => {
    render(<ExecutionMachinePicker chat={fixture()} />);
    const { user, trigger } = await openPicker();
    await user.keyboard("{Escape}");
    expect(trigger).toHaveFocus();
    expect(trigger).toHaveAttribute("aria-expanded", "false");
  });
});

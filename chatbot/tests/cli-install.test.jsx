import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, expect, test, vi } from "vitest";
import { CLIInstallAction, cliInstallHint } from "../src/cli-install.jsx";

afterEach(() => {
  sessionStorage.clear();
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

function jsonResponse(body, status = 200) {
  return { ok: status < 400, status, json: async () => body };
}

test("offers install only for a missing CLI failure", () => {
  expect(cliInstallHint("请先在个人配置中配置自己的 CodeBuddy API Key")).toBeNull();
  expect(
    cliInstallHint(
      "Claude Code（claude-code）没有安装在当前服务器上。安装后即可在对话里使用。",
    ),
  ).toEqual({ label: "Claude Code", engine: "claude-code" });
});

test("installs the named CLI and asks to send again", async () => {
  const fetchMock = vi.fn(async (_url, options) =>
    options?.method === "POST"
      ? jsonResponse({ engine: "opencode", installed: true })
      : jsonResponse({ engine: "opencode", status: "missing" }),
  );
  vi.stubGlobal("fetch", fetchMock);
  const done = vi.fn();
  render(
    <CLIInstallAction
      text="OpenCode（opencode）没有安装在当前服务器上。安装后即可在对话里使用。"
      onResult={done}
    />,
  );
  expect(screen.getByText("安装在服务器上继续，关闭页面也不会中断。")).toBeInTheDocument();
  const install = await screen.findByRole("button", { name: "安装 OpenCode" });
  await waitFor(() => expect(install).toBeEnabled());
  await userEvent.click(install);
  expect(fetchMock).toHaveBeenCalledWith(
    "/api/assistant/cli-install",
    expect.objectContaining({ method: "POST", credentials: "same-origin" }),
  );
  expect(JSON.parse(fetchMock.mock.calls.find((call) => call[1]?.method === "POST")[1].body)).toEqual({
    engine: "opencode",
  });
  expect(done).toHaveBeenCalledWith("OpenCode 已安装，请重新发送。");
});

test("stays on automatic install when a reload finds the download still running", async () => {
  vi.stubGlobal(
    "fetch",
    vi.fn(async () => jsonResponse({ engine: "pi", status: "installing" })),
  );
  render(
    <CLIInstallAction text="Pi（pi）没有安装在当前服务器上。安装后即可在对话里使用。" />,
  );
  expect(await screen.findByRole("button", { name: "自动安装" })).toBeDisabled();
});

test("a reload shows automatic install before the status request returns", () => {
  sessionStorage.setItem("loopforge.cli-install.opencode", "installing");
  vi.stubGlobal("fetch", vi.fn(() => new Promise(() => {})));
  render(
    <CLIInstallAction text="OpenCode（opencode）没有安装在当前服务器上。安装后即可在对话里使用。" />,
  );
  expect(screen.getByRole("button", { name: "自动安装" })).toBeDisabled();
});

test("a finished install replaces the generation failure", async () => {
  const done = vi.fn();
  vi.stubGlobal(
    "fetch",
    vi.fn(async () => jsonResponse({ engine: "opencode", status: "installed" })),
  );
  render(
    <CLIInstallAction
      text="OpenCode（opencode）没有安装在当前服务器上。安装后即可在对话里使用。"
      onResult={done}
    />,
  );
  await waitFor(() => expect(done).toHaveBeenCalledWith("OpenCode 已安装，请重新发送。"));
});

test("keeps the install button when the download fails", async () => {
  vi.stubGlobal(
    "fetch",
    vi.fn(async (_url, options) =>
      options?.method === "POST"
        ? jsonResponse({ error: "安装失败：registry unreachable" }, 502)
        : jsonResponse({ engine: "pi", status: "missing" }),
    ),
  );
  render(
    <CLIInstallAction text="Pi（pi）没有安装在当前服务器上。安装后即可在对话里使用。" />,
  );
  const install = await screen.findByRole("button", { name: "安装 Pi" });
  await waitFor(() => expect(install).toBeEnabled());
  await userEvent.click(install);
  expect(await screen.findByText("安装失败：registry unreachable")).toBeInTheDocument();
  expect(screen.getByRole("button", { name: "安装 Pi" })).toBeEnabled();
});

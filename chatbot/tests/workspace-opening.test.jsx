import React from "react";
import { render, screen, waitFor, fireEvent } from "@testing-library/react";
import { afterEach, expect, test, vi } from "vitest";
import WorkspaceOpening from "../src/WorkspaceOpening";

afterEach(() => vi.unstubAllGlobals());
const response = (data) => ({ ok: true, status: 200, json: async () => data });
const run = (state, extra = {}) => ({
  kind: "workspace",
  events: [
    {
      workspaceOpening: {
        state,
        phase: "clone",
        message: "正在克隆仓库",
        startedAt: new Date().toISOString(),
        timeoutSeconds: 600,
        ...extra,
      },
    },
  ],
});

test("finds a new binding while the open tool is still running and updates progress", async () => {
  let progress = run("running", { output: "Receiving objects: 20%, 50 KiB/s" });
  const fetch = vi.fn(async (path) =>
    response(
      path.includes("/sessions/") ? { workspaceRunId: "new-run" } : progress,
    ),
  );
  vi.stubGlobal("fetch", fetch);
  const view = render(<WorkspaceOpening sessionId="chat" busy interval={20} />);
  expect(await screen.findByText(/50 KiB\/s/)).toBeInTheDocument();
  expect(fetch).toHaveBeenCalledWith("/api/runs/new-run", expect.anything());
  expect(screen.getByText(/本阶段限时 10分钟/)).toBeInTheDocument();
  progress = run("failed", { message: "clone: context deadline exceeded" });
  expect(await screen.findByText("工作区准备失败")).toBeInTheDocument();
  expect(screen.getByRole("button")).toBeDisabled();
  view.rerender(
    <WorkspaceOpening
      sessionId="chat"
      runId="new-run"
      busy={false}
      interval={20}
    />,
  );
  await waitFor(() => expect(screen.getByRole("button")).toBeEnabled());
});

test("offers retry for the same run and hides recovered errors", async () => {
  vi.stubGlobal(
    "fetch",
    vi.fn(async () =>
      response(
        run("failed", { message: "docker build: apt mirror unreachable" }),
      ),
    ),
  );
  const retry = vi.fn();
  const view = render(<WorkspaceOpening runId="r1" onRetry={retry} />);
  fireEvent.click(
    await screen.findByRole("button", { name: /重试当前工作区/ }),
  );
  expect(retry).toHaveBeenCalledWith("r1");
  vi.stubGlobal(
    "fetch",
    vi.fn(async () => response(run("ready"))),
  );
  view.rerender(<WorkspaceOpening runId="r1" busy onRetry={retry} />);
  await waitFor(() =>
    expect(screen.queryByLabelText("工作区准备进度")).not.toBeInTheDocument(),
  );
});

test("keeps frozen Dockerfile warning visible after readiness", async () => {
  vi.stubGlobal(
    "fetch",
    vi.fn(async () =>
      response(
        run("ready", {
          warning: "项目 Dockerfile 已更新；当前工作区仍使用创建时的版本。",
        }),
      ),
    ),
  );
  render(<WorkspaceOpening runId="r1" readOnly />);
  expect(await screen.findByText(/项目 Dockerfile 已更新/)).toBeInTheDocument();
  expect(screen.queryByRole("button")).not.toBeInTheDocument();
});

test("changing conversations ignores a late response from the old workspace", async () => {
  let resolveOld;
  vi.stubGlobal(
    "fetch",
    vi.fn((path) =>
      path.endsWith("old")
        ? new Promise((resolve) => {
            resolveOld = resolve;
          })
        : Promise.resolve(response(run("ready"))),
    ),
  );
  const view = render(<WorkspaceOpening runId="old" />);
  await waitFor(() => expect(resolveOld).toBeTypeOf("function"));
  view.rerender(<WorkspaceOpening runId="new" />);
  resolveOld(response(run("failed", { message: "old failure" })));
  await waitFor(() =>
    expect(screen.queryByText("old failure")).not.toBeInTheDocument(),
  );
});

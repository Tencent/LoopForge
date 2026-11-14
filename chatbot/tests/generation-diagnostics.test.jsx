import React from "react";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";

const { read } = vi.hoisted(() => ({ read: vi.fn() }));
vi.mock("../src/api.js", async (original) => ({
  ...(await original()),
  api: read,
}));
import GenerationDiagnostics from "../src/GenerationDiagnostics.jsx";

const failure = {
  code: "RATE_LIMITED",
  phase: "provider",
  message: "模型服务请求过于频繁（429）",
  suggestion: "请稍后再试，或检查模型服务的限流配置。",
};
const report = {
  version: 1,
  generationId: "turn-one",
  sessionId: "session-one",
  status: "failed",
  startedAt: "2026-11-13T05:00:00Z",
  failure,
  attempts: [
    {
      id: "turn-one/1",
      engine: "codebuddy",
      model: "model-example",
      status: "failed",
      phase: "execution",
      exitCode: 7,
      events: [
        {
          kind: "error",
          message: "rate limit reached",
          statusCode: 429,
          requestId: "request-example",
          traceId: "trace-example",
          retryable: true,
        },
      ],
      stderr: ["Authorization: <redacted>"],
    },
  ],
};
beforeEach(() => {
  read.mockReset();
  read.mockResolvedValue(report);
});
afterEach(() => {
  vi.restoreAllMocks();
});

describe("generation diagnostics", () => {
  test("shows the failure and fetches details only on request", async () => {
    render(
      <GenerationDiagnostics
        sessionId="session-one"
        generationId="turn-one"
        failure={failure}
      />,
    );
    expect(screen.getByText(failure.message)).toBeInTheDocument();
    expect(read).not.toHaveBeenCalled();
    fireEvent.click(screen.getByRole("button", { name: "查看详情" }));
    expect(await screen.findByText("request-example")).toBeInTheDocument();
    expect(screen.getByText("trace-example")).toBeInTheDocument();
    expect(screen.getByText("退出码 7")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "下载诊断" })).toHaveAttribute(
      "href",
      "/api/assistant/sessions/session-one/generations/turn-one/diagnostics?download=1",
    );
    expect(screen.getByRole("button", { name: "查看详情" })).toHaveAttribute(
      "aria-expanded",
      "true",
    );
  });
  test("copies the report belonging to this failure even after a retry", async () => {
    const writeText = vi.fn(async () => {});
    Object.defineProperty(navigator, "clipboard", {
      configurable: true,
      value: { writeText },
    });
    render(
      <GenerationDiagnostics
        sessionId="session-one"
        generationId="turn-one"
        failure={failure}
      />,
    );
    fireEvent.click(screen.getByRole("button", { name: "复制诊断" }));
    expect(await screen.findByRole("status")).toHaveTextContent(
      "已复制诊断信息",
    );
    expect(read).toHaveBeenCalledWith(
      "/api/assistant/sessions/session-one/generations/turn-one/diagnostics",
    );
    expect(JSON.parse(writeText.mock.calls[0][0])).toEqual(report);
  });
  test("offers a quiet on-demand entry during execution and refreshes it", async () => {
    read.mockResolvedValue({ ...report, status: "running", failure: null });
    render(
      <GenerationDiagnostics
        sessionId="session-one"
        generationId="turn-one"
        compact
      />,
    );
    expect(
      screen.queryByText(/没有.*进展|已运行|90 秒/),
    ).not.toBeInTheDocument();
    expect(read).not.toHaveBeenCalled();
    fireEvent.click(screen.getByRole("button", { name: "诊断信息" }));
    fireEvent.click(await screen.findByRole("button", { name: "刷新诊断" }));
    await waitFor(() => expect(read).toHaveBeenCalledTimes(2));
  });
  test("shows retrieval errors and ignores a late response from another turn", async () => {
    let resolve;
    read.mockReturnValueOnce(
      new Promise((done) => {
        resolve = done;
      }),
    );
    const { rerender } = render(
      <GenerationDiagnostics
        sessionId="session-one"
        generationId="turn-one"
        failure={failure}
      />,
    );
    fireEvent.click(screen.getByRole("button", { name: "查看详情" }));
    rerender(
      <GenerationDiagnostics
        sessionId="session-two"
        generationId="turn-two"
        failure={failure}
      />,
    );
    resolve(report);
    await waitFor(() =>
      expect(screen.queryByText("request-example")).not.toBeInTheDocument(),
    );
    read.mockRejectedValueOnce(new Error("该轮次没有可用的诊断记录"));
    fireEvent.click(screen.getByRole("button", { name: "查看详情" }));
    expect(await screen.findByRole("alert")).toHaveTextContent(
      "该轮次没有可用的诊断记录",
    );
    expect(screen.getByRole("link", { name: "下载诊断" })).toHaveAttribute(
      "href",
      expect.stringContaining("session-two/generations/turn-two"),
    );
  });
  test("renders v2 assessment, environment, tool failures and timeline", async () => {
    read.mockResolvedValueOnce({
      ...report,
      version: 2,
      environment: {
        platformVersion: "v0.9.0",
        revision: "43265cd12345",
        os: "linux",
        arch: "amd64",
        instanceId: "api-prod-1",
      },
      termination: {
        code: "CHECKPOINT_LOST",
        message: "超过 45 秒未收到运行实例心跳，生成已中断",
      },
      assessment: {
        outcome: "termination_recorded",
        summary: "超过 45 秒未收到运行实例心跳，生成已中断",
        evidence: [
          "工具 tapd__stories_list（gateway/1，来源：gateway）有开始记录，未收到结束记录",
        ],
        unknowns: ["只确认平台未继续保存执行状态"],
        nextSteps: ["按 environment.instanceId 查询服务端日志"],
      },
      tools: [
        {
          id: "gateway/1",
          source: "gateway",
          name: "tapd__stories_list",
          status: "unfinished",
          error: "upstream timeout",
        },
      ],
      timeline: [
        {
          at: "2026-11-13T05:00:01Z",
          source: "platform",
          kind: "generation.started",
        },
      ],
    });
    render(
      <GenerationDiagnostics
        sessionId="session-v2"
        generationId="turn-v2"
        failure={failure}
      />,
    );
    fireEvent.click(screen.getByRole("button", { name: "查看详情" }));
    expect(await screen.findByText("api-prod-1")).toBeInTheDocument();
    expect(screen.getByText("CHECKPOINT_LOST")).toBeInTheDocument();
    expect(screen.getByText("tapd__stories_list")).toBeInTheDocument();
    expect(screen.getByText(/执行时间线 · 1 条记录/)).toBeInTheDocument();
  });
  test("read-only viewers see the failure without detail actions", () => {
    const { container, rerender } = render(
      <GenerationDiagnostics
        sessionId="session-one"
        generationId="turn-one"
        failure={failure}
        readOnly
      />,
    );
    expect(screen.getByText(failure.message)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "查看详情" })).toBeNull();
    expect(screen.queryByRole("link", { name: "下载诊断" })).toBeNull();
    rerender(
      <GenerationDiagnostics
        sessionId="session-one"
        generationId="turn-two"
        compact
        readOnly
      />,
    );
    expect(container).toBeEmptyDOMElement();
    expect(read).not.toHaveBeenCalled();
  });
});

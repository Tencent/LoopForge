import React from "react";
import { afterEach, describe, expect, test, vi } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import IssueDispatchCard from "../src/IssueDispatchCard";
import {
  dispatchableIssueLoop,
  savedIssueCardsByMessage,
} from "../src/issue-dispatch.js";
import { collectArtifacts } from "../src/api.js";
import { useConversationRuns } from "../src/runs.js";

describe("issue dispatch helpers", () => {
  test("collects saved issues from assets and confirmed drafts, keeping only the latest message per issue", () => {
    const messages = [
      { role: "user", content: "@列表导出 CSV 帮我澄清这个需求" },
      {
        role: "assistant",
        content: JSON.stringify({
          message: "先保存一版",
          drafts: [
            {
              tool: "issues_save",
              saved: true,
              body: { id: "iss-1", projectId: "p1", title: "旧标题" },
              savedResult: { id: "iss-1", projectId: "p1", title: "列表导出 CSV" },
            },
            {
              tool: "issues_save",
              saved: false,
              body: { id: "iss-unsaved", projectId: "p1", title: "未确认" },
            },
          ],
        }),
      },
      {
        role: "assistant",
        content: JSON.stringify({ message: "已再次更新保存", drafts: [] }),
        assets: [
          { kind: "issue", id: "iss-1", name: "列表导出 CSV（终版）", projectId: "p1", tool: "issues_save" },
          { kind: "issue", id: "iss-foreign", name: "别项目", projectId: "p2", tool: "issues_save" },
          { kind: "role", id: "r1", name: "后端", projectId: "p1", tool: "agents_save" },
        ],
      },
    ];
    const map = savedIssueCardsByMessage(messages, "p1");
    expect(map.has(1)).toBe(false);
    expect(map.get(2)).toEqual([
      { id: "iss-1", title: "列表导出 CSV（终版）", projectId: "p1" },
    ]);
  });

  test("issue assets are excluded from the Artifacts drawer", () => {
    const artifacts = collectArtifacts({
      id: "chat-1",
      messages: [
        {
          role: "assistant",
          content: JSON.stringify({ message: "需求 #4 已保存", drafts: [] }),
          assets: [
            { kind: "issue", id: "iss-4", name: "导出 CSV", projectId: "p1", tool: "issues_save" },
          ],
        },
      ],
    });
    expect(artifacts).toEqual([]);
  });

  test("dispatchableIssueLoop allows unstarted bound LOOPs and filters no-LOOP, running, or terminal issues", () => {
    const base = {
      issue: { id: "iss-4", number: 4, title: "导出 CSV", projectId: "p1", status: "backlog", workflowId: "wf-fe" },
      overall: { runCount: 0, status: "not_started" },
      lanes: [{ workflowId: "wf-fe", workflowName: "通用前端交付", started: false, runCount: 0 }],
    };
    expect(dispatchableIssueLoop(base, "p1")).toEqual({
      issue: base.issue,
      workflowId: "wf-fe",
      workflowName: "通用前端交付",
      laneCount: 1,
    });
    expect(dispatchableIssueLoop({ ...base, issue: { ...base.issue, sopMode: "none" } }, "p1")).toBeNull();
    expect(dispatchableIssueLoop({ ...base, issue: { ...base.issue, status: "done" } }, "p1")).toBeNull();
    expect(dispatchableIssueLoop({ ...base, overall: { runCount: 1 } }, "p1")).toBeNull();
    expect(
      dispatchableIssueLoop({ ...base, lanes: [{ ...base.lanes[0], started: true }] }, "p1"),
    ).toBeNull();
    expect(
      dispatchableIssueLoop({ ...base, issue: { ...base.issue, workflowId: "" }, lanes: [] }, "p1"),
    ).toBeNull();
    expect(dispatchableIssueLoop(base, "other-project")).toBeNull();
  });
});

describe("IssueDispatchCard", () => {
  const originalFetch = globalThis.fetch;
  afterEach(() => {
    globalThis.fetch = originalFetch;
  });

  test("dispatches the bound LOOP with the chat session header and switches to view-run state", async () => {
    const user = userEvent.setup();
    const onDispatched = vi.fn();
    const onOpenRun = vi.fn();
    globalThis.fetch = vi.fn(async (url, init) => {
      if (url === "/api/issues/iss-4/requirement") {
        return {
          ok: true,
          status: 200,
          json: async () => ({
            issue: { id: "iss-4", number: 4, title: "导出 CSV", projectId: "p1", status: "backlog", workflowId: "wf-fe" },
            overall: { runCount: 0 },
            lanes: [{ workflowId: "wf-fe", workflowName: "通用前端交付", started: false, runCount: 0 }],
          }),
        };
      }
      if (url === "/api/issues/iss-4/dispatch") {
        expect(init.method).toBe("POST");
        expect(init.headers["x-agentflow-chat-session"]).toBe("chat-1");
        expect(JSON.parse(init.body)).toEqual({ workflowId: "wf-fe" });
        return {
          ok: true,
          status: 201,
          json: async () => ({ id: "task-1", runId: "run-99", workflowId: "wf-fe" }),
        };
      }
      throw new Error(`unexpected fetch: ${url}`);
    });

    render(
      <IssueDispatchCard
        issueId="iss-4"
        fallbackTitle="导出 CSV"
        projectId="p1"
        sessionId="chat-1"
        onDispatched={onDispatched}
        onOpenRun={onOpenRun}
      />,
    );

    const button = await screen.findByRole("button", { name: "派发 LOOP" });
    expect(screen.getByText("#4 导出 CSV")).toBeInTheDocument();
    expect(screen.getByText("需求已保存 · 已绑定 LOOP「通用前端交付」")).toBeInTheDocument();

    await user.click(button);

    await waitFor(() => expect(screen.getByRole("button", { name: "已派发" })).toBeDisabled());
    expect(onDispatched).toHaveBeenCalledWith(
      { id: "task-1", runId: "run-99", workflowId: "wf-fe" },
      expect.objectContaining({ runId: "run-99", workflowId: "wf-fe" }),
    );
    await user.click(screen.getByRole("button", { name: "查看运行" }));
    expect(onOpenRun).toHaveBeenCalledWith("run-99", "通用前端交付");
  });

  test("stays hidden for no-LOOP requirements and reports dispatch errors cleanly", async () => {
    const user = userEvent.setup();
    globalThis.fetch = vi.fn(async (url) => {
      if (url === "/api/issues/iss-none/requirement") {
        return {
          ok: true,
          status: 200,
          json: async () => ({
            issue: { id: "iss-none", title: "不走 LOOP", projectId: "p1", status: "backlog", sopMode: "none" },
            overall: { runCount: 0 },
            lanes: [],
          }),
        };
      }
      if (url === "/api/issues/iss-err/requirement") {
        return {
          ok: true,
          status: 200,
          json: async () => ({
            issue: { id: "iss-err", title: "缺凭证", projectId: "p1", status: "backlog", workflowId: "wf-fe" },
            overall: { runCount: 0 },
            lanes: [{ workflowId: "wf-fe", workflowName: "通用前端交付", started: false, runCount: 0 }],
          }),
        };
      }
      return {
        ok: false,
        status: 409,
        json: async () => ({ error: "请先配置执行凭据" }),
      };
    });

    const { container } = render(
      <IssueDispatchCard issueId="iss-none" projectId="p1" sessionId="chat-1" />,
    );
    await waitFor(() => expect(globalThis.fetch).toHaveBeenCalledWith("/api/issues/iss-none/requirement", expect.anything()));
    expect(container).toBeEmptyDOMElement();

    render(<IssueDispatchCard issueId="iss-err" projectId="p1" sessionId="chat-1" />);
    await user.click(await screen.findByRole("button", { name: "派发 LOOP" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("请先配置执行凭据");
    expect(screen.getByRole("button", { name: "派发 LOOP" })).toBeEnabled();
  });

  test("useConversationRuns fetches extra dispatched run IDs immediately", async () => {
    globalThis.fetch = vi.fn(async (url) => {
      if (url === "/api/runs/run-404") {
        return {
          ok: true,
          status: 200,
          json: async () => ({
            id: "run-404",
            status: "succeeded",
            workflowId: "wf-fe",
            executionContext: { projectId: "p1", issueId: "iss-4" },
            workflowSnapshot: { id: "wf-fe", name: "通用前端交付", nodes: [] },
            nodes: {},
          }),
        };
      }
      throw new Error(`unexpected fetch: ${url}`);
    });
    function Probe({ extra }) {
      const runs = useConversationRuns({ id: "s-1", projectId: "p1", messages: [] }, 0, extra);
      return <div data-testid="runs">{runs.map((r) => r.id).join(",")}</div>;
    }
    const { rerender } = render(<Probe extra={[]} />);
    expect(screen.getByTestId("runs")).toHaveTextContent("");
    rerender(<Probe extra={["run-404"]} />);
    await waitFor(() => expect(screen.getByTestId("runs")).toHaveTextContent("run-404"));
  });
});

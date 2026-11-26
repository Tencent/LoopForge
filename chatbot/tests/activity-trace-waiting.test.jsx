import React from "react";
import { render, screen } from "@testing-library/react";
import { expect, test } from "vitest";
import { ReplyTimeline } from "../src/ActivityTrace.jsx";

const start = "2026-01-01T00:00:00Z";
const finish = "2026-01-01T00:00:05Z";
const tool = (id, detail, name = "Bash", extra = {}) => ({
  id,
  kind: "tool",
  tool: name,
  detail,
  startedAt: start,
  ...extra,
});
const approval = (id, name, summary, input = {}) => ({
  id,
  actorId: 1,
  tool: name,
  summary,
  input,
  createdAt: start,
});

test("a pending write pauses the reply: waiting title, badge on the row, no spinner", () => {
  const steps = [
    { id: "t1", kind: "text", text: "准备推送。" },
    tool("a", "git push origin fix"),
  ];
  const { container } = render(
    <ReplyTimeline
      steps={steps}
      startedAt={start}
      live
      approvals={[approval("ap1", "Bash", "git push", { command: "git push origin fix" })]}
    />,
  );
  expect(screen.getByText(/待审批 · 已用/)).toBeInTheDocument();
  expect(screen.queryByText(/正在处理/)).toBeNull();
  const row = container.querySelector(".execution-step");
  expect(row.getAttribute("data-status")).toBe("waiting_approval");
  expect(row.textContent).toContain("待审批");
  // 目标仍然可见：等待的是哪条命令一目了然。
  expect(row.textContent).toContain("git push origin fix");
  expect(row.querySelector('[data-icon-motion="busy"]')).toBeNull();
});

test("an MCP tool whose name carries a CLI prefix still finds its row", () => {
  const steps = [
    { id: "t1", kind: "text", text: "更新需求。" },
    tool("a", '{"id":"456"}', "mcp__tapd__stories_update"),
  ];
  const { container } = render(
    <ReplyTimeline
      steps={steps}
      startedAt={start}
      live
      approvals={[approval("ap1", "stories_update", "TAPD · stories_update", { arguments: { id: "456" } })]}
    />,
  );
  const row = container.querySelector(".execution-step");
  expect(row.getAttribute("data-status")).toBe("waiting_approval");
  expect(row.querySelector(".execution-step-badge")).toHaveTextContent("待审批");
});

test("a tool the CLI named differently still gets a line of its own", () => {
  const steps = [
    { id: "t1", kind: "text", text: "创建 PR。" },
    tool("a", "git status", "Bash", { finishedAt: finish }),
  ];
  const { container } = render(
    <ReplyTimeline
      steps={steps}
      startedAt={start}
      live
      approvals={[approval("ap1", "gh", "GitHub CLI 写操作", { command: "gh pr create" })]}
    />,
  );
  const fallback = container.querySelector('.execution-waiting[data-approval="waiting"]');
  expect(fallback).not.toBeNull();
  expect(fallback.textContent).toContain("待审批：GitHub CLI 写操作");
});

test("a reply with no tool steps yet still says what it waits for", () => {
  const { container } = render(
    <ReplyTimeline
      steps={[]}
      startedAt={start}
      live
      approvals={[approval("ap1", "Bash", "git push", { command: "git push origin fix" })]}
    />,
  );
  const current = container.querySelector('.execution-current[data-approval="waiting"]');
  expect(current).not.toBeNull();
  expect(current.textContent).toContain("待审批：git push");
  expect(container.textContent).not.toContain("正在处理");
});

test("several pending writes are counted in the title and on the row", () => {
  const steps = [tool("a", "git push origin fix")];
  const { container } = render(
    <ReplyTimeline
      steps={steps}
      startedAt={start}
      live
      approvals={[
        approval("ap1", "Bash", "git push", { command: "git push origin fix" }),
        approval("ap2", "Bash", "git push", { command: "git push origin main" }),
      ]}
    />,
  );
  expect(screen.getByText(/待审批 · 2 项 · 已用/)).toBeInTheDocument();
  expect(container.querySelector(".execution-step-badge")).toHaveTextContent("待审批 2 项");
});

test("without a pending write the live reply keeps saying it is working", () => {
  const steps = [tool("a", "git push origin fix")];
  const { container } = render(<ReplyTimeline steps={steps} startedAt={start} live />);
  expect(screen.getByText(/正在处理/)).toBeInTheDocument();
  expect(container.querySelector(".execution-step-badge")).toBeNull();
  expect(container.querySelector('.execution-step[data-status="running"]')).not.toBeNull();
});

test("a finished reply never marks a row as waiting", () => {
  const steps = [tool("a", "git push origin fix", "Bash", { finishedAt: finish })];
  const { container } = render(
    <ReplyTimeline
      steps={steps}
      startedAt={start}
      finishedAt={finish}
      approvals={[approval("ap1", "Bash", "git push", { command: "git push origin fix" })]}
    />,
  );
  expect(container.querySelector(".execution-step-badge")).toBeNull();
  expect(screen.queryByText(/待审批/)).toBeNull();
});

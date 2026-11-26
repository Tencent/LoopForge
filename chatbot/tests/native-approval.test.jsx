import React from "react";
import { render, screen } from "@testing-library/react";
import { expect, test, vi } from "vitest";
import NativeApprovalPanel, {
  approvalCommandText,
  approvalKind,
  hasHiddenFields,
} from "../src/NativeApprovalPanel";

const decide = vi.fn();
const waited = (seconds) => new Date(Date.now() - (seconds * 1000 + 500)).toISOString();
const approvals = (items, extra = {}) => ({
  items,
  count: items.length,
  saving: "",
  error: "",
  expired: "",
  decide,
  ...extra,
});
const gitPush = {
  id: "a1",
  actorId: 1,
  tool: "Bash",
  summary: "git push",
  input: { command: "git push origin fix" },
  createdAt: waited(32),
};

test("the card states the tool, the redacted command and the risk of a push", () => {
  const { container } = render(<NativeApprovalPanel approvals={approvals([gitPush])} />);
  expect(screen.getByText("待审批")).toBeInTheDocument();
  expect(container.querySelector(".native-approval-summary")).toHaveTextContent("git push");
  expect(container.querySelector(".native-approval-kind")).toHaveTextContent("git push");
  expect(screen.getByText("git push origin fix")).toBeInTheDocument();
  expect(screen.getByText("Bash")).toBeInTheDocument();
  expect(screen.getByText("已等待 32 秒")).toBeInTheDocument();
  expect(
    screen.getByText("会把本地提交推送到远端仓库，推送后无法在平台侧撤销。"),
  ).toBeInTheDocument();
  // 时效说明不写分钟数，避免与后端的审批超时漂移。
  expect(screen.getByText(/本次审批会失效/)).toBeInTheDocument();
  expect(screen.queryByText(/\d+\s*分钟/)).toBeNull();
  const card = container.querySelector(".native-approval");
  expect(card.getAttribute("data-approval-id")).toBe("a1");
  expect(card.getAttribute("data-tool")).toBe("Bash");
  // 主操作在最右，DOM 顺序即焦点顺序。
  const buttons = [...card.querySelectorAll(".native-approval-actions button")];
  expect(buttons.map((button) => button.textContent)).toEqual(["拒绝", "批准本次操作"]);
});

test("a remote repository write names itself and warns that others will see it", () => {
  const { container } = render(
    <NativeApprovalPanel
      approvals={approvals([
        { id: "a2", actorId: 1, tool: "Bash", summary: "GitHub CLI 写操作", input: { command: "gh pr create" } },
      ])}
    />,
  );
  // 类型标签沿用后端摘要原文，命令块展示要批准的那条命令。
  expect(container.querySelector(".native-approval-kind")).toHaveTextContent("GitHub CLI 写操作");
  expect(container.querySelector(".native-approval-input")).toHaveTextContent("gh pr create");
  expect(
    screen.getByText("会创建或修改远端仓库记录（PR、Issue、评论、Release 等），对他人可见。"),
  ).toBeInTheDocument();
});

test("an external MCP write keeps the arguments and flags hidden fields", () => {
  const { container } = render(
    <NativeApprovalPanel
      approvals={approvals([
        {
          id: "mcp1",
          actorId: 1,
          tool: "stories_update",
          summary: "TAPD · stories_update（1 个字段已隐藏：token）",
          input: { server: "tapd", tool: "stories_update", arguments: { id: "456", token: "[redacted]" } },
        },
      ])}
    />,
  );
  expect(screen.getByText("外部 MCP 写入")).toBeInTheDocument();
  expect(screen.getByText("stories_update")).toBeInTheDocument();
  expect(container.querySelector("pre").textContent).toContain('"token": "[redacted]"');
  expect(screen.getByText("包含已隐藏的敏感字段")).toBeInTheDocument();
  expect(
    screen.getByText("会通过平台网关写入第三方系统，影响该 MCP 服务中的数据。"),
  ).toBeInTheDocument();
});

test("a command without hidden fields does not claim any", () => {
  render(<NativeApprovalPanel approvals={approvals([gitPush])} />);
  expect(screen.queryByText("包含已隐藏的敏感字段")).toBeNull();
});

test("only the card being decided is disabled, so another write can be answered", () => {
  render(
    <NativeApprovalPanel
      approvals={approvals(
        [gitPush, { ...gitPush, id: "a3", input: { command: "git push origin main" }, createdAt: waited(1) }],
        { saving: "a1" },
      )}
    />,
  );
  const cards = [...document.querySelectorAll(".native-approval")];
  expect(cards).toHaveLength(2);
  const [first, second] = cards;
  expect(first.querySelectorAll("button:disabled")).toHaveLength(2);
  expect(second.querySelectorAll("button:disabled")).toHaveLength(0);
});

test("a failed decision is reported with the card still in place", () => {
  render(<NativeApprovalPanel approvals={approvals([gitPush], { error: "审批失败，请重试" })} />);
  expect(screen.getByRole("alert")).toHaveTextContent("审批失败，请重试");
  expect(screen.getByText("git push origin fix")).toBeInTheDocument();
});

test("an expired approval is reported even though its card is gone", () => {
  render(<NativeApprovalPanel approvals={approvals([], { expired: "本轮已结束，审批已失效" })} />);
  expect(screen.getByRole("status")).toHaveTextContent("本轮已结束，审批已失效");
});

test("nothing is rendered while no write waits", () => {
  const { container } = render(<NativeApprovalPanel approvals={approvals([])} />);
  expect(container).toBeEmptyDOMElement();
});

test("the card wording is derived from the summary the server sent", () => {
  expect(approvalKind("git push").kind).toBe("git push");
  expect(approvalKind("GitLab API 写请求").risk).toMatch(/远端仓库记录/);
  expect(approvalKind("HTTP 写请求").kind).toBe("HTTP 写请求");
  expect(approvalKind("推送镜像：app:v1").risk).toMatch(/镜像库/);
  expect(approvalKind("集群或基础设施变更").risk).toMatch(/集群/);
  expect(approvalKind("TAPD · stories_update")).toEqual({
    kind: "外部 MCP 写入",
    risk: "会通过平台网关写入第三方系统，影响该 MCP 服务中的数据。",
  });
  // 认不出的摘要也给一张说明清楚的卡片，绝不隐藏。
  expect(approvalKind("")).toEqual({
    kind: "外部写入",
    risk: "会写入本会话沙箱之外的系统。",
  });
});

test("the command block shows the approved payload as the server sent it", () => {
  expect(approvalCommandText({ command: "ls -la" })).toBe("ls -la");
  expect(approvalCommandText({ cmd: ["git", "push"] })).toBe("git push");
  expect(approvalCommandText({ arguments: { a: 1 } })).toBe('{\n  "arguments": {\n    "a": 1\n  }\n}');
  expect(hasHiddenFields("[redacted]")).toBe(true);
  expect(hasHiddenFields("git push")).toBe(false);
});

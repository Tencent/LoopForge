import React from "react";
import { act, cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, beforeEach, expect, test, vi } from "vitest";
const { apiMock, writeMock } = vi.hoisted(() => ({ apiMock: vi.fn(), writeMock: vi.fn() }));
vi.mock("../src/api", () => ({
  api: apiMock,
  write: writeMock,
  sessionPath: (id) => `/api/assistant/sessions/${id}`,
}));
import NativeApprovalPanel from "../src/NativeApprovalPanel";
import { useNativeApprovals } from "../src/useNativeApprovals";
import { sessionReplyStatus } from "../src/session-status.js";

const createdAt = new Date(Date.now() - 5000).toISOString();
const item = {
  id: "a1",
  actorId: 1,
  tool: "Bash",
  summary: "git push",
  input: { command: "git push origin fix" },
  createdAt,
};
const chat = { session: { id: "chat1" }, user: { id: 1 }, busy: true };

// One component on purpose: the status line, the cards and the poll must all
// agree, because they read the same hook.
function Probe({ chat: current }) {
  const approvals = useNativeApprovals(current);
  const status = sessionReplyStatus(current.session, {
    generation: null,
    busy: current.busy,
    approvalCount: approvals.count,
  });
  return (
    <div>
      <span data-testid="status">{status.label}</span>
      <span data-testid="state">{status.state}</span>
      <NativeApprovalPanel approvals={approvals} />
    </div>
  );
}

const cardCount = () => document.querySelectorAll(".native-approval").length;
const settle = () => act(async () => { await vi.advanceTimersByTimeAsync(0); });
const advance = (ms) => act(async () => { await vi.advanceTimersByTimeAsync(ms); });

beforeEach(() => {
  apiMock.mockReset();
  writeMock.mockReset();
  writeMock.mockResolvedValue({});
  vi.useFakeTimers();
});

afterEach(() => {
  vi.useRealTimers();
  cleanup();
});

test("the waiting state and the card appear together within one poll", async () => {
  apiMock.mockResolvedValueOnce([]).mockResolvedValue([item]);
  render(<Probe chat={chat} />);
  await settle();
  expect(screen.getByTestId("status")).toHaveTextContent("正在回复");
  expect(cardCount()).toBe(0);

  await advance(999);
  expect(cardCount()).toBe(0);
  expect(screen.getByTestId("state")).toHaveTextContent("running");

  await advance(1);
  expect(screen.getByTestId("status")).toHaveTextContent("待审批");
  expect(screen.getByTestId("state")).toHaveTextContent("waiting_approval");
  expect(cardCount()).toBe(1);
});

test("a local, read-only or finished conversation never asks for approvals", async () => {
  const view = render(<Probe chat={{ ...chat, busy: false }} />);
  await settle();
  expect(apiMock).not.toHaveBeenCalled();
  view.rerender(<Probe chat={{ ...chat, local: true }} />);
  await settle();
  expect(apiMock).not.toHaveBeenCalled();
  view.rerender(<Probe chat={{ ...chat, readOnly: true }} />);
  await settle();
  expect(apiMock).not.toHaveBeenCalled();
});

test("approving sends one decision, then the card and the waiting state go together", async () => {
  apiMock.mockResolvedValue([item]);
  render(<Probe chat={chat} />);
  await settle();
  await advance(1000);
  expect(cardCount()).toBe(1);

  fireEvent.click(screen.getByRole("button", { name: "批准本次操作" }));
  await settle();
  expect(writeMock).toHaveBeenCalledWith("/api/assistant/sessions/chat1/native-approvals/a1", {
    decision: "allow",
  });
  expect(cardCount()).toBe(0);
  expect(screen.getByTestId("status")).toHaveTextContent("正在回复");
});

test("denying sends the deny decision for that item only", async () => {
  const other = { ...item, id: "a2", tool: "Bash", input: { command: "gh pr create" }, createdAt };
  apiMock.mockResolvedValue([item, other]);
  render(<Probe chat={chat} />);
  await settle();
  await advance(1000);
  fireEvent.click(screen.getAllByRole("button", { name: "拒绝" })[0]);
  await settle();
  expect(writeMock).toHaveBeenCalledWith("/api/assistant/sessions/chat1/native-approvals/a1", {
    decision: "deny",
  });
  expect(screen.queryByText("git push origin fix")).toBeNull();
  expect(screen.getByText("gh pr create")).toBeInTheDocument();
});

test("a failed decision keeps the card and says why", async () => {
  apiMock.mockResolvedValue([item]);
  writeMock.mockRejectedValueOnce(new Error("审批失败，请重试"));
  render(<Probe chat={chat} />);
  await settle();
  await advance(1000);
  fireEvent.click(screen.getByRole("button", { name: "批准本次操作" }));
  await settle();
  expect(screen.getByRole("alert")).toHaveTextContent("审批失败，请重试");
  expect(cardCount()).toBe(1);
  expect(screen.getByRole("button", { name: "批准本次操作" })).toBeEnabled();
});

test("an approval that expired with its turn is removed and reported", async () => {
  apiMock.mockResolvedValue([item]);
  writeMock.mockRejectedValueOnce(
    Object.assign(new Error("本轮已结束，审批已失效"), { status: 409 }),
  );
  render(<Probe chat={chat} />);
  await settle();
  await advance(1000);
  fireEvent.click(screen.getByRole("button", { name: "批准本次操作" }));
  await settle();
  expect(screen.getByRole("status")).toHaveTextContent("本轮已结束，审批已失效");
  expect(cardCount()).toBe(0);
});

test("cards follow the order the writes were raised in", async () => {
  const later = { ...item, id: "a2", input: { command: "git push origin main" }, createdAt: new Date(Date.now() - 1000).toISOString() };
  apiMock.mockResolvedValue([later, item]);
  const { container } = render(<Probe chat={chat} />);
  await settle();
  await advance(1000);
  expect(
    [...container.querySelectorAll(".native-approval")].map((card) => card.dataset.approvalId),
  ).toEqual(["a1", "a2"]);
});

test("another speaker's pending write is never shown", async () => {
  apiMock.mockResolvedValue([item, { ...item, id: "a9", actorId: 2, input: { command: "git push theirs" } }]);
  render(<Probe chat={chat} />);
  await settle();
  await advance(1000);
  expect(screen.getByText("git push origin fix")).toBeInTheDocument();
  expect(screen.queryByText("git push theirs")).toBeNull();
});

test("a poll already in flight when the decision lands does not bring the card back", async () => {
  let hold;
  apiMock
    .mockResolvedValueOnce([item])
    .mockImplementationOnce(() => new Promise((resolve) => { hold = resolve; }))
    .mockResolvedValue([]);
  render(<Probe chat={chat} />);
  await settle();
  await advance(1000);
  expect(cardCount()).toBe(1);
  fireEvent.click(screen.getByRole("button", { name: "批准本次操作" }));
  await settle();
  expect(cardCount()).toBe(0);
  await act(async () => { hold([item]); await vi.advanceTimersByTimeAsync(0); });
  expect(cardCount()).toBe(0);
});

test("the turn ending clears the cards and stops the poll", async () => {
  apiMock.mockResolvedValue([item]);
  const view = render(<Probe chat={chat} />);
  await settle();
  await advance(1000);
  expect(cardCount()).toBe(1);
  view.rerender(<Probe chat={{ ...chat, busy: false }} />);
  await settle();
  expect(cardCount()).toBe(0);
  expect(screen.getByTestId("status")).toHaveTextContent("当前没有生成中的回复");
  expect(vi.getTimerCount()).toBe(0);
});

test("leaving the conversation clears the cards and the pending request", async () => {
  apiMock.mockResolvedValue([item]);
  const view = render(<Probe chat={chat} />);
  await settle();
  await advance(1000);
  expect(cardCount()).toBe(1);
  // 另一个会话有它自己的待审批集合，切换瞬间不能看到上一个会话的卡片。
  apiMock.mockResolvedValue([]);
  view.rerender(<Probe chat={{ ...chat, session: { id: "chat2" } }} />);
  expect(cardCount()).toBe(0);
  await settle();
  expect(cardCount()).toBe(0);
  view.unmount();
  expect(vi.getTimerCount()).toBe(0);
});

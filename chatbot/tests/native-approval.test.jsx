import React from "react";
import { render, screen, fireEvent, waitFor } from "@testing-library/react";
import { beforeEach, expect, test, vi } from "vitest";
const { apiMock, writeMock } = vi.hoisted(() => ({ apiMock: vi.fn(), writeMock: vi.fn() }));
vi.mock("../src/api", () => ({ api: apiMock, write: writeMock, sessionPath: (id) => `/api/assistant/sessions/${id}` }));
import NativeApprovalPanel from "../src/NativeApprovalPanel";
const chat = { session: { id: "chat1" }, user: { id: 1 }, busy: true };
beforeEach(() => {
  apiMock.mockReset();
  writeMock.mockReset();
  writeMock.mockResolvedValue({});
});
test("shows the speaker's external write and sends a single-action decision", async () => {
  apiMock.mockResolvedValue([
    { id: "a1", actorId: 1, tool: "Bash", summary: "git push", input: { command: "git push origin fix" } },
    { id: "a2", actorId: 2, tool: "Bash", summary: "GitHub CLI 写操作", input: { command: "gh pr create" } },
  ]);
  render(<NativeApprovalPanel chat={chat} />);
  expect(await screen.findByText("批准外部写入：git push")).toBeInTheDocument();
  expect(screen.getByText("git push origin fix")).toBeInTheDocument();
  expect(screen.queryByText(/GitHub CLI/)).not.toBeInTheDocument();
  fireEvent.click(screen.getByRole("button", { name: "批准本次操作" }));
  await waitFor(() => expect(writeMock).toHaveBeenCalledWith("/api/assistant/sessions/chat1/native-approvals/a1", { decision: "allow" }));
  await waitFor(() => expect(screen.queryByText("批准外部写入：git push")).not.toBeInTheDocument());
});
test("clears pending cards as soon as the turn ends", async () => {
  apiMock.mockResolvedValue([{ id: "a1", actorId: 1, tool: "Bash", summary: "git push", input: { command: "git push" } }]);
  const { rerender } = render(<NativeApprovalPanel chat={chat} />);
  await screen.findByText("批准外部写入：git push");
  rerender(<NativeApprovalPanel chat={{ ...chat, busy: false }} />);
  await waitFor(() => expect(screen.queryByText("批准外部写入：git push")).not.toBeInTheDocument());
});

test("shows an external MCP tool and the arguments being approved", async () => {
  apiMock.mockResolvedValue([{
    id: "mcp1", actorId: 1, tool: "stories_update", summary: "TAPD · stories_update",
    input: { server: "tapd", tool: "stories_update", arguments: { id: "456", name: "Approved title" } },
  }]);
  const { container } = render(<NativeApprovalPanel chat={chat} />);
  expect(await screen.findByText("批准外部写入：TAPD · stories_update")).toBeInTheDocument();
  expect(container.querySelector("pre").textContent).toContain('"name": "Approved title"');
  fireEvent.click(screen.getByRole("button", { name: "拒绝" }));
  await waitFor(() => expect(writeMock).toHaveBeenCalledWith("/api/assistant/sessions/chat1/native-approvals/mcp1", { decision: "deny" }));
});

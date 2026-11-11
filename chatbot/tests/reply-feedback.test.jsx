import React from "react";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { describe, expect, test, vi } from "vitest";

const { apiMock } = vi.hoisted(() => ({ apiMock: vi.fn(async () => ({})) }));
vi.mock("../src/api.js", async (original) => ({
  ...(await original()),
  api: apiMock,
}));

import ReplyFeedback from "../src/ReplyFeedback.jsx";

describe("ReplyFeedback", () => {
  test("posts helpful feedback with the session project and message id", async () => {
    apiMock.mockClear();
    render(
      <ReplyFeedback
        projectId="p1"
        sessionId="chat-1"
        index={3}
        message={{ id: "msg-3", createdAt: "2026-11-11T00:00:00Z" }}
      />,
    );
    fireEvent.click(screen.getByRole("button", { name: "有用" }));
    await waitFor(() => expect(apiMock).toHaveBeenCalledTimes(1));
    expect(apiMock.mock.calls[0][0]).toBe("/api/knowledge/feedback");
    const body = JSON.parse(apiMock.mock.calls[0][1].body);
    expect(body).toMatchObject({
      projectId: "p1",
      kind: "helpful",
      surface: "main_bot",
      sessionId: "chat-1",
      messageId: "msg-3",
    });
    expect(await screen.findByText("已反馈：有用")).toBeTruthy();
  });

  test("does not render without a project", () => {
    const { container } = render(<ReplyFeedback index={0} message={{}} />);
    expect(container.textContent).toBe("");
  });
});

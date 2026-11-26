import React from "react";
import { fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, test, vi } from "vitest";
import ImConversationPane from "../src/ImConversationPane.jsx";

describe("ImConversationPane", () => {
  test("renders read-only IM transcript and triggers continue-in-workbench action", () => {
    const onContinue = vi.fn();
    const onRefresh = vi.fn();
    render(
      <ImConversationPane
        summary={{
          id: "conv-1",
          channel: "feishu",
          chatType: "group",
          botName: "飞书研发助手",
          projectId: "p1",
          projectName: "LoopForge",
          writeApproval: "confirm",
        }}
        detail={{
          conversation: {
            id: "conv-1",
            channel: "feishu",
            chatType: "group",
            botName: "飞书研发助手",
            projectId: "p1",
            projectName: "LoopForge",
            writeApproval: "confirm",
          },
          messages: [
            {
              id: "m1",
              senderId: "ou_zhang",
              content: "帮我查下构建失败原因",
              quote: "流水线 #42",
              reply: "已定位：单测超时导致失败。",
              status: "replied",
              createdAt: "2026-11-26T01:00:00Z",
              updatedAt: "2026-11-26T01:00:10Z",
            },
          ],
        }}
        loading={false}
        error=""
        continuing={false}
        onContinue={onContinue}
        onRefresh={onRefresh}
      />,
    );

    expect(screen.getAllByText("飞书 · 群聊").length).toBeGreaterThanOrEqual(1);
    expect(screen.getByText("帮我查下构建失败原因")).toBeTruthy();
    expect(screen.getByText("已定位：单测超时导致失败。")).toBeTruthy();
    expect(screen.getByText(/IM 会话在工作台默认只读回看/)).toBeTruthy();

    fireEvent.click(screen.getByRole("button", { name: "转入个人会话继续" }));
    expect(onContinue).toHaveBeenCalledTimes(1);

    fireEvent.click(screen.getByRole("button", { name: "刷新" }));
    expect(onRefresh).toHaveBeenCalledTimes(1);
  });
});

import React, { createRef } from "react";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, test, vi } from "vitest";

const apiMock = vi.hoisted(() =>
  vi.fn(async () => ({
    items: [
      { id: "ISSUE-1", name: "需求一", type: "issue", projectName: "项目一" },
      { id: "repo-1", name: "仓库一", type: "repository", projectName: "项目一" },
    ],
    nextOffset: 0,
  })),
);
vi.mock("../src/api.js", async (original) => ({
  ...(await original()),
  api: apiMock,
}));

import ActivityTrace from "../src/ActivityTrace.jsx";
import MentionPicker from "../src/MentionPicker.jsx";
import ModelPicker from "../src/ModelPicker.jsx";
import Participants, { Avatar, authorFor, peopleFor } from "../src/Participants.jsx";
import PermissionPicker from "../src/PermissionPicker.jsx";
import { SidebarSection } from "../src/SidebarPreview.jsx";

describe("pickers and activity", () => {
  test("selects mention categories and results", async () => {
    const user = userEvent.setup();
    const selected = vi.fn();
    const editor = { current: document.createElement("textarea") };
    const ref = createRef();
    render(
      <MentionPicker
        ref={ref}
        projectId="p1"
        trigger={{ query: "", start: 0, end: 1 }}
        onSelect={selected}
        onClose={vi.fn()}
        editor={editor}
        issueId="ISSUE-1"
        onManageIssue={vi.fn()}
        canManageIssue
      />,
    );
    await user.click(screen.getByText("需求"));
    expect(await screen.findByText("需求一")).toBeInTheDocument();
    await user.click(screen.getByText("需求一"));
    expect(selected).toHaveBeenCalledWith(expect.objectContaining({ id: "ISSUE-1" }));
    fireEvent.keyDown(screen.getByRole("dialog"), { key: "Escape" });
  });

  test("filters and selects models and permission modes", async () => {
    const user = userEvent.setup();
    const selectModel = vi.fn();
    const selectPermission = vi.fn();
    render(
      <>
        <ModelPicker
          models={[
            { id: "m1", name: "模型一", provider: "openai", family: "GPT" },
            { id: "m2", name: "模型二", provider: "anthropic", disabled: true },
          ]}
          value="m1"
          open
          onOpenChange={vi.fn()}
          onSelect={selectModel}
        />
        <PermissionPicker
          value="confirm"
          open
          onOpenChange={vi.fn()}
          onSelect={selectPermission}
        />
      </>,
    );
    await user.type(screen.getByLabelText("搜索模型"), "模型一");
    await user.click(screen.getByRole("menuitemradio", { name: /模型一/ }));
    await user.click(screen.getByRole("menuitemradio", { name: "完全权限" }));
    expect(selectModel).toHaveBeenCalledWith("m1");
    expect(selectPermission).toHaveBeenCalledWith("auto");
  });

  test("expands detailed activity including failures", async () => {
    const user = userEvent.setup();
    const start = "2026-01-01T00:00:00Z";
    const finish = "2026-01-01T00:00:03Z";
    const steps = [
      { id: "1", kind: "thinking", text: "正在分析", startedAt: start, finishedAt: finish },
      { id: "2", kind: "command", tool: "shell", detail: "npm test", output: "ok", startedAt: start, finishedAt: finish },
      { id: "3", kind: "read", detail: "/tmp/file.js", startedAt: start, finishedAt: finish, failed: true, output: "未配置 CodeBuddy API Key" },
      { id: "4", kind: "status", text: "连接服务", startedAt: start, finishedAt: finish },
    ];
    render(<ActivityTrace steps={steps} startedAt={start} finishedAt={finish} />);
    await user.click(screen.getByText(/已处理/));
    for (const summary of document.querySelectorAll(".execution-step-title"))
      await user.click(summary);
    expect(screen.getByLabelText("失败")).toBeInTheDocument();
  });

  test("renders participants, avatars and sidebar section", async () => {
    const user = userEvent.setup();
    const session = {
      id: "s1",
      shared: true,
      unknownParticipants: true,
      participants: [{ id: 1, name: "测试" }],
      messages: [{ role: "user", authorId: 1, authorName: "测试" }],
    };
    const users = [{ id: 1, chineseName: "测试用户", loginName: "tester" }];
    expect(authorFor(session.messages[0], session, users).name).toBe("测试用户");
    expect(peopleFor(session, users)).toHaveLength(1);
    render(
      <>
        <Participants session={session} users={users} />
        <Participants session={session} users={users} compact />
        <Avatar person={{ name: "甲", loginName: "a" }} />
        <SidebarSection title="项目一" icon="folder" count={2} expanded onToggle={vi.fn()}>
          <span>会话项</span>
        </SidebarSection>
      </>,
    );
    await user.click(screen.getByText("项目一"));
    fireEvent.error(document.querySelector('img[src*="tester"]'));
    expect(screen.getByText("会话项")).toBeInTheDocument();
  });

  test("stacks participant avatars and folds extra speakers into a counter", () => {
    const participants = ["甲", "乙", "丙", "丁"].map((name, index) => ({
      id: index + 1,
      name,
    }));
    const { container } = render(
      <Participants session={{ id: "s2", participants }} users={[]} compact />,
    );
    expect(container.querySelectorAll(".human-avatar")).toHaveLength(3);
    expect(container.querySelectorAll(".human-avatar.is-stacked")).toHaveLength(2);
    expect(container.querySelector(".participant-count").textContent).toBe("+1");
  });
});

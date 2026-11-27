import React from "react";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";

const chat = {
  user: { id: 1, loginName: "tester", chineseName: "测试用户" },
  users: [{ id: 1, loginName: "tester", chineseName: "测试用户" }],
  projects: [{ id: "p1", name: "核心平台", ownerId: 1, members: [] }],
  models: [{ id: "m1", name: "GPT-Test", provider: "openai" }],
  sessions: [],
  imConversations: [],
  session: null,
  activeId: null,
  revealId: null,
  projectId: "p1",
  issueId: "",
  model: "m1",
  effort: "",
  executor: "codebuddy",
  executors: [{ id: "codebuddy", icon: "sparkles", label: "CodeBuddy", detail: "CodeBuddy" }],
  setExecutor: vi.fn(),
  sponsoredModel: "m1",
  generation: null,
  live: { answer: "", thinking: "", tools: [], activity: [] },
  error: "",
  ready: true,
  busy: false,
  submitting: false,
  readOnly: false,
  open: vi.fn(),
  newChat: vi.fn(),
  setError: vi.fn(),
  setModel: vi.fn(),
  setEffort: vi.fn(),
  setSession: vi.fn(),
  send: vi.fn(async () => true),
  stop: vi.fn(),
  rename: vi.fn(),
  archive: vi.fn(),
  remove: vi.fn(),
  edit: vi.fn(),
  regenerate: vi.fn(),
  selectProject: vi.fn(async () => true),
  linkIssue: vi.fn(),
  applyDraft: vi.fn(),
  refresh: vi.fn(async () => {}),
};

vi.mock("../src/useChat.js", () => ({ useChat: () => chat }));
vi.mock("../src/useLocalConversations.js", () => ({
  useLocalConversations: (value) => value,
}));
vi.mock("../src/runs.js", async (original) => ({
  ...(await original()),
  useConversationRuns: () => [],
}));
vi.mock("mermaid", () => ({
  default: { initialize: vi.fn(), render: vi.fn(async () => ({ svg: "<svg />" })) },
}));

import App from "../src/App.jsx";

describe("App 3-tab sidebar actions (IM continue & scheduled rerun)", () => {
  const originalFetch = globalThis.fetch;

  beforeEach(() => {
    chat.sessions = [];
    chat.imConversations = [];
    chat.session = null;
    chat.activeId = null;
    chat.open.mockReset();
    chat.refresh.mockReset().mockResolvedValue(undefined);
    chat.setError.mockReset();
  });

  afterEach(() => {
    globalThis.fetch = originalFetch;
  });

  test("shows initial IM badge, continues an IM conversation via POST, and clears IM view when switching tabs", async () => {
    const imItem = {
      id: "conv-1",
      botId: "bot-1",
      botName: "研发群助手",
      channel: "wecom",
      channelName: "企业微信",
      projectId: "p1",
      projectName: "核心平台",
      chatType: "group",
      writeApproval: "readonly",
      senderId: "lisi",
      preview: "帮我看下构建失败",
      status: "replied",
      updatedAt: "2026-11-26T08:00:00Z",
    };
    chat.imConversations = [imItem];

    const calls = [];
    globalThis.fetch = vi.fn(async (url, init = {}) => {
      calls.push({ url: String(url), method: init.method || "GET" });
      if (String(url) === "/api/bot-interactions/conversations") {
        return { ok: true, status: 200, json: async () => [imItem] };
      }
      if (String(url) === "/api/bot-interactions/conversations/conv-1") {
        return {
          ok: true,
          status: 200,
          json: async () => ({
            conversation: imItem,
            messages: [
              {
                id: "m1",
                senderId: "lisi",
                content: "帮我看下构建失败",
                reply: "已定位到超时测试",
                status: "replied",
                createdAt: "2026-11-26T08:00:00Z",
                updatedAt: "2026-11-26T08:00:05Z",
              },
            ],
          }),
        };
      }
      if (String(url) === "/api/bot-interactions/conversations/conv-1/continue") {
        return { ok: true, status: 201, json: async () => ({ id: "chat-continued", source: "chat" }) };
      }
      return { ok: true, status: 200, json: async () => [] };
    });

    render(<App />);
    const toggle = screen.queryByRole("button", { name: "显示会话侧边栏" });
    if (toggle) fireEvent.click(toggle);

    // Badge is visible on initial load before opening IM tab.
    const imTab = screen.getByRole("tab", { name: /IM/ });
    expect(imTab).toHaveTextContent("1");

    fireEvent.click(imTab);
    const row = await screen.findByRole("button", { name: /研发群助手 · 群聊/ });
    fireEvent.click(row);

    const continueBtn = await screen.findByRole("button", { name: "转入个人会话继续" });

    // Switching to 定时任务 tab clears the IM pane immediately.
    fireEvent.click(screen.getByRole("tab", { name: /定时任务/ }));
    expect(screen.queryByRole("button", { name: "转入个人会话继续" })).toBeNull();

    // Switch back to IM, open again, and click "转入个人会话继续".
    fireEvent.click(imTab);
    fireEvent.click(await screen.findByRole("button", { name: /研发群助手 · 群聊/ }));
    fireEvent.click(await screen.findByRole("button", { name: "转入个人会话继续" }));

    await waitFor(() => {
      expect(calls).toContainEqual({
        url: "/api/bot-interactions/conversations/conv-1/continue",
        method: "POST",
      });
      expect(chat.open).toHaveBeenCalledWith("chat-continued");
    });
    expect(chat.setError).not.toHaveBeenCalled();
    expect(continueBtn).toBeTruthy();
  });

  test("triggers scheduled job rerun from top context banner and sidebar quick button via POST", async () => {
    const schedSession = {
      id: "sched-s1",
      title: "定时 · 每日站会摘要",
      projectId: "p1",
      source: "scheduled",
      taskId: "job-1",
      updatedAt: "2026-11-26T09:00:00Z",
      messages: [{ role: "user", content: "生成站会摘要" }],
    };
    chat.sessions = [schedSession];
    chat.activeId = "sched-s1";
    chat.session = schedSession;

    const calls = [];
    globalThis.fetch = vi.fn(async (url, init = {}) => {
      calls.push({ url: String(url), method: init.method || "GET" });
      if (String(url) === "/api/schedule-jobs") {
        return {
          ok: true,
          status: 200,
          json: async () => [{ id: "job-1", name: "每日站会摘要", projectId: "p1", skipDispatch: true, enabled: true, cron: "0 9 * * 1-5" }],
        };
      }
      if (String(url) === "/api/schedule-jobs/job-1/run") {
        return {
          ok: true,
          status: 202,
          json: async () => ({ id: "job-1", name: "每日站会摘要", lastStatus: "running", lastSessionId: "sched-s2" }),
        };
      }
      return { ok: true, status: 200, json: async () => [] };
    });

    render(<App />);

    const rerunBtn = await screen.findByRole("button", { name: "立即再跑一次" });
    fireEvent.click(rerunBtn);

    await waitFor(() => {
      expect(calls).toContainEqual({ url: "/api/schedule-jobs/job-1/run", method: "POST" });
      expect(chat.open).toHaveBeenCalledWith("sched-s2");
    });
    expect(chat.setError).not.toHaveBeenCalled();
  });
});

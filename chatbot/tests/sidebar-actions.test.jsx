import React from "react";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";

const chat = {
  user: { id: 1, loginName: "tester", chineseName: "测试用户" },
  users: [{ id: 1, loginName: "tester", chineseName: "测试用户" }],
  projects: [{ id: "p1", name: "核心平台", ownerId: 1, members: [] }],
  models: [{ id: "m1", name: "GPT-Test", provider: "openai" }],
  sessions: [],
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

describe("App 2-tab sidebar actions (personal / scheduled & scheduled rerun)", () => {
  const originalFetch = globalThis.fetch;

  beforeEach(() => {
    chat.sessions = [];
    chat.session = null;
    chat.activeId = null;
    chat.open.mockReset();
    chat.refresh.mockReset().mockResolvedValue(undefined);
    chat.setError.mockReset();
  });

  afterEach(() => {
    globalThis.fetch = originalFetch;
  });

  test("renders personal and scheduled tabs cleanly without an IM tab", async () => {
    chat.sessions = [
      {
        id: "chat-1",
        title: "普通对话",
        projectId: "p1",
        source: "chat",
        updatedAt: "2026-11-26T08:00:00Z",
      },
      {
        id: "sched-1",
        title: "定时 · 每日巡检",
        projectId: "p1",
        source: "scheduled",
        taskId: "job-1",
        updatedAt: "2026-11-26T09:00:00Z",
      },
    ];
    globalThis.fetch = vi.fn(async () => ({ ok: true, status: 200, json: async () => [] }));

    render(<App />);
    const toggle = screen.queryByRole("button", { name: "显示会话侧边栏" });
    if (toggle) fireEvent.click(toggle);

    const personalTab = screen.getByRole("tab", { name: /个人/ });
    const scheduledTab = screen.getByRole("tab", { name: /定时任务/ });
    expect(personalTab).toHaveAttribute("aria-selected", "true");
    expect(scheduledTab).toHaveAttribute("aria-selected", "false");
    expect(screen.queryByRole("tab", { name: /IM/ })).toBeNull();

    fireEvent.click(scheduledTab);
    expect(scheduledTab).toHaveAttribute("aria-selected", "true");
    expect(await screen.findByText("每日巡检")).toBeTruthy();
  });

  test("guests see a sign-in hint on both tabs instead of an endless connecting label", async () => {
    const user = chat.user;
    const ready = chat.ready;
    chat.user = { id: 0, loginName: "guest", chineseName: "访客", isGuest: true };
    chat.ready = false;
    globalThis.fetch = vi.fn(async () => ({ ok: true, status: 200, json: async () => [] }));
    try {
      render(<App />);
      const toggle = screen.queryByRole("button", { name: "显示会话侧边栏" });
      if (toggle) fireEvent.click(toggle);

      expect(screen.getByText("登录后查看历史会话")).toBeTruthy();
      expect(screen.queryByText("正在连接…")).toBeNull();

      fireEvent.click(screen.getByRole("tab", { name: /定时任务/ }));
      expect(screen.getByText("登录后查看历史会话")).toBeTruthy();
      expect(screen.queryByText("正在连接…")).toBeNull();
      expect(screen.queryByText("还没有定时任务会话")).toBeNull();
    } finally {
      chat.user = user;
      chat.ready = ready;
    }
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

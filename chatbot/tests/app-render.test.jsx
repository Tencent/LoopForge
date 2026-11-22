import React from "react";
import { act, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, test, vi } from "vitest";
import { modelProviderFor } from "../src/ModelProviderIcon";

const chat = {
  user: { id: "u1", loginName: "tester", chineseName: "测试用户" },
  users: [{ id: "u1", loginName: "tester", chineseName: "测试用户" }],
  projects: [
    { id: "p1", name: "示例项目", ownerId: "u1", members: ["u1"] },
    { id: "p2", name: "未加入项目", ownerId: "u2", members: [] },
  ],
  models: [
    { id: "m1", name: "GPT-Test", provider: "openai", effort: true },
    { id: "m2", name: "Claude-Test", provider: "anthropic" },
  ],
  sessions: [],
  session: null,
  activeId: null,
  revealId: null,
  projectId: "",
  issueId: "",
  model: "m1",
  effort: "",
  executor: "codebuddy",
  executors: [
    { id: "codebuddy", icon: "sparkles", label: "CodeBuddy", detail: "CodeBuddy" },
    { id: "codex", icon: "terminal", label: "Codex", detail: "Codex" },
  ],
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
  refresh: vi.fn(),
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

describe("App", () => {
  test("pages the transcript without losing the reading position or original message indexes", () => {
    const original = { activeId: chat.activeId, session: chat.session };
    const messages = Array.from({ length: 85 }, (_, index) => ({
      role: "user", content: "Message " + index,
    }));
    const open = (id, rows) => Object.assign(chat, { activeId: id, session: { id, messages: rows } });
    const more = () => screen.queryByRole("button", { name: "显示更早的消息" });
    open("long", messages.slice(0, 39));
    try {
      const view = render(<App />);
      const rows = () => [...view.container.querySelectorAll("[data-message-index]")];
      expect(rows()).toHaveLength(39);
      expect(more()).toBeNull();
      open("long", messages);
      view.rerender(<App />);
      expect(rows()).toHaveLength(40);
      expect(rows()[0].dataset.messageIndex).toBe("45");
      const scroller = view.container.querySelector(".message-scroll");
      Object.defineProperty(scroller, "scrollHeight", { value: 2000 });
      fireEvent.scroll(scroller, { target: { scrollTop: 100 } });
      open("long", [...messages, { role: "assistant", content: "New reply" }]);
      view.rerender(<App />);
      expect(rows()[0].dataset.messageIndex).toBe("45");
      expect(rows().at(-1)).toHaveTextContent("New reply");
      fireEvent.click(more());
      expect(rows()[0].dataset.messageIndex).toBe("5");
      fireEvent.click(within(rows()[0]).getByRole("button", { name: "编辑消息" }));
      expect(within(screen.getByRole("dialog")).getByRole("textbox")).toHaveValue("Message 5");
      fireEvent.click(within(screen.getByRole("dialog")).getByRole("button", { name: "关闭" }));
      fireEvent.click(more());
      expect(rows()).toHaveLength(86);
      expect(more()).toBeNull();
      open("other", messages);
      view.rerender(<App />);
      expect(rows()).toHaveLength(40);
      expect(rows()[0].dataset.messageIndex).toBe("45");
    } finally {
      Object.assign(chat, original);
    }
  });

  test("pauses without showing an intermediate stopping state", () => {
    const previous = { ...chat };
    Object.assign(chat, { activeId: "stopping-chat", busy: true, stopping: true,
      generation: { id: "g-stop", status: "running" },
      session: { id: "stopping-chat", messages: [{ role: "user", content: "检查目录" }] } });
    let view;
    try {
      view = render(<App />);
      expect(screen.queryByText(/正在停止/)).not.toBeInTheDocument();
      expect(screen.getByRole("button", { name: "停止生成" })).toBeDisabled();
      expect(screen.queryByRole("button", { name: "发送消息" })).not.toBeInTheDocument();
      Object.assign(chat, { busy: false, stopping: false, generation: { id: "g-stop", status: "cancelled" } });
      view.rerender(<App />);
      expect(screen.queryByText("正在停止…")).not.toBeInTheDocument();
      expect(screen.getByRole("button", { name: "发送消息" })).toBeInTheDocument();
    } finally {
      view?.unmount();
      for (const key of Object.keys(chat)) if (!(key in previous)) delete chat[key];
      Object.assign(chat, previous);
    }
  });
  test("keeps draft approval across a reload and moves it to the saved conversation", async () => {
    const original = { activeId: chat.activeId, session: chat.session };
    const user = userEvent.setup();
    const first = render(<App />);
    await user.click(screen.getByRole("button", { name: "操作确认方式" }));
    await user.click(screen.getByRole("menuitemradio", { name: "帮我批准" }));
    expect(screen.getByRole("button", { name: "操作确认方式" })).toHaveTextContent("帮我批准");
    first.unmount();
    const reloaded = render(<App />);
    try {
      expect(screen.getByRole("button", { name: "操作确认方式" })).toHaveTextContent("帮我批准");
      chat.send.mockImplementationOnce(async ({ onSessionCreated }) => {
        onSessionCreated({ id: "new-session" });
        return true;
      });
      await user.type(screen.getByRole("textbox", { name: /消息|输入/ }), "你好");
      await user.click(screen.getByRole("button", { name: "发送消息" }));
      expect(chat.send).toHaveBeenLastCalledWith(expect.objectContaining({ approvalMode: "assisted" }));
      expect(screen.getByRole("button", { name: "操作确认方式" })).toHaveTextContent("请求批准");
      Object.assign(chat, { activeId: "new-session", session: { id: "new-session", messages: [] } });
      reloaded.rerender(<App />);
      expect(screen.getByRole("button", { name: "操作确认方式" })).toHaveTextContent("帮我批准");
    } finally {
      Object.assign(chat, original);
    }
  });
  test.each([
    { name: "moves a downgrade", submitted: "auto", changed: "请求批准", saved: "请求批准" },
    { name: "moves an upgrade", submitted: "confirm", changed: "完全权限", saved: "完全权限" },
    { name: "keeps the current tab's approval", submitted: "confirm", otherTab: true, changed: "完全权限", saved: "请求批准" },
  ])("$name while the first session saves", async ({ submitted, changed, saved, otherTab }) => {
    const original = { activeId: chat.activeId, session: chat.session };
    const user = userEvent.setup();
    const view = render(<App />);
    const select = async (name) => {
      await user.click(screen.getByRole("button", { name: "操作确认方式" }));
      await user.click(screen.getByRole("menuitemradio", { name }));
    };
    try {
      if (submitted === "auto") await select("完全权限");
      let finish;
      chat.send.mockImplementationOnce(({ onSessionCreated }) => new Promise((resolve) => {
        finish = () => {
          onSessionCreated?.({ id: "new-session" });
          resolve(true);
        };
      }));
      await user.type(screen.getByRole("textbox", { name: "消息" }), "你好");
      await user.click(screen.getByRole("button", { name: "发送消息" }));
      expect(chat.send).toHaveBeenLastCalledWith(expect.objectContaining({ approvalMode: submitted }));
      if (otherTab)
        localStorage.setItem("loopforge.assistant.conversation.u1.approval.new:", JSON.stringify({ mode: "auto" }));
      else
        await select(changed);
      await act(async () => finish());
      expect(screen.getByRole("button", { name: "操作确认方式" })).toHaveTextContent(otherTab ? changed : "请求批准");
      Object.assign(chat, { activeId: "new-session", session: { id: "new-session", messages: [] } });
      view.rerender(<App />);
      expect(screen.getByRole("button", { name: "操作确认方式" })).toHaveTextContent(saved);
    } finally {
      Object.assign(chat, original);
    }
  });
  test("keeps the open conversation's draft when another draft finishes sending", async () => {
    const original = { activeId: chat.activeId, session: chat.session };
    const user = userEvent.setup();
    const view = render(<App />);
    let finish;
    chat.send.mockImplementationOnce(({ onSessionCreated }) => new Promise((resolve) => {
      finish = () => {
        onSessionCreated({ id: "new-session" });
        resolve(true);
      };
    }));
    const input = () => screen.getByRole("textbox", { name: "消息" });
    try {
      await user.type(input(), "原草稿");
      await user.click(screen.getByRole("button", { name: "发送消息" }));
      Object.assign(chat, { activeId: "s1", session: { id: "s1", messages: [] } });
      view.rerender(<App />);
      await user.type(input(), "另一个会话的草稿");
      await act(async () => finish());
      expect(input()).toHaveValue("另一个会话的草稿");
    } finally {
      Object.assign(chat, original);
    }
  });
  test.each([false, true])("keeps a newer draft and its approval distinct from the first send (before save: %s)", async (beforeSave) => {
    const original = { activeId: chat.activeId, session: chat.session, projectId: chat.projectId };
    const user = userEvent.setup();
    const saved = { id: "new-session", projectId: "p1", messages: [] };
    let create, accept, wasSubmittedDraft;
    chat.send.mockImplementationOnce(({ onSessionCreated, isDraftActive }) => new Promise((resolve) => {
      create = () => onSessionCreated(saved);
      accept = () => resolve(true);
      wasSubmittedDraft = isDraftActive;
    }));
    const input = () => screen.getByRole("textbox", { name: "消息" });
    try {
      chat.projectId = "p1";
      const view = render(<App />);
      await user.type(input(), "原草稿");
      await user.click(screen.getByRole("button", { name: "发送消息" }));
      if (!beforeSave) {
        await act(async () => create());
        Object.assign(chat, { activeId: saved.id, session: saved });
        view.rerender(<App />);
        Object.assign(chat, { activeId: null, session: null });
        view.rerender(<App />);
      }
      await user.clear(input());
      await user.type(input(), "另一份新草稿");
      expect(wasSubmittedDraft()).toBe(false);
      await user.click(screen.getByRole("button", { name: "操作确认方式" }));
      await user.click(screen.getByRole("menuitemradio", { name: "完全权限" }));
      if (beforeSave) await act(async () => create());
      await act(async () => accept());
      expect(input()).toHaveValue("另一份新草稿");
      expect(JSON.parse(localStorage.getItem("agentflow.native-chat.draft.u1.new:p1")).text).toBe("另一份新草稿");
      expect(screen.getByRole("button", { name: "操作确认方式" })).toHaveTextContent("完全权限");
      Object.assign(chat, { activeId: saved.id, session: saved });
      view.rerender(<App />);
      expect(screen.getByRole("button", { name: "操作确认方式" })).toHaveTextContent("请求批准");
    } finally {
      Object.assign(chat, original);
    }
  });
  test("restores full permission only for the conversation and account that picked it", async () => {
    const original = { activeId: chat.activeId, session: chat.session, user: chat.user };
    const user = userEvent.setup();
    const open = (id) => Object.assign(chat, { activeId: id, session: id && { id, messages: [] } });
    try {
      open("s1");
      const first = render(<App />);
      await user.click(screen.getByRole("button", { name: "操作确认方式" }));
      await user.click(screen.getByRole("menuitemradio", { name: "完全权限" }));
      open("s2");
      first.rerender(<App />);
      expect(screen.getByRole("button", { name: "操作确认方式" })).toHaveTextContent("请求批准");
      open(null);
      first.rerender(<App />);
      expect(screen.getByRole("button", { name: "操作确认方式" })).toHaveTextContent("请求批准");
      first.unmount();

      open("s1");
      const reloaded = render(<App />);
      expect(screen.getByRole("button", { name: "操作确认方式" })).toHaveTextContent("完全权限");
      await user.type(screen.getByRole("textbox", { name: "消息" }), "你好");
      await user.click(screen.getByRole("button", { name: "发送消息" }));
      expect(chat.send).toHaveBeenLastCalledWith(expect.objectContaining({ approvalMode: "auto" }));
      chat.user = { id: "u2", loginName: "another-user" };
      reloaded.rerender(<App />);
      expect(screen.getByRole("button", { name: "操作确认方式" })).toHaveTextContent("请求批准");
      await user.type(screen.getByRole("textbox", { name: "消息" }), "新账号");
      await user.click(screen.getByRole("button", { name: "发送消息" }));
      expect(chat.send).toHaveBeenLastCalledWith(expect.objectContaining({ approvalMode: "confirm" }));
    } finally {
      Object.assign(chat, original);
    }
  });
  test("starts at request approval when the same conversation is bound to a project", async () => {
    const original = { activeId: chat.activeId, session: chat.session, projectId: chat.projectId };
    const user = userEvent.setup();
    const session = { id: "personal-chat", messages: [] };
    try {
      Object.assign(chat, { activeId: session.id, session });
      const view = render(<App />);
      await user.click(screen.getByRole("button", { name: "操作确认方式" }));
      await user.click(screen.getByRole("menuitemradio", { name: "完全权限" }));
      chat.session = { ...session, projectId: "p1" };
      chat.projectId = "p1";
      view.rerender(<App />);
      expect(screen.getByRole("button", { name: "操作确认方式" })).toHaveTextContent("请求批准");
      await user.type(screen.getByRole("textbox", { name: "消息" }), "项目内继续");
      await user.click(screen.getByRole("button", { name: "发送消息" }));
      expect(chat.send).toHaveBeenLastCalledWith(expect.objectContaining({ approvalMode: "confirm" }));
    } finally {
      Object.assign(chat, original);
    }
  });

  test.each([
    { phase: "navigation", away: true },
    { phase: "in-session retry after reload" },
    { phase: "independently edited original", edited: true },
    { phase: "return to the original during retry", returnDuringRetry: true },
    { phase: "a different prompt in the saved session", differentPrompt: true },
    { phase: "replacing the restored copy", replacesCopy: true },
  ])("retains the first draft on failure and reconciles it after $phase", async ({ away, edited, returnDuringRetry, differentPrompt, replacesCopy }) => {
    const original = { activeId: chat.activeId, session: chat.session, projectId: chat.projectId };
    const user = userEvent.setup();
    const saved = { id: "new-session", messages: [], projectId: "p1" };
    const failedDraft = {
      text: "@订单导出 请继续",
      attachments: [{ id: "upload-1", name: "说明.txt", mimeType: "text/plain" }],
      references: [{ type: "issue", id: "i1", name: "订单导出", projectId: "p1" }],
    };
    const originKey = "agentflow.native-chat.draft.u1.new:p1";
    localStorage.setItem(originKey, JSON.stringify(failedDraft));
    let create, rejectGeneration;
    chat.send.mockImplementationOnce(({ onSessionCreated }) => new Promise((resolve) => {
      create = () => onSessionCreated(saved);
      rejectGeneration = () => resolve(false);
    }));
    try {
      chat.projectId = "p1";
      let view = render(<App />);
      await user.click(screen.getByRole("button", { name: "操作确认方式" }));
      await user.click(screen.getByRole("menuitemradio", { name: "帮我批准" }));
      await user.click(screen.getByRole("button", { name: "发送消息" }));
      await act(async () => create());
      chat.session = away ? { id: "other-session", messages: [], projectId: "p1" } : saved;
      chat.activeId = chat.session.id;
      view.rerender(<App />);
      const nextPrompt = "会话里另写的一条消息";
      if (differentPrompt) await user.type(screen.getByRole("textbox", { name: "消息" }), nextPrompt);
      await act(async () => rejectGeneration());
      if (away) {
        Object.assign(chat, { activeId: null, session: null });
        view.rerender(<App />);
      } else {
        view.unmount();
        if (edited) localStorage.setItem(originKey, JSON.stringify({ ...failedDraft, text: "另一份草稿" }));
        view = render(<App />);
      }
      expect(screen.getByRole("button", { name: "操作确认方式" })).toHaveTextContent("帮我批准");
      expect(screen.getByRole("textbox", { name: "消息" })).toHaveValue(differentPrompt ? nextPrompt : failedDraft.text);
      if (replacesCopy) {
        const input = screen.getByRole("textbox", { name: "消息" });
        await user.clear(input);
        await user.type(input, nextPrompt);
      }
      let acceptRetry;
      if (returnDuringRetry) chat.send.mockImplementationOnce(() => new Promise((resolve) => {
        acceptRetry = () => resolve(true);
      }));
      await user.click(screen.getByRole("button", { name: "发送消息" }));
      const submitted = differentPrompt ? { text: nextPrompt, attachments: [], references: [] }
        : replacesCopy ? { text: nextPrompt, attachments: failedDraft.attachments } : failedDraft;
      expect(chat.send).toHaveBeenLastCalledWith(expect.objectContaining({ ...submitted, approvalMode: "assisted" }));
      if (!away) {
        Object.assign(chat, { activeId: null, session: null });
        view.rerender(<App />);
        if (acceptRetry) {
          expect(screen.getByRole("textbox", { name: "消息" })).toHaveValue(failedDraft.text);
          await act(async () => acceptRetry());
        }
        const remaining = edited ? "另一份草稿" : (differentPrompt || replacesCopy) ? failedDraft.text : "";
        expect(screen.getByRole("textbox", { name: "消息" })).toHaveValue(remaining);
        expect(screen.getByRole("button", { name: "操作确认方式" })).toHaveTextContent(remaining ? "帮我批准" : "请求批准");
      }
    } finally {
      Object.assign(chat, original);
    }
  });

  test("labels a historical model by its ID when it has no catalog entry", () => {
    const original = chat.model;
    try {
      chat.model = "codex:retired-model";
      render(<App />);
      expect(screen.getByRole("button", { name: "选择模型" })).toHaveTextContent("codex:retired-model");
    } finally {
      chat.model = original;
    }
  });
  test("waits for the selected conversation before allowing model settings to change", () => {
    const original = { activeId: chat.activeId, session: chat.session };
    const settings = ["CLI", "选择模型", "思考强度"];
    try {
      Object.assign(chat, { activeId: "s1", session: null });
      const view = render(<App />);
      for (const name of settings) expect(screen.getByRole("button", { name })).toBeDisabled();
      chat.session = { id: "s1", messages: [] };
      view.rerender(<App />);
      for (const name of settings) expect(screen.getByRole("button", { name })).toBeEnabled();
    } finally {
      Object.assign(chat, original);
    }
  });
  test("regenerates an edited shared branch with the returned main conversation's approval mode", async () => {
    const original = { activeId: chat.activeId, session: chat.session };
    const user = userEvent.setup();
    const main = { id: "sop-main", shared: true, messages: [{ role: "user", content: "原消息" }] };
    const branch = { ...main, id: "sop-branch", parentSessionId: main.id, rootSessionId: main.id };
    const select = async (name) => {
      await user.click(screen.getByRole("button", { name: "操作确认方式" }));
      await user.click(screen.getByRole("menuitemradio", { name }));
    };
    try {
      Object.assign(chat, { activeId: main.id, session: main });
      const view = render(<App />);
      await select("帮我批准");
      Object.assign(chat, { activeId: branch.id, session: branch });
      view.rerender(<App />);
      await select("完全权限");
      await user.click(screen.getByLabelText("编辑消息"));
      const edit = within(screen.getByRole("dialog")).getByRole("textbox");
      await user.clear(edit);
      await user.type(edit, "修改后的消息");
      chat.edit.mockResolvedValueOnce(main);
      await user.click(screen.getByText("保存并重新生成"));
      expect(chat.regenerate).toHaveBeenLastCalledWith(main, "assisted");
    } finally {
      Object.assign(chat, original);
    }
  });
  test("does not ask for per-command native approval in a working conversation", () => {
    const previous = {...chat};
    Object.assign(chat,{activeId:"workspace-chat",busy:true,session:{id:"workspace-chat",runtimeMode:"workspace",messages:[{role:"user",content:"检查目录"}]}});
    let view;
    try {
      view=render(<App />);
      expect(screen.queryByRole("region",{name:"待确认的原生操作"})).not.toBeInTheDocument();
    } finally { view?.unmount(); Object.assign(chat,previous); }
  });
  test("offers execution machine selection on the home page and moves it into the conversation header", async () => {
    const previous = { ...chat };
    const selectMachine = vi.fn();
    Object.assign(chat, {
      machinesReady: true, defaultRunnerId: "mac", machines: [{ id: "mac", name: "开发 Mac", status: "online" }],
      selectMachine, refreshMachines: vi.fn(),
    });
    const user = userEvent.setup();
    let view;
    try {
      view = render(<App />);
      let trigger = screen.getByRole("button", { name: "执行机器：开发 Mac" });
      expect(trigger.closest(".composer-context")).not.toBeNull();
      trigger.getBoundingClientRect = () => ({ top: 500, bottom: 528, left: 200, right: 400, width: 200, height: 28 });
      await user.click(trigger);
      await user.click(screen.getByRole("option", { name: /LoopForge 云端/ }));
      expect(selectMachine).toHaveBeenCalledWith("");
      Object.assign(chat, { activeId: "s1", session: { id: "s1", messages: [] } });
      view.rerender(<App />);
      expect(screen.getAllByRole("button", { name: "执行机器：开发 Mac" })).toHaveLength(1);
      chat.session.messages = [{ role: "user", content: "检查项目" }];
      chat.session.executionTarget = {pool:"runner",runnerId:"mac"};
      view.rerender(<App />);
      expect(screen.queryByRole("button", { name: "执行机器：开发 Mac" })).not.toBeInTheDocument();
      expect(screen.getByText("开发 Mac").closest(".header-actions")).not.toBeNull();
    } finally {
      view?.unmount();
      for (const key of Object.keys(chat)) if (!(key in previous)) delete chat[key];
      Object.assign(chat, previous);
    }
  });

  test("hides environments by default and remembers each user's advanced preference", async () => {
    const previous = { ...chat };
    const selectSandbox = vi.fn();
    Object.assign(chat, { projectId: "p1", sandboxProfileId: "business", sandboxes: [], selectSandbox });
    const user = userEvent.setup();
    let view = render(<App />);
    expect(screen.queryByRole("button", { name: /^运行环境/ })).not.toBeInTheDocument();
    await user.click(screen.getByLabelText("添加"));
    await user.click(screen.getByRole("menuitem", { name: "高级设置" }));
    await user.click(screen.getByRole("button", { name: /^高级环境选项/ }));
    await user.click(screen.getByRole("option", { name: "显示", exact: true }));
    await user.click(screen.getByRole("button", { name: "完成", exact: true }));
    const environment = screen.getByRole("button", { name: /^运行环境/ });
    expect(environment.closest(".composer-context")).toContainElement(screen.getByLabelText("选择项目"));
    expect(environment.closest(".composer-toolbar")).toBeNull();
    view.unmount();
    view = render(<App />);
    expect(screen.getByRole("button", { name: /^运行环境/ })).toBeInTheDocument();
    view.unmount();
    Object.assign(chat, { user: { ...chat.user, id: "another-user" } });
    view = render(<App />);
    expect(screen.queryByRole("button", { name: /^运行环境/ })).not.toBeInTheDocument();
    expect(selectSandbox).not.toHaveBeenCalled();
    expect(chat.sandboxProfileId).toBe("business");
    view.unmount();
    Object.assign(chat, previous);
  });

  beforeEach(() => {
    vi.clearAllMocks();
    history.replaceState({}, "", "/");
  });

  test("a requirement handed over by the workbench opens @-mentioned in a new conversation", async () => {
    const previous = { ...chat };
    const originalFetch = globalThis.fetch;
    const issues = {
      i1: { id: "i1", title: "个人主页徽章", projectId: "p1" },
      i2: { id: "i2", title: "导出报表", projectId: "p1" },
    };
    globalThis.fetch = vi.fn(async (path) => ({
      ok: true,
      status: 200,
      json: async () => ({ issue: issues[decodeURIComponent(String(path).split("/").pop())] }),
    }));
    Object.assign(chat, { projectId: "p1", startProjectDraft: vi.fn() });
    history.replaceState({}, "", "/?projectId=p1&mentionIssue=i1");
    let view;
    try {
      view = render(<App />);
      const box = screen.getByRole("textbox");
      await waitFor(() => expect(box.value).toBe("@个人主页徽章 "));
      expect(screen.getByLabelText("移除引用 个人主页徽章")).toBeInTheDocument();
      expect(globalThis.fetch).toHaveBeenCalledWith("/api/issues/i1", expect.anything());
      expect(new URLSearchParams(location.search).has("mentionIssue")).toBe(false);

      window.dispatchEvent(new MessageEvent("message", {
        data: { type: "agentflow-chat-mention-issue", issueId: "i2", projectId: "p1" },
        origin: location.origin,
        source: window.parent,
      }));
      expect(chat.startProjectDraft).toHaveBeenCalledWith("p1");
      await waitFor(() => expect(box.value).toBe("@导出报表 "));
      expect(screen.queryByLabelText("移除引用 个人主页徽章")).not.toBeInTheDocument();
      expect(screen.getByLabelText("移除引用 导出报表")).toBeInTheDocument();
    } finally {
      view?.unmount();
      globalThis.fetch = originalFetch;
      Object.assign(chat, previous);
      delete chat.startProjectDraft;
      localStorage.clear();
    }
  });

  test("a requirement handoff keeps the project's unsent new-conversation draft", async () => {
    const previous = { ...chat };
    const originalFetch = globalThis.fetch;
    globalThis.fetch = vi.fn(async () => ({
      ok: true,
      status: 200,
      json: async () => ({ issue: { id: "i3", title: "订单导出", projectId: "p1" } }),
    }));
    Object.assign(chat, { projectId: "p1", startProjectDraft: vi.fn() });
    const owner = chat.user?.id || "guest";
    const projectDraftKey = `agentflow.native-chat.draft.${owner}.new:p1`;
    const projectDraft = JSON.stringify({
      text: "写了一半的问题",
      attachments: [{ id: "att-1", name: "spec.pdf", type: "application/pdf", size: 12 }],
      references: [],
    });
    localStorage.setItem(projectDraftKey, projectDraft);
    history.replaceState({}, "", "/?projectId=p1&mentionIssue=i3");
    let view;
    try {
      view = render(<App />);
      const box = screen.getByRole("textbox");
      await waitFor(() => expect(box.value).toBe("@订单导出 "));
      expect(screen.queryByText("spec.pdf")).not.toBeInTheDocument();
      expect(localStorage.getItem(projectDraftKey)).toBe(projectDraft);
      // The draft is saved by an effect that runs after the mention renders;
      // on a slow runner the box can already show it while storage does not.
      await waitFor(() => {
        const issueDraft = JSON.parse(localStorage.getItem(`agentflow.native-chat.draft.${owner}.issue:i3`));
        expect(issueDraft.references).toEqual([{ type: "issue", id: "i3", name: "订单导出", projectId: "p1" }]);
      });

      fireEvent.change(box, { target: { value: "@订单导出 先聊范围" } });
      window.dispatchEvent(new MessageEvent("message", {
        data: { type: "agentflow-chat-mention-issue", issueId: "i3", projectId: "p1" },
        origin: location.origin,
        source: window.parent,
      }));
      await waitFor(() => expect(globalThis.fetch).toHaveBeenCalledTimes(2));
      expect(box.value).toBe("@订单导出 先聊范围");
    } finally {
      view?.unmount();
      globalThis.fetch = originalFetch;
      Object.assign(chat, previous);
      delete chat.startProjectDraft;
      localStorage.clear();
    }
  });

  test("a requirement moved to another project starts with request approval", async () => {
    const original = { projectId: chat.projectId, projects: chat.projects };
    const fetchBefore = globalThis.fetch;
    const user = userEvent.setup();
    vi.useFakeTimers({ toFake: ["requestAnimationFrame", "cancelAnimationFrame"] });
    globalThis.fetch = vi.fn(async () => ({
      ok: true,
      status: 200,
      json: async () => ({ issue: { id: "moved-issue", title: "迁移需求", projectId: chat.projectId } }),
    }));
    try {
      chat.projects = original.projects.map((project) => ({ ...project, members: ["u1"] }));
      chat.projectId = "p1";
      history.replaceState({}, "", "/?projectId=p1&mentionIssue=moved-issue");
      const first = render(<App />);
      await waitFor(() => expect(screen.getByRole("textbox", { name: "消息" })).toHaveValue("@迁移需求 "));
      await user.click(screen.getByRole("button", { name: "操作确认方式" }));
      // The handoff's focus request can run after the user opens the menu.
      act(() => { vi.advanceTimersToNextFrame(); });
      await user.click(screen.getByRole("menuitemradio", { name: "完全权限" }));
      first.unmount();

      chat.projectId = "p2";
      history.replaceState({}, "", "/?projectId=p2&mentionIssue=moved-issue");
      render(<App />);
      await waitFor(() => expect(screen.getByRole("textbox", { name: "消息" })).toHaveValue("@迁移需求 "));
      expect(screen.getByRole("button", { name: "操作确认方式" })).toHaveTextContent("请求批准");
      await user.click(screen.getByRole("button", { name: "发送消息" }));
      expect(chat.send).toHaveBeenLastCalledWith(expect.objectContaining({ approvalMode: "confirm" }));
    } finally {
      vi.useRealTimers();
      globalThis.fetch = fetchBefore;
      Object.assign(chat, original);
      history.replaceState({}, "", "/");
    }
  });

  test("leaving a requirement handoff before its details load cancels the mention", async () => {
    const previous = { ...chat };
    const originalFetch = globalThis.fetch;
    let resolveIssue;
    globalThis.fetch = vi.fn(() => new Promise((resolve) => {
      resolveIssue = () => resolve({
        ok: true,
        status: 200,
        json: async () => ({ issue: { id: "i4", title: "慢需求", projectId: "p1" } }),
      });
    }));
    Object.assign(chat, { projectId: "p1", startProjectDraft: vi.fn() });
    const owner = chat.user?.id || "guest";
    const projectDraftKey = `agentflow.native-chat.draft.${owner}.new:p1`;
    const projectDraft = JSON.stringify({ text: "原来的草稿", attachments: [], references: [] });
    localStorage.setItem(projectDraftKey, projectDraft);
    history.replaceState({}, "", "/?projectId=p1&mentionIssue=i4");
    let view;
    try {
      view = render(<App />);
      await waitFor(() => expect(globalThis.fetch).toHaveBeenCalledTimes(1));
      fireEvent.keyDown(window, { key: "O", ctrlKey: true, shiftKey: true });
      expect(chat.newChat).toHaveBeenCalled();
      const box = screen.getByRole("textbox");
      await waitFor(() => expect(box.value).toBe("原来的草稿"));
      resolveIssue();
      await new Promise((resolve) => setTimeout(resolve, 50));
      expect(box.value).toBe("原来的草稿");
      expect(globalThis.fetch).toHaveBeenCalledTimes(1);
      expect(localStorage.getItem(projectDraftKey)).toBe(projectDraft);
    } finally {
      view?.unmount();
      globalThis.fetch = originalFetch;
      Object.assign(chat, previous);
      delete chat.startProjectDraft;
      localStorage.clear();
    }
  });

  test("renders the ready empty state and opens primary controls", async () => {
    const user = userEvent.setup();
    render(<App />);

    expect(screen.getByText("快速接入")).toBeInTheDocument();
    const settings = screen.queryByLabelText("设置");
    if (settings) await user.click(settings);
    fireEvent.keyDown(document, { key: "k", ctrlKey: true });
    expect(screen.getByRole("dialog")).toBeInTheDocument();
    fireEvent.keyDown(document, { key: "Escape" });
    await user.click(screen.getByLabelText("选择项目"));
    expect(screen.getByRole("listbox", { name: "项目" })).toBeInTheDocument();
    fireEvent.keyDown(document, { key: "Escape" });
    // With no project the picker is highlighted so newcomers notice it.
    expect(screen.getByLabelText("选择项目")).toHaveClass("is-unset");
    await user.click(screen.getByText("接入测试环境"));
    expect(screen.getByRole("textbox").value).toMatch(/^带我一步步接入测试环境/);
    await user.click(screen.getByText("选用 SOP 与角色"));
    expect(screen.getByRole("textbox").value).toMatch(/产品、前端、后端交付 SOP/);
  });

  test("questions and replies show when they were sent, and the header switches project", async () => {
    const user = userEvent.setup();
    const previous = { ...chat };
    const today = (h, m) => {
      const d = new Date();
      d.setHours(h, m, 0, 0);
      return d.toISOString();
    };
    Object.assign(chat, {
      sessions: [{ id: "s9", title: "带时间", projectId: "p1", updatedAt: today(9, 0), messages: [] }],
      activeId: "s9",
      session: {
        id: "s9",
        title: "带时间",
        projectId: "p1",
        model: "m1",
        messages: [
          // Saved before questions were stamped: falls back to the reply start.
          { role: "user", content: "旧问题", createdAt: "0001-01-01T00:00:00Z" },
          { role: "assistant", content: "旧回答", startedAt: today(8, 5), createdAt: today(8, 6), activity: [] },
          { role: "user", content: "新问题", createdAt: today(9, 1) },
          { role: "assistant", content: "新回答", startedAt: today(9, 1), createdAt: today(9, 2), activity: [] },
        ],
      },
      projectId: "p1",
    });
    const view = render(<App />);
    const times = [...document.querySelectorAll("time.message-time")].map((t) => t.textContent);
    expect(times).toEqual(["08:05", "08:06", "09:01", "09:02"]);
    expect(document.querySelectorAll(".message-author time.message-time")).toHaveLength(2);
    expect(document.querySelectorAll(".message-actions time.message-time")).toHaveLength(2);

    const switcher = screen.getByRole("button", { name: "当前项目：示例项目，切换项目" });
    // jsdom has no layout; give the anchor a box so the menu stays open.
    switcher.getBoundingClientRect = () => ({ top: 10, bottom: 30, left: 600, right: 700, width: 100, height: 20 });
    await user.click(switcher);
    const list = screen.getByRole("listbox", { name: "切换项目" });
    expect(list).toBeInTheDocument();
    fireEvent.click(screen.getByRole("option", { name: /个人对话/ }));
    expect(chat.selectProject).toHaveBeenCalledWith("");
    view.unmount();
    Object.assign(chat, previous);
  });

  test("renders a populated conversation and sends text", async () => {
    const user = userEvent.setup();
    const previous = { ...chat };
    Object.assign(chat, {
      sessions: [
        {
          id: "s1",
          title: "已有会话",
          projectId: "p1",
          userId: "another-creator",
          updatedAt: new Date().toISOString(),
          messages: [],
        },
      ],
      activeId: "s1",
      session: {
        id: "s1",
        title: "已有会话",
        projectId: "p1",
        userId: "another-creator",
        model: "m1",
        messages: [
          { role: "user", content: "你好", createdAt: new Date().toISOString() },
          {
            role: "assistant",
            content: "回答内容",
            createdAt: new Date().toISOString(),
            activity: [],
          },
        ],
      },
      projectId: "p1",
    });
    const view = render(<App />);
    expect(screen.getAllByText("已有会话").length).toBeGreaterThan(0);
    const textbox = screen.getByRole("textbox");
    await user.type(textbox, "继续");
    const send = screen.queryByLabelText(/发送/);
    if (send) await user.click(send);
    expect(chat.send).toHaveBeenCalled();

    await user.click(screen.getByLabelText("编辑消息"));
    const edit = screen.getByRole("dialog");
    await user.clear(edit.querySelector("textarea"));
    await user.type(edit.querySelector("textarea"), "修改后的消息");
    chat.edit.mockResolvedValueOnce({ id: "s1", messages: [] });
    await user.click(screen.getByText("保存并重新生成"));
    expect(chat.edit).toHaveBeenCalled();

    await user.click(screen.getByLabelText("重命名会话"));
    const rename = screen.getByRole("dialog").querySelector("input");
    await user.clear(rename);
    await user.type(rename, "新会话名");
    chat.rename.mockResolvedValueOnce(true);
    await user.click(screen.getByText("保存"));

    const showSidebar = screen.queryByLabelText("显示会话侧边栏");
    if (showSidebar) await user.click(showSidebar);
    // Pinning moves the row to another section; query the live row each time.
    const openSessionMenu = async () => {
      const sessionLink = document.querySelector(".session-link");
      expect(sessionLink).toBeInTheDocument();
      fireEvent.contextMenu(sessionLink, { clientX: 20, clientY: 20 });
      return screen.findByRole("menu", { name: "会话操作" });
    };
    await user.click(within(await openSessionMenu()).getByRole("menuitem", { name: "置顶" }));
    await user.click(within(await openSessionMenu()).getByRole("menuitem", { name: "归档" }));
    expect(chat.archive).toHaveBeenCalled();
    await waitFor(() => expect(screen.queryByRole("menu", { name: "会话操作" })).not.toBeInTheDocument());
    await user.click(within(await openSessionMenu()).getByRole("menuitem", { name: "删除" }));
    const deleteDialog = await screen.findByRole("dialog", { name: "删除会话" });
    await user.click(within(deleteDialog).getByRole("button", { name: "删除" }));
    expect(chat.remove).toHaveBeenCalled();
    await user.click(await screen.findByLabelText("筛选会话"));
    await user.click(screen.getByText("已归档"));
    await user.click(screen.getByLabelText("搜索会话"));
    await user.type(screen.getByLabelText("搜索会话名称"), "已有");
    fireEvent.keyDown(document, { key: "Escape" });
    fireEvent.keyDown(document, { key: ",", ctrlKey: true });
    expect(screen.getByText("个人配置与模型凭据")).toBeInTheDocument();
    view.unmount();
    Object.assign(chat, previous);
  });

  test("renders live generation, errors, artifacts, and toolbar menus", async () => {
    const user = userEvent.setup();
    const previous = { ...chat };
    Object.assign(chat, {
      sessions: [
        { id: "s2", title: "运行会话", projectId: "p1", updatedAt: new Date().toISOString(), messages: [] },
      ],
      activeId: "s2",
      session: {
        id: "s2",
        title: "运行会话",
        projectId: "p1",
        model: "m1",
        messages: [
          {
            role: "assistant",
            content: JSON.stringify({
              message: "生成了产物",
              drafts: [{ tool: "workflows_save", body: { name: "交付 SOP", projectId: "p1" } }],
            }),
            attachments: [{ id: "img1", name: "result.png", mimeType: "image/png" }],
            createdAt: new Date().toISOString(),
          },
        ],
      },
      projectId: "p1",
      generation: { id: "g1", status: "running", startedAt: new Date().toISOString(), updatedAt: new Date().toISOString() },
      live: {
        answer: "流式回答",
        thinking: "思考中",
        tools: [],
        activity: [{ id: "live1", kind: "thinking", text: "分析", startedAt: new Date().toISOString() }],
      },
      error: "出现测试错误",
      busy: true,
    });
    render(<App />);
    expect(screen.getByText("流式回答")).toBeInTheDocument();
    expect(screen.getByText("出现测试错误")).toBeInTheDocument();
    await user.click(screen.getByLabelText("查看会话产物"));
    expect(screen.getByLabelText("会话产物")).toBeInTheDocument();
    const add = screen.queryByLabelText("添加");
    if (add) await user.click(add);
    const project = screen.queryByLabelText("选择项目");
    if (project) await user.click(project);
    const stop = screen.queryByLabelText("停止生成");
    if (stop) await user.click(stop);
    expect(chat.stop).toHaveBeenCalled();
    Object.assign(chat, previous);
  });

  test("the composer switches CLI and keeps the effort picker", async () => {
    const user = userEvent.setup();
    const previous = { ...chat };
    chat.setExecutor = vi.fn();
    chat.executors = [
      ...previous.executors,
      { id: "pi", icon: "terminal", label: "Pi", detail: "Pi" },
      { id: "agy", icon: "sparkles", label: "Antigravity", detail: "Antigravity" },
      { id: "kimi", icon: "terminal", label: "Kimi Code", detail: "Kimi Code" },
    ];
    const view = render(<App />);
    await user.click(screen.getByRole("button", { name: "CLI" }));
    const pi = await screen.findByRole("menuitemradio", { name: "Pi" });
    expect(pi.querySelector('[data-model-provider="pi"] svg')).not.toBeNull();
    const agy = screen.getByRole("menuitemradio", { name: "Antigravity" });
    expect(agy.querySelector('[data-model-provider="antigravity"] svg')).not.toBeNull();
    const kimi = screen.getByRole("menuitemradio", { name: "Kimi Code" });
    expect(kimi.querySelector('[data-model-provider="kimi-code"] img')).not.toBeNull();
    expect(modelProviderFor({ id: "agy:gemini-4-neon-high" })).toBe("gemini");
    expect(modelProviderFor({ id: "agy:claude-opus-5-5-high" })).toBe("claude");
    expect(modelProviderFor({ id: "claude-code:opus" })).toBe("claude");
    expect(modelProviderFor({ id: "cursor:composer-2.5" })).toBe("cursor");
    expect(modelProviderFor({ id: "cursor:cursor-grok-4.6" })).toBe("xai");
    expect(modelProviderFor({ id: "cursor:muse-wonder-1.0" })).toBe("meta");
    expect(modelProviderFor({ id: "auto", engine: "cursor" })).toBe("cursor");
    expect(modelProviderFor({ id: "kimi:k3.1-pro-highspeed" })).toBe("kimi");
    expect(modelProviderFor({ id: "kimi:deepseek-v4-pro" })).toBe("deepseek");
    await user.click(await screen.findByRole("menuitemradio", { name: "Codex" }));
    expect(chat.setExecutor).toHaveBeenCalledWith("codex");
    expect(screen.getByRole("button", { name: "思考强度" })).toBeInTheDocument();
    chat.executor = "codex";
    view.rerender(<App />);
    expect(screen.getByRole("button", { name: "思考强度" })).toBeInTheDocument();
    // A saved reply locks the CLI: the switch leaves the composer and the
    // title shows which CLI this conversation uses.
    Object.assign(chat, {
      session: {
        id: "s1",
        title: "已开始",
        executor: "codex",
        messages: [{ id: "a1", role: "assistant", model: "m1", content: "hi" }],
      },
    });
    view.rerender(<App />);
    expect(screen.queryByRole("button", { name: "CLI" })).not.toBeInTheDocument();
    expect(document.querySelector(".conversation-engine")).toHaveTextContent("Codex");
    // The first reply, still running, locks it the same way.
    Object.assign(chat, {
      busy: true,
      executor: "codebuddy",
      session: { id: "s2", title: "生成中", executor: "codebuddy", messages: [] },
    });
    view.rerender(<App />);
    expect(screen.queryByRole("button", { name: "CLI" })).not.toBeInTheDocument();
    expect(document.querySelector(".conversation-engine")).toHaveTextContent("CodeBuddy");
    Object.assign(chat, previous);
  });

  test("offers executor setup before the first chat, along with local upload and read-only states", async () => {
    const previous = { ...chat };
    Object.assign(chat, {
      user: { id: "u1", loginName: "tester" },
      executor: "",
      executors: [],
      machinesReady: true,
      activeId: null,
      session: null,
      sessions: [],
      projectId: "",
      sponsoredModel: "",
      local: false,
      busy: false,
      generation: null,
      live: { answer: "", thinking: "", tools: [], activity: [] },
      error: "",
    });
    const view = render(<App />);
    expect(screen.getByRole("button", { name: "新增执行器" })).toBeInTheDocument();
    expect(screen.queryByText(/CodeBuddy API Key/)).toBeNull();
    expect(screen.getByRole("button", { name: "选择模型" })).toBeDisabled();
    chat.executor = "codex";
    chat.executors = previous.executors.filter(item => item.id === "codex");
    view.rerender(<App />);
    expect(screen.queryByRole("button", { name: "新增执行器" })).toBeNull();
    expect(screen.getByRole("button", { name: "CLI" })).toHaveTextContent("Codex");
    chat.executor = "codebuddy";
    chat.local = true;
    view.rerender(<App />);
    const fileInput = document.querySelector('input[type="file"]');
    fireEvent.change(fileInput, {
      target: { files: [new File(["hello"], "hello.txt", { type: "text/plain" })] },
    });
    expect(await screen.findByText("hello.txt")).toBeInTheDocument();
    const originalFetch = globalThis.fetch;
    globalThis.fetch = vi.fn(async () => ({
      ok: true,
      status: 200,
      json: async () => ({ id: "remote-file", name: "remote.txt", mimeType: "text/plain", size: 6 }),
    }));
    chat.local = false;
    view.rerender(<App />);
    fireEvent.change(document.querySelector('input[type="file"]'), {
      target: { files: [new File(["remote"], "remote.txt", { type: "text/plain" })] },
    });
    expect(await screen.findByText("remote.txt")).toBeInTheDocument();
    globalThis.fetch = originalFetch;

    Object.assign(chat, {
      local: false,
      activeId: "readonly",
      session: {
        id: "readonly", title: "共享会话", projectId: "p2", readOnly: true,
        shared: true, messages: [{ role: "assistant", content: "只读内容", createdAt: new Date().toISOString() }],
      },
      projectId: "p2",
      readOnly: true,
    });
    view.rerender(<App />);
    expect(document.querySelector(".readonly-notice")?.textContent).toContain("只能查看这个会话");

    Object.assign(chat, {
      session: { ...chat.session, readOnly: false },
      readOnly: false,
      generation: { id: "g2", status: "failed", error: "中断原因" },
      live: {
        answer: "部分内容",
        thinking: "",
        tools: [],
        activity: [{ id: "a", kind: "command", detail: "test", startedAt: new Date().toISOString() }],
      },
    });
    view.rerender(<App />);
    expect(screen.getByText("中断原因")).toBeInTheDocument();
    Object.assign(chat, previous);
  });

  test("previews a pasted screenshot in the composer instead of only a filename", async () => {
    const originalFetch = globalThis.fetch;
    const originalCreate = globalThis.URL.createObjectURL;
    const originalRevoke = globalThis.URL.revokeObjectURL;
    globalThis.URL.createObjectURL = vi.fn(() => "blob:composer-preview");
    globalThis.URL.revokeObjectURL = vi.fn();
    globalThis.fetch = vi.fn(async () => ({
      ok: true,
      status: 200,
      json: async () => ({ id: "shot-1", name: "shot.png", mimeType: "image/png", size: 2048 }),
    }));
    Object.assign(chat, {
      local: false,
      activeId: null,
      session: null,
      projectId: "p1",
      readOnly: false,
    });
    const view = render(<App />);
    fireEvent.paste(screen.getByLabelText("消息"), {
      clipboardData: {
        items: [
          {
            kind: "file",
            getAsFile: () => new File(["png"], "shot.png", { type: "image/png" }),
          },
        ],
      },
    });
    const thumb = await screen.findByAltText("shot.png");
    expect(thumb.getAttribute("src")).toBe("blob:composer-preview");
    expect(thumb.closest(".composer-attachment-thumb")).toBeTruthy();
    fireEvent.click(screen.getByLabelText("查看图片 shot.png"));
    expect(screen.getByLabelText("查看大图 shot.png")).toBeInTheDocument();
    view.unmount();
    globalThis.fetch = originalFetch;
    if (originalCreate) globalThis.URL.createObjectURL = originalCreate;
    else delete globalThis.URL.createObjectURL;
    if (originalRevoke) globalThis.URL.revokeObjectURL = originalRevoke;
    else delete globalThis.URL.revokeObjectURL;
  });

  test("uploads a pasted screenshot over plain HTTP, where crypto.randomUUID is missing", async () => {
    const originalFetch = globalThis.fetch;
    const originalCreate = globalThis.URL.createObjectURL;
    const originalRevoke = globalThis.URL.revokeObjectURL;
    Object.defineProperty(globalThis.crypto, "randomUUID", {
      configurable: true,
      value: undefined,
    });
    globalThis.URL.createObjectURL = vi.fn(() => "blob:http-preview");
    globalThis.URL.revokeObjectURL = vi.fn();
    globalThis.fetch = vi.fn(async () => ({
      ok: true,
      status: 200,
      json: async () => ({ id: "shot-2", name: "http.png", mimeType: "image/png", size: 10 }),
    }));
    Object.assign(chat, {
      local: false,
      activeId: null,
      session: null,
      projectId: "p1",
      readOnly: false,
    });
    try {
      const view = render(<App />);
      fireEvent.paste(screen.getByLabelText("消息"), {
        clipboardData: {
          items: [
            {
              kind: "file",
              getAsFile: () => new File(["png"], "http.png", { type: "image/png" }),
            },
          ],
        },
      });
      await screen.findByLabelText("查看图片 http.png");
      expect(chat.setError).not.toHaveBeenCalledWith(
        expect.stringContaining("randomUUID"),
      );
      view.unmount();
    } finally {
      delete globalThis.crypto.randomUUID;
      globalThis.fetch = originalFetch;
      if (originalCreate) globalThis.URL.createObjectURL = originalCreate;
      else delete globalThis.URL.createObjectURL;
      if (originalRevoke) globalThis.URL.revokeObjectURL = originalRevoke;
      else delete globalThis.URL.revokeObjectURL;
    }
  });

  const artifactSessions = (messages) => ({
    id: "session-artifacts",
    title: "产物会话",
    projectId: "p1",
    messages,
  });

  test("reveals the artifact drawer when a reply generates an artifact", async () => {
    const previous = { ...chat };
    Object.assign(chat, {
      local: false,
      projectId: "p1",
      activeId: "session-artifacts",
      readOnly: false,
      sessions: [],
      busy: false,
      generation: null,
      live: { answer: "", thinking: "", tools: [], activity: [] },
      session: artifactSessions([
        { id: "msg-1", role: "assistant", content: "先看一下现状" },
      ]),
    });
    const view = render(<App />);
    expect(document.getElementById("conversation-artifacts")).toBeNull();

    Object.assign(chat, {
      session: artifactSessions([
        { id: "msg-1", role: "assistant", content: "先看一下现状" },
        {
          id: "msg-2",
          role: "assistant",
          content: "脚本已经生成",
          attachments: [
            { id: "att-1", name: "代码-1.bash", mimeType: "text/x-shellscript", size: 32 },
          ],
        },
      ]),
    });
    view.rerender(<App />);
    await waitFor(() =>
      expect(document.getElementById("conversation-artifacts")).toBeTruthy(),
    );
    expect(
      document.getElementById("conversation-artifacts").getAttribute("aria-hidden"),
    ).toBeNull();
    view.unmount();
    Object.assign(chat, previous);
  });

  test("keeps the artifact drawer closed for a restored conversation without new artifacts", async () => {
    const previous = { ...chat };
    Object.assign(chat, {
      local: false,
      projectId: "p1",
      activeId: "session-artifacts",
      readOnly: false,
      sessions: [],
      busy: false,
      generation: null,
      live: { answer: "", thinking: "", tools: [], activity: [] },
      session: artifactSessions([
        {
          id: "msg-1",
          role: "assistant",
          content: "上次生成的脚本",
          attachments: [
            { id: "att-1", name: "代码-1.bash", mimeType: "text/x-shellscript", size: 32 },
          ],
        },
      ]),
    });
    const view = render(<App />);
    expect(screen.getByLabelText("查看会话产物")).toBeInTheDocument();
    await new Promise((resolve) => setTimeout(resolve, 320));
    expect(document.getElementById("conversation-artifacts")).toBeNull();
    view.unmount();
    Object.assign(chat, previous);
  });

  test("keeps the artifact drawer closed when the user attaches a file", async () => {
    const previous = { ...chat };
    Object.assign(chat, {
      local: false,
      projectId: "p1",
      activeId: "session-artifacts",
      readOnly: false,
      sessions: [],
      busy: false,
      generation: null,
      live: { answer: "", thinking: "", tools: [], activity: [] },
      session: artifactSessions([
        { id: "msg-1", role: "user", content: "看下这个文件" },
      ]),
    });
    const view = render(<App />);

    Object.assign(chat, {
      session: artifactSessions([
        {
          id: "msg-1",
          role: "user",
          content: "看下这个文件",
          attachments: [
            { id: "att-upload", name: "截图.png", mimeType: "image/png", size: 64 },
          ],
        },
      ]),
    });
    view.rerender(<App />);
    await new Promise((resolve) => setTimeout(resolve, 320));
    expect(document.getElementById("conversation-artifacts")).toBeNull();
    view.unmount();
    Object.assign(chat, previous);
  });
});

describe("sidebar conversation filter", () => {
  test("only lists conversations the reader took part in", async () => {
    const user = userEvent.setup();
    const previous = { ...chat };
    const now = new Date().toISOString();
    Object.assign(chat, {
      activeId: null,
      session: null,
      projectId: "p1",
      sessions: [
        {
          id: "mine",
          title: "我参与的项目会话",
          projectId: "p1",
          shared: true,
          participants: [
            { id: "u1", name: "tester" },
            { id: "u2", name: "同事" },
          ],
          updatedAt: now,
          messages: [],
        },
        {
          id: "theirs",
          title: "同事独自发起的会话",
          projectId: "p1",
          shared: true,
          participants: [{ id: "u2", name: "同事" }],
          updatedAt: now,
          messages: [],
        },
      ],
    });
    const view = render(<App />);
    try {
      const showSidebar = screen.queryByLabelText("显示会话侧边栏");
      if (showSidebar) await user.click(showSidebar);
      expect(screen.getAllByText("同事独自发起的会话").length).toBeGreaterThan(0);

      await user.click(screen.getByLabelText("筛选会话"));
      await user.click(screen.getByText("仅我参与的对话"));

      expect(screen.queryByText("同事独自发起的会话")).not.toBeInTheDocument();
      expect(screen.getAllByText("我参与的项目会话").length).toBeGreaterThan(0);

      await user.click(screen.getByLabelText("筛选会话"));
      expect(
        screen.getByText("仅我参与的对话").closest("[role=menuitemradio]"),
      ).toHaveAttribute("aria-checked", "true");
      await user.click(screen.getByText("最近的会话"));
      expect(screen.getAllByText("同事独自发起的会话").length).toBeGreaterThan(0);
    } finally {
      view.unmount();
      Object.assign(chat, previous);
    }
  });
});

describe("sidebar reply status", () => {
  test("an unread completed reply shows a blue dot, a running one spins, a failed one warns", async () => {
    const user = userEvent.setup();
    const previous = { ...chat };
    const originalFetch = globalThis.fetch;
    globalThis.fetch = vi.fn(async () => ({ ok: true, status: 200, json: async () => ({ ok: true, recorded: true }) }));
    const now = new Date().toISOString();
    const row = (id, title, generationStatus, generationId, unread = false) => ({
      id, title, projectId: "p1", userId: "u1", generationStatus, generationId, unread, updatedAt: now, messages: [],
    });
    Object.assign(chat, {
      activeId: null,
      session: null,
      projectId: "p1",
      sessions: [
        row("unread", "完成未读", "completed", "g2", true),
        row("read", "完成已读", "completed", "g3"),
        row("busy", "正在生成", "running", "g4"),
        row("broken", "生成出错", "failed", "g5"),
      ],
    });
    const view = render(<App />);
    try {
      const showSidebar = screen.queryByLabelText("显示会话侧边栏");
      if (showSidebar) await user.click(showSidebar);
      const status = (title) =>
        screen.getAllByText(title)[0].closest(".session-link").querySelector(".session-status");
      await waitFor(() => expect(status("完成未读")).toHaveAttribute("data-session-status", "unread"));
      expect(status("完成未读")).toHaveAccessibleName("有新回复未读");
      expect(status("完成未读").querySelector(".session-unread-dot")).not.toBeNull();
      expect(status("完成已读")).toBeNull();
      expect(status("正在生成").querySelector('[data-icon="arc-spinner"]')).not.toBeNull();
      expect(status("生成出错").querySelector('[data-icon="alert"]')).not.toBeNull();

      // Opening the conversation reports the reply read and clears its dot.
      Object.assign(chat, { activeId: "unread" });
      view.rerender(<App />);
      await waitFor(() =>
        expect(globalThis.fetch).toHaveBeenCalledWith(
          "/api/assistant/sessions/unread/read",
          expect.objectContaining({ method: "POST", body: JSON.stringify({ generationId: "g2" }) }),
        ),
      );
      Object.assign(chat, { activeId: null });
      view.rerender(<App />);
      await waitFor(() => expect(status("完成未读")).toBeNull());
    } finally {
      view.unmount();
      globalThis.fetch = originalFetch;
      Object.assign(chat, previous);
    }
  });
});

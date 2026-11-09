import React from "react";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, test, vi } from "vitest";

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
  test("labels the conversation host as LoopForge cloud in text and hover hint", () => {
    const view = render(<App />);
    const host = view.container.querySelector(".composer-context .host-status");
    expect(host).toHaveTextContent("LoopForge 云端");
    expect(host).toHaveAttribute("title", "对话在 LoopForge 云端运行");
    expect(view.container.querySelector(".composer-context")).not.toHaveTextContent("AgentFlow");
    view.unmount();
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
    const view = render(<App />);
    await user.click(screen.getByRole("button", { name: "CLI" }));
    await user.click(await screen.findByRole("menuitemradio", { name: "Codex" }));
    expect(chat.setExecutor).toHaveBeenCalledWith("codex");
    expect(screen.getByRole("button", { name: "思考强度" })).toBeInTheDocument();
    chat.executor = "codex";
    view.rerender(<App />);
    expect(screen.getByRole("button", { name: "思考强度" })).toBeInTheDocument();
    Object.assign(chat, previous);
  });

  test("covers credential, local upload, read-only, and interrupted states", async () => {
    const previous = { ...chat };
    Object.assign(chat, {
      user: { id: "u1", loginName: "tester", codexApiKeyConfigured: true },
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
    expect(screen.getByText(/当前执行器 CodeBuddy 需要先配置 CodeBuddy API Key/)).toBeInTheDocument();
    expect(screen.getByText(/也可以在输入框下方把执行器切换过去/)).toBeInTheDocument();
    chat.executor = "codex";
    view.rerender(<App />);
    expect(screen.queryByText(/当前执行器 CodeBuddy/)).toBeNull();
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

import { act, fireEvent, renderHook, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, test, vi } from "vitest";

const { apiMock, writeMock } = vi.hoisted(() => {
  const baseSession = {
    id: "s1",
    title: "会话一",
    projectId: "p1",
    model: "m1",
    effort: "",
    updatedAt: "2026-01-01T00:00:00Z",
    messages: [{ role: "user", content: "原消息" }],
  };
  const apiMock = vi.fn(async (path, options) => {
    if (options?.method === "DELETE") return {};
    if (path === "/mcp")
      return { result: { structuredContent: { data: { id: "saved" } } } };
    if (path === "/api/auth/current") return { user: { id: 1, loginName: "tester", codeBuddyApiKeyConfigured: true } };
    if (path === "/api/projects") return [{ id: "p1", name: "项目一", ownerId: 1, members: [1] }];
    if (path === "/api/assistant/models?executor=codex") return { default: "default", models: [{ id: "default", name: "CLI default", family: "gpt" }, { id: "gpt-5", name: "GPT-5", family: "gpt" }] };
    if (path === "/api/assistant/models") return { default: "m1", sponsoredModel: "m1", models: [{ id: "m1", name: "模型一", effort: true }] };
    if (path === "/api/users") return [{ id: 1, loginName: "tester", codeBuddyApiKeyConfigured: true }];
    if (path === "/api/assistant/sessions") return [baseSession];
    if (path.includes("/generation")) return { id: "g1", status: "completed", events: [] };
    if (path.startsWith("/api/assistant/sessions/")) return structuredClone(baseSession);
    return [];
  });
  const writeMock = vi.fn(async (path, body) => {
    if (path === "/api/assistant/generations")
      return { id: "g1", status: "running", events: [] };
    if (path.endsWith("/fork") || path.endsWith("/edit"))
      return { ...baseSession, id: path.endsWith("/fork") ? "s2" : "s1", messages: [{ role: "user", content: body.content }] };
    if (path.includes("/issue")) return { ...baseSession, issueId: body.issueId };
    return { ...baseSession, ...body, id: body?.id || baseSession.id };
  });
  return { apiMock, writeMock };
});

vi.mock("../src/api.js", async (original) => ({
  ...(await original()),
  api: apiMock,
  write: writeMock,
  newId: () => "new-session",
}));

import { EXECUTORS, useChat } from "../src/useChat.js";
import {
  isLocalConversation,
  useLocalConversations,
} from "../src/useLocalConversations.js";
import { usePanelPresence } from "../src/usePanelPresence.js";
import { useArtifactPanelLayout } from "../src/useArtifactPanelLayout.js";
import { useConversationRuns } from "../src/runs.js";

describe("conversation hooks", () => {
  test("refreshes environments after returning from sandbox creation", async () => {
    const { result } = renderHook(() => useChat());
    await waitFor(() => expect(result.current.ready).toBe(true));
    const original = apiMock.getMockImplementation();
    apiMock.mockImplementation((path, options) => path === "/api/sandboxes"
      ? Promise.resolve([{id:"created-from-chat",driver:"docker",projectId:"p1"}])
      : original(path, options));
    try {
      act(() => window.dispatchEvent(new MessageEvent("message", {
        origin: location.origin, source: window.parent,
        data: {type:"agentflow-environments-changed"},
      })));
      await waitFor(() => expect(result.current.sandboxes).toEqual([expect.objectContaining({id:"created-from-chat"})]));
    } finally { apiMock.mockImplementation(original); }
  });

  beforeEach(() => {
    vi.clearAllMocks();
    history.replaceState({}, "", "/");
  });

  test("switches Main Bot to another CLI and sends its executor", async () => {
    localStorage.removeItem("loopforge.assistant.executor");
    const { result } = renderHook(() => useChat());
    await waitFor(() => expect(result.current.ready).toBe(true));
    expect(result.current.executor).toBe("codebuddy");
    expect(result.current.executors.map((item) => item.id)).toEqual(EXECUTORS.map((item) => item.id));
    act(() => result.current.setExecutor("codex"));
    await waitFor(() => expect(result.current.models.map((m) => m.id)).toEqual(["default", "gpt-5"]));
    expect(localStorage.getItem("loopforge.assistant.executor")).toBe("codex");
    expect(result.current.model).toBe("default");
    expect(result.current.effort).toBe("");
    expect(result.current.sponsored).toBe(false);
    act(() => result.current.setExecutor("unknown"));
    expect(result.current.executor).toBe("codex");
    await act(() => result.current.send({ text: "你好" }));
    expect(writeMock).toHaveBeenCalledWith(
      "/api/assistant/generations",
      expect.objectContaining({ executor: "codex", model: "default", effort: "" }),
    );
    act(() => result.current.setExecutor("codebuddy"));
    expect(result.current.model).toBe("m1");
    localStorage.removeItem("loopforge.assistant.executor");
  });

  test("without an own key chat is limited to the administrator's shared models", async () => {
    localStorage.removeItem("loopforge.assistant.executor");
    const original = apiMock.getMockImplementation();
    apiMock.mockImplementation(async (path, options) => {
      if (path === "/api/auth/current") return { user: { id: 1, loginName: "tester", codeBuddyApiKeyConfigured: false } };
      if (path === "/api/users") return [{ id: 1, loginName: "tester", codeBuddyApiKeyConfigured: false }];
      if (path === "/api/assistant/models")
        return { default: "glm-5.1", sponsoredModel: "glm-5.1", sponsoredModels: ["glm-5.1", "kimi-k2.6"], models: [{ id: "glm-5.1", name: "GLM" }, { id: "kimi-k2.6", name: "Kimi" }] };
      return original(path, options);
    });
    try {
      const { result } = renderHook(() => useChat());
      await waitFor(() => expect(result.current.ready).toBe(true));
      expect(result.current.sponsored).toBe(true);
      expect(result.current.sponsoredModels).toEqual(["glm-5.1", "kimi-k2.6"]);
      expect(result.current.model).toBe("glm-5.1");
      let accepted;
      act(() => { accepted = result.current.setModel("kimi-k2.6"); });
      expect(accepted).toBe(true);
      expect(result.current.model).toBe("kimi-k2.6");
      act(() => { accepted = result.current.setModel("claude-opus-5"); });
      expect(accepted).toBe(false);
      expect(result.current.model).toBe("kimi-k2.6");
      expect(result.current.error).toContain("glm-5.1、kimi-k2.6");
    } finally {
      apiMock.mockImplementation(original);
    }
  });

  test("persists the draft sandbox selection when creating a conversation", async () => {
    history.replaceState({}, "", "/?projectId=p1");
    const { result } = renderHook(() => useChat());
    await waitFor(() => expect(result.current.ready).toBe(true));
    await act(() => result.current.selectSandbox("business-docker"));
    expect(result.current.sandboxProfileId).toBe("business-docker");
    await act(() => result.current.send({ text: "改代码" }));
    expect(writeMock).toHaveBeenCalledWith("/api/assistant/sessions/new-session", expect.objectContaining({ sandboxProfileId: "business-docker" }), "PUT");
  });

  test("loads chat and exercises conversation commands", async () => {
    const { result } = renderHook(() => useChat());
    await waitFor(() => expect(result.current.ready).toBe(true));
    expect(result.current.sessions).toHaveLength(1);

    await act(() => result.current.send({ text: "新问题" }));
    expect(writeMock).toHaveBeenCalledWith(
      "/api/assistant/generations",
      expect.objectContaining({ messages: expect.any(Array) }),
    );
    act(() => result.current.setModel("m1"));
    await act(() => result.current.selectProject("p1"));
    await act(() => result.current.rename("s1", "新标题"));
    await act(() => result.current.archive("s1", true));
    await act(() => result.current.remove("s1"));
    await act(() => result.current.linkIssue("ISSUE-1"));
    await act(() => result.current.refresh());
  });

  test("opens, edits, regenerates, stops and starts a known session", async () => {
    const { result } = renderHook(() => useChat());
    await waitFor(() => expect(result.current.ready).toBe(true));
    act(() => result.current.open("s1"));
    await waitFor(() => expect(result.current.session?.id).toBe("s1"));
    await act(() => result.current.edit(0, "修改", true));
    await act(() => result.current.regenerate({
      id: "s1", projectId: "p1", model: "m1", messages: [{ role: "user", content: "修改" }],
    }));
    await act(() => result.current.stop());
    apiMock.mockResolvedValueOnce({
      id: "s1",
      projectId: "p1",
      messages: [{
        role: "assistant",
        content: JSON.stringify({ message: "确认", drafts: [{ tool: "issues_save", body: { title: "需求" } }] }),
      }],
    });
    await act(() => result.current.applyDraft(0, 0, { approved: true }));
    const mcp = apiMock.mock.calls.find(([path]) => path === "/mcp");
    expect(mcp[1].headers).toEqual({ "x-agentflow-chat-session": "s1" });
    expect(JSON.parse(mcp[1].body).params).toMatchObject({ name: "issues_save", arguments: { body: { title: "需求" } } });
    act(() => result.current.newChat());
    expect(result.current.activeId).toBeNull();
  });

  test("a confirmed project change refreshes both project pickers and new questions carry a time", async () => {
    const parent = { postMessage: vi.fn() };
    const original = Object.getOwnPropertyDescriptor(window, "parent");
    Object.defineProperty(window, "parent", { configurable: true, get: () => parent });
    try {
      const { result } = renderHook(() => useChat());
      await waitFor(() => expect(result.current.ready).toBe(true));
      act(() => result.current.open("s1"));
      await waitFor(() => expect(result.current.session?.id).toBe("s1"));
      apiMock.mockResolvedValueOnce({
        id: "s1",
        projectId: "p1",
        messages: [{
          role: "assistant",
          content: JSON.stringify({ message: "确认", drafts: [{ tool: "projects_save", body: { id: "okr", name: "OKR系统" } }] }),
        }],
      });
      const projectLoads = () => apiMock.mock.calls.filter(([path]) => path === "/api/projects").length;
      const before = projectLoads();
      await act(() => result.current.applyDraft(0, 0, { approved: true }));
      await waitFor(() => expect(projectLoads()).toBe(before + 1));
      expect(parent.postMessage).toHaveBeenCalledWith({ type: "agentflow-projects-changed" }, location.origin);
      const generation = writeMock.mock.calls.filter(([path]) => path === "/api/assistant/generations").at(-1);
      expect(generation[1].messages.at(-1)).toMatchObject({ role: "user", createdAt: expect.stringMatching(/^\d{4}-\d{2}-\d{2}T/) });
    } finally {
      if (original) Object.defineProperty(window, "parent", original);
      else delete window.parent;
    }
  });

  test("a proposal card accepts before it runs, and a timed-out one never runs", async () => {
    const { result } = renderHook(() => useChat());
    await waitFor(() => expect(result.current.ready).toBe(true));
    act(() => result.current.open("s1"));
    await waitFor(() => expect(result.current.session?.id).toBe("s1"));
    const draft = { tool: "issues_dispatch", path: { id: "i1" }, proposal: { reason: "走 SOP", timing: "start" } };
    const withDraft = (extra = {}) => ({
      id: "s1",
      projectId: "p1",
      messages: [{ role: "assistant", content: JSON.stringify({ message: "建议", drafts: [{ ...draft, ...extra }] }) }],
    });
    const original = writeMock.getMockImplementation();
    const mcpCalls = () => apiMock.mock.calls.filter(([path]) => path === "/mcp");
    try {
      writeMock.mockImplementation((path, body, method) =>
        path.endsWith("/proposals")
          ? Promise.reject(Object.assign(Error("这条建议已经处理过"), { status: 409 }))
          : original(path, body, method),
      );
      apiMock.mockResolvedValueOnce(withDraft());
      await act(() => expect(result.current.applyDraft(0, 0, { approved: true })).rejects.toThrow("已经处理过"));
      expect(mcpCalls()).toHaveLength(0);

      writeMock.mockImplementation((path, body, method) =>
        path.endsWith("/proposals")
          ? Promise.resolve({ session: withDraft({ decision: "accepted" }), followUp: null })
          : original(path, body, method),
      );
      apiMock.mockResolvedValueOnce(withDraft());
      await act(() => result.current.applyDraft(0, 0, { approved: true }));
      const accept = writeMock.mock.calls.findIndex(([path]) => path.endsWith("/proposals"));
      const mcp = apiMock.mock.calls.findIndex(([path]) => path === "/mcp");
      expect(writeMock.mock.invocationCallOrder[accept]).toBeLessThan(apiMock.mock.invocationCallOrder[mcp]);
      const saved = writeMock.mock.calls.find(([path, , method]) => path === "/api/assistant/sessions/s1" && method === "PUT");
      expect(JSON.parse(saved[1].messages[0].content).drafts[0]).toMatchObject({ decision: "accepted", saved: true });
    } finally {
      writeMock.mockImplementation(original);
    }
  });

  test.each(["declined", "muted", "expired"])("a %s proposal decision refreshes a stale card without writing", async (decision) => {
    const { result } = renderHook(() => useChat());
    await waitFor(() => expect(result.current.ready).toBe(true));
    act(() => result.current.open("s1"));
    await waitFor(() => expect(result.current.session?.id).toBe("s1"));
    const draft = { tool: "issues_dispatch", path: { id: "i1" }, proposal: { reason: "走 SOP", timing: "start" } };
    const withDraft = (value) => ({
      ...result.current.session,
      messages: [{ role: "assistant", content: JSON.stringify({ message: "建议", drafts: [value] }) }],
    });
    act(() => result.current.setSession(withDraft(draft)));
    const latest = withDraft({ ...draft, decision });
    apiMock.mockResolvedValueOnce(latest);

    await act(() => expect(result.current.applyDraft(0, 0, { approved: true })).rejects.toThrow("这条建议已经处理过"));

    expect(apiMock).not.toHaveBeenCalledWith("/mcp", expect.anything());
    expect(writeMock).not.toHaveBeenCalled();
    expect(result.current.session).toEqual(latest);
  });

  test("provides local fixture conversations and completes a local reply", async () => {
    history.replaceState({}, "", "/?localPreview=1");
    vi.useFakeTimers();
    const real = {
      user: { id: 1, loginName: "tester", chineseName: "测试" },
      ready: true, activeId: null, sessions: [], projectId: "", model: "m1",
      open: vi.fn(), newChat: vi.fn(), setError: vi.fn(), setModel: vi.fn(),
      setSession: vi.fn(), send: vi.fn(), stop: vi.fn(), rename: vi.fn(),
      archive: vi.fn(), remove: vi.fn(), edit: vi.fn(), regenerate: vi.fn(),
      selectProject: vi.fn(), linkIssue: vi.fn(), applyDraft: vi.fn(), refresh: vi.fn(),
    };
    const { result } = renderHook(() => useLocalConversations(real));
    await act(async () => {});
    const local = result.current.sessions[0];
    expect(isLocalConversation(local.id)).toBe(true);
    act(() => result.current.open(local.id));
    expect(result.current.local).toBe(true);
    await act(() => result.current.send({ text: "请总结", attachments: [], references: [] }));
    await act(() => vi.advanceTimersByTimeAsync(700));
    expect(result.current.session.messages.at(-1).role).toBe("assistant");
    await act(() => result.current.rename(local.id, "重命名"));
    await act(() => result.current.linkIssue("ISSUE-2"));
    await act(() => result.current.edit(0, "修改本地消息", false));
    let forked;
    await act(async () => { forked = await result.current.edit(0, "创建分支", true); });
    await act(() => result.current.regenerate(forked));
    await act(() => result.current.stop());
    await act(() => result.current.selectProject("p1"));
    await act(() => result.current.archive(forked.id, true));
    await act(() => result.current.remove(local.id));
    vi.useRealTimers();
    history.replaceState({}, "", "/");
  });

  test("a new account has no fixture conversations unless explicitly previewing", async () => {
    history.replaceState({}, "", "/");
    const real = { user: { id: 99 }, ready: true, activeId: null, sessions: [], model: "m1" };
    const { result } = renderHook(() => useLocalConversations(real));
    await act(async () => {});
    expect(result.current.sessions).toEqual([]);
    expect(result.current.localSessions).toBe(false);
  });

  test("tracks opening and closing presence", () => {
    const { result, rerender } = renderHook(
      ({ open }) => usePanelPresence(open, 0),
      { initialProps: { open: false } },
    );
    rerender({ open: true });
    expect(result.current.mounted).toBe(true);
    rerender({ open: false });
    expect(result.current.phase).toBe("closing");
  });

  test("resizes and keyboard-controls the artifact panel layout", () => {
    const close = vi.fn();
    const host = document.createElement("div");
    host.className = "chat-shell";
    host.innerHTML = '<header class="conversation-header"></header><main class="conversation-main"></main><aside class="artifact-panel"><button>inside</button></aside>';
    document.body.append(host);
    const panel = host.querySelector(".artifact-panel");
    host.querySelector(".conversation-header").getBoundingClientRect = () => ({ width: 1000, top: 0, bottom: 40, left: 0, right: 1000 });
    panel.setPointerCapture = vi.fn();
    const root = { current: panel };
    const { result, rerender, unmount } = renderHook(
      ({ nodeId, open }) => useArtifactPanelLayout(root, close, nodeId, open),
      { initialProps: { nodeId: null, open: true } },
    );
    act(() => result.current.startResize({ button: 0, clientX: 600, pointerId: 1, currentTarget: panel, preventDefault: vi.fn() }));
    act(() => result.current.resize({ clientX: 550 }));
    act(() => result.current.finishResize());
    act(() => result.current.resizeByKey({ key: "End", preventDefault: vi.fn() }));
    act(() => result.current.resizeByKey({ key: "Enter", preventDefault: vi.fn() }));
    rerender({ nodeId: "n1", open: true });
    rerender({ nodeId: null, open: false });
    fireEvent.keyDown(document, { key: "Escape" });
    unmount();
    host.remove();
    expect(panel.setPointerCapture).toHaveBeenCalled();
  });

  test("loads conversation runs from session run references", async () => {
    apiMock
      .mockResolvedValueOnce({ id: "run-one", status: "failed" })
      .mockResolvedValueOnce({ id: "run-two", status: "succeeded" });
    const { result } = renderHook(() =>
      useConversationRuns({
        id: "s1",
        runId: "run-one",
        messages: [{ role: "assistant", runIds: ["run-two"] }],
      }),
    );
    await waitFor(() => expect(result.current).toHaveLength(2));
  });

  test("surfaces command failures without throwing from chat actions", async () => {
    const { result } = renderHook(() => useChat());
    await waitFor(() => expect(result.current.ready).toBe(true));
    apiMock.mockRejectedValueOnce(new Error("读取失败"));
    await act(() => result.current.rename("s1", "失败"));
    writeMock.mockRejectedValueOnce(new Error("保存失败"));
    await act(() => result.current.archive("s1", true));
    apiMock.mockRejectedValueOnce(new Error("删除失败"));
    await act(() => result.current.remove("s1"));
    expect(result.current.ready).toBe(true);
  });

  test("consumes generation updates from EventSource", async () => {
    const sources = [];
    class FakeEventSource {
      constructor(url) {
        this.url = url;
        sources.push(this);
      }
      addEventListener(type, listener) {
        if (type === "generation") this.generation = listener;
      }
      close = vi.fn();
    }
    globalThis.EventSource = FakeEventSource;
    apiMock.mockImplementation(async (path) => {
      if (path === "/api/auth/current")
        return { user: { id: 1, loginName: "tester", codeBuddyApiKeyConfigured: true } };
      if (path === "/api/projects")
        return [{ id: "p1", name: "项目一", ownerId: 1, members: [1] }];
      if (path === "/api/assistant/models")
        return { default: "m1", sponsoredModel: "m1", models: [{ id: "m1", name: "模型一" }] };
      if (path === "/api/users")
        return [{ id: 1, loginName: "tester", codeBuddyApiKeyConfigured: true }];
      if (path === "/api/assistant/sessions") return [];
      if (path.includes("/generation"))
        return {
          id: "g-stream",
          status: "running",
          events: [{ sequence: 1, data: { kind: "answer", text: "部分" } }],
        };
      if (path.startsWith("/api/assistant/sessions/"))
        return {
          id: "s1", title: "流式会话", projectId: "p1", model: "m1",
          messages: [{ role: "user", content: "开始" }],
        };
      return [];
    });
    const { result, unmount } = renderHook(() => useChat());
    await waitFor(() => expect(result.current.ready).toBe(true));
    act(() => result.current.open("s1"));
    await waitFor(() => expect(sources).toHaveLength(1));
    await act(async () => {
      sources[0].generation({
        data: JSON.stringify({
          id: "g-stream",
          status: "completed",
          events: [{ sequence: 2, full: true, data: { kind: "answer", text: "完成" } }],
        }),
      });
    });
    await waitFor(() => expect(result.current.generation?.status).toBe("completed"));
    expect(result.current.live.answer).toBe("部分完成");
    unmount();
    delete globalThis.EventSource;
  });

  test("replaces a new prompt title with the background summary without touching messages", async () => {
    const prompt = "请帮我把积分商城的需求、缺口和验收标准整理出来";
    const original = apiMock.getMockImplementation();
    let summarized = false;
    apiMock.mockImplementation(async (path, options) => {
      if (path === "/api/assistant/sessions/s1") {
        const messages = [{ role: "user", content: prompt }];
        if (!summarized) return { id: "s1", title: prompt.slice(0, 36), projectId: "p1", model: "m1", messages };
        return { id: "s1", title: "积分商城梳理", titleGenerated: true, messages: [] };
      }
      return original(path, options);
    });
    try {
      history.replaceState({}, "", "/?session=s1");
      const { result, unmount } = renderHook(() => useChat());
      await waitFor(() => expect(result.current.session?.title).toBe(prompt.slice(0, 36)));
      summarized = true;
      await waitFor(() => expect(result.current.session?.title).toBe("积分商城梳理"), { timeout: 5000 });
      expect(result.current.session.titleGenerated).toBe(true);
      expect(result.current.session.messages).toEqual([{ role: "user", content: prompt }]);
      unmount();
    } finally {
      apiMock.mockImplementation(original);
    }
  });
});

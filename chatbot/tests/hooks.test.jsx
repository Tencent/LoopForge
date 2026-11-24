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
    if (path === "/api/assistant/models?executor=codex") return { default: "default", models: [{ id: "default", name: "CLI default", family: "gpt" }, { id: "gpt-5", name: "GPT-5", family: "gpt", efforts: ["high", "xhigh"] }] };
    if (path === "/api/assistant/models") return {
      default: "m1", sponsoredModel: "m1", models: [{ id: "m1", name: "模型一", effort: true }],
      engines: [{ id: "codebuddy", configured: true }, { id: "codex", configured: true }, { id: "claude-code", configured: false }],
    };
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

import { useChat } from "../src/useChat.js";
import { writeComposerPreference } from "../src/composer-preferences.js";
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
    expect(result.current.executors.map((item) => item.id)).toEqual(["codebuddy", "codex"]);
    act(() => result.current.setExecutor("codex"));
    await waitFor(() => expect(result.current.models.map((m) => m.id)).toEqual(["gpt-5"]));
    expect(result.current.model).toBe("gpt-5");
    expect(result.current.effort).toBe("");
    expect(result.current.sponsored).toBe(false);
    act(() => result.current.setExecutor("unknown"));
    expect(result.current.executor).toBe("codex");
    await act(() => result.current.send({ text: "你好" }));
    expect(writeMock).toHaveBeenCalledWith(
      "/api/assistant/generations",
      expect.objectContaining({ executor: "codex", model: "gpt-5", effort: "" }),
    );
    act(() => result.current.setExecutor("codebuddy"));
    expect(result.current.model).toBe("m1");
    localStorage.removeItem("loopforge.assistant.executor");
  });

  test("restores the CLI and model saved by an earlier version", async () => {
    localStorage.setItem("loopforge.assistant.executor", "codex");
    localStorage.setItem("loopforge.assistant.model.codex", "gpt-5");
    const { result } = renderHook(() => useChat());
    await waitFor(() => expect(result.current.ready).toBe(true));
    expect(result.current.executor).toBe("codex");
    await waitFor(() => expect(result.current.model).toBe("gpt-5"));
  });

  test("requires an available executor for a new chat and recovers a stale CLI preference", async () => {
    const original = apiMock.getMockImplementation();
    const user = { id: 1, loginName: "tester" };
    let engines = [];
    apiMock.mockImplementation(async (path, options) => {
      if (path === "/api/auth/current") return { user };
      if (path === "/api/users") return [user];
      if (path === "/api/assistant/models") return { default: "m1", models: [{ id: "m1" }], engines };
      return original(path, options);
    });
    writeComposerPreference("executor", "codebuddy");
    try {
      const { result, unmount } = renderHook(useChat);
      await waitFor(() => expect(result.current.ready).toBe(true));
      expect(result.current).toMatchObject({ executor: "", executors: [], model: "" });
      await act(async () => expect(await result.current.send({ text: "第一条消息" })).toBe(false));
      expect(writeMock).not.toHaveBeenCalled();
      unmount();

      engines = [{ id: "codebuddy", configured: false }, { id: "codex", configured: true }];
      const configured = renderHook(useChat);
      await waitFor(() => expect(configured.result.current.ready).toBe(true));
      expect(configured.result.current.executor).toBe("codex");
      expect(configured.result.current.executors.map(item => item.id)).toEqual(["codex"]);
    } finally {
      apiMock.mockImplementation(original);
      writeComposerPreference("executor", null);
    }
  });

  test("removes a deleted CLI and selects the re-added executor without reloading the chat", async () => {
    const original = apiMock.getMockImplementation();
    let zcode = true, codebuddy = true;
    const identity = { id: 1, loginName: "tester", avatarUrl: "/avatar.png" };
    apiMock.mockImplementation(async (path, options) => {
      if (path === "/api/auth/current") return { user: identity };
      if (path === "/api/users") return [{
        id: 1, loginName: "tester",
        ...(zcode && { agentCliCredentials: { zcode: { apiKeyConfigured: true } } }),
        ...(codebuddy && { codeBuddyApiKeyConfigured: true }),
      }];
      if (path === "/api/assistant/models") return {
        models: [], engines: [{ id: "zcode", configured: zcode }, { id: "codebuddy", configured: codebuddy }],
        preferences: { executor: zcode ? "zcode" : codebuddy ? "codebuddy" : "" },
      };
      return original(path, options);
    });
    const notifyChange = () => act(() => window.dispatchEvent(new MessageEvent("message", {
      origin: location.origin, source: window.parent, data: { type: "agentflow-credentials-changed" },
    })));
    try {
      const { result } = renderHook(useChat);
      await waitFor(() => expect(result.current.ready).toBe(true));
      expect(result.current.executor).toBe("zcode");
      expect(result.current.executors.map(item => item.id)).toEqual(["codebuddy", "zcode"]);

      zcode = codebuddy = false;
      notifyChange();
      await waitFor(() => expect(result.current.executors).toEqual([]));
      await waitFor(() => expect(result.current.user.agentCliCredentials).toBeUndefined());

      codebuddy = true;
      notifyChange();
      await waitFor(() => expect(result.current.executor).toBe("codebuddy"));
      expect(result.current.executors.map(item => item.id)).toEqual(["codebuddy"]);
      expect(result.current.user.avatarUrl).toBe(identity.avatarUrl);
    } finally {
      apiMock.mockImplementation(original);
    }
  });

  test.each([
    { status: "running", accepted: false },
    { status: "running", accepted: true },
    { status: "completed", accepted: true },
    { status: "running", accepted: true, transcriptUnavailable: true },
    { status: "running", accepted: true, superseded: true },
  ])("recovers a lost send response only for its own request ($status, accepted: $accepted, unavailable: $transcriptUnavailable, superseded: $superseded)", async ({ status, accepted, transcriptUnavailable, superseded }) => {
    const originalAPI = apiMock.getMockImplementation();
    const originalWrite = writeMock.getMockImplementation();
    let submitted;
    apiMock.mockImplementation(async (path, options) => {
      if (path === "/api/assistant/sessions/s1/generation") return {
        id: "concurrent-generation", status, events: [],
        clientRequestId: accepted && !superseded ? submitted.clientRequestId : "another-members-request",
      };
      if (submitted && path === "/api/assistant/sessions/s1") {
        if (transcriptUnavailable) throw Object.assign(Error("记录暂不可用"), { status: 503 });
        if (superseded) return { ...(await originalAPI(path, options)), recentClientRequestIds: [submitted.clientRequestId] };
      }
      return originalAPI(path, options);
    });
    writeMock.mockImplementation((path, body, method) => {
      if (path !== "/api/assistant/generations") return originalWrite(path, body, method);
      submitted = body;
      return Promise.reject(new TypeError("发送响应丢失"));
    });
    try {
      const { result } = renderHook(useChat);
      await waitFor(() => expect(result.current.ready).toBe(true));
      act(() => result.current.open("s1"));
      await waitFor(() => expect(result.current).toMatchObject({
        session: expect.objectContaining({ id: "s1" }), replyStatePending: false,
      }));
      await act(async () => expect(await result.current.send({ text: "这条消息必须正确确认" })).toBe(accepted));
      expect(submitted.clientRequestId).toBeTruthy();
      if (!accepted) expect(result.current.error).toBe("发送响应丢失");
    } finally {
      apiMock.mockImplementation(originalAPI);
      writeMock.mockImplementation(originalWrite);
    }
  });

  test("an earlier generation poll cannot replace a newly accepted running generation", async () => {
    const originalAPI = apiMock.getMockImplementation();
    const originalWrite = writeMock.getMockImplementation();
    const previous = { id: "previous", status: "completed", events: [] };
    const running = { id: "new-reply", status: "running", events: [] };
    let reads = 0, reportPrevious;
    apiMock.mockImplementation((path, options) => {
      if (path.includes("/sessions/s1/generation?")) {
        reads++;
        if (reads === 2) return new Promise((resolve) => { reportPrevious = () => resolve(previous); });
        return Promise.resolve(reads === 1 ? previous : running);
      }
      return originalAPI(path, options);
    });
    writeMock.mockImplementation((path, body, method) => path === "/api/assistant/generations"
      ? Promise.resolve(running) : originalWrite(path, body, method));
    try {
      const { result } = renderHook(useChat);
      await waitFor(() => expect(result.current.ready).toBe(true));
      act(() => result.current.open("s1"));
      await waitFor(() => expect(reportPrevious).toBeTypeOf("function"), { timeout: 3500 });
      await act(async () => expect(await result.current.send({ text: "新的问题" })).toBe(true));
      expect(result.current.generation?.id).toBe(running.id);
      await act(async () => reportPrevious());
      expect(result.current).toMatchObject({ generation: running, busy: true });
      await waitFor(() => expect(reads).toBeGreaterThan(2));
      expect(result.current.generation?.id).toBe(running.id);
    } finally {
      apiMock.mockImplementation(originalAPI);
      writeMock.mockImplementation(originalWrite);
    }
  });

  test("pauses a send if the late Codex runner catalog no longer supports the displayed effort", async () => {
    const original = apiMock.getMockImplementation();
    const session = {
      id: "s1", titleGenerated: true, model: "codex:gpt-5.5", executor: "codex", effort: "max",
      executionTarget: { pool: "runner", runnerId: "codex-runner" },
      messages: [{ role: "user", content: "原消息" }],
    };
    let loadCatalog;
    apiMock.mockImplementation(async (path, options) => {
      if (path === "/api/assistant/sessions/s1") return session;
      if (path === "/api/runners?mine=1") return [
        { id: "codex-runner", ownerUserId: 1, runtimes: [{ provider: "codex", authMode: "local" }] },
      ];
      if (path === "/api/assistant/models?executor=codex") return new Promise((resolve) => {
        loadCatalog = () => resolve({ models: [{ id: "gpt-5.5", efforts: ["low", "medium", "high", "xhigh"] }] });
      });
      return original(path, options);
    });
    try {
      history.replaceState({}, "", "/?session=s1");
      const { result } = renderHook(useChat);
      await waitFor(() => expect(result.current).toMatchObject({
        session: expect.objectContaining({ id: "s1" }), replyStatePending: false,
        model: session.model, effort: session.effort, machinesReady: true,
      }));
      await waitFor(() => expect(loadCatalog).toBeTypeOf("function"));
      let request;
      act(() => { request = result.current.send({ text: "保留我的思考强度" }); });
      await act(async () => {
        loadCatalog();
        expect(await request).toBe(false);
      });
      expect(result.current.error).toContain("请重新选择");
      expect(writeMock).not.toHaveBeenCalledWith("/api/assistant/generations", expect.anything());
      act(() => result.current.setEffort("high"));
      await act(async () => expect(await result.current.send({ text: "保留我的思考强度" })).toBe(true));
      expect(writeMock).toHaveBeenCalledWith("/api/assistant/generations", expect.objectContaining({
        executor: "codex", model: "gpt-5.5", effort: "high",
      }));
    } finally {
      apiMock.mockImplementation(original);
    }
  });

  test("restores the conversation's unsent choices even after choosing a stronger model elsewhere", async () => {
    const original = apiMock.getMockImplementation();
    apiMock.mockImplementation(async (path, options) => {
      if (path === "/api/assistant/models") return {
        default: "m1",
        models: [{ id: "m1" }, { id: "m2", efforts: ["high"] }, { id: "m3", efforts: ["max"] }],
      };
      if (path === "/api/assistant/sessions/s2") return { id: "s2", model: "m1", messages: [] };
      return original(path, options);
    });
    try {
      const first = renderHook(useChat);
      await waitFor(() => expect(first.result.current.ready).toBe(true));
      act(() => first.result.current.open("s1"));
      await waitFor(() => expect(first.result.current.session?.id).toBe("s1"));
      act(() => first.result.current.setModel("m2"));
      act(() => first.result.current.setEffort("high"));

      act(() => first.result.current.open("s2"));
      await waitFor(() => expect(first.result.current.session?.id).toBe("s2"));
      expect(first.result.current).toMatchObject({ model: "m1", effort: "" });
      act(() => first.result.current.setModel("m3"));
      act(() => first.result.current.setEffort("max"));

      act(() => first.result.current.newChat());
      expect(first.result.current).toMatchObject({ model: "m3", effort: "max" });
      act(() => first.result.current.open("s1"));
      await waitFor(() => expect(first.result.current.session?.id).toBe("s1"));
      expect(first.result.current).toMatchObject({ model: "m2", effort: "high" });
      expect(writeMock).not.toHaveBeenCalled();

      first.unmount();
      const reloaded = renderHook(useChat);
      await waitFor(() => expect(reloaded.result.current.session?.id).toBe("s1"));
      expect(reloaded.result.current).toMatchObject({ model: "m2", effort: "high" });
      await act(() => reloaded.result.current.send({ text: "继续原会话" }));
      expect(writeMock).toHaveBeenCalledWith("/api/assistant/generations", expect.objectContaining({
        sessionId: "s1", model: "m2", effort: "high",
      }));
    } finally {
      apiMock.mockImplementation(original);
    }
  });

  test("restores model choices if the generation poll opens the transcript first", async () => {
    const original = apiMock.getMockImplementation();
    const stored = {
      id: "s1", title: "会话", titleGenerated: true, model: "m1", executor: "codebuddy",
      messages: [{ role: "user", content: "原消息" }],
    };
    let initial, reads = 0;
    apiMock.mockImplementation((path, options) => {
      if (path === "/api/assistant/models") return Promise.resolve({
        default: "m1", models: [{ id: "m1" }, { id: "m2", efforts: ["high"] }],
      });
      if (path === "/api/assistant/sessions/s1") {
        if (++reads > 1) return Promise.resolve(stored);
        return new Promise((resolve) => { initial = () => resolve(stored); });
      }
      return original(path, options);
    });
    writeComposerPreference("conversation.1.model.s1", JSON.stringify({ executor: "codebuddy", model: "m2", effort: "high" }));
    try {
      history.replaceState({}, "", "/?session=s1");
      const { result } = renderHook(useChat);
      await waitFor(() => expect(result.current.session?.id).toBe("s1"));
      expect(reads).toBe(2);
      expect(result.current).toMatchObject({ model: "m2", effort: "high" });
      await act(() => result.current.send({ text: "继续" }));
      expect(writeMock).toHaveBeenCalledWith("/api/assistant/generations", expect.objectContaining({
        sessionId: "s1", model: "m2", effort: "high",
      }));
      await act(async () => initial());
      expect(result.current.session.messages.at(-1)?.content).toBe("继续");
    } finally {
      initial?.();
      apiMock.mockImplementation(original);
    }
  });

  test.each([
    "credential check", "first save", "first save after reopening the draft",
  ])("keeps a model picked during the %s for later messages", async (delay) => {
    const originalAPI = apiMock.getMockImplementation();
    const originalWrite = writeMock.getMockImplementation();
    let saved, completePending, userReads = 0;
    apiMock.mockImplementation((path, options) => {
      if (path === "/api/assistant/models") return Promise.resolve({
        default: "m1", models: [{ id: "m1" }, { id: "m2", efforts: ["high"] }],
      });
      if (path === "/api/users" && delay === "credential check") {
        if (userReads++ === 0) return Promise.resolve([{ id: 1, loginName: "tester" }]);
        return new Promise((resolve) => {
          completePending = () => resolve([{ id: 1, loginName: "tester", codeBuddyApiKeyConfigured: true }]);
        });
      }
      if (path === "/api/assistant/sessions/new-session") return Promise.resolve(saved);
      return originalAPI(path, options);
    });
    writeMock.mockImplementation((path, body, method) => {
      if (path !== "/api/assistant/sessions/new-session" || method !== "PUT") return originalWrite(path, body, method);
      saved = body;
      if (delay === "credential check") return Promise.resolve(saved);
      return new Promise((resolve) => { completePending = () => resolve(saved); });
    });
    try {
      const { result } = renderHook(useChat);
      await waitFor(() => expect(result.current.machinesReady).toBe(true));
      await act(() => result.current.selectProject("p1"));
      let sending;
      act(() => { sending = result.current.send({ text: "第一条消息" }); });
      await waitFor(() => expect(completePending).toBeTypeOf("function"));
      if (delay === "first save after reopening the draft") {
        act(() => result.current.open("s1"));
        await waitFor(() => expect(result.current.session?.id).toBe("s1"));
        act(() => result.current.newChat());
      }
      act(() => result.current.setModel("m2"));
      act(() => result.current.setEffort("high"));
      await act(async () => {
        completePending();
        await sending;
      });
      expect(writeMock).toHaveBeenCalledWith("/api/assistant/generations", expect.objectContaining({
        model: "m1", effort: "",
      }));
      expect(result.current).toMatchObject({ activeId: "new-session", model: "m2", effort: "high" });
      act(() => result.current.newChat());
      act(() => result.current.open("new-session"));
      await waitFor(() => expect(result.current.session?.id).toBe("new-session"));
      expect(result.current).toMatchObject({ model: "m2", effort: "high" });
    } finally {
      completePending?.();
      apiMock.mockImplementation(originalAPI);
      writeMock.mockImplementation(originalWrite);
    }
  });

  test("sends a draft to its own conversation when navigation happens during a credential check", async () => {
    const original = apiMock.getMockImplementation();
    let userReads = 0, completeCheck;
    apiMock.mockImplementation((path, options) => {
      if (path === "/api/assistant/models") return Promise.resolve({
        default: "m1", models: [{ id: "m1" }],
      });
      if (path === "/api/users") {
        if (userReads++ === 0) return Promise.resolve([{ id: 1, loginName: "tester" }]);
        return new Promise((resolve) => {
          completeCheck = () => resolve([{ id: 1, loginName: "tester", codeBuddyApiKeyConfigured: true }]);
        });
      }
      return original(path, options);
    });
    try {
      const { result } = renderHook(useChat);
      await waitFor(() => expect(result.current.machinesReady).toBe(true));
      let sending;
      act(() => { sending = result.current.send({ text: "原草稿", approvalMode: "auto" }); });
      await waitFor(() => expect(completeCheck).toBeTypeOf("function"));
      act(() => result.current.open("s1"));
      await waitFor(() => expect(result.current.session?.id).toBe("s1"));
      await act(async () => {
        completeCheck();
        expect(await sending).toBe(true);
      });
      const requests = writeMock.mock.calls.filter(([path]) => path === "/api/assistant/generations");
      expect(requests).toHaveLength(1);
      expect(requests[0][1]).toMatchObject({
        sessionId: "new-session", approvalMode: "auto", messages: [expect.objectContaining({ content: "原草稿" })],
      });
      expect(result.current).toMatchObject({
        activeId: "s1", session: { id: "s1", messages: [expect.objectContaining({ content: "原消息" })] },
      });
    } finally {
      completeCheck?.();
      apiMock.mockImplementation(original);
    }
  });

  test("reports an unsent message when the catalog fails before the generation request", async () => {
    const original = apiMock.getMockImplementation();
    let rejectCatalog;
    apiMock.mockImplementation((path, options) => {
      if (path === "/api/assistant/models?executor=codex")
        return new Promise((_, reject) => { rejectCatalog = reject; });
      if (path === "/api/assistant/sessions/s1")
        return Promise.resolve({ id: "s1", executor: "codex", model: "codex:historical", messages: [{ role: "user", content: "原消息" }] });
      if (path === "/api/assistant/sessions/s1/generation")
        return Promise.resolve({ id: "another-turn", status: "running" });
      return original(path, options);
    });
    try {
      history.replaceState({}, "", "/?session=s1");
      const { result } = renderHook(useChat);
      await waitFor(() => expect(result.current.model).toBe("codex:historical"));
      await waitFor(() => expect(rejectCatalog).toBeTypeOf("function"));
      let sending;
      act(() => { sending = result.current.send({ text: "继续" }); });
      await act(async () => {
        rejectCatalog(Error("目录加载失败"));
        expect(await sending).toBe(false);
      });
      expect(result.current.error).toBe("目录加载失败");
      expect(writeMock).not.toHaveBeenCalledWith("/api/assistant/generations", expect.anything());
      expect(apiMock).not.toHaveBeenCalledWith("/api/assistant/sessions/s1/generation");
    } finally {
      rejectCatalog?.(Error("目录加载失败"));
      apiMock.mockImplementation(original);
    }
  });

  test("does not create a chat without a concrete model on Pi", async () => {
    const original = apiMock.getMockImplementation();
    apiMock.mockImplementation(async (path, options) => path === "/api/assistant/models"
      ? { ...(await original(path, options)), engines: [{ id: "pi", configured: true }] }
      : original(path, options));
    const view = renderHook(() => useChat());
    try {
      await waitFor(() => expect(view.result.current.ready).toBe(true));
      act(() => view.result.current.setExecutor("pi"));
      await waitFor(() => expect(apiMock).toHaveBeenCalledWith("/api/assistant/models?executor=pi"));
      expect(view.result.current.models).toEqual([]);
      await act(async () => expect(await view.result.current.send({ text: "开始" })).toBe(false));
      expect(writeMock).not.toHaveBeenCalled();
    } finally {
      view.unmount();
      apiMock.mockImplementation(original);
      localStorage.removeItem("loopforge.assistant.executor");
    }
  });

  test("reads Antigravity model families and restores a previous variant as an effort", async () => {
    const originalAPI = apiMock.getMockImplementation();
    const originalWrite = writeMock.getMockImplementation();
    apiMock.mockImplementation((path, options) => {
      if (path === "/api/runners?mine=1") return Promise.resolve([
        { id: "mac", name: "Mac", status: "online", ownerUserId: 1,
          runtimes: [{ provider: "agy", authMode: "local" }] },
      ]);
      if (path === "/api/runners/default") return Promise.resolve({ runnerId: "mac" });
      return originalAPI(path, options);
    });
    writeMock.mockImplementation((path, body) => path === "/api/runners/mac/models/probe"
      ? Promise.resolve({ models: [
        {
          id: "claude-sonnet-5-5-medium", name: "Claude Sonnet 5.5",
          efforts: {
            low: "claude-sonnet-5-5-low", medium: "claude-sonnet-5-5-medium", high: "claude-sonnet-5-5-high",
          },
          discoveredReasoningEfforts: ["low", "medium", "high"],
        },
        { id: "claude-opus-5-5-medium", name: "Claude Opus 5.5" },
      ] })
      : originalWrite(path, body));

    writeComposerPreference("model.agy", "agy:claude-sonnet-5-5-high");
    const view = renderHook(() => useChat());
    try {
      await waitFor(() => expect(view.result.current.defaultRunnerId).toBe("mac"));
      act(() => view.result.current.setExecutor("agy"));
      await waitFor(() => expect(view.result.current.models.map((m) => m.id)).toEqual([
        "agy:claude-sonnet-5-5-medium", "agy:claude-opus-5-5-medium",
      ]));
      expect(writeMock).toHaveBeenCalledWith("/api/runners/mac/models/probe", { provider: "agy", useSaved: true });
      expect(view.result.current.model).toBe("agy:claude-sonnet-5-5-medium");
      expect(view.result.current.modelDetails.efforts).toEqual(["low", "medium", "high"]);
      expect(view.result.current.effort).toBe("high");
      act(() => view.result.current.setEffort(""));
      expect(view.result.current.effort).toBe("");
      act(() => view.result.current.setEffort("low"));
      await act(() => view.result.current.send({ text: "使用低思考强度" }));
      expect(writeMock).toHaveBeenCalledWith("/api/assistant/generations", expect.objectContaining({
        model: "agy:claude-sonnet-5-5-medium", effort: "low", executionTarget: { pool: "runner", runnerId: "mac" },
      }));
    } finally {
      view.unmount();
      apiMock.mockImplementation(originalAPI);
      writeMock.mockImplementation(originalWrite);
      localStorage.removeItem("loopforge.assistant.executor");
      localStorage.removeItem("loopforge.assistant.model.agy");
      localStorage.removeItem("loopforge.assistant.effort.agy.claude-sonnet-5-5-medium");
      history.replaceState({}, "", "/");
    }
  });

  test("asks for a new model before continuing on a retired Antigravity variant", async () => {
    const originalAPI = apiMock.getMockImplementation();
    const originalWrite = writeMock.getMockImplementation();
    const session = {
      id: "legacy-agy", executor: "agy", model: "agy:gemini-3.8-flash-high", effort: "",
      executionTarget: { pool: "runner", runnerId: "mac" },
      messages: [{ role: "user", content: "原消息" }, { role: "assistant", content: "原回复", model: "agy:gemini-3.8-flash-high" }],
    };
    apiMock.mockImplementation(async (path, options) => {
      if (path === "/api/runners?mine=1") return [
        { id: "mac", status: "online", ownerUserId: 1, runtimes: [{ provider: "agy", authMode: "local" }] },
      ];
      if (path === "/api/assistant/sessions/legacy-agy") return structuredClone(session);
      return originalAPI(path, options);
    });
    writeMock.mockImplementation(async (path, body) => path === "/api/runners/mac/models/probe"
      ? { models: [{
        id: "gemini-3.8-flash-low", name: "Gemini 3.8 Flash", discoveredReasoningEfforts: ["low", "medium"],
        efforts: { low: "gemini-3.8-flash-low", medium: "gemini-3.8-flash-medium" },
      }] }
      : originalWrite(path, body));
    const view = renderHook(() => useChat());
    try {
      await waitFor(() => expect(view.result.current.ready).toBe(true));
      act(() => view.result.current.open(session.id));
      await waitFor(() => expect(view.result.current.models.map(model => model.id)).toEqual(["agy:gemini-3.8-flash-low"]));
      await act(async () => expect(await view.result.current.send({ text: "继续" })).toBe(false));
      expect(view.result.current.error).toContain("请重新选择模型");
      expect(writeMock).not.toHaveBeenCalledWith("/api/assistant/generations", expect.anything());
      act(() => view.result.current.setModel("agy:gemini-3.8-flash-low"));
      await act(async () => expect(await view.result.current.send({ text: "继续" })).toBe(true));
      expect(writeMock).toHaveBeenCalledWith("/api/assistant/generations", expect.objectContaining({
        sessionId: session.id, model: "agy:gemini-3.8-flash-low", effort: "",
      }));
    } finally {
      view.unmount();
      apiMock.mockImplementation(originalAPI);
      writeMock.mockImplementation(originalWrite);
      localStorage.removeItem("loopforge.assistant.executor");
      localStorage.removeItem("loopforge.assistant.model.agy");
      localStorage.removeItem("loopforge.assistant.effort.agy.gemini-3.8-flash-low");
    }
  });

  test("keeps the draft unsent when the new runner does not list the visible Antigravity model", async () => {
    const originalAPI = apiMock.getMockImplementation();
    const originalWrite = writeMock.getMockImplementation();
    const gemini = "agy:gemini-a-high", claude = "agy:claude-b-medium";
    const runners = ["runner-a", "runner-b"].map((id) => ({
      id, status: "online", ownerUserId: 1, runtimes: [{ provider: "agy", authMode: "local" }],
    }));
    let completeProbe;
    apiMock.mockImplementation(async (path, options) => {
      if (path === "/api/runners?mine=1") return runners;
      if (path === "/api/runners/default") return { runnerId: "runner-a" };
      return originalAPI(path, options);
    });
    writeMock.mockImplementation((path, body, method) => {
      if (path === "/api/runners/runner-a/models/probe")
        return Promise.resolve({ models: [{ id: "gemini-a-high" }] });
      if (path === "/api/runners/runner-b/models/probe")
        return new Promise((resolve) => { completeProbe = () => resolve({ models: [{ id: "claude-b-medium" }] }); });
      return originalWrite(path, body, method);
    });
    try {
      const { result } = renderHook(useChat);
      await waitFor(() => expect(result.current.defaultRunnerId).toBe("runner-a"));
      act(() => result.current.setExecutor("agy"));
      await waitFor(() => expect(result.current.model).toBe(gemini));
      act(() => result.current.setModel(gemini));
      await act(async () => result.current.selectMachine("runner-b"));
      await waitFor(() => expect(completeProbe).toBeTypeOf("function"));
      expect(result.current.model).toBe(gemini);
      let request;
      act(() => { request = result.current.send({ text: "不要换模型" }); });
      await act(async () => {
        completeProbe();
        expect(await request).toBe(false);
      });
      expect(result.current.error).toContain("请重新选择");
      expect(writeMock).not.toHaveBeenCalledWith("/api/assistant/sessions/new-session", expect.anything(), "PUT");
      expect(writeMock).not.toHaveBeenCalledWith("/api/assistant/generations", expect.anything());
      act(() => result.current.setModel(claude));
      await act(async () => expect(await result.current.send({ text: "确认新模型" })).toBe(true));
      expect(writeMock).toHaveBeenCalledWith("/api/assistant/generations", expect.objectContaining({
        model: claude, executionTarget: { pool: "runner", runnerId: "runner-b" },
      }));
    } finally {
      apiMock.mockImplementation(originalAPI);
      writeMock.mockImplementation(originalWrite);
      localStorage.removeItem("loopforge.assistant.executor");
      localStorage.removeItem("loopforge.assistant.model.agy");
      localStorage.removeItem("loopforge.assistant.effort.agy.gemini-a-high");
      localStorage.removeItem("loopforge.assistant.effort.agy.claude-b-medium");
    }
  });

  test.each([
    { action: "send" },
    { action: "send", slowMachines: true },
    { action: "regenerate" },
  ])("validates the saved effort against the target runner catalog before $action ($slowMachines)", async ({ action, slowMachines }) => {
    const originalAPI = apiMock.getMockImplementation();
    const originalWrite = writeMock.getMockImplementation();
    const model = "agy:gemini-3.8-flash-low";
    const stored = {
      id: "agy-session", executor: "agy", model,
      executionTarget: { pool: "runner", runnerId: "mac" },
      messages: [{ role: "user", content: "原消息" }],
    };
    const runners = [{
      id: "mac", status: "online", ownerUserId: 1,
      runtimes: [{ provider: "agy", authMode: "local" }],
    }];
    const catalog = { models: [{
      id: "gemini-3.8-flash-low", name: "Gemini 3.8 Flash",
      discoveredReasoningEfforts: ["low", "high"],
      efforts: { low: "gemini-3.8-flash-low", high: "gemini-3.8-flash-high" },
    }] };
    let completeMachines, completeCatalog;
    const machines = slowMachines
      ? new Promise((resolve) => { completeMachines = () => resolve(runners); })
      : Promise.resolve(runners);
    apiMock.mockImplementation((path, options) => {
      if (path === "/api/runners?mine=1") return machines;
      if (path === "/api/assistant/sessions/agy-session") return Promise.resolve(stored);
      return originalAPI(path, options);
    });
    writeMock.mockImplementation((path, body, method) => {
      if (path !== "/api/runners/mac/models/probe") return originalWrite(path, body, method);
      return new Promise((resolve) => { completeCatalog = () => resolve(catalog); });
    });
    writeComposerPreference("conversation.1.model.agy-session", JSON.stringify({ executor: "agy", model, effort: "high" }));
    try {
      history.replaceState({}, "", action === "send" ? "/?session=agy-session" : "/?session=s1");
      const { result } = renderHook(useChat);
      await waitFor(() => expect(result.current.session?.id).toBe(action === "send" ? stored.id : "s1"));
      if (!slowMachines) await waitFor(() => expect(result.current.machinesReady).toBe(true));
      let request;
      act(() => { request = action === "send" ? result.current.send({ text: "继续" }) : result.current.regenerate(stored); });
      if (completeMachines) await act(async () => completeMachines());
      await waitFor(() => expect(completeCatalog).toBeTypeOf("function"));
      expect(writeMock).not.toHaveBeenCalledWith("/api/assistant/generations", expect.anything());
      await act(async () => {
        completeCatalog();
        expect(await request).toBe(true);
      });
      expect(writeMock).toHaveBeenCalledWith("/api/assistant/generations", expect.objectContaining({
        sessionId: stored.id, executor: "agy", model, effort: "high",
      }));
      expect(writeMock.mock.calls.filter(([path]) => path === "/api/runners/mac/models/probe")).toHaveLength(1);
    } finally {
      completeMachines?.();
      completeCatalog?.();
      apiMock.mockImplementation(originalAPI);
      writeMock.mockImplementation(originalWrite);
    }
  });

  test.each(["after saving a key", "during the first open"])("refreshes a pending Cursor catalog %s", async (scenario) => {
    const original = apiMock.getMockImplementation();
    let configured = scenario === "during the first open";
    let discovered = false;
    let pendingReads = 0;
    const fallback = { id: "widget", name: "Widget" };
    writeComposerPreference("executor", "cursor");
    apiMock.mockImplementation(async (path, options) => {
      if (path === "/api/assistant/models?executor=cursor") return { models: [fallback] };
      if (path === "/api/assistant/models" && configured) {
        if (!discovered) pendingReads++;
        return {
          default: "m1",
          cursorModelsPending: !discovered,
          engines: [{ id: "cursor", configured: true }],
          models: [{ ...fallback, id: "cursor:widget", engine: "cursor", efforts: discovered ? ["low", "xhigh"] : [] }],
        };
      }
      return original(path, options);
    });
    try {
      const { result } = renderHook(() => useChat());
      await waitFor(() => expect(result.current.ready).toBe(true));
      if (!configured) {
        configured = true;
        act(() => window.dispatchEvent(new MessageEvent("message", {
          origin: location.origin,
          source: window.parent,
          data: { type: "agentflow-credentials-changed" },
        })));
      }
      await waitFor(() => expect(pendingReads).toBeGreaterThan(0));
      await waitFor(() => expect(result.current.modelDetails?.name).toBe("Widget"));
      expect(result.current.modelDetails.efforts || []).toEqual([]);
      act(() => { result.current.setModel(result.current.model); });
      discovered = true;
      await waitFor(() => expect(result.current.modelDetails.efforts).toEqual(["low", "xhigh"]), { timeout: 3500 });
      act(() => result.current.setEffort("xhigh"));
      expect(result.current.effort).toBe("xhigh");
      await act(() => result.current.send({ text: "验证强度" }));
      expect(writeMock).toHaveBeenCalledWith(
        "/api/assistant/generations", expect.objectContaining({ executor: "cursor", model: "cursor:widget", effort: "xhigh" }),
      );
    } finally {
      apiMock.mockImplementation(original);
    }
  });

  test.each([
    [false, "请先在个人配置中配置自己的 CodeBuddy API Key"],
    [true, "当前 CLI 暂无可用模型，请在个人配置中获取或添加模型。"],
  ])("blocks sends without a usable CodeBuddy model (key configured: %s)", async (configured, message) => {
    localStorage.removeItem("loopforge.assistant.executor");
    const original = apiMock.getMockImplementation();
    const user = { id: 1, loginName: "tester", codeBuddyApiKeyConfigured: configured };
    apiMock.mockImplementation((path, options) => {
      if (path === "/api/auth/current") return Promise.resolve({ user });
      if (path === "/api/users") return Promise.resolve([user]);
      if (path === "/api/assistant/models") return Promise.resolve({
        default: "default", engines: [{ id: "codebuddy", configured: true }], models: [{ id: "default" }, { id: "default-model" }],
      });
      return original(path, options);
    });
    try {
      const { result } = renderHook(useChat);
      await waitFor(() => expect(result.current.ready && result.current.machinesReady).toBe(true));
      expect(result.current.models).toEqual([]);
      expect(result.current.model).toBe("");
      await act(async () => expect(await result.current.send({ text: "草稿" })).toBe(false));
      expect(result.current.error).toBe(message);
      expect(writeMock).not.toHaveBeenCalled();
    } finally {
      apiMock.mockImplementation(original);
    }
  });

  test("reloads the model list if a CodeBuddy key was added in another tab", async () => {
    localStorage.removeItem("loopforge.assistant.executor");
    const original = apiMock.getMockImplementation();
    const user = { id: 1, loginName: "tester" };
    let configured = false;
    apiMock.mockImplementation((path, options) => {
      if (path === "/api/auth/current") return Promise.resolve({ user });
      if (path === "/api/users") return Promise.resolve([{ ...user, codeBuddyApiKeyConfigured: configured }]);
      if (path === "/api/assistant/models") return Promise.resolve({
        default: "default", engines: [{ id: "codebuddy", configured: true }], models: configured ? [{ id: "deepseek-v4.1-flash" }] : [],
      });
      return original(path, options);
    });
    try {
      const { result } = renderHook(useChat);
      await waitFor(() => expect(result.current.ready && result.current.machinesReady).toBe(true));
      configured = true;
      await act(async () => expect(await result.current.send({ text: "草稿" })).toBe(false));
      expect(result.current.error).toContain("正在刷新模型列表");
      expect(writeMock).not.toHaveBeenCalled();
      await waitFor(() => expect(result.current.model).toBe("deepseek-v4.1-flash"));
      await act(async () => expect(await result.current.send({ text: "草稿" })).toBe(true));
      expect(writeMock).toHaveBeenCalledWith("/api/assistant/generations", expect.objectContaining({
        executor: "codebuddy", model: "deepseek-v4.1-flash",
      }));
    } finally {
      apiMock.mockImplementation(original);
    }
  });

  test.each([
    ["personal default", "codebuddy:default", { executor: "codebuddy", model: "m3", effort: "high" }, "m2", "m3", "high"],
    ["catalog default for the original CLI", "codebuddy:default", { executor: "codex", model: "gpt-5" }, "m2", "m2", ""],
    ["first available model", "codebuddy:default", { executor: "codebuddy", model: "retired" }, "default", "m1", ""],
    ["explicitly selected model", "m1", { executor: "codebuddy", model: "m3" }, "m2", "m1", ""],
  ])("uses the %s when reopening and regenerating an old conversation", async (_name, savedModel, preferences, catalogDefault, expectedModel, expectedEffort) => {
    localStorage.removeItem("loopforge.assistant.executor");
    const original = apiMock.getMockImplementation();
    const saved = {
      id: "s1", projectId: "p1", executor: "codebuddy", model: savedModel,
      messages: [{ role: "user", content: "继续" }],
    };
    apiMock.mockImplementation((path, options) => {
      if (path === "/api/assistant/models") return Promise.resolve({
        default: catalogDefault, preferences, models: [{ id: "m1" }, { id: "m2" }, { id: "m3", efforts: ["high"] }],
      });
      if (path === "/api/assistant/sessions/s1") return Promise.resolve(saved);
      return original(path, options);
    });
    try {
      const { result } = renderHook(useChat);
      await waitFor(() => expect(result.current.ready && result.current.machinesReady).toBe(true));
      act(() => result.current.open(saved.id));
      await waitFor(() => expect(result.current.session?.id).toBe(saved.id));
      expect(result.current.model).toBe(expectedModel);
      expect(result.current.effort).toBe(expectedEffort);
      await act(() => result.current.regenerate(saved, "confirm"));
      expect(writeMock).toHaveBeenCalledWith("/api/assistant/generations", expect.objectContaining({
        sessionId: saved.id, executor: "codebuddy", model: expectedModel, effort: expectedEffort,
      }));
    } finally {
      apiMock.mockImplementation(original);
    }
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

  test("restores model and effort choices in fresh chats and across catalog sources", async () => {
    localStorage.setItem("loopforge.assistant.model.codebuddy", "default");
    const original = apiMock.getMockImplementation();
    let personalCodexCatalog = false;
    apiMock.mockImplementation(async (path, options) => {
      if (path === "/api/assistant/models") {
        const models = [
          { id: "default", name: "CLI 默认模型" },
          { id: "m1", name: "模型一", efforts: ["low"] },
          { id: "claude-opus-5.5", name: "Opus", efforts: ["max"] },
        ];
        if (personalCodexCatalog)
          models.push({ id: "codex:gpt-5", engine: "codex", name: "GPT-5", efforts: ["high"] });
        return { default: "default", defaultEfforts: ["low"], models, engines: [{ id: "codex", configured: true }] };
      }
      return original(path, options);
    });
    try {
      const first = renderHook(() => useChat());
      await waitFor(() => expect(first.result.current.ready).toBe(true));
      expect(first.result.current.models.map((m) => m.id)).toEqual(["m1", "claude-opus-5.5"]);
      expect(first.result.current.model).toBe("m1");
      act(() => first.result.current.setEffort("low"));
      await act(() => first.result.current.send({ text: "运行第一个可用模型" }));
      expect(writeMock).toHaveBeenCalledWith(
        "/api/assistant/generations",
        expect.objectContaining({ model: "m1", effort: "low" }),
      );
      act(() => first.result.current.newChat());
      act(() => { first.result.current.setModel("claude-opus-5.5"); });
      act(() => first.result.current.setEffort("max"));
      act(() => first.result.current.open("s1"));
      await waitFor(() => expect(first.result.current.session?.id).toBe("s1"));
      expect(first.result.current.model).toBe("m1");
      expect(first.result.current.effort).toBe("");
      act(() => first.result.current.newChat());
      expect(first.result.current.model).toBe("claude-opus-5.5");
      expect(first.result.current.effort).toBe("max");
      await act(() => first.result.current.send({ text: "你好" }));
      expect(writeMock).toHaveBeenCalledWith(
        "/api/assistant/generations",
        expect.objectContaining({ model: "claude-opus-5.5", effort: "max" }),
      );
      act(() => first.result.current.newChat());
      act(() => first.result.current.setExecutor("codex"));
      await waitFor(() => expect(first.result.current.models.map((m) => m.id)).toEqual(["gpt-5"]));
      act(() => { first.result.current.setModel("gpt-5"); });
      act(() => first.result.current.setEffort("high"));
      act(() => first.result.current.setExecutor("codebuddy"));
      expect(first.result.current.model).toBe("claude-opus-5.5");
      expect(first.result.current.effort).toBe("max");
      act(() => first.result.current.setExecutor("codex"));
      act(() => first.result.current.newChat());
      expect(first.result.current.model).toBe("gpt-5");
      expect(first.result.current.effort).toBe("high");
      first.unmount();
      personalCodexCatalog = true;
      history.replaceState({}, "", "/");
      const reloaded = renderHook(() => useChat());
      await waitFor(() => expect(reloaded.result.current.ready).toBe(true));
      expect(reloaded.result.current.executor).toBe("codex");
      expect(reloaded.result.current.model).toBe("codex:gpt-5");
      expect(reloaded.result.current.effort).toBe("high");
      act(() => reloaded.result.current.setExecutor("codebuddy"));
      expect(reloaded.result.current.model).toBe("claude-opus-5.5");
      expect(reloaded.result.current.effort).toBe("max");
      act(() => reloaded.result.current.setModel("m1"));
      expect(reloaded.result.current.effort).toBe("low");
      act(() => reloaded.result.current.setModel("claude-opus-5.5"));
      expect(reloaded.result.current.effort).toBe("max");
      act(() => reloaded.result.current.setEffort(""));
      // A remembered model that is no longer offered falls back to the list.
      writeComposerPreference("model.codebuddy", "retired-model");
      act(() => reloaded.result.current.newChat());
      expect(reloaded.result.current.model).toBe("m1");
      expect(reloaded.result.current.effort).toBe("low");
      act(() => reloaded.result.current.setModel("claude-opus-5.5"));
      expect(reloaded.result.current.effort).toBe("");
    } finally {
      apiMock.mockImplementation(original);
    }
  });

  test("regenerating after an edit follows the composer's approval mode", async () => {
    const { result } = renderHook(() => useChat());
    await waitFor(() => expect(result.current.ready).toBe(true));
    const stored = { id: "s1", projectId: "p1", model: "m1", messages: [{ role: "user", content: "修改" }] };
    await act(() => result.current.regenerate(stored, "assisted"));
    expect(writeMock).toHaveBeenLastCalledWith("/api/assistant/generations", expect.objectContaining({ approvalMode: "assisted" }));
    await act(() => result.current.regenerate(stored, "bogus"));
    expect(writeMock).toHaveBeenLastCalledWith("/api/assistant/generations", expect.objectContaining({ approvalMode: "confirm" }));
  });

  test("regenerates using the model and CLI of the conversation returned by an edit", async () => {
    const originalAPI = apiMock.getMockImplementation();
    const originalWrite = writeMock.getMockImplementation();
    const main = {
      id: "main", executor: "codex", model: "codex:gpt-main",
      messages: [{ role: "user", content: "修改" }],
    };
    apiMock.mockImplementation((path, options) => {
      if (path === "/api/assistant/models") return Promise.resolve({
        default: "m1", engines: [{ id: "codebuddy", configured: true }, { id: "codex", configured: true }], models: [
          { id: "m1" },
          { id: "codex:gpt-main", engine: "codex" },
          { id: "codex:gpt-alternate", engine: "codex", efforts: ["high"] },
        ],
      });
      if (path === "/api/assistant/sessions/main") return Promise.resolve(main);
      return originalAPI(path, options);
    });
    writeMock.mockImplementation((path, body, method) =>
      path === "/api/assistant/sessions/s1/edit" ? Promise.resolve(main) : originalWrite(path, body, method));
    try {
      const { result } = renderHook(useChat);
      await waitFor(() => expect(result.current.ready).toBe(true));
      act(() => result.current.open(main.id));
      await waitFor(() => expect(result.current.model).toBe("codex:gpt-main"));
      act(() => result.current.setModel("codex:gpt-alternate"));
      act(() => result.current.setEffort("high"));
      act(() => result.current.open("s1"));
      await waitFor(() => expect(result.current.session?.id).toBe("s1"));

      const { edit, regenerate } = result.current;
      await act(async () => regenerate(await edit(0, "修改"), "assisted"));
      expect(writeMock).toHaveBeenCalledWith("/api/assistant/generations", expect.objectContaining({
        sessionId: main.id, executor: "codex", model: "codex:gpt-alternate", effort: "high",
        approvalMode: "assisted",
      }));
    } finally {
      apiMock.mockImplementation(originalAPI);
      writeMock.mockImplementation(originalWrite);
    }
  });

  test("regenerates and reopens a fork using its server-resolved CLI for an unprefixed model", async () => {
    const original = apiMock.getMockImplementation();
    const fork = {
      id: "fork", parentSessionId: "s1", executor: "codex", model: "gpt-5", effort: "high",
      messages: [{ role: "user", content: "修改" }],
    };
    const parent = { ...fork, id: "s1", parentSessionId: undefined, executor: "codex" };
    apiMock.mockImplementation((path, options) => {
      if (path === "/api/assistant/sessions") return Promise.resolve([parent, fork]);
      if (path === "/api/assistant/sessions/s1") return Promise.resolve(parent);
      if (path === "/api/assistant/sessions/fork") return Promise.resolve(fork);
      return original(path, options);
    });
    try {
      history.replaceState({}, "", "/?session=s1");
      const { result, unmount } = renderHook(useChat);
      await waitFor(() => expect(result.current.effort).toBe("high"));
      await act(() => result.current.regenerate(fork));
      expect(writeMock).toHaveBeenCalledWith("/api/assistant/generations", expect.objectContaining({
        sessionId: fork.id, executor: "codex", model: "gpt-5", effort: "high",
      }));
      unmount();
      const reopened = renderHook(useChat);
      await waitFor(() => expect(reopened.result.current.effort).toBe("high"));
      expect(reopened.result.current).toMatchObject({ executor: "codex", model: "gpt-5" });
    } finally {
      apiMock.mockImplementation(original);
    }
  });

  test("an open conversation keeps its model and effort when the model is missing from the picker", async () => {
    localStorage.removeItem("loopforge.assistant.executor");
    const original = apiMock.getMockImplementation();
    const codexSession = {
      id: "s9", title: "Codex 会话", projectId: "p1", executor: "codex", model: "codex:gpt-5.6-sol", effort: "high",
      updatedAt: "2026-01-01T00:00:00Z",
      messages: [{ role: "user", content: "hi" }, { role: "assistant", content: "ok", model: "codex:gpt-5.6-sol" }],
    };
    apiMock.mockImplementation(async (path, options) =>
      path === "/api/assistant/sessions/s9" ? structuredClone(codexSession) : original(path, options));
    try {
      history.replaceState({}, "", "/?session=s9");
      const { result } = renderHook(() => useChat());
      await waitFor(() => expect(result.current.session?.id).toBe("s9"));
      await waitFor(() => expect(apiMock).toHaveBeenCalledWith("/api/assistant/models?executor=codex"));
      await act(async () => {});
      expect(result.current.executor).toBe("codex");
      expect(result.current.model).toBe("codex:gpt-5.6-sol");
      expect(result.current.effort).toBe("high");
      expect(result.current.modelDetails.efforts).toEqual(["high"]);
      await act(() => result.current.send({ text: "继续" }));
      expect(writeMock).toHaveBeenCalledWith("/api/assistant/generations", expect.objectContaining({
        model: "codex:gpt-5.6-sol", effort: "high",
      }));
      act(() => result.current.setEffort(""));
      expect(result.current).toMatchObject({ model: "codex:gpt-5.6-sol", effort: "" });
    } finally {
      apiMock.mockImplementation(original);
      localStorage.removeItem("loopforge.assistant.executor");
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

  test("keeps machine choices local and binds the final choice on the first send", async () => {
    const { result } = renderHook(() => useChat());
    await waitFor(() => expect(result.current.machinesReady).toBe(true));
    await act(() => result.current.selectMachine(""));
    await act(() => result.current.selectMachine("mac"));
    await act(() => result.current.refreshMachines());
    expect(result.current.defaultRunnerId).toBe("mac");
    expect(result.current.session).toBeNull();
    expect(result.current.activeId).toBeNull();
    expect(location.search).not.toContain("session=");
    expect(writeMock).not.toHaveBeenCalled();
    await act(() => result.current.send({text:"开始"}));
    expect(writeMock).toHaveBeenCalledWith("/api/assistant/generations",expect.objectContaining({executionTarget:{pool:"runner",runnerId:"mac"}}));
    await act(async () => expect(await result.current.selectMachine("")).toBe(false));
  });

  test("waits for the personal machine default before allowing the first send", async () => {
    const original=apiMock.getMockImplementation();
    let release;
    const delayed=new Promise(resolve=>{release=resolve;});
    apiMock.mockImplementation((path,opts)=>path==="/api/runners/default"?delayed:original(path,opts));
    try {
      const {result}=renderHook(()=>useChat());
      await waitFor(()=>expect(result.current.ready).toBe(true));
      await act(async()=>expect(await result.current.send({text:"开始"})).toBe(false));
      expect(result.current.error).toBe("执行机器仍在加载，请稍后发送。");
      expect(writeMock).not.toHaveBeenCalled();
      await act(async()=>{release({runnerId:"mac"});});
      await waitFor(()=>expect(result.current.defaultRunnerId).toBe("mac"));
    } finally {apiMock.mockImplementation(original);release({runnerId:"mac"});}
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
      if (path === "/api/runs/active-workspace") {
        return {
          workflowSnapshot: {
            sandboxProfileId: "mac-tools",
            sandboxSnapshot: { profile: { id: "mac-tools", driver: "native" } },
          },
        };
      }
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
    expect(result.current.workspaceSandbox).toBeNull();
    await act(async () => {
      sources[0].generation({
        data: JSON.stringify({
          id: "g-stream",
          status: "running",
          runtimeMode: "workspace",
          workspaceRunId: "active-workspace",
          events: [],
        }),
      });
    });
    await waitFor(() => expect(result.current.workspaceSandbox).toEqual({ id: "mac-tools", driver: "native" }));
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

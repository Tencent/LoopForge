import { act, renderHook, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";

const state = vi.hoisted(() => ({ transcript: [], sessionSettings: {}, generation: null, generationGate: null, preferences: {} }));
const { apiMock, writeMock } = vi.hoisted(() => {
  const apiMock = vi.fn(async (path) => {
    if (path === "/api/auth/current")
      return { user: { id: 1, loginName: "tester", codeBuddyApiKeyConfigured: true } };
    if (path === "/api/projects") return [{ id: "p1", name: "项目一", ownerId: 1, members: [1] }];
    if (path === "/api/assistant/models")
      return { default: "m1", sponsoredModel: "m1", models: [
        { id: "m1", name: "模型一" },
        { id: "codex:gpt-5", engine: "codex", name: "GPT-5", efforts: ["high"] },
      ] };
    if (path === "/api/assistant/preferences") return structuredClone(state.preferences);
    if (path === "/api/users") return [{ id: 1, loginName: "tester", codeBuddyApiKeyConfigured: true }];
    if (path === "/api/assistant/sessions") return [];
    if (path.startsWith("/api/assistant/sessions/s2")) {
      if (path.includes("/generation")) throw Object.assign(new Error("not found"), { status: 404 });
      return { id: "s2", title: "别的会话", projectId: "p1", model: "m1", messages: [] };
    }
    if (path.includes("/generation")) {
      if (state.generationGate) await state.generationGate;
      return structuredClone(state.generation);
    }
    if (path.startsWith("/api/assistant/sessions/"))
      return { id: "s1", title: "排队", projectId: "p1", model: "m1",
        ...state.sessionSettings, messages: structuredClone(state.transcript) };
    return [];
  });
  const writeMock = vi.fn(async (path) => {
    if (path === "/api/assistant/generations") return { id: "g-next", status: "running", events: [] };
    return {};
  });
  return { apiMock, writeMock };
});

vi.mock("../src/api.js", async (original) => ({
  ...(await original()),
  api: apiMock,
  write: writeMock,
}));

import { useChat } from "../src/useChat.js";

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
const unfinished = {
  role: "assistant",
  content: JSON.stringify({ message: "正在看 zg", drafts: [] }),
  unfinished: "生成已停止",
  activity: [{ kind: "tool", tool: "runs_get" }],
};
const steerPosts = () =>
  writeMock.mock.calls.filter(([path]) => path.includes("/generation/steer"));
const emit = async (g) => {
  state.generation = g;
  await act(async () => {
    sources.at(-1).generation({ data: JSON.stringify(g) });
  });
};
const generationPosts = () =>
  writeMock.mock.calls.filter(([path]) => path === "/api/assistant/generations");

async function openRunning() {
  const hook = renderHook(() => useChat());
  await waitFor(() => expect(hook.result.current.ready).toBe(true));
  act(() => hook.result.current.open("s1"));
  await waitFor(() => expect(sources).toHaveLength(1));
  await waitFor(() => expect(hook.result.current.busy).toBe(true));
  return hook;
}
async function finish(status) {
  // The server stores the unfinished reply before the turn leaves "running".
  state.transcript = [{ role: "user", content: "查失败节点" }, unfinished];
  await act(async () => {
    sources[0].generation({ data: JSON.stringify({ id: "g-running", status, events: [] }) });
  });
}

describe("sending while a reply runs", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    sources.length = 0;
    localStorage.clear();
    history.replaceState({}, "", "/");
    state.transcript = [{ role: "user", content: "查失败节点" }];
    state.sessionSettings = {};
    state.generation = { id: "g-running", status: "running", events: [{ sequence: 1, data: { kind: "tool", tool: "runs_get" } }] };
    state.generationGate = null;
    state.preferences = {};
    globalThis.EventSource = FakeEventSource;
  });
  afterEach(() => {
    delete globalThis.EventSource;
  });

  test("queues instead of rejecting, then sends after the reply is stored", async () => {
    const { result, unmount } = await openRunning();
    let ok;
    await act(async () => {
      ok = await result.current.send({ text: "应该看 CI 而不是 lint", approvalMode: "auto" });
    });
    expect(ok).toBe(true);
    expect(result.current.error).toBe("");
    expect(result.current.queue.map((m) => m.text)).toEqual(["应该看 CI 而不是 lint"]);
    expect(generationPosts()).toHaveLength(0);
    // Queuing must not interrupt the running reply.
    expect(writeMock.mock.calls.some(([path]) => path.includes("/generation/steer") || path.includes("/generation/cancel"))).toBe(false);

    await finish("failed");
    await waitFor(() => expect(generationPosts()).toHaveLength(1));
    const body = generationPosts()[0][1];
    expect(body).toMatchObject({ approvalMode: "auto", approvalUserId: 1 });
    // The queued question follows the stored unfinished reply.
    expect(body.messages.map((m) => m.role)).toEqual(["user", "assistant", "user"]);
    expect(body.messages[1].unfinished).toBe("生成已停止");
    expect(body.messages[2].content).toBe("应该看 CI 而不是 lint");
    await waitFor(() => expect(result.current.queue).toHaveLength(0));
    unmount();
  });

  test("stopping holds the queue until the user continues", async () => {
    const { result, unmount } = await openRunning();
    await act(async () => {
      await result.current.send({ text: "排队的消息" });
    });
    await act(() => result.current.stop());
    expect(result.current.queueHeld).toBe(true);
    await finish("cancelled");
    await waitFor(() => expect(result.current.session?.messages).toHaveLength(2));
    expect(generationPosts()).toHaveLength(0);
    expect(result.current.queue).toHaveLength(1);

    act(() => result.current.resumeQueue());
    await waitFor(() => expect(generationPosts()).toHaveLength(1));
    expect(generationPosts()[0][1].messages.at(-1).content).toBe("排队的消息");
    unmount();
  });

  test("an explicitly resumed queue uses request approval after binding its conversation to a project", async () => {
    state.sessionSettings = { projectId: "" };
    const { result, unmount } = await openRunning();
    await act(async () => result.current.send({ text: "稍后继续", approvalMode: "auto" }));
    await act(async () => result.current.stop());
    await finish("cancelled");
    await waitFor(() => expect(result.current.replyStatePending).toBe(false));
    expect(result.current.queueHeld).toBe(true);
    writeMock.mockImplementationOnce(async (path, body, method) => {
      expect([path, method]).toEqual(["/api/assistant/sessions/s1", "PUT"]);
      state.sessionSettings = { projectId: body.projectId };
      return body;
    });
    await act(async () => result.current.selectProject("p1"));
    expect(result.current.session.projectId).toBe("p1");
    await act(async () => expect(await result.current.sendNow(result.current.queue[0].id)).toBe(true));
    expect(generationPosts()[0][1]).toMatchObject({
      sessionId: "s1", projectId: "p1", approvalMode: "confirm", approvalUserId: 1,
    });
    unmount();
  });

  test("the saved immediate-send preference steers with attachments and queues later sends during handoff", async () => {
    state.preferences = { steerOnSend: true };
    const { result, unmount } = await openRunning();
    const attachments = [{ id: "screenshot", name: "image.png", mimeType: "image/png" }];
    const references = [{ type: "issue", id: "issue-1" }];
    await act(async () => {
      expect(await result.current.send({ text: "立即看这里", attachments, references, approvalMode: "assisted" })).toBe(true);
      expect(await result.current.send({ text: "下一条补充" })).toBe(true);
    });
    expect(steerPosts()).toHaveLength(1);
    expect(steerPosts()[0][1]).toMatchObject({
      content: "立即看这里", model: "m1", attachments, references,
      approvalMode: "assisted", approvalUserId: 1,
    });
    expect(result.current.steering?.text).toBe("立即看这里");
    expect(result.current.queue.map((m) => m.text)).toEqual(["下一条补充"]);
    expect(generationPosts()).toHaveLength(0);
    unmount();
  });

  test("holds an immediate send when the runner catalog invalidates the chosen effort", async () => {
    const originalAPI = apiMock.getMockImplementation();
    const originalWrite = writeMock.getMockImplementation();
    state.preferences = { steerOnSend: true };
    state.sessionSettings = {
      executor: "agy", model: "agy:gemini-thinking", effort: "high",
      executionTarget: { pool: "runner", runnerId: "runner-a" },
    };
    let completeProbe;
    apiMock.mockImplementation((path, options) => path === "/api/runners?mine=1"
      ? Promise.resolve([{ id: "runner-a", ownerUserId: 1, runtimes: [{ provider: "agy", authMode: "local" }] }])
      : originalAPI(path, options));
    writeMock.mockImplementation((path, body, method) => path === "/api/runners/runner-a/models/probe"
      ? new Promise((resolve) => { completeProbe = () => resolve({
        models: [{ id: "gemini-thinking", discoveredReasoningEfforts: ["low"] }],
      }); })
      : originalWrite(path, body, method));
    try {
      const { result, unmount } = await openRunning();
      await waitFor(() => expect(completeProbe).toBeTypeOf("function"));
      expect(result.current.effort).toBe("high");
      let request;
      act(() => { request = result.current.send({ text: "保留这条补充" }); });
      await act(async () => {
        completeProbe();
        expect(await request).toBe(true);
      });
      expect(result.current.error).toContain("请重新选择");
      expect(result.current.queueHeld).toBe(true);
      expect(result.current.queue.map((item) => item.text)).toEqual(["保留这条补充"]);
      expect(steerPosts()).toHaveLength(0);
      await finish("completed");
      expect(generationPosts()).toHaveLength(0);
      expect(result.current.queue).toHaveLength(1);
      unmount();
    } finally {
      apiMock.mockImplementation(originalAPI);
      writeMock.mockImplementation(originalWrite);
    }
  });

  test.each(["already running", "starts while opening", "finishes while opening"])(
    "follows the shared session's first CLI when another member's reply %s",
    async (phase) => {
      localStorage.setItem("loopforge.assistant.conversation.1.model.s1",
        JSON.stringify({ executor: "codebuddy", model: "m1" }));
      state.preferences = { steerOnSend: true };
      const actualChoice = { shared: true, executor: "codex", model: "codex:gpt-5", effort: "high" };
      let reportGeneration;
      if (phase === "already running") state.sessionSettings = actualChoice;
      else {
        state.sessionSettings = { shared: true, executor: "codebuddy" };
        state.generationGate = new Promise((resolve) => { reportGeneration = resolve; });
      }
      const { result, unmount } = renderHook(useChat);
      await waitFor(() => expect(result.current.ready).toBe(true));
      act(() => result.current.open("s1"));
      if (reportGeneration) {
        await waitFor(() => expect(result.current.session?.id).toBe("s1"));
        expect(result.current.executor).toBe("codebuddy");
        expect(result.current.replyStatePending).toBe(true);
        await act(async () => expect(await result.current.send({ text: "等待恢复会话" })).toBe(false));
        expect(generationPosts()).toHaveLength(0);
        expect(steerPosts()).toHaveLength(0);
        state.sessionSettings = actualChoice;
        if (phase === "finishes while opening") {
          state.transcript.push({ ...unfinished, model: actualChoice.model });
          state.generation = { ...state.generation, status: "completed" };
        }
        state.generationGate = null;
        await act(async () => reportGeneration());
      }
      await waitFor(() => expect(result.current).toMatchObject({
        executor: actualChoice.executor, model: actualChoice.model, effort: actualChoice.effort,
        busy: phase !== "finishes while opening",
      }));
      await act(async () => expect(await result.current.send({ text: "请继续" })).toBe(true));
      const requests = phase === "finishes while opening" ? generationPosts() : steerPosts();
      expect(requests).toHaveLength(1);
      expect(requests[0][1]).toMatchObject({ model: actualChoice.model, effort: actualChoice.effort });
      unmount();
    },
  );

  test("waits for the transcript when a new shared generation starts in an already open session", async () => {
    const original = apiMock.getMockImplementation();
    let started = false, resolveTranscript;
    state.preferences = { steerOnSend: true };
    state.sessionSettings = { titleGenerated: true, shared: true, model: "m1", executor: "codebuddy" };
    apiMock.mockImplementation((path, options) => {
      if (path.includes("/sessions/s1/generation") && !started)
        return Promise.reject(Object.assign(Error("生成不存在"), { status: 404 }));
      if (path === "/api/assistant/sessions/s1" && started && !resolveTranscript)
        return new Promise((resolve) => { resolveTranscript = resolve; });
      return original(path, options);
    });
    try {
      const { result } = renderHook(useChat);
      await waitFor(() => expect(result.current.ready).toBe(true));
      act(() => result.current.open("s1"));
      await waitFor(() => expect(result.current).toMatchObject({
        session: expect.objectContaining({ id: "s1" }), replyStatePending: false, executor: "codebuddy",
      }));
      started = true;
      await waitFor(() => expect(resolveTranscript).toBeTypeOf("function"), { timeout: 3500 });
      await act(async () => expect(await result.current.send({ text: "先等待会话同步" })).toBe(false));
      expect(generationPosts()).toHaveLength(0);
      expect(steerPosts()).toHaveLength(0);
      state.sessionSettings = { ...state.sessionSettings, model: "codex:gpt-5", executor: "codex", effort: "high" };
      await act(async () => resolveTranscript(await original("/api/assistant/sessions/s1")));
      expect(result.current).toMatchObject({ replyStatePending: false, busy: true, executor: "codex" });
      await act(async () => expect(await result.current.send({ text: "继续任务" })).toBe(true));
      expect(steerPosts()[0][1]).toMatchObject({ model: "codex:gpt-5", effort: "high" });
    } finally {
      apiMock.mockImplementation(original);
    }
  });

  test.each([
    [400, "请先配置 API Key"],
    [409, "这个对话由 Codex 回复，不能中途切换 CLI"],
  ])("an immediate send rejected with %i holds the queue", async (status, error) => {
    state.preferences = { steerOnSend: true };
    const { result, unmount } = await openRunning();
    writeMock.mockRejectedValueOnce(Object.assign(new Error(error), { status }));
    await act(async () => {
      expect(await result.current.send({ text: "不能丢失" })).toBe(true);
    });
    expect(steerPosts()).toHaveLength(1);
    expect(result.current.steering).toBe(null);
    expect(result.current.queue.map((m) => m.text)).toEqual(["不能丢失"]);
    expect(result.current.error).toBe(error);
    expect(result.current.queueHeld).toBe(true);
    await finish("completed");
    await waitFor(() => expect(result.current.session?.messages).toHaveLength(2));
    expect(generationPosts()).toHaveLength(0);
    expect(result.current.queue.map((m) => m.text)).toEqual(["不能丢失"]);
    unmount();
  });

  test("a reply ending before the steer response does not retry a rejected message", async () => {
    state.preferences = { steerOnSend: true };
    const { result, unmount } = await openRunning();
    let rejectSteer;
    writeMock.mockImplementationOnce(() => new Promise((_, reject) => { rejectSteer = reject; }));
    let sent;
    act(() => { sent = result.current.send({ text: "等待引导结果" }); });

    await finish("completed");
    await waitFor(() => expect(result.current.session?.messages).toHaveLength(2));
    expect(generationPosts()).toHaveLength(0);
    await act(async () => {
      rejectSteer(Object.assign(new Error("请先配置 API Key"), { status: 400 }));
      expect(await sent).toBe(true);
    });
    expect(result.current.queueHeld).toBe(true);
    expect(result.current.queue.map((m) => m.text)).toEqual(["等待引导结果"]);
    expect(generationPosts()).toHaveLength(0);
    unmount();
  });

  test("saving the setting updates an already open chat", async () => {
    const { result, unmount } = await openRunning();
    state.preferences = { steerOnSend: true };
    await act(async () => {
      window.dispatchEvent(new MessageEvent("message", {
        source: window.parent, origin: location.origin,
        data: { type: "agentflow-assistant-preferences-changed" },
      }));
    });
    await act(async () => { await result.current.send({ text: "使用新设置" }); });
    expect(steerPosts()).toHaveLength(1);
    expect(result.current.queue).toHaveLength(0);
    expect(result.current.steering?.text).toBe("使用新设置");
    unmount();
  });

  test("a slow initial preferences read cannot overwrite a newly saved setting", async () => {
    let release;
    const original = apiMock.getMockImplementation();
    apiMock.mockImplementation((path) => path === "/api/assistant/preferences"
      ? new Promise((resolve) => { release = resolve; }) : original(path));
    const { result, unmount } = await openRunning();
    apiMock.mockImplementation(original);
    state.preferences = { steerOnSend: true };
    await act(async () => {
      window.dispatchEvent(new MessageEvent("message", {
        source: window.parent, origin: location.origin,
        data: { type: "agentflow-assistant-preferences-changed" },
      }));
    });
    await act(async () => { release({ steerOnSend: false }); });
    await act(async () => { await result.current.send({ text: "按最新设置发送" }); });
    expect(steerPosts()).toHaveLength(1);
    unmount();
  });

  test("returning to a hidden tab picks up the saved send preference", async () => {
    const { result, unmount } = await openRunning();
    const visibility = vi.spyOn(document, "visibilityState", "get").mockReturnValue("hidden");
    const preferenceReads = () => apiMock.mock.calls.filter(([path]) => path === "/api/assistant/preferences").length;
    const initialReads = preferenceReads();
    try {
      await act(async () => { document.dispatchEvent(new Event("visibilitychange")); });
      expect(preferenceReads()).toBe(initialReads);
      state.preferences = { steerOnSend: true };
      visibility.mockReturnValue("visible");
      await act(async () => { document.dispatchEvent(new Event("visibilitychange")); });
      await act(async () => { await result.current.send({ text: "切回后使用新设置" }); });
      expect(steerPosts()).toHaveLength(1);
    } finally {
      visibility.mockRestore();
      unmount();
    }
  });

  test("prevents repeated stop requests and waits for actual process exit", async () => {
    const { result, unmount } = await openRunning();
    let resolve;
    writeMock.mockImplementationOnce(() => new Promise((done) => { resolve = done; }));
    let request;
    act(() => { request = result.current.stop(); });
    expect(result.current.stopping).toBe(true);
    expect(result.current.busy).toBe(true);
    await act(() => result.current.stop());
    expect(writeMock.mock.calls.filter(([path]) => path.includes("/generation/cancel"))).toHaveLength(1);
    await act(async () => { resolve({}); await request; });
    expect(result.current.stopping).toBe(true);
    expect(result.current.busy).toBe(true);
    await finish("cancelled");
    await waitFor(() => expect(result.current.busy).toBe(false));
    expect(result.current.stopping).toBe(false);
    unmount();
  });

  test("reflects a stop requested from another client and allows retry after a failed request", async () => {
    const { result, unmount } = await openRunning();
    writeMock.mockRejectedValueOnce(new Error("cancel unavailable"));
    await act(() => result.current.stop());
    expect(result.current.stopping).toBe(false);
    expect(result.current.busy).toBe(true);
    expect(result.current.error).toBe("cancel unavailable");
    await emit({ ...state.generation, cancelRequested: true });
    expect(result.current.stopping).toBe(true);
    await act(() => result.current.stop());
    expect(writeMock.mock.calls.filter(([path]) => path.includes("/generation/cancel"))).toHaveLength(1);
    unmount();
  });

  test("a removed message is never sent", async () => {
    const { result, unmount } = await openRunning();
    await act(async () => {
      await result.current.send({ text: "算了" });
    });
    act(() => result.current.removeQueued(result.current.queue[0].id));
    expect(result.current.queue).toHaveLength(0);
    await finish("completed");
    await waitFor(() => expect(result.current.session?.messages).toHaveLength(2));
    expect(generationPosts()).toHaveLength(0);
    unmount();
  });

  test("send now interrupts the running reply with that message only", async () => {
    const { result, unmount } = await openRunning();
    await act(async () => {
      await result.current.send({ text: "先看 CI" });
      await result.current.send({ text: "然后整理结论" });
    });
    const [first, second] = result.current.queue;
    await act(async () => {
      await result.current.sendNow(first.id);
    });
    expect(steerPosts()).toHaveLength(1);
    expect(steerPosts()[0][0]).toContain("generationId=g-running");
    expect(steerPosts()[0][1].content).toBe("先看 CI");
    expect(result.current.steering?.text).toBe("先看 CI");
    expect(result.current.queue.map((m) => m.id)).toEqual([second.id]);

    // The interrupted reply ends and the server keeps it with the steer message.
    state.transcript = [
      { role: "user", content: "查失败节点" },
      { ...unfinished, stopped: true },
      { role: "user", content: "先看 CI", trigger: "steer" },
    ];
    await emit({ id: "g-running", status: "cancelled", events: [], steer: { content: "先看 CI" } });
    await waitFor(() => expect(result.current.session?.messages).toHaveLength(3));
    // The server starts the follow-up turn itself; the queue must not race it.
    expect(generationPosts()).toHaveLength(0);
    expect(result.current.steering?.text).toBe("先看 CI");

    state.generation = { id: "g-follow", status: "running", events: [] };
    await waitFor(() => expect(sources).toHaveLength(2), { timeout: 3500 });
    await waitFor(() => expect(result.current.steering).toBe(null));
    expect(generationPosts()).toHaveLength(0);
    expect(result.current.queue.map((m) => m.text)).toEqual(["然后整理结论"]);

    state.transcript = [...state.transcript, { role: "assistant", content: "CI 的结果" }];
    await emit({ id: "g-follow", status: "completed", events: [] });
    await waitFor(() => expect(generationPosts()).toHaveLength(1));
    expect(generationPosts()[0][1].messages.at(-1).content).toBe("然后整理结论");
    unmount();
  });

  test("send now on a held queue sends that message first", async () => {
    const { result, unmount } = await openRunning();
    await act(async () => {
      await result.current.send({ text: "第一条" });
      await result.current.send({ text: "第二条" });
    });
    await act(() => result.current.stop());
    await finish("cancelled");
    await waitFor(() => expect(result.current.session?.messages).toHaveLength(2));
    expect(result.current.queueHeld).toBe(true);

    await act(async () => {
      await result.current.sendNow(result.current.queue[1].id);
    });
    expect(steerPosts()).toHaveLength(0);
    expect(generationPosts()).toHaveLength(1);
    expect(generationPosts()[0][1].messages.at(-1).content).toBe("第二条");
    expect(result.current.queue.map((m) => m.text)).toEqual(["第一条"]);
    expect(result.current.queueHeld).toBe(true);
    unmount();
  });

  test("a reply that ended before send now was taken sends the message next", async () => {
    const { result, unmount } = await openRunning();
    await act(async () => {
      await result.current.send({ text: "补充一句" });
    });
    writeMock.mockImplementationOnce(async () => {
      throw Object.assign(new Error("生成已结束或不存在"), { status: 409 });
    });
    await act(async () => {
      await result.current.sendNow(result.current.queue[0].id);
    });
    expect(result.current.error).toBe("");
    expect(result.current.steering).toBe(null);
    expect(result.current.queue.map((m) => m.text)).toEqual(["补充一句"]);
    await finish("completed");
    await waitFor(() => expect(generationPosts()).toHaveLength(1));
    expect(generationPosts()[0][1].messages.at(-1).content).toBe("补充一句");
    unmount();
  });

  test("switching away and back keeps the queue while the reply still runs", async () => {
    const { result, unmount } = await openRunning();
    await act(async () => {
      await result.current.send({ text: "切走前排队的消息" });
    });
    expect(result.current.queue.map((m) => m.text)).toEqual(["切走前排队的消息"]);

    act(() => result.current.open("s2"));
    await waitFor(() => expect(result.current.session?.id).toBe("s2"));
    expect(result.current.queue).toHaveLength(0);

    // Coming back, the conversation loads before its running generation does.
    // The server rejects a new turn while one runs (409).
    let release;
    state.generationGate = new Promise((done) => { release = done; });
    writeMock.mockImplementation(async (path) => {
      if (path === "/api/assistant/generations")
        throw Object.assign(new Error("此会话正在后台生成"), { status: 409 });
      return {};
    });
    act(() => result.current.open("s1"));
    await waitFor(() => expect(result.current.session?.id).toBe("s1"));
    await act(async () => { await new Promise((done) => setTimeout(done, 50)); });
    expect(generationPosts()).toHaveLength(0);
    expect(result.current.queue.map((m) => m.text)).toEqual(["切走前排队的消息"]);

    state.generationGate = null;
    await act(async () => { release(); });
    await waitFor(() => expect(result.current.busy).toBe(true));
    expect(generationPosts()).toHaveLength(0);
    expect(result.current.queue.map((m) => m.text)).toEqual(["切走前排队的消息"]);
    expect(result.current.queueHeld).toBe(false);
    unmount();
  });

  test("a rejected queued send is never mistaken for an accepted one", async () => {
    const { result, unmount } = await openRunning();
    await act(async () => {
      await result.current.send({ text: "不能丢" });
    });
    act(() => result.current.open("s2"));
    await waitFor(() => expect(result.current.session?.id).toBe("s2"));
    act(() => result.current.open("s1"));
    await waitFor(() => expect(result.current.busy).toBe(true));
    // The reply finishes but another one is already running there; the
    // server rejects the queued send with 409. The message must come back, not disappear.
    writeMock.mockImplementationOnce(async () => {
      throw Object.assign(new Error("此会话正在后台生成"), { status: 409 });
    });
    state.generation = { id: "g-other", status: "running", events: [] };
    state.transcript = [{ role: "user", content: "查失败节点" }, unfinished];
    await act(async () => {
      sources.at(-1).generation({ data: JSON.stringify({ id: "g-running", status: "completed", events: [] }) });
    });
    await waitFor(() => expect(generationPosts()).toHaveLength(1));
    await waitFor(() => expect(result.current.queue.map((m) => m.text)).toEqual(["不能丢"]));
    // The rejected message is not left on screen as if it had been sent.
    await waitFor(() => expect(result.current.session?.messages.map((m) => m.content)).toEqual(["查失败节点", unfinished.content]));
    unmount();
  });

  test("waits for the admitted reply when only a stored receipt confirms a queued send", async () => {
    const originalAPI = apiMock.getMockImplementation();
    const originalWrite = writeMock.getMockImplementation();
    state.sessionSettings = { titleGenerated: true };
    let admit, acceptedId, replyFinished = false;
    const holdFirstResponse = new Promise((resolve) => { admit = resolve; });
    apiMock.mockImplementation(async (path, options) => {
      if (path === "/api/assistant/sessions/s1/generation" && acceptedId)
        throw Object.assign(Error("回复状态暂不可用"), { status: 503 });
      return originalAPI(path, options);
    });
    writeMock.mockImplementation(async (path, body, method) => {
      if (path !== "/api/assistant/generations") return originalWrite(path, body, method);
      if (replyFinished) return { id: "second-reply", status: "running", events: [] };
      if (acceptedId) throw Object.assign(Error("此会话正在生成，请等待完成或停止"), { status: 409 });
      await holdFirstResponse;
      acceptedId = body.clientRequestId;
      state.transcript = body.messages;
      state.sessionSettings.recentClientRequestIds = [acceptedId];
      state.generation = { id: "accepted", status: "running", events: [], clientRequestId: acceptedId };
      throw new TypeError("连接在确认发送后断开");
    });
    try {
      const { result, unmount } = await openRunning();
      await act(async () => {
        await result.current.send({ text: "排队第一条" });
        await result.current.send({ text: "排队第二条" });
      });
      await finish("completed");
      await waitFor(() => expect(generationPosts()).toHaveLength(1));
      await act(async () => admit());
      expect(result.current.replyStatePending).toBe(true);
      expect(generationPosts()).toHaveLength(1);
      expect(result.current.queueHeld).toBe(false);
      expect(result.current.queue.map((entry) => entry.text)).toEqual(["排队第二条"]);
      await waitFor(() => expect(sources).toHaveLength(2), { timeout: 3500 });
      expect(result.current.generation?.id).toBe("accepted");
      expect(generationPosts()).toHaveLength(1);
      state.transcript = [...state.transcript, { role: "assistant", content: "回复第一条" }];
      replyFinished = true;
      await emit({ id: "accepted", status: "completed", events: [], clientRequestId: acceptedId });
      await waitFor(() => expect(generationPosts()).toHaveLength(2));
      expect(generationPosts()[1][1].messages.at(-1)?.content).toBe("排队第二条");
      expect(result.current.queueHeld).toBe(false);
      unmount();
    } finally {
      apiMock.mockImplementation(originalAPI);
      writeMock.mockImplementation(originalWrite);
    }
  });
});

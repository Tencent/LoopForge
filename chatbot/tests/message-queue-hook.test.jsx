import { act, renderHook, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";

const state = vi.hoisted(() => ({ transcript: [], generation: null }));
const { apiMock, writeMock } = vi.hoisted(() => {
  const apiMock = vi.fn(async (path) => {
    if (path === "/api/auth/current")
      return { user: { id: 1, loginName: "tester", codeBuddyApiKeyConfigured: true } };
    if (path === "/api/projects") return [{ id: "p1", name: "项目一", ownerId: 1, members: [1] }];
    if (path === "/api/assistant/models")
      return { default: "m1", sponsoredModel: "m1", models: [{ id: "m1", name: "模型一" }] };
    if (path === "/api/users") return [{ id: 1, loginName: "tester", codeBuddyApiKeyConfigured: true }];
    if (path === "/api/assistant/sessions") return [];
    if (path.includes("/generation")) return structuredClone(state.generation);
    if (path.startsWith("/api/assistant/sessions/"))
      return { id: "s1", title: "排队", projectId: "p1", model: "m1", messages: structuredClone(state.transcript) };
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
    sources[0].generation({ data: JSON.stringify(g) });
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
    state.generation = { id: "g-running", status: "running", events: [{ sequence: 1, data: { kind: "tool", tool: "runs_get" } }] };
    globalThis.EventSource = FakeEventSource;
  });
  afterEach(() => {
    delete globalThis.EventSource;
  });

  test("queues instead of rejecting, then sends after the reply is stored", async () => {
    const { result, unmount } = await openRunning();
    let ok;
    await act(async () => {
      ok = await result.current.send({ text: "应该看 CI 而不是 lint" });
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

    await emit({ id: "g-follow", status: "running", events: [] });
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
      throw new Error("生成已结束或不存在");
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
});

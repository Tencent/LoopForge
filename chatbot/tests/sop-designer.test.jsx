import React from "react";
import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";

const { read, send } = vi.hoisted(() => ({ read: vi.fn(), send: vi.fn() }));
vi.mock("../src/api.js", async (original) => ({
  ...(await original()),
  api: read,
  write: send,
}));
import SopDesigner from "../src/SopDesigner.jsx";
import {
  BEGIN, BEGUN, CONFIGURE_ROLE, CONTEXT, DRAFT, END, NEW_CONVERSATION, PLACE, READY, REPLY, RESET,
  beginOnCanvas, designerParams, draftSummary, isDesignerSurface, latestSession, postToCanvas, replyDraft, scenarioPage,
} from "../src/sop-designer.js";

const draftReply = (body, warnings = []) => ({
  role: "assistant",
  model: "cb-model",
  content: JSON.stringify({ message: "已加上评审", drafts: [{ tool: "workflows_save", body, warnings }] }),
});

describe("sop-designer helpers", () => {
  test("parse the surface URL", () => {
    expect(designerParams("?surface=sop-designer&projectId=p&workflowId=w")).toEqual({ surface: "sop-designer", projectId: "p", workflowId: "w" });
    expect(isDesignerSurface("?surface=sop-designer")).toBe(true);
    expect(isDesignerSurface("?projectId=p")).toBe(false);
  });

  test("pick the newest conversation of the SOP", () => {
    const rows = [
      { id: "old", projectId: "p1", workflowId: "w", updatedAt: "2026-11-01T00:00:00Z" },
      { id: "other-project", projectId: "p2", workflowId: "w", updatedAt: "2026-11-09T00:00:00Z" },
      { id: "other", projectId: "p1", workflowId: "x", updatedAt: "2026-11-09T00:00:00Z" },
      { id: "new", projectId: "p1", workflowId: "w", updatedAt: "2026-11-05T00:00:00Z" },
    ];
    expect(latestSession(rows, "p1", "w").id).toBe("new");
    expect(latestSession([], "p1", "w")).toBe(null);
  });

  test("read the workflows_save draft of a reply", () => {
    const body = { name: "交付", nodes: [{ id: "a" }, { id: "b" }], edges: [{ source: "a", target: "b" }] };
    expect(replyDraft(draftReply(body, ["节点 a 未绑定角色"]))).toEqual({ tool: "workflows_save", body, warnings: ["节点 a 未绑定角色"] });
    expect(replyDraft({ content: JSON.stringify({ message: "x", drafts: [{ tool: "workflows_save", body: JSON.stringify(body) }] }) }).body).toEqual(body);
    expect(replyDraft({ content: JSON.stringify({ message: "x", drafts: [{ tool: "issues_save", body }] }) })).toBe(null);
    expect(replyDraft({ content: "纯文本回答" })).toBe(null);
    expect(draftSummary({ body })).toBe("交付 · 2 个节点 · 1 条连线");
  });

  test("page through the scenarios", () => {
    expect(scenarioPage(0)).toHaveLength(4);
    expect(scenarioPage(2)).toEqual(scenarioPage(0));
  });

  test("begin a turn with the canvas, or without one when opened alone", async () => {
    expect(await beginOnCanvas(window, window)).toEqual({ ok: true, canvas: null });
    const frame = document.createElement("iframe");
    document.body.append(frame);
    const canvas = frame.contentWindow;
    canvas.postMessage = vi.fn((message) => {
      window.dispatchEvent(new MessageEvent("message", { source: canvas, origin: location.origin, data: { type: BEGUN, requestId: "stale", ok: false } }));
      window.dispatchEvent(new MessageEvent("message", { source: window, origin: location.origin, data: { type: BEGUN, requestId: message.requestId, ok: false } }));
      window.dispatchEvent(new MessageEvent("message", { source: canvas, origin: location.origin, data: { type: BEGUN, requestId: message.requestId, ok: true, canvas: { id: "w" } } }));
    });
    const begun = await beginOnCanvas(canvas, window);
    expect(canvas.postMessage.mock.calls[0][0].type).toBe(BEGIN);
    expect(begun).toMatchObject({ ok: true, canvas: { id: "w" } });
    vi.useFakeTimers();
    canvas.postMessage = vi.fn();
    const silent = beginOnCanvas(canvas, window, 50);
    vi.advanceTimersByTime(60);
    expect((await silent).ok).toBe(false);
    vi.useRealTimers();
    postToCanvas(RESET, { a: 1 }, canvas, window);
    expect(canvas.postMessage).toHaveBeenLastCalledWith({ type: RESET, a: 1 }, location.origin);
    frame.remove();
  });
});

describe("SopDesigner", () => {
  let frame, canvas, posted, generation, stored;
  const reply = (data) =>
    act(() => {
      window.dispatchEvent(new MessageEvent("message", { source: canvas, origin: location.origin, data }));
    });

  beforeEach(() => {
    localStorage.clear();
    frame = document.createElement("iframe");
    document.body.append(frame);
    canvas = frame.contentWindow;
    posted = [];
    canvas.postMessage = vi.fn((message) => {
      posted.push(message);
      if (message.type === BEGIN) {
        queueMicrotask(() => reply({ type: BEGUN, requestId: message.requestId, ok: true, projectId: "p1", workflowId: "sop-a", canvas: { id: "sop-a", nodes: [{ id: "plan" }], edges: [] } }));
      }
    });
    generation = null;
    stored = null;
    read.mockReset();
    send.mockReset();
    read.mockImplementation(async (path) => {
      if (path === "/api/assistant/models") return { default: "cb-model", models: [{ id: "cb-model", name: "CB model" }, { id: "codex:gpt", name: "GPT", engine: "codex" }], engines: [{ id: "codebuddy", name: "CodeBuddy", configured: true }, { id: "codex", name: "Codex", configured: true }] };
      if (path.startsWith("/api/assistant/sessions?")) return stored ? [{ id: stored.id, projectId: stored.projectId, workflowId: stored.workflowId, updatedAt: "2026-11-17T08:00:00Z" }] : [];
      if (path.includes("/generation")) {
        if (!generation) throw Object.assign(new Error("not found"), { status: 404 });
        return generation;
      }
      if (path.startsWith("/api/assistant/sessions/")) return stored;
      throw new Error("unexpected " + path);
    });
  });

  afterEach(() => {
    frame.remove();
    vi.useRealTimers();
  });

  test("a turn asks the canvas for its graph, streams the draft to it and hands over the reply", async () => {
    send.mockImplementation(async (path, body) => {
      expect(path).toBe("/api/assistant/generations");
      stored = { id: body.sessionId, workflowId: "sop-a", messages: [body.messages[0]] };
      generation = {
        id: "g1", status: "running", startedAt: "2026-11-17T08:00:00Z",
        events: [
          { sequence: 1, data: { kind: "draftStart" } },
          { sequence: 2, data: { kind: "draftDelta", detail: '{"message":"加评审","drafts":[{"tool":"workflows_save","body":{"operations":[' } },
        ],
      };
      return generation;
    });
    render(<SopDesigner search="?surface=sop-designer&projectId=p1&workflowId=sop-a" canvas={canvas} />);
    expect(posted[0].type).toBe(READY);
    await screen.findByText("需求交付 · 澄清到放行");
    fireEvent.click(screen.getByText("需求交付 · 澄清到放行"));
    expect(screen.getByLabelText("描述要编排的流程").value).toMatch(/需求澄清/);
    fireEvent.change(screen.getByLabelText("描述要编排的流程"), { target: { value: "在实现后加人工评审" } });
    fireEvent.click(screen.getByLabelText("发送编排要求"));

    await waitFor(() => expect(send).toHaveBeenCalled());
    const request = send.mock.calls[0][1];
    expect(request).toMatchObject({
      surface: "sop-designer", projectId: "p1", workflowId: "sop-a", approvalMode: "confirm", model: "cb-model",
      canvas: { id: "sop-a", nodes: [{ id: "plan" }], edges: [] },
    });
    expect(request.messages).toEqual([expect.objectContaining({ role: "user", content: "在实现后加人工评审" })]);
    expect(await screen.findByText("在实现后加人工评审")).toBeTruthy();
    await waitFor(() => expect(posted.some((m) => m.type === DRAFT && m.buffer.includes("operations"))).toBe(true));
    expect(screen.getByLabelText("停止生成")).toBeTruthy();

    const body = { id: "sop-a", name: "交付", nodes: [{ id: "plan" }, { id: "review" }], edges: [{ source: "plan", target: "review" }] };
    stored = { ...stored, messages: [...stored.messages, draftReply(body, ["节点 review 未绑定角色"])] };
    generation = { ...generation, status: "completed", events: [] };
    await waitFor(() => expect(posted.some((m) => m.type === REPLY)).toBe(true), { timeout: 3000 });
    const handed = posted.find((m) => m.type === REPLY);
    expect(handed).toMatchObject({ generationId: "g1", message: "已加上评审", draft: { body, warnings: ["节点 review 未绑定角色"] } });
    expect(await screen.findByText("交付 · 2 个节点 · 1 条连线")).toBeTruthy();
    expect(screen.getByText("节点 review 未绑定角色")).toBeTruthy();
    fireEvent.click(screen.getByText("重新放到画布"));
    expect(posted.at(-1)).toEqual({ type: PLACE, draft: body });
  });

  test("reopening the panel shows the conversation without replaying its draft", async () => {
    const body = { id: "sop-a", name: "交付", nodes: [], edges: [] };
    stored = { id: "chat-old", projectId: "p1", workflowId: "sop-a", messages: [{ role: "user", content: "搭一条流程" }, draftReply(body)] };
    generation = { id: "g-old", status: "completed", events: [] };
    render(<SopDesigner search="?surface=sop-designer&projectId=p1&workflowId=sop-a" canvas={canvas} />);
    expect(await screen.findByText("搭一条流程")).toBeTruthy();
    await new Promise((resolve) => setTimeout(resolve, 50));
    expect(posted.some((m) => m.type === REPLY)).toBe(false);
    expect(read).toHaveBeenCalledWith("/api/assistant/sessions?surface=sop-designer&workflowId=sop-a&projectId=p1");

    reply({ type: CONTEXT, projectId: "p2", workflowId: "sop-a" });
    await waitFor(() => expect(read).toHaveBeenCalledWith("/api/assistant/sessions?surface=sop-designer&workflowId=sop-a&projectId=p2"));
    expect(screen.queryByText("搭一条流程")).toBeNull();
    reply({ type: NEW_CONVERSATION });
    expect(await screen.findByText("需求交付 · 澄清到放行")).toBeTruthy();
    expect(posted.at(-1).type).toBe(RESET);

    stored = null;
    reply({ type: CONTEXT, projectId: "p1", workflowId: "sop-b" });
    await waitFor(() => expect(read).toHaveBeenCalledWith("/api/assistant/sessions?surface=sop-designer&workflowId=sop-b&projectId=p1"));
  });

  test("opens the role configuration from a saved assistant draft", async () => {
    const role = { id: "reviewer", name: "改动评审", description: "检查本次改动", prompt: "总结评审结果" };
    stored = {
      id: "chat-role", projectId: "p1", workflowId: "sop-a",
      messages: [{ role: "assistant", content: JSON.stringify({
        message: "已准备角色草稿", drafts: [{ tool: "agents_save", body: role }],
      }) }],
    };
    generation = { id: "g-role", status: "completed", events: [] };
    render(<SopDesigner search="?surface=sop-designer&projectId=p1&workflowId=sop-a" canvas={canvas} />);
    fireEvent.click(await screen.findByRole("button", { name: "配置并创建角色" }));
    expect(posted.at(-1)).toEqual({ type: CONFIGURE_ROLE, projectId: "p1", workflowId: "sop-a", role });
    expect(send).not.toHaveBeenCalled();
  });

  test("a deleted conversation starts fresh and can accept another turn", async () => {
    stored = { id: "chat-deleted", projectId: "p1", workflowId: "sop-a", messages: [{ role: "user", content: "上一轮" }] };
    send.mockImplementation(async (_path, body) => {
      const messages = [...(stored?.messages || []), ...body.messages, { role: "assistant", content: "已收到" }];
      stored = { id: body.sessionId, projectId: "p1", workflowId: "sop-a", messages };
      generation = { id: "g-" + send.mock.calls.length, status: "completed", events: [] };
      return generation;
    });
    render(<SopDesigner search="?surface=sop-designer&projectId=p1&workflowId=sop-a" canvas={canvas} />);
    await screen.findByText("上一轮");
    const readSession = read.getMockImplementation();
    read.mockImplementation((path) => {
      if (path === "/api/assistant/sessions/chat-deleted") {
        return Promise.reject(Object.assign(new Error("对话不存在"), { status: 404 }));
      }
      return readSession(path);
    });
    stored = null;

    fireEvent.change(screen.getByLabelText("描述要编排的流程"), { target: { value: "恢复后的第一轮" } });
    fireEvent.click(screen.getByLabelText("发送编排要求"));
    await screen.findByText("已收到");
    const newSession = send.mock.calls[0][1].sessionId;
    expect(newSession).not.toBe("chat-deleted");
    expect(posted.some((m) => m.type === RESET)).toBe(true);
    expect(screen.queryByText("上一轮")).toBeNull();

    fireEvent.change(screen.getByLabelText("描述要编排的流程"), { target: { value: "恢复后的第二轮" } });
    fireEvent.click(screen.getByLabelText("发送编排要求"));
    await waitFor(() => expect(send).toHaveBeenCalledTimes(2));
    expect(send.mock.calls[1][1].sessionId).toBe(newSession);
    expect(screen.queryByRole("alert")).toBeNull();
  });

  test("a delayed generation response stays with the project that sent it", async () => {
    let finish;
    send.mockImplementation(() => new Promise((resolve) => { finish = resolve; }));
    render(<SopDesigner search="?surface=sop-designer&projectId=p1&workflowId=sop-a" canvas={canvas} />);
    fireEvent.change(await screen.findByLabelText("描述要编排的流程"), { target: { value: "只给项目一的消息" } });
    fireEvent.click(screen.getByLabelText("发送编排要求"));
    await waitFor(() => expect(send).toHaveBeenCalled());
    reply({ type: CONTEXT, projectId: "p2", workflowId: "sop-a" });
    await waitFor(() => expect(read).toHaveBeenCalledWith("/api/assistant/sessions?surface=sop-designer&workflowId=sop-a&projectId=p2"));
    const oldId = send.mock.calls[0][1].sessionId;
    await act(async () => finish({ id: "previous-generation", sessionId: oldId, status: "running" }));
    expect(read.mock.calls.some(([url]) => url.includes("/sessions/" + oldId))).toBe(false);
  });

  test("a refused or failed turn is reported and the canvas is told to restore", async () => {
    canvas.postMessage = vi.fn((message) => {
      posted.push(message);
      if (message.type === BEGIN) queueMicrotask(() => reply({ type: BEGUN, requestId: message.requestId, ok: false, error: "画布还在生成上一轮的流程，请稍候" }));
    });
    render(<SopDesigner search="?surface=sop-designer&projectId=p1&workflowId=sop-a" canvas={canvas} />);
    fireEvent.change(await screen.findByLabelText("描述要编排的流程"), { target: { value: "改一下" } });
    fireEvent.click(screen.getByLabelText("发送编排要求"));
    expect(await screen.findByRole("alert")).toHaveProperty("textContent", "画布还在生成上一轮的流程，请稍候");
    expect(send).not.toHaveBeenCalled();
    expect(posted.at(-1)).toEqual({ type: END, error: "画布还在生成上一轮的流程，请稍候" });
  });

  test("without a project the turn does not start", async () => {
    render(<SopDesigner search="?surface=sop-designer&workflowId=sop-a" canvas={canvas} />);
    fireEvent.change(await screen.findByLabelText("描述要编排的流程"), { target: { value: "搭一条" } });
    fireEvent.click(screen.getByLabelText("发送编排要求"));
    expect((await screen.findByRole("alert")).textContent).toBe("先在顶栏选择项目");
    expect(posted.some((m) => m.type === BEGIN)).toBe(false);
  });

  test("another CLI starts a new conversation and keeps the choice", async () => {
    const body = { id: "sop-a", name: "交付", nodes: [], edges: [] };
    stored = { id: "chat-old", projectId: "p1", workflowId: "sop-a", messages: [{ role: "user", content: "搭一条流程" }, draftReply(body)] };
    send.mockImplementation(async (path, request) => ({ id: "g2", status: "running", events: [], sessionId: request.sessionId }));
    render(<SopDesigner search="?surface=sop-designer&projectId=p1&workflowId=sop-a" canvas={canvas} />);
    await screen.findByText("搭一条流程");
    expect(localStorage.getItem("agentflow.orchestratorModel")).toBe(null);
    // Pick Codex from the CLI picker.
    fireEvent.click(await screen.findByRole("button", { name: "CLI" }));
    fireEvent.click(await screen.findByRole("menuitemradio", { name: "Codex" }));
    await waitFor(() => expect(localStorage.getItem("agentflow.orchestratorModel")).toBe("codex:gpt"));
    expect(localStorage.getItem("agentflow.orchestratorModel.codex")).toBe("codex:gpt");
    fireEvent.change(screen.getByLabelText("描述要编排的流程"), { target: { value: "再加一步" } });
    fireEvent.click(screen.getByLabelText("发送编排要求"));
    await waitFor(() => expect(send).toHaveBeenCalled());
    const request = send.mock.calls[0][1];
    expect(request.model).toBe("codex:gpt");
    expect(request.sessionId).not.toBe("chat-old");
  });
});

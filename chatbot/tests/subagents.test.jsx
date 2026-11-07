import React from "react";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, test, vi } from "vitest";
import { mergeActivity, collectSubagents, activityFace, resumeMissed } from "../src/activity.js";
import ArtifactPanel from "../src/ArtifactPanel.jsx";

const events = (...items) => items.map((data, sequence) => ({ data, sequence }));
const at = "2026-01-01T00:00:00Z";

describe("dispatched subagents", () => {
  test("a subagent's steps and report nest under its dispatch", () => {
    const steps = mergeActivity(
      [],
      events(
        { kind: "tool", id: "call-1", tool: "Agent", detail: "补单测", subagent: "role-qa", role: "测试工程师", at },
        { kind: "thinking", text: "先看改动", parent: "call-1", at },
        { kind: "tool", id: "read-1", tool: "Read", detail: "a.go", parent: "call-1", at },
        { kind: "agentText", text: "已补 3 个用例", full: true, parent: "call-1", at },
        { kind: "toolDone", id: "call-1", agentId: "agent-aaa", at },
        { kind: "answer", text: "测试工程师已完成", at },
      ),
    );
    expect(steps.map((step) => step.kind)).toEqual(["tool", "text"]);
    const dispatch = steps[0];
    expect(dispatch.agentId).toBe("agent-aaa");
    expect(dispatch.children.map((step) => step.kind)).toEqual(["thinking", "tool", "text"]);
    expect(dispatch.children[2].text).toBe("已补 3 个用例");
    expect(activityFace(dispatch).done).toBe("「测试工程师」已完成");
  });

  test("a resumed agent keeps one entry across turns", () => {
    const turn = (id, detail, agentId) => ({
      role: "assistant",
      activity: [
        { kind: "tool", id, tool: "Agent", detail, subagent: "role-qa", role: "测试工程师", at },
        { kind: "agentText", text: detail + " 完成", parent: id, full: true, at },
        { kind: "toolDone", id, agentId, at },
      ],
    });
    const agents = collectSubagents([turn("c1", "补单测", "agent-aaa"), { role: "user" }, turn("c2", "再补边界", "agent-aaa"), turn("c3", "写文档", "agent-bbb")]);
    expect(agents.map((agent) => [agent.agentId, agent.dispatches.length])).toEqual([
      ["agent-aaa", 2],
      ["agent-bbb", 1],
    ]);
  });

  test("the artifacts list has a 子 Agent section that opens like an SOP node", async () => {
    const messages = [
      { role: "user", content: "补单测" },
      {
        role: "assistant",
        activity: [
          { kind: "tool", id: "c1", tool: "Agent", detail: "补单测", input: "给 a.go 补单测", role: "测试工程师", at },
          { kind: "tool", id: "p1", tool: "发布产物", detail: "测试报告", parent: "c1", at },
          { kind: "toolDone", id: "p1", parent: "c1", at },
          { kind: "agentText", text: "用例都通过了", parent: "c1", full: true, at },
          { kind: "toolDone", id: "c1", agentId: "agent-aaa", output: "用例都通过了", at },
        ],
      },
    ];
    const report = { id: "a1", name: "测试报告", kind: "attachment", source: "AI 生成" };
    const chat = { session: { id: "s1", projectId: "p1", messages }, projectId: "p1", projects: [] };
    let selected = null;
    const setSelected = vi.fn((value) => (selected = value));
    const panel = () => (
      <div className="chat-shell">
        <ArtifactPanel artifacts={[report]} runs={[]} selected={selected} setSelected={setSelected} chat={chat} onClose={vi.fn()} open />
      </div>
    );
    const view = render(panel());
    // No tab of its own: subagents are a section of the artifacts list.
    expect(screen.queryByRole("tab", { name: /子 Agent/ })).toBeNull();
    expect(screen.getByRole("heading", { name: "子 Agent" })).toBeTruthy();
    await userEvent.click(screen.getByRole("button", { name: /测试工程师/ }));
    view.rerender(panel());
    // Like an SOP node: the task it was given, then its report, once.
    expect(screen.getByRole("tab", { name: /会话过程/ })).toBeTruthy();
    expect(screen.getByLabelText("收到的任务").textContent).toContain("给 a.go 补单测");
    expect(screen.getAllByText("用例都通过了")).toHaveLength(1);
    await userEvent.click(screen.getByRole("tab", { name: /产物 1/ }));
    await userEvent.click(screen.getByRole("button", { name: /测试报告/ }));
    expect(setSelected).toHaveBeenLastCalledWith(report);
  });
});

describe("role deliverables", () => {
  test("collected files show in the 产物 tab and a missing required one is marked", async () => {
    const messages = [
      { role: "user", content: "归档" },
      {
        role: "assistant",
        activity: [
          { kind: "tool", id: "c1", tool: "Agent", detail: "归档", input: "归档本次改动", role: "前端归档", at },
          { kind: "agentText", text: "已写 archive.md", parent: "c1", full: true, at },
          { kind: "toolDone", id: "c1", agentId: "agent-aaa", at },
          { kind: "tool", id: "d0", tool: "交付物", detail: "archive.md", text: "已收集交付物 · archive.md", parent: "c1", at },
          { kind: "toolDone", id: "d0", parent: "c1", at },
          { kind: "tool", id: "d1", tool: "交付物", detail: "review.md", text: "缺少必需交付物 · review.md", parent: "c1", at },
          { kind: "toolDone", id: "d1", parent: "c1", failed: true, at },
        ],
      },
    ];
    const archive = { id: "a1", name: "archive.md", kind: "attachment", source: "AI 生成", messageIndex: 1 };
    const chat = { session: { id: "s1", projectId: "p1", messages }, projectId: "p1", projects: [] };
    let selected = null;
    const setSelected = vi.fn((value) => (selected = value));
    const panel = () => (
      <div className="chat-shell">
        <ArtifactPanel artifacts={[archive]} runs={[]} selected={selected} setSelected={setSelected} chat={chat} onClose={vi.fn()} open />
      </div>
    );
    const view = render(panel());
    await userEvent.click(screen.getByRole("button", { name: /前端归档/ }));
    view.rerender(panel());
    const tabs = screen.getAllByRole("tab", { name: /产物 2/ });
    await userEvent.click(tabs[tabs.length - 1]);
    expect(screen.getByText("缺少必需交付物")).toBeTruthy();
    await userEvent.click(screen.getByRole("button", { name: /archive\.md/ }));
    expect(setSelected).toHaveBeenLastCalledWith(archive);
  });
});

describe("a resumed role's deliverable", () => {
  const dispatch = (id, extra = {}) => [
    { kind: "tool", id, tool: "Agent", detail: "归档", role: "前端归档", subagent: "role-fe-archive", ...extra, at },
    { kind: "toolDone", id, agentId: "agent-aaa", at },
    { kind: "tool", id: `d-${id}`, tool: "交付物", detail: "archive.md", parent: id, at },
    { kind: "toolDone", id: `d-${id}`, parent: id, at },
  ];
  const first = { id: "a1", name: "archive.md", kind: "attachment", messageIndex: 1 };
  const saved = [
    { role: "user", content: "归档" },
    { role: "assistant", activity: dispatch("c1") },
  ];
  const render2 = (chat, artifacts) => {
    let selected = { kind: "subagent", agentKey: "agent-aaa" };
    const setSelected = vi.fn((value) => (selected = value));
    render(
      <div className="chat-shell">
        <ArtifactPanel artifacts={artifacts} runs={[]} selected={selected} setSelected={setSelected} chat={chat} onClose={vi.fn()} open />
      </div>,
    );
    return setSelected;
  };

  test("waits for its own reply instead of opening the older file", async () => {
    const chat = {
      busy: true,
      live: { activity: mergeActivity([], events(...dispatch("c2", { agentId: "agent-aaa" }))) },
      session: { id: "s1", projectId: "p1", messages: [...saved, { role: "user", content: "补一行" }] },
      projectId: "p1",
      projects: [],
    };
    render2(chat, [first]);
    const tabs = screen.getAllByRole("tab", { name: /产物 1/ });
    await userEvent.click(tabs[tabs.length - 1]);
    // One row for the file, the latest dispatch's, not openable yet.
    expect(screen.getAllByText("archive.md")).toHaveLength(1);
    expect(screen.getByText("交付物 · 回复完成后可打开")).toBeTruthy();
    expect(screen.queryByRole("button", { name: /archive\.md/ })).toBeNull();
  });

  test("opens the attachment of the reply that holds the dispatch", async () => {
    const second = { id: "a2", name: "archive.md", kind: "attachment", messageIndex: 3 };
    const chat = {
      session: {
        id: "s1",
        projectId: "p1",
        messages: [...saved, { role: "user", content: "补一行" }, { role: "assistant", activity: dispatch("c2", { agentId: "agent-aaa" }) }],
      },
      projectId: "p1",
      projects: [],
    };
    const setSelected = render2(chat, [first, second]);
    const tabs = screen.getAllByRole("tab", { name: /产物 1/ });
    await userEvent.click(tabs[tabs.length - 1]);
    await userEvent.click(screen.getByRole("button", { name: /archive\.md/ }));
    expect(setSelected).toHaveBeenLastCalledWith(second);
  });
});

describe("a resume that did not take", () => {
  const start = { kind: "tool", id: "c2", tool: "Agent", detail: "补一行", input: "再补一行", subagent: "role-fe", role: "前端", agentId: "agent-aaa", at };
  const saved = [
    { role: "user", content: "x" },
    {
      role: "assistant",
      activity: [
        { kind: "tool", id: "c1", tool: "Agent", detail: "归档", subagent: "role-fe", role: "前端", at },
        { kind: "toolDone", id: "c1", agentId: "agent-aaa", at },
      ],
    },
    { role: "user", content: "再补一行" },
  ];

  test("is flagged when the CLI finishes under another agent", () => {
    const [step] = mergeActivity([], events(start, { kind: "toolDone", id: "c2", agentId: "agent-new", at }));
    expect(step.agentId).toBe("agent-new");
    expect(resumeMissed(step)).toBe(true);
    const [kept] = mergeActivity([], events(start, { kind: "toolDone", id: "c2", agentId: "agent-aaa", at }));
    expect(resumeMissed(kept)).toBe(false);
  });

  test("the open panel follows the dispatch to its new agent", async () => {
    let selected = { kind: "subagent", agentKey: "agent-aaa", dispatchId: "c1" };
    const setSelected = vi.fn((value) => (selected = value));
    const chat = (activity) => ({
      busy: true,
      live: { activity: mergeActivity([], events(...activity)) },
      session: { id: "s1", projectId: "p1", messages: saved },
      projectId: "p1",
      projects: [],
    });
    const panel = (c) => (
      <div className="chat-shell">
        <ArtifactPanel artifacts={[]} runs={[]} selected={selected} setSelected={setSelected} chat={c} onClose={vi.fn()} open />
      </div>
    );
    const view = render(panel(chat([start])));
    expect(screen.getAllByLabelText("收到的任务").map((el) => el.textContent).join("|")).toContain("再补一行");
    view.rerender(panel(chat([start, { kind: "toolDone", id: "c2", agentId: "agent-new", output: "补好了", at }])));
    expect(setSelected).toHaveBeenLastCalledWith(expect.objectContaining({ agentKey: "agent-new", dispatchId: "c2" }));
    view.rerender(panel(chat([start, { kind: "toolDone", id: "c2", agentId: "agent-new", output: "补好了", at }])));
    expect(screen.getByRole("note").textContent).toContain("复用未生效");
    expect(screen.getByLabelText("收到的任务").textContent).toContain("再补一行");
  });
});

describe("a resumed dispatch while it runs", () => {
  test("joins its agent's row at once", () => {
    const saved = [
      {
        role: "assistant",
        activity: [
          { kind: "tool", id: "c1", tool: "Agent", detail: "梳理入口", subagent: "role-fe", role: "前端", at },
          { kind: "toolDone", id: "c1", agentId: "agent-aaa", at },
        ],
      },
    ];
    const live = mergeActivity(
      [],
      events({ kind: "tool", id: "c2", tool: "Agent", detail: "补充样式", subagent: "role-fe", role: "前端", agentId: "agent-aaa", at }),
    );
    const agents = collectSubagents(saved, live);
    expect(agents).toHaveLength(1);
    expect(agents[0].dispatches.map((step) => step.detail)).toEqual(["梳理入口", "补充样式"]);
    expect(agents[0].dispatches[1].finishedAt).toBeUndefined();
  });
});

describe("an opened new subagent", () => {
  test("stays open when it finishes and gets its agent ID", async () => {
    const start = { kind: "tool", id: "c9", tool: "Agent", detail: "整理入口", input: "看 chatbot 入口", subagent: "role-fe", role: "前端", at };
    let selected = null;
    const setSelected = vi.fn((value) => (selected = value));
    const panel = (chat) => (
      <div className="chat-shell">
        <ArtifactPanel artifacts={[]} runs={[]} selected={selected} setSelected={setSelected} chat={chat} onClose={vi.fn()} open />
      </div>
    );
    const running = {
      busy: true,
      live: { activity: mergeActivity([], events(start)) },
      session: { id: "s1", projectId: "p1", messages: [{ role: "user", content: "x" }] },
      projectId: "p1",
      projects: [],
    };
    const view = render(panel(running));
    await userEvent.click(screen.getByRole("button", { name: /前端/ }));
    view.rerender(panel(running));
    expect(screen.getByLabelText("收到的任务").textContent).toContain("看 chatbot 入口");
    const finished = {
      ...running,
      busy: false,
      live: { activity: [] },
      session: {
        ...running.session,
        messages: [
          { role: "user", content: "x" },
          { role: "assistant", activity: [start, { kind: "toolDone", id: "c9", agentId: "agent-new", output: "入口在 main.jsx", at }] },
        ],
      },
    };
    view.rerender(panel(finished));
    expect(screen.queryByText("正在加载…")).toBeNull();
    expect(screen.getByText("入口在 main.jsx")).toBeTruthy();
  });
});

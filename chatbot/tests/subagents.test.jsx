import React from "react";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, test, vi } from "vitest";
import { mergeActivity, collectSubagents, activityFace } from "../src/activity.js";
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

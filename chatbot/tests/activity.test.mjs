import { test } from "vitest";
import assert from "node:assert/strict";
import { mergeEvents } from "../src/api.js";
import {
  mergeActivity,
  savedActivity,
  activityFace,
  activityForReply,
  activityTarget,
  thoughtLine,
} from "../src/activity.js";
const event = (sequence, data) => ({ sequence, data });
test("ordered thoughts, tool updates and completion survive incremental batches without mutating prior state", () => {
  const first = mergeEvents({}, [
    event(1, { kind: "thinkingStart", at: "2026-10-27T10:00:00Z" }),
    event(2, { kind: "thinking", text: "检查项目" }),
    event(3, { kind: "tool", id: "one", tool: "Bash" }),
  ]);
  const second = mergeEvents(first, [
    event(4, {
      kind: "tool",
      id: "one",
      tool: "Bash",
      detail: "go test ./...",
    }),
    event(5, {
      kind: "toolDone",
      id: "one",
      failed: true,
      at: "2026-10-27T10:00:04Z",
    }),
    event(6, { kind: "thinkingStart" }),
    event(7, { kind: "thinking", text: "修复失败原因" }),
  ]);
  assert.deepEqual(
    second.activity.map((s) => s.kind),
    ["thinking", "tool", "thinking"],
  );
  assert.equal(second.activity[1].detail, "go test ./...");
  assert.equal(second.activity[1].failed, true);
  assert.equal(first.activity[1].detail, "");
  assert(!first.activity[1].finishedAt);
});
test("activity summaries replace internal tool names with readable subjects while retaining raw detail", () => {
  const tool = {
    kind: "tool",
    tool: "mcp__agentflow_session__agentflow__workflows_list",
    detail: "mcp__agentflow_session__agentflow__workflows_list",
  };
  assert.equal(activityFace(tool).kind, "workflow");
  assert.match(activityFace(tool).done, /SOP/);
  assert.equal(activityTarget(tool), "");
  const schema = {
    kind: "tool",
    tool: "workbench_tool_schema",
    detail: "issues_save",
  };
  assert.equal(activityFace(schema).done, "已查看工具说明");
  assert.equal(activityTarget(schema), "");
  const open = {
    kind: "tool",
    tool: "mcp__agentflow_session__workbench_workspace_open",
    detail: '{"goal":"修复登录按钮文案"}',
  };
  assert.equal(activityFace(open).done, "已打开工作区");
  assert.equal(activityTarget(open), "修复登录按钮文案");
  assert.equal(
    activityFace({ kind: "tool", tool: "workbench_workspace_submit_mr" }).done,
    "已提交 MR",
  );
  const read = {
    kind: "tool",
    tool: "Read",
    detail: '{"file_path":"/workspace/chatbot/src/App.jsx"}',
  };
  assert.equal(activityTarget(read), "App.jsx");
  assert.equal(read.detail, '{"file_path":"/workspace/chatbot/src/App.jsx"}');
  assert.equal(
    activityTarget({
      kind: "tool",
      tool: "Bash",
      detail: '{"command":"npm test"}',
    }),
    "npm test",
  );
});
test("saved activity replays distinct thinking blocks and preserves typed tool icons", () => {
  const message = {
    activity: [
      { kind: "thinking", id: "thought-1", text: "先读取" },
      { kind: "thinking", id: "thought-2", text: "再分析" },
      { kind: "tool", id: "read", tool: "Read", detail: "api.js" },
      { kind: "toolDone", id: "read" },
      { kind: "tool", id: "find", tool: "Grep", detail: "symbol" },
    ],
  };
  const steps = savedActivity(JSON.parse(JSON.stringify(message)));
  assert.deepEqual(steps.map((s) => s.text).slice(0, 2), ["先读取", "再分析"]);
  assert(steps[2].finishedAt);
  assert.equal(activityFace(steps[2]).kind, "read");
  assert.equal(activityFace(steps[3]).kind, "search");
  assert.equal(activityFace({ tool: "Edit" }).kind, "edit");
  assert.equal(
    activityFace({ tool: "workbench_workspace_command" }).kind,
    "command",
  );
});
test("last generation fallback cannot attach one reply execution to a different historical answer", () => {
  const message = {
    startedAt: "2026-10-27T10:00:00Z",
    content: '{"message":"ok"}',
  };
  const generation = {
    status: "completed",
    startedAt: "2026-10-27T10:00:00Z",
    reply: { message: "ok" },
  };
  const steps = [{ id: "read", kind: "tool" }];
  assert.deepEqual(activityForReply(message, generation, steps), steps);
  assert.deepEqual(
    activityForReply(
      { ...message, content: '{"message":"other"}' },
      generation,
      steps,
    ),
    [],
  );
  assert.deepEqual(
    activityForReply(
      { ...message, startedAt: "2026-10-26T10:00:00Z" },
      generation,
      steps,
    ),
    [],
  );
});
test("successful hidden terminal completions stay hidden and failed commands retain their state", () => {
  assert.deepEqual(
    mergeActivity([], [event(1, { kind: "toolDone", id: "hidden" })]),
    [],
  );
  const steps = mergeActivity(
    [],
    [
      event(1, { kind: "tool", id: "cmd", tool: "Bash", detail: "make test" }),
      event(2, { kind: "toolDone", id: "cmd", failed: true }),
    ],
  );
  assert.equal(steps.length, 1);
  assert.equal(steps[0].failed, true);
});

test("thinking rows show a one-line headline like Codex", () => {
  assert.equal(thoughtLine("**Inspecting SOP**\n细节", true), "Inspecting SOP");
  assert.equal(thoughtLine("先读取 SOP 列表\n再确认节点", false), "先读取 SOP 列表");
  assert.equal(thoughtLine("先读取 SOP 列表\n再确认节点", true), "再确认节点");
  assert.equal(
    activityTarget({ kind: "thinking", text: "用户只是问候。\n直接回复。", finishedAt: "x" }),
    "用户只是问候。",
  );
  assert.equal(activityTarget({ kind: "thinking", text: "" }), "");
});

test("SOP node events fold into one Codex-style trace per turn", async () => {
  const { nodeConversationItems, groupNodeActivity } = await import(
    "../src/node-conversation.js"
  );
  const events = [
    { sequence: 1, category: "tool", message: "Read /workspace/a/SKILL.md", timestamp: "2026-10-29T11:43:20Z", tool: { name: "Read", operation: "tool.use", callId: "r" } },
    { sequence: 2, category: "tool", message: "Read 完成", timestamp: "2026-10-29T11:43:21Z", tool: { name: "Read", operation: "tool.result", callId: "r", success: true } },
    { sequence: 3, category: "tool", message: "Bash pwd && ls -la", timestamp: "2026-10-29T11:43:22Z", tool: { name: "Bash", operation: "tool.use", callId: "b" } },
    { sequence: 4, category: "tool", message: "Bash exit 1", timestamp: "2026-10-29T11:43:23Z", level: "error", tool: { name: "Bash", operation: "tool.result", callId: "b", success: false } },
    { sequence: 5, category: "reasoning", message: "Let me look at the repo.\nThen test.", timestamp: "2026-10-29T11:43:25Z" },
    { sequence: 6, category: "output", message: "I'll start.", timestamp: "2026-10-29T11:43:30Z" },
    { sequence: 7, category: "tool", message: "TodoWrite {}", timestamp: "2026-10-29T11:43:31Z", tool: { name: "TodoWrite", operation: "tool.use", callId: "t" } },
  ];
  const view = groupNodeActivity(
    nodeConversationItems(events, [], { nodeId: "n" }),
    { live: true },
  );
  assert.deepEqual(view.map((item) => item.type), ["trace", "reply", "trace"]);
  const [first, , last] = view;
  assert.equal(first.live, false);
  assert.equal(first.finishedAt, "2026-10-29T11:43:30Z");
  assert.deepEqual(
    first.steps.map((s) => `${activityFace(s).done} ${activityTarget(s)}`),
    ["已读取 SKILL.md", "已运行 pwd && ls -la", "已思考 Let me look at the repo."],
  );
  assert.equal(first.steps[0].output, "");
  assert.equal(first.steps[1].failed, true);
  assert.equal(first.steps[1].output, "Bash exit 1");
  assert.equal(last.live, true);
  assert.equal(last.steps[0].finishedAt, null);
  assert.equal(activityFace(last.steps[0]).busy, "正在更新计划");
});

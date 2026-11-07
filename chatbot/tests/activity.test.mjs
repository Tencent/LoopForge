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
  hasTextSegments,
  replyBlocks,
  trailingReply,
  finalReply,
  traceBlocks,
  activityGroupSummary,
  activityGroupKind,
  streamingAnswerOpen,
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

test("paragraphs stay between the tool calls that separate them", () => {
  const steps = mergeActivity(
    [],
    [
      event(1, { kind: "answer", text: "第 1 段" }),
      event(2, { kind: "tool", id: "a", tool: "issues_list" }),
      event(3, { kind: "toolDone", id: "a" }),
      event(4, { kind: "answer", text: "第 2 段" }),
    ],
  );
  assert.deepEqual(
    steps.map((step) => step.kind),
    ["text", "tool", "text"],
  );
  assert.equal(hasTextSegments(steps), true);
  assert.deepEqual(
    replyBlocks(steps).map((block) => block.type),
    ["text", "activity", "text"],
  );
  assert.equal(replyBlocks(steps)[0].text, "第 1 段");
  assert.equal(replyBlocks(steps)[2].text, "第 2 段");
  const replayed = mergeActivity(steps, [
    event(7, { kind: "answer", text: "第 1 段", full: true }),
  ]);
  assert.equal(replayed.filter((step) => step.kind === "text").length, 2);
  assert.equal(trailingReply("第 1 段\n\n第 2 段", steps), "");
  assert.equal(trailingReply("第 1 段\n\n第 2 段\n\n补充", steps), "补充");
  assert.equal(finalReply("第 1 段\n\n第 2 段\n\n补充", steps), "第 2 段\n\n补充");
  assert.equal(finalReply("第 1 段第 2 段", steps), "第 2 段");
  assert.equal(finalReply("第 2 段\n\n第 2 段", steps), "第 2 段");
  const repeated = [{ kind: "text", id: "a", text: "收到，这是一个新测试对话。" }];
  assert.equal(
    finalReply("收到，这是一个新测试对话。\n\n收到，这是一个新测试对话。", repeated),
    "收到，这是一个新测试对话。",
  );
  assert.equal(
    finalReply("## 合入结果\n| 项 | 值 |\n|---|---|", [
      { kind: "text", id: "a", text: "##合入结果|项 |值 |\n|---|---|" },
    ]),
    "## 合入结果\n| 项 | 值 |\n|---|---|",
  );
  assert.equal(
    finalReply("第一段。\n\n## 合入结果\n| 项 |", [
      { kind: "text", id: "a", text: "第一段。" },
      { kind: "tool", id: "t", tool: "Bash" },
      { kind: "text", id: "b", text: "##合入结果|项 |" },
    ]),
    "## 合入结果\n| 项 |",
  );
  assert.deepEqual(
    traceBlocks([
      { kind: "text", id: "a", text: "收到，这是一个新测试对话。" },
      { kind: "tool", id: "t", tool: "Bash" },
      { kind: "text", id: "b", text: "收到，这是一个新测试对话。" },
    ]).map((block) => block.type),
    ["activity"],
  );
  assert.deepEqual(
    traceBlocks(steps).map((block) => block.type),
    ["text", "activity"],
  );
  assert.equal(traceBlocks(steps)[0].text, "第 1 段");
  assert.equal(traceBlocks(steps)[1].steps[0].kind, "tool");
  const reset = mergeActivity(steps, [
    event(5, { kind: "answerReset" }),
    event(6, { kind: "answer", text: "重写" }),
  ]);
  assert.deepEqual(
    reset.filter((step) => step.kind === "text").map((step) => step.text),
    ["重写"],
  );
  assert.equal(reset.filter((step) => step.kind === "tool").length, 1);
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

test("tool rounds fold into one summary and only the trailing round stays active", () => {
  const steps = [
    { id: "t1", kind: "text", text: "先查前端代码" },
    { id: "th", kind: "thinking", text: "看看组件", finishedAt: "done" },
    { id: "a", kind: "tool", tool: "Bash", detail: "grep -rn x", finishedAt: "done" },
    { id: "b", kind: "tool", tool: "Bash", detail: "sed -n 1,9p a.js", finishedAt: "done" },
    { id: "c", kind: "tool", tool: "Read", detail: "src/a.js", finishedAt: "done" },
  ];
  const running = traceBlocks(steps, { live: true });
  assert.deepEqual(running.map((block) => block.type), ["text", "activity"]);
  assert.equal(streamingAnswerOpen(steps), false);
  assert.equal(running[1].active, true);
  assert.equal(running[1].steps.length, 4);
  assert.equal(activityGroupSummary(running[1].steps), "已运行命令、读取文件");
  assert.equal(
    activityGroupSummary(running[1].steps, { active: true }),
    "正在运行命令、读取文件",
  );
  assert.equal(activityGroupKind(running[1].steps), "command");
  // A new paragraph after the round closes it.
  const moved = traceBlocks([...steps, { id: "t2", kind: "text", text: "找到了" }], {
    live: true,
  });
  assert.equal(moved[1].active, false);
  assert.equal(traceBlocks(steps)[0].active, false);
  assert.equal(activityGroupSummary([steps[1]]), "已思考");
  const many = ["Bash", "Read", "Grep", "Edit", "WebFetch"].map((tool, i) => ({
    id: `m${i}`,
    kind: "tool",
    tool,
  }));
  assert.equal(
    activityGroupSummary(many),
    "已运行命令、读取文件、搜索代码等",
  );
});

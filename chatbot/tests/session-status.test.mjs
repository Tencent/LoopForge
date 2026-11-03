import { test } from "vitest";
import assert from "node:assert/strict";
import { runSessionState, sessionReplyStatus, sessionTooltip } from "../src/session-status.js";
import { createLocalConversations } from "../src/local-conversation-fixtures.js";

test("the current generation corrects an old sidebar snapshot without borrowing another session's state", () => {
  const row = { id: "current", generationStatus: "running" };
  assert.equal(sessionReplyStatus(row, { generation: { sessionId: "current", status: "failed" } }).state, "failed");
  assert.equal(sessionReplyStatus(row, { generation: { sessionId: "other", status: "failed" } }).state, "running");
  assert.equal(sessionReplyStatus({ ...row, generationStatus: "completed" }, { busy: true }).state, "running");
  assert.equal(sessionReplyStatus(row, { generation: { status: "cancelled" } }).state, "cancelled");
});

test("a finished reply does not claim that a task is finished or infer approval from an unknown status", () => {
  assert.equal(sessionReplyStatus({ id: "one", generationStatus: "completed" }).label, "回复完成");
  assert.equal(sessionReplyStatus({ id: "two", generationStatus: "future-provider-state" }).state, "idle");
  assert.equal(sessionReplyStatus({ id: "three" }).state, "idle");
});

test("the initial browser-local conversations contain finished messages rather than fictitious active generations", () => {
  const rows = createLocalConversations({ id: 7, loginName: "local" }, "model-original");
  assert.ok(rows.length > 1);
  assert.ok(rows.every((row) => row.generationStatus === "completed" && row.messages.at(-1).role === "assistant"));
  assert.ok(rows.every((row) => row.model === "model-original" && row.userId === 7));
});

test("a run conversation only carries a chip while the SOP needs a person", () => {
  assert.equal(runSessionState({ id: "chat", runId: "run-1" }), null);
  assert.equal(runSessionState({ id: "sop-1", runId: "run-1", shared: true, runStatus: "succeeded" }), null);
  assert.deepEqual(
    runSessionState({ id: "sop-1", runId: "run-1", shared: true, runStatus: "waiting", runStatusLabel: "待处理" }),
    { state: "waiting", label: "待处理" },
  );
  assert.equal(runSessionState({ id: "sop-2", runId: "run-2", shared: true, runStatus: "running" }).label, "运行中");
  assert.equal(
    runSessionState({ id: "sop-3", runId: "run-3", shared: true, runStatus: "failed", runStatusLabel: "运行失败" }).label,
    "运行失败",
  );
});

test("hover text names a run conversation's SOP and run state, a chat's reply state", () => {
  const run = { id: "sop-1", runId: "run-1", shared: true, displayTitle: "#24 登录改版", title: "通用后端交付 SOP", sopName: "通用后端交付", runStatusLabel: "已完成" };
  assert.equal(
    sessionTooltip(run, { projectName: "AgentFlow", replyLabel: "当前没有生成中的回复" }),
    "#24 登录改版 · AgentFlow · 通用后端交付 · 已完成",
  );
  assert.equal(sessionTooltip({ ...run, runStatusLabel: "" }, { replyLabel: "正在回复" }), "#24 登录改版 · 通用后端交付 · 正在回复");
  assert.equal(sessionTooltip({ id: "c", title: "普通会话", sopName: "x" }, { replyLabel: "回复完成" }), "普通会话 · 回复完成");
  assert.equal(sessionTooltip({ id: "n" }), "新对话");
  // A title already led by the SOP does not repeat it.
  assert.equal(
    sessionTooltip({ ...run, displayTitle: "模拟90s流程 · 10月13日 11:23", sopName: "模拟90s流程" }),
    "模拟90s流程 · 10月13日 11:23 · 已完成",
  );
});

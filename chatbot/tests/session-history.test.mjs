import { test } from "vitest";
import assert from "node:assert/strict";
import { emptyRunConversation, groupSessionHistory, idleRunConversation } from "../src/session-history.js";

const projects = [
  { id: "mall", name: "积分商城" },
  { id: "pay", name: "Payments" },
  { id: "mall-copy", name: "积分商城" },
];
const session = (id, projectId, day, extra = {}) => ({
  id,
  title: id,
  projectId,
  updatedAt: `2026-10-${day}T00:00:00Z`,
  ...extra,
});
const sessions = [
  session("商城旧会话", "mall", 20),
  session("个人会话", "", 29),
  session("支付新会话", "pay", 28),
  session("商城新会话", "mall", 27),
  session("商城已归档", "mall", 29, { archived: true }),
  session("同名项目的会话", "mall-copy", 21),
  session("未载入项目的会话", "missing", 22),
];

test("history is separated by project ID, with the current project first and personal last", () => {
  const groups = groupSessionHistory(sessions, projects, {
    archived: false,
    currentProjectId: "mall",
    pinned: ["商城旧会话"],
  });
  assert.deepEqual(
    groups.map((group) => group.projectId),
    ["mall", "pay", "missing", "mall-copy", ""],
  );
  assert.deepEqual(
    groups[0].sessions.map((row) => row.id),
    ["商城旧会话", "商城新会话"],
  );
  assert.equal(groups[2].name, "missing");
  assert.equal(groups[3].name, "积分商城");
  assert.equal(groups[4].name, "个人对话");
  assert.equal(sessions[0].id, "商城旧会话");
});

test("archive and search retain project boundaries and omit empty groups", () => {
  const archived = groupSessionHistory(sessions, projects, { archived: true });
  assert.deepEqual(
    archived.map((group) => group.projectId),
    ["mall"],
  );
  assert.deepEqual(
    archived[0].sessions.map((row) => row.id),
    ["商城已归档"],
  );
  const search = groupSessionHistory(sessions, projects, {
    query: "  payments  ",
  });
  assert.deepEqual(
    search.map((group) => group.projectId),
    ["pay"],
  );
  const byTitle = groupSessionHistory(sessions, projects, {
    query: "新会话",
    archived: false,
  });
  assert.deepEqual(
    byTitle.map((group) => group.projectId),
    ["pay", "mall"],
  );
  assert.deepEqual(
    groupSessionHistory(sessions, projects, { query: "不存在" }),
    [],
  );
});

test("personal view promotes its own group without treating unknown projects as personal", () => {
  const groups = groupSessionHistory(sessions, [], { currentProjectId: "" });
  assert.equal(groups[0].name, "个人对话");
  assert.equal(groups[0].sessions.length, 1);
  assert.equal(groups.find((group) => group.projectId === "mall").name, "mall");
});

test("only finished runs nobody talked in count as idle", () => {
  const rows = [
    session("普通会话", "mall", 27),
    session("sop-a", "mall", 28, { shared: true, runId: "run-a", messageCount: 0, runStatus: "succeeded" }),
    session("sop-b", "mall", 29, { shared: true, runId: "run-b", messageCount: 2, runStatus: "succeeded" }),
    session("sop-c", "mall", 30, { shared: true, runId: "run-c", messageCount: 0, runStatus: "waiting" }),
    session("sop-d", "mall", 30, { shared: true, runId: "run-d", messageCount: 0, runStatus: "failed" }),
    session("sop-e", "mall", 30, { shared: true, runId: "run-e", messageCount: 0, runStatus: "cancelled" }),
    session("sop-f", "mall", 30, { shared: true, runId: "run-f", runStatus: "succeeded" }),
    session("sop-branch-x", "mall", 31, { shared: true, runId: "run-b", messageCount: 0, runStatus: "succeeded" }),
  ];
  assert.equal(emptyRunConversation(rows[1]), true);
  assert.equal(emptyRunConversation(rows[2]), false);
  assert.equal(emptyRunConversation(rows[0]), false);
  assert.deepEqual(
    rows.filter(idleRunConversation).map((row) => row.id),
    // sop-f: an unread message count never hides a run.
    ["sop-a", "sop-e"],
  );
  // Grouping itself no longer splits run conversations out.
  const groups = groupSessionHistory(rows, projects);
  assert.equal(groups[0].sessions.length, rows.length);
  assert.equal(groups[0].runSessions, undefined);
});

test("search also matches the SOP of a run conversation", () => {
  const run = session("sop-a", "mall", 28, { shared: true, runId: "run-a", displayTitle: "#7 修复登录失败", sopName: "通用后端交付" });
  const groups = groupSessionHistory([run, session("其他", "mall", 27)], projects, { query: "后端交付" });
  assert.deepEqual(groups[0].sessions.map((row) => row.id), ["sop-a"]);
});

import { describe, expect, test } from "vitest";
import {
  previewSessions,
  splitSidebarSections,
} from "../src/session-sections.js";

const NOW = Date.parse("2026-11-02T12:00:00+08:00");
const minutes = (n) => new Date(NOW - n * 60000).toISOString();

const projects = [
  { id: "agentflow", name: "AgentFlow" },
  { id: "dp", name: "Payments" },
];

const session = (id, extra = {}) => ({
  id,
  title: id,
  projectId: "agentflow",
  updatedAt: minutes(3),
  ...extra,
});

const split = (sessions, options = {}) =>
  splitSidebarSections(sessions, projects, options);

describe("splitSidebarSections", () => {
  test("hides persisted blank drafts without hiding started or task conversations", () => {
    const {groups} = split([
      session("blank",{executionTargetLocked:false}),
      session("started",{executionTargetLocked:true}),
      session("task",{executionTargetLocked:false,taskId:"scheduled"}),
    ]);
    expect(groups.flatMap(g=>g.sessions.map(s=>s.id))).toEqual(["started","task"]);
  });
  test("a running reply stays in its own project group", () => {
    const running = session("run", {
      projectId: "dp",
      generationStatus: "running",
      updatedAt: minutes(12),
    });
    const { groups } = split([running]);

    expect(groups.map((group) => group.projectId)).toEqual(["dp"]);
    expect(groups[0].sessions.map((row) => row.id)).toEqual(["run"]);
  });

  test("a just finished reply stays in its project group as well", () => {
    const done = session("done", { generationStatus: "completed", updatedAt: minutes(1) });

    expect(split([done]).groups.map((group) => group.projectId)).toEqual([
      "agentflow",
    ]);
  });

  test("pinned sessions get their own block and never repeat below", () => {
    const pinned = session("pinned");
    const other = session("other");
    const { pinnedSessions, groups } = split([pinned, other], {
      pinned: ["pinned"],
    });

    expect(pinnedSessions.map((entry) => entry.id)).toEqual(["pinned"]);
    expect(groups.flatMap((group) => group.sessions.map((s) => s.id))).toEqual([
      "other",
    ]);
  });

  test("project groups keep the project list order whatever ran last", () => {
    const busy = session("busy", {
      projectId: "dp",
      generationStatus: "running",
      updatedAt: minutes(1),
    });
    const quiet = session("quiet", { updatedAt: minutes(90) });

    expect(split([busy, quiet]).groups.map((group) => group.projectId)).toEqual([
      "agentflow",
      "dp",
    ]);
  });

  test("search matches the project name, not only the title", () => {
    const dp = session("dp-task", { title: "修复登录", projectId: "dp" });
    const own = session("own", { title: "侧栏改造" });

    expect(
      split([dp, own], { query: "payments" }).groups.map((g) => g.projectId),
    ).toEqual(["dp"]);
    expect(split([dp, own]).groups.map((g) => g.projectId)).toEqual([
      "agentflow",
      "dp",
    ]);
  });

  test("archived conversations only show in the archived filter", () => {
    const old = session("old", { archived: true, generationStatus: "completed" });

    expect(split([old]).groups).toHaveLength(0);
    expect(split([old], { archived: true }).groups).toHaveLength(1);
  });
});

describe("splitSidebarSections with SOP run conversations", () => {
  const run = (id, extra = {}) =>
    session(id, { shared: true, runId: `run-${id}`, messageCount: 0, runStatus: "succeeded", ...extra });
  const ids = (sections) => sections.groups.flatMap((g) => g.sessions.map((s) => s.id)).sort();

  test("finished runs nobody talked in leave the history; the rest stay as rows", () => {
    const sections = split(
      [
        session("chat"),
        run("done"),
        run("stopped", { runStatus: "cancelled" }),
        run("talked", { messageCount: 3 }),
        run("waiting", { runStatus: "waiting" }),
        run("running", { runStatus: "running" }),
        run("failed", { runStatus: "failed" }),
        run("uncounted", { messageCount: undefined }),
      ],
      { hideIdleRuns: true },
    );

    expect(ids(sections)).toEqual(["chat", "failed", "running", "talked", "uncounted", "waiting"]);
    expect(sections.groups[0]).not.toHaveProperty("runSessions");
  });

  test("the open run conversation stays", () => {
    const sections = split([run("open"), run("other")], { hideIdleRuns: true, activeId: "open" });

    expect(ids(sections)).toEqual(["open"]);
  });

  test("a pinned run conversation is listed once, under 置顶", () => {
    const sections = split([run("pinned")], { hideIdleRuns: true, pinned: ["pinned"] });

    expect(sections.pinnedSessions.map((s) => s.id)).toEqual(["pinned"]);
    expect(sections.groups).toHaveLength(0);
  });

  test("without hideIdleRuns nothing is left out", () => {
    expect(ids(split([run("quiet")]))).toEqual(["quiet"]);
  });

  test("a search still finds a hidden run, by its title or its SOP", () => {
    const hidden = run("r1", {
      title: "通用后端交付 SOP（LoopForge）",
      displayTitle: "#7 修复登录失败",
      sopName: "通用后端交付",
    });
    const all = [hidden, session("other")];

    expect(ids(split(all, { hideIdleRuns: true }))).toEqual(["other"]);
    expect(ids(split(all, { hideIdleRuns: true, query: "登录失败" }))).toEqual(["r1"]);
    expect(ids(split(all, { hideIdleRuns: true, query: "后端交付" }))).toEqual(["r1"]);
  });
});

describe("previewSessions", () => {
  const list = Array.from({ length: 9 }, (_, i) => session(`s${i}`));
  test("keeps the open conversation visible beyond the limit", () => {
    expect(previewSessions(list, 6, "s8").map((s) => s.id)).toEqual(["s0", "s1", "s2", "s3", "s4", "s5", "s8"]);
    expect(previewSessions(list, 6, "s2").map((s) => s.id)).toEqual(["s0", "s1", "s2", "s3", "s4", "s5"]);
    expect(previewSessions(list, 6, "").length).toBe(6);
    expect(previewSessions(list.slice(0, 3), 6, "s8").length).toBe(3);
  });
});

describe("仅我参与的对话 filter", () => {
  const idsOf = (sections) =>
    sections.groups.flatMap((group) => group.sessions.map((s) => s.id)).sort();
  const shared = (id, participants) => session(id, { shared: true, participants });

  test("keeps the conversations whose participants list the reader", () => {
    const mine = shared("mine", [
      { id: 1, name: "我" },
      { id: 2, name: "同事" },
    ]);
    const theirs = shared("theirs", [{ id: 2, name: "同事" }]);

    expect(idsOf(split([mine, theirs], { mineOnly: true, currentUserId: 1 }))).toEqual(["mine"]);
    expect(idsOf(split([mine, theirs], { mineOnly: true, currentUserId: 2 }))).toEqual([
      "mine",
      "theirs",
    ]);
  });

  test("keeps the reader's own conversation before anyone has written in it", () => {
    const draft = session("draft", { userId: 7 });
    const others = session("others", { userId: 8 });

    expect(idsOf(split([draft, others], { mineOnly: true, currentUserId: 7 }))).toEqual([
      "draft",
    ]);
  });

  test("lists nothing when no reader is signed in", () => {
    expect(split([session("a")], { mineOnly: true }).groups).toHaveLength(0);
  });
});

import { describe, expect, test } from "vitest";
import {
  attentionMeta,
  previewSessions,
  relativeTime,
  splitSidebarSections,
} from "../src/session-sections.js";

const NOW = Date.parse("2026-11-02T12:00:00+08:00");
const minutes = (n) => new Date(NOW - n * 60000).toISOString();
const days = (n) => new Date(NOW - n * 24 * 60 * 60000).toISOString();

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
  splitSidebarSections(sessions, projects, { now: NOW, ...options });

describe("splitSidebarSections", () => {
  test("running sessions surface with their project and skip the project tree", () => {
    const running = session("run", {
      projectId: "dp",
      generationStatus: "running",
      updatedAt: minutes(12),
    });
    const { attention, groups } = split([running]);

    expect(attention).toHaveLength(1);
    expect(attention[0].status.state).toBe("running");
    expect(attention[0].projectName).toBe("Payments");
    expect(groups).toHaveLength(0);
  });

  test("finished replies stay in the section until the user opens them", () => {
    const unread = session("unread", { generationStatus: "completed" });
    const read = session("read", { generationStatus: "completed" });
    const { attention } = split([unread, read], {
      seen: { read: unread.updatedAt },
    });

    expect(attention.map((entry) => entry.session.id)).toEqual(["unread"]);
  });

  test("a read record older than the last reply still counts as unread", () => {
    const updated = session("again", {
      generationStatus: "failed",
      updatedAt: minutes(2),
    });
    const { attention } = split([updated], { seen: { again: minutes(30) } });

    expect(attention).toHaveLength(1);
    expect(attention[0].status.state).toBe("failed");
  });

  test("unread replies older than the window drop out", () => {
    const fresh = session("fresh", {
      generationStatus: "completed",
      updatedAt: days(6),
    });
    const stale = session("stale", {
      generationStatus: "completed",
      updatedAt: days(8),
    });
    const { attention } = split([fresh, stale]);

    expect(attention.map((entry) => entry.session.id)).toEqual(["fresh"]);
  });

  test("pinned sessions get their own block and never repeat below", () => {
    const pinned = session("pinned");
    const other = session("other");
    const { pinnedSessions, attention, groups } = split([pinned, other], {
      pinned: ["pinned"],
    });

    expect(pinnedSessions.map((entry) => entry.id)).toEqual(["pinned"]);
    expect(attention).toHaveLength(0);
    expect(groups.flatMap((group) => group.sessions.map((s) => s.id))).toEqual([
      "other",
    ]);
  });

  test("running replies sort before finished ones", () => {
    const done = session("done", {
      generationStatus: "completed",
      updatedAt: minutes(1),
    });
    const running = session("running", {
      generationStatus: "running",
      updatedAt: minutes(40),
    });
    const { attention } = split([done, running]);

    expect(attention.map((entry) => entry.session.id)).toEqual([
      "running",
      "done",
    ]);
  });

  test("limits the section and reports how many are hidden", () => {
    const sessions = Array.from({ length: 8 }, (_, index) =>
      session(`s${index}`, { generationStatus: "completed" }),
    );
    const { attention, attentionHidden } = split(sessions, {
      attentionLimit: 5,
    });

    expect(attention).toHaveLength(8);
    expect(attentionHidden).toBe(3);
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
    expect(split([old]).attention).toHaveLength(0);
    expect(split([old], { archived: true }).groups).toHaveLength(1);
  });
});

describe("attention labels", () => {
  test("relative times read naturally in Chinese", () => {
    expect(relativeTime(minutes(0), NOW)).toBe("刚刚");
    expect(relativeTime(minutes(12), NOW)).toBe("12 分钟前");
    expect(relativeTime(days(2), NOW)).toBe("2 天前");
  });

  test("each reply state gets its own suffix", () => {
    const entry = (state) => ({
      session: { updatedAt: minutes(4) },
      status: { state },
    });

    expect(attentionMeta(entry("running"), NOW)).toBe("执行中 · 4 分钟前");
    expect(attentionMeta(entry("completed"), NOW)).toBe("4 分钟前完成");
    expect(attentionMeta(entry("failed"), NOW)).toBe("4 分钟前失败");
    expect(attentionMeta(entry("interrupted"), NOW)).toBe("4 分钟前中断");
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

describe("first visit baseline", () => {
  test("replies that finished before the read record existed are not new", () => {
    const before = session("before", { generationStatus: "completed", updatedAt: minutes(40) });
    const after = session("after", { generationStatus: "completed", updatedAt: minutes(5) });
    const running = session("busy", { generationStatus: "running", updatedAt: minutes(60) });
    const { attention, groups } = split([before, after, running], {
      seen: { __since: minutes(20) },
    });
    expect(attention.map((entry) => entry.session.id)).toEqual(["busy", "after"]);
    expect(groups.flatMap((g) => g.sessions.map((s) => s.id))).toEqual(["before"]);
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

import {
  groupSessionHistory,
  idleRunConversation,
  sessionProjectName,
  sessionSearchText,
} from "./session-history.js";
import { sessionReplyStatus } from "./session-status.js";

const DAY = 24 * 60 * 60 * 1000;

// A reply that finished three weeks ago is no longer something the user is
// waiting on; keeping it pinned to the top turns the section into a second inbox.
export const ATTENTION_WINDOW_DAYS = 7;
export const ATTENTION_LIMIT = 5;
export const PROJECT_PREVIEW_LIMIT = 6;

const DONE = new Set(["completed", "failed", "cancelled", "interrupted"]);

// The first rows of a project group, plus the open conversation when it sits
// further down: opening an older conversation (from a link or search) must not
// leave the sidebar without a trace of where the reader is.
export function previewSessions(sessions, limit, activeId = "") {
  const list = sessions || [];
  if (list.length <= limit) return list;
  const head = list.slice(0, limit);
  const active = activeId && list.slice(limit).find((session) => session.id === activeId);
  return active ? [...head, active] : head;
}

export function relativeTime(value, now = Date.now()) {
  const time = Date.parse(value) || 0;
  if (!time || time > now) return "";
  const minutes = Math.floor((now - time) / 60000);
  if (minutes < 1) return "刚刚";
  if (minutes < 60) return `${minutes} 分钟前`;
  const hours = Math.floor(minutes / 60);
  if (hours < 24) return `${hours} 小时前`;
  const days = Math.floor(hours / 24);
  if (days < 30) return `${days} 天前`;
  return `${Math.floor(days / 30)} 个月前`;
}

export function attentionMeta(entry, now = Date.now()) {
  const label = relativeTime(entry.session.updatedAt, now);
  if (entry.status.state === "running") return label ? `执行中 · ${label}` : "执行中";
  if (entry.status.state === "failed") return `${label}失败`;
  if (entry.status.state === "interrupted") return `${label}中断`;
  if (entry.status.state === "cancelled") return `${label}已停止`;
  return `${label}完成`;
}

// Splits the sidebar into the three blocks it renders: 置顶, 进行中·待查看 and
// the per-project history. A session only ever belongs to one block, so the
// same conversation cannot be listed twice while it is running.
export function splitSidebarSections(
  sessions,
  projects,
  {
    archived = false,
    query = "",
    pinned = [],
    seen = {},
    now = Date.now(),
    windowDays = ATTENTION_WINDOW_DAYS,
    attentionLimit = ATTENTION_LIMIT,
    currentProjectId = "",
    activeId = "",
    hideIdleRuns = false,
  } = {},
) {
  const needle = query.trim().toLocaleLowerCase();
  const pinIds = new Set(pinned);
  const windowStart = now - windowDays * DAY;
  const updated = (session) => Date.parse(session.updatedAt) || 0;

  const visible = (sessions || []).filter((session) => {
    if (!!session.archived !== !!archived) return false;
    if (!needle) {
      // Finished runs nobody talked in are left to the 运行 page; a search,
      // a pin or opening one still shows it.
      return !(
        hideIdleRuns &&
        session.id !== activeId &&
        !pinIds.has(session.id) &&
        idleRunConversation(session)
      );
    }
    const name = sessionProjectName(session.projectId, projects);
    return sessionSearchText(session, name).includes(needle);
  });

  const pinnedSessions = visible
    .filter((session) => pinIds.has(session.id))
    .sort((a, b) => updated(b) - updated(a));

  const attention = [];
  if (!archived) {
    for (const session of visible) {
      if (pinIds.has(session.id)) continue;
      const status = sessionReplyStatus(session);
      const time = updated(session);
      if (status.state === "running") {
        attention.push({
          session,
          status,
          projectName: sessionProjectName(session.projectId, projects),
        });
        continue;
      }
      if (!DONE.has(status.state)) continue;
      if (time < windowStart) continue;
      const seenAt = Math.max(
        Date.parse(seen[session.id] || "") || 0,
        Date.parse(seen.__since || "") || 0,
      );
      if (time <= seenAt) continue;
      attention.push({
        session,
        status,
        projectName: sessionProjectName(session.projectId, projects),
      });
    }
    attention.sort(
      (a, b) =>
        Number(b.status.state === "running") -
          Number(a.status.state === "running") ||
        updated(b.session) - updated(a.session),
    );
  }

  const attentionIds = new Set(attention.map((entry) => entry.session.id));
  const rest = visible.filter(
    (session) => !pinIds.has(session.id) && !attentionIds.has(session.id),
  );

  return {
    pinnedSessions,
    attention,
    attentionHidden: Math.max(0, attention.length - attentionLimit),
    groups: groupSessionHistory(rest, projects, {
      archived,
      currentProjectId,
    }),
  };
}

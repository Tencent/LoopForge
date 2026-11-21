import {
  groupSessionHistory,
  idleRunConversation,
  scheduledSession,
  sessionProjectName,
  sessionSearchText,
} from "./session-history.js";
import { executionLocked } from "./execution-target.js";

export const PROJECT_PREVIEW_LIMIT = 6;

// "仅我参与的对话" keeps the conversations whose transcript names the reader.
// Shared project conversations list every human who wrote in them, so this is
// the only filter that tells a colleague's conversation from one the reader
// joined. Rows saved before participants were cached, and chats nobody has
// written in yet, carry no list at all: ownership decides for those, so the
// reader's own empty conversation does not disappear.
export function sessionIncludesUser(session, userId) {
  const me = String(userId ?? "").trim();
  if (!me) return false;
  const participants = Array.isArray(session?.participants)
    ? session.participants
    : [];
  if (participants.length)
    return participants.some(
      (participant) => String(participant?.id ?? "").trim() === me,
    );
  return String(session?.userId ?? "").trim() === me;
}

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

// Every execution of a scheduled job keeps its own conversation under the same
// title; all but the most recent one per project and title are superseded.
export function supersededScheduledSessions(sessions) {
  const newest = new Map();
  const older = new Set();
  for (const session of sessions || []) {
    if (!scheduledSession(session)) continue;
    const key = `${session.projectId || ""}\u0000${session.title || ""}`;
    const kept = newest.get(key);
    if (!kept) {
      newest.set(key, session);
    } else if ((Date.parse(session.updatedAt) || 0) > (Date.parse(kept.updatedAt) || 0)) {
      older.add(kept.id);
      newest.set(key, session);
    } else {
      older.add(session.id);
    }
  }
  return older;
}

// Splits the sidebar into the two blocks it renders: 置顶 and the per-project
// history. A pinned session only ever belongs to 置顶, so the same conversation
// cannot be listed twice. A running or newly finished reply stays in its own
// project group: the project tree is the only place a conversation lives.
export function splitSidebarSections(
  sessions,
  projects,
  {
    archived = false,
    mineOnly = false,
    currentUserId = "",
    query = "",
    pinned = [],
    activeId = "",
    hideIdleRuns = false,
  } = {},
) {
  const needle = query.trim().toLocaleLowerCase();
  const pinIds = new Set(pinned);
  const olderScheduled = hideIdleRuns && !needle ? supersededScheduledSessions(sessions) : new Set();

  const visible = (sessions || []).filter((session) => {
    // Old versions persisted a blank conversation when choosing a machine.
    if (session.executionTargetLocked === false && !executionLocked(session)) return false;
    if (!!session.archived !== !!archived) return false;
    if (mineOnly && !sessionIncludesUser(session, currentUserId)) return false;
    if (!needle) {
      // Runs nobody talked in are left to the 运行 page, and a scheduled job
      // shows only its latest execution; a search, a pin or opening one still
      // shows it.
      return !(
        hideIdleRuns &&
        session.id !== activeId &&
        !pinIds.has(session.id) &&
        (idleRunConversation(session) || olderScheduled.has(session.id))
      );
    }
    const name = sessionProjectName(session.projectId, projects);
    return sessionSearchText(session, name).includes(needle);
  });

  const pinnedSessions = visible
    .filter((session) => pinIds.has(session.id))
    .sort(
      (a, b) =>
        (Date.parse(b.updatedAt) || 0) - (Date.parse(a.updatedAt) || 0),
    );

  const rest = visible.filter((session) => !pinIds.has(session.id));

  return {
    pinnedSessions,
    groups: groupSessionHistory(rest, projects, { archived }),
  };
}

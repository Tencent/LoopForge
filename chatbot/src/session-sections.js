import {
  groupSessionHistory,
  idleRunConversation,
  scheduledSession,
  sessionProjectName,
  sessionSearchText,
} from "./session-history.js";
import { executionLocked } from "./execution-target.js";
import { sessionReplyStatus, sidebarReplyStatus } from "./session-status.js";
import { unreadReply } from "./session-unread.js";

function isSessionUnread(session, activeId, readReplies) {
  if (!session?.unread || session.id === activeId) return false;
  if (session.generationId) {
    return unreadReply(session, { activeId, read: readReplies || {} });
  }
  return !(readReplies && typeof readReplies === "object" && readReplies[session.id]);
}

export const PROJECT_PREVIEW_LIMIT = 6;
export const SCHEDULED_TASK_PREVIEW_LIMIT = 5;

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

// Extract a clean task label from a scheduled session's title when no schedule
// job record is loaded yet.
export function scheduledTaskTitle(session, jobsById = null) {
  const taskId = String(session?.taskId || "").trim();
  if (taskId && jobsById && jobsById.has(taskId)) {
    const jobName = String(jobsById.get(taskId)?.name || "").trim();
    if (jobName) return jobName;
  }
  const raw = String(session?.displayTitle || session?.title || "").trim();
  const stripped = raw.replace(/^定时\s*·\s*/, "").trim();
  return stripped || "定时任务";
}

function visibleSessions(
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
    bucket = "all",
  } = {},
) {
  const needle = query.trim().toLocaleLowerCase();
  const pinIds = new Set(pinned);
  return (sessions || []).filter((session) => {
    // Old versions persisted a blank conversation when choosing a machine.
    if (session.executionTargetLocked === false && !executionLocked(session)) return false;
    if (!!session.archived !== !!archived) return false;
    if (bucket === "personal" && scheduledSession(session)) return false;
    if (bucket === "scheduled" && !scheduledSession(session)) return false;
    if (mineOnly && !sessionIncludesUser(session, currentUserId)) return false;
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
}

// Splits the sidebar into the two blocks it renders: 置顶 and the per-project
// history. A pinned session only ever belongs to 置顶, so the same conversation
// cannot be listed twice. A running or newly finished reply stays in its own
// project group: the project tree is the only place a conversation lives.
// Pass bucket="personal" or bucket="scheduled" to scope to one sidebar tab;
// leaving bucket unset ("all") preserves the legacy combined view.
export function splitSidebarSections(
  sessions,
  projects,
  options = {},
) {
  const {
    archived = false,
    pinned = [],
  } = options;
  const pinIds = new Set(pinned);
  const visible = visibleSessions(sessions, projects, options);

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

// Groups scheduled task conversations by their schedule job (taskId, or title
// fallback for legacy rows) so repeated runs of one task stay tidy.
export function groupScheduledSessionsByTask(
  sessions,
  projects,
  {
    jobs = [],
    archived = false,
    mineOnly = false,
    currentUserId = "",
    query = "",
    pinned = [],
    activeId = "",
    readReplies = null,
    activeGeneration = null,
    activeBusy = false,
  } = {},
) {
  const jobsById = new Map((jobs || []).map((job) => [job.id, job]));
  const visible = visibleSessions(sessions, projects, {
    archived,
    mineOnly,
    currentUserId,
    query,
    pinned,
    activeId,
    hideIdleRuns: false,
    bucket: "scheduled",
  });
  const updated = (s) => Date.parse(s.updatedAt) || 0;
  const groups = new Map();
  for (const session of visible) {
    const taskId = String(session.taskId || "").trim();
    const title = scheduledTaskTitle(session, jobsById);
    const key = taskId ? `task:${taskId}` : `title:${session.projectId || ""}:${title}`;
    const job = taskId ? jobsById.get(taskId) || null : null;
    if (!groups.has(key)) {
      groups.set(key, {
        key,
        taskId,
        title,
        projectId: session.projectId || job?.projectId || "",
        projectName: sessionProjectName(session.projectId || job?.projectId || "", projects),
        job,
        fixedSession: job?.sessionMode === "fixed",
        sessions: [],
        latestAt: 0,
        running: false,
        failed: false,
        unread: 0,
      });
    }
    const group = groups.get(key);
    group.sessions.push(session);
    const ts = updated(session);
    if (ts > group.latestAt) group.latestAt = ts;
    const isActive = session.id === activeId;
    const unread = isSessionUnread(session, activeId, readReplies);
    const st = sidebarReplyStatus(
      sessionReplyStatus(session, {
        generation: isActive ? activeGeneration : null,
        busy: isActive ? activeBusy : false,
      }),
      unread,
    );
    if (st.state === "running") group.running = true;
    if (st.state === "failed") group.failed = true;
    if (st.state === "unread") group.unread += 1;
  }
  for (const group of groups.values()) {
    group.sessions.sort((a, b) => updated(b) - updated(a));
  }
  return [...groups.values()].sort(
    (a, b) =>
      Number(b.running) - Number(a.running) ||
      b.latestAt - a.latestAt ||
      a.title.localeCompare(b.title),
  );
}

// Computes compact cross-tab indicators for the 2 sidebar tabs (个人 / 定时任务).
export function summarizeSidebarTabBadges(
  sessions,
  {
    activeId = "",
    archived = false,
    mineOnly = false,
    currentUserId = "",
    readReplies = null,
    activeGeneration = null,
    activeBusy = false,
  } = {},
) {
  const personal = { unread: 0, running: 0 };
  const scheduled = { total: 0, running: 0, failed: 0, unread: 0 };
  for (const session of sessions || []) {
    if (session.executionTargetLocked === false && !executionLocked(session)) continue;
    if (!!session.archived !== !!archived) continue;
    if (mineOnly && !sessionIncludesUser(session, currentUserId)) continue;
    const isActive = session.id === activeId;
    const unread = isSessionUnread(session, activeId, readReplies);
    const st = sidebarReplyStatus(
      sessionReplyStatus(session, {
        generation: isActive ? activeGeneration : null,
        busy: isActive ? activeBusy : false,
      }),
      unread,
    );
    if (scheduledSession(session)) {
      scheduled.total += 1;
      if (st.state === "running") scheduled.running += 1;
      else if (st.state === "failed") scheduled.failed += 1;
      else if (st.state === "unread") scheduled.unread += 1;
    } else {
      if (st.state === "running") personal.running += 1;
      else if (st.state === "unread") personal.unread += 1;
    }
  }
  return { personal, scheduled };
}

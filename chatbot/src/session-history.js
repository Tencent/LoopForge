export function sessionProjectName(projectId, projects) {
  return projectId
    ? projects.find((project) => project.id === projectId)?.name || projectId
    : "个人对话";
}

// What a history search matches: the names shown for the row, the SOP a run
// conversation belongs to and its project.
export function sessionSearchText(session, projectName) {
  return `${session.title || ""} ${session.displayTitle || ""} ${session.sopName || ""} ${projectName}`.toLocaleLowerCase();
}

// An untouched SOP run conversation: the platform created it with the run and
// nobody has written to it since.
export function emptyRunConversation(session) {
  return Boolean(
    session?.shared &&
      session?.runId &&
      session?.messageCount === 0 &&
      !String(session.id || "").startsWith("sop-branch-"),
  );
}

const FINISHED_RUN = new Set(["succeeded", "cancelled"]);

// A finished run nobody ever talked in is a run record, not a conversation:
// the 运行 page lists it, so the history leaves it out. Runs still going,
// waiting for a person or failed stay, as does any run with an unknown state.
export function idleRunConversation(session) {
  return emptyRunConversation(session) && FINISHED_RUN.has(session.runStatus);
}

// A scheduled task conversation, marked by the backend source or, for rows
// written before the marker existed, by its historical title prefix.
export function scheduledSession(session) {
  if (session?.source) return session.source === "scheduled";
  return !session?.shared && /^定时 · /.test(String(session?.title || ""));
}

// Project groups keep one fixed order, the order the project list itself comes
// in: 个人对话 first, projects next, leftover IDs (a project the reader can no
// longer see) last. Opening a conversation, or a reply finishing in it, must
// not move a project up the sidebar, so nothing here looks at recency.
function compareProjectGroups(a, b, rank) {
  return (
    Number(!b.projectId) - Number(!a.projectId) ||
    rank(a.projectId) - rank(b.projectId) ||
    a.projectId.localeCompare(b.projectId)
  );
}

export function groupSessionHistory(
  sessions,
  projects,
  { archived = null, query = "", pinned = [] } = {},
) {
  const groups = new Map();
  const needle = query.trim().toLocaleLowerCase();
  const pinIds = new Set(pinned);
  const updated = (session) => Date.parse(session.updatedAt) || 0;
  const known = new Map((projects || []).map((project, index) => [project.id, index]));
  const rank = (projectId) => {
    const index = known.get(projectId);
    return index === undefined ? Number.MAX_SAFE_INTEGER : index;
  };
  for (const session of sessions) {
    if (archived !== null && !!session.archived !== archived) continue;
    const projectId = session.projectId || "";
    const name = sessionProjectName(projectId, projects);
    if (needle && !sessionSearchText(session, name).includes(needle))
      continue;
    if (!groups.has(projectId))
      groups.set(projectId, {
        projectId,
        name,
        sessions: [],
      });
    groups.get(projectId).sessions.push(session);
  }
  for (const group of groups.values()) {
    group.sessions.sort(
      (a, b) =>
        Number(pinIds.has(b.id)) - Number(pinIds.has(a.id)) ||
        updated(b) - updated(a),
    );
  }
  return [...groups.values()].sort((a, b) => compareProjectGroups(a, b, rank));
}

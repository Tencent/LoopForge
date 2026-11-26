import { parseReply } from "./api.js";

const SAVED_ISSUE_TOOLS = new Set(["issues_save", "issues_import_tapd"]);
const TERMINAL_ISSUE_STATUSES = new Set([
  "done",
  "closed",
  "completed",
  "cancelled",
]);

/**
 * Collects requirements saved during assistant turns (both direct MCP writes
 * in auto/assisted modes via `message.assets` and confirmed drafts in confirm
 * mode) and maps each issue to the latest message index that saved it.
 */
export function savedIssueCardsByMessage(messages = [], projectId = "") {
  const latest = new Map();
  (messages || []).forEach((message, messageIndex) => {
    if (message?.role !== "assistant") return;
    const record = (id, title, itemProjectId) => {
      const cleanId = String(id || "").trim();
      if (!cleanId) return;
      const cleanProject = String(itemProjectId || "").trim();
      if (projectId && cleanProject && cleanProject !== projectId) return;
      latest.set(cleanId, {
        id: cleanId,
        title: String(title || "").trim(),
        projectId: cleanProject || projectId,
        messageIndex,
      });
    };
    for (const asset of message.assets || []) {
      if (asset?.kind === "issue" && asset.id) {
        record(asset.id, asset.name, asset.projectId);
      }
    }
    const reply = parseReply(message);
    for (const draft of reply.drafts || []) {
      if (!draft?.saved || !SAVED_ISSUE_TOOLS.has(draft.tool)) continue;
      const saved = draft.savedResult?.data || draft.savedResult;
      const id = saved?.id || draft.body?.id || draft.path?.id;
      const title = saved?.title || draft.body?.title || "";
      const itemProjectId = saved?.projectId || draft.body?.projectId || "";
      record(id, title, itemProjectId);
    }
  });
  const byMessage = new Map();
  for (const entry of latest.values()) {
    const list = byMessage.get(entry.messageIndex) || [];
    list.push({
      id: entry.id,
      title: entry.title,
      projectId: entry.projectId,
    });
    byMessage.set(entry.messageIndex, list);
  }
  return byMessage;
}

/**
 * Resolves the first bound LOOP eligible for one-click dispatch from a
 * `/api/issues/:id/requirement` snapshot. Returns null when the requirement
 * opts out of LOOPs, has no bound LOOP, is already completed/cancelled, or has
 * already started a LOOP run.
 */
export function dispatchableIssueLoop(snapshot, projectId = "") {
  const issue = snapshot?.issue;
  if (!issue?.id) return null;
  if (projectId && issue.projectId && issue.projectId !== projectId) return null;
  if (String(issue.sopMode || "").trim().toLowerCase() === "none") return null;
  const status = String(issue.status || "").trim().toLowerCase();
  if (TERMINAL_ISSUE_STATUSES.has(status)) return null;
  const lanes = Array.isArray(snapshot?.lanes) ? snapshot.lanes : [];
  if (
    Number(snapshot?.overall?.runCount || 0) > 0 ||
    lanes.some((lane) => lane?.started || Number(lane?.runCount || 0) > 0)
  ) {
    return null;
  }
  const boundLanes = lanes.filter((lane) => String(lane?.workflowId || "").trim());
  const firstLane = boundLanes[0];
  const workflowId = String(firstLane?.workflowId || issue.workflowId || "").trim();
  if (!workflowId) return null;
  const workflowName = String(
    firstLane?.workflowName || firstLane?.label || workflowId,
  ).trim();
  return {
    issue,
    workflowId,
    workflowName,
    laneCount: boundLanes.length || 1,
  };
}

import { useEffect, useState } from "react";
import { sessionIncludesUser } from "./session-sections.js";

// The history list marks a reply that finished while the reader was elsewhere
// with a blue dot, as Codex does. The reader has seen a reply once it finished
// while its conversation was open; the record is the generation ID of that
// reply, per conversation. Reading is a property of this reader's screen, not
// of the shared transcript, so the record stays in this browser.
export const SEEN_REPLIES_KEY = "loopforge.chat.seenReplies.v1";

export function seenRepliesKey(userId) {
  return `${SEEN_REPLIES_KEY}.${userId}`;
}

function finished(status) {
  return Boolean(status) && status !== "running" && status !== "idle";
}

// Brings the record up to date with the history list and the open
// conversation. Without a record (the first visit) every reply that has
// already finished counts as seen, so the feature does not light up the whole
// history; a reply still running gets its dot when it finishes.
export function reconcileSeenReplies(
  stored,
  sessions,
  { activeId = "", generation = null } = {},
) {
  const rows = (sessions || []).filter((session) => session?.id);
  let next;
  if (!stored) {
    next = {};
    for (const session of rows)
      if (session.generationId && finished(session.generationStatus))
        next[session.id] = session.generationId;
  } else if (rows.length) {
    // The list holds every conversation the reader can open; a deleted one
    // leaves the record with it.
    const ids = new Set(rows.map((session) => session.id));
    next = Object.fromEntries(
      Object.entries(stored).filter(([id]) => ids.has(id)),
    );
  } else {
    next = { ...stored };
  }
  if (activeId) {
    const row = rows.find((session) => session.id === activeId);
    if (row?.generationId && finished(row.generationStatus))
      next[activeId] = row.generationId;
    // The open conversation's live generation is fresher than the list row.
    if (
      generation?.id &&
      finished(generation.status) &&
      (!generation.sessionId || generation.sessionId === activeId)
    )
      next[activeId] = generation.id;
  }
  return next;
}

// A reply is unread when it completed, the reader has not had its
// conversation open since, and the conversation is one the reader takes part
// in: a colleague's project chat is not news to everyone in the project.
// Failed and interrupted replies keep their own warning icons.
export function unreadReply(session, seen, { activeId = "", userId = "" } = {}) {
  if (!seen || !session?.id || session.id === activeId) return false;
  if (session.generationStatus !== "completed" || !session.generationId)
    return false;
  if (seen[session.id] === session.generationId) return false;
  return sessionIncludesUser(session, userId);
}

function sameRecord(a, b) {
  if (a === b) return true;
  if (!a || !b) return false;
  const keys = Object.keys(a);
  return (
    keys.length === Object.keys(b).length &&
    keys.every((key) => a[key] === b[key])
  );
}

function readRecord(key) {
  try {
    const value = JSON.parse(localStorage.getItem(key));
    return value && typeof value === "object" && !Array.isArray(value)
      ? value
      : null;
  } catch {
    return null;
  }
}

// The seen-reply record for the history list, or null until the list has
// loaded. Each update starts from the stored record, so two open tabs do not
// undo each other's reads.
export function useSeenReplies({
  userId,
  sessions,
  enabled = true,
  activeId = "",
  generation = null,
}) {
  const [seen, setSeen] = useState(null);
  const key = enabled && userId != null && userId !== "" ? seenRepliesKey(userId) : "";
  const generationId = generation?.id || "";
  const generationStatus = generation?.status || "";
  const generationSession = generation?.sessionId || "";
  useEffect(() => {
    if (!key) {
      setSeen(null);
      return;
    }
    const stored = readRecord(key);
    const next = reconcileSeenReplies(stored, sessions, {
      activeId,
      generation: generationId
        ? { id: generationId, status: generationStatus, sessionId: generationSession }
        : null,
    });
    if (!sameRecord(stored, next)) {
      try {
        localStorage.setItem(key, JSON.stringify(next));
      } catch {
        // Storage full or blocked: the dots still follow this page's reads.
      }
    }
    setSeen((current) => (sameRecord(current, next) ? current : next));
  }, [key, sessions, activeId, generationId, generationStatus, generationSession]);
  return seen;
}

import { useEffect, useRef, useState } from "react";
import { sessionPath, write } from "./api.js";

// The history list marks a reply that completed after the reader last opened
// its conversation with a blue dot, as Codex does. The server keeps which
// reply each reader has seen and flags the rest `unread` in the list, so a
// conversation that finished while nobody had the page open is dotted too,
// on every device.
export function unreadReply(session, { activeId = "", read = {} } = {}) {
  if (!session?.unread || !session.generationId || session.id === activeId)
    return false;
  // Read on this page; the next list refresh agrees.
  return read[session.id] !== session.generationId;
}

// The completed reply on screen: the open conversation's live generation, or
// the unread reply its list row names.
function openReply(sessions, activeId, generation) {
  if (!activeId) return "";
  if (
    generation?.id &&
    generation.status === "completed" &&
    (!generation.sessionId || generation.sessionId === activeId)
  )
    return generation.id;
  const row = (sessions || []).find((session) => session.id === activeId);
  return row?.unread && row.generationId ? row.generationId : "";
}

// Reports the open conversation's completed reply as read, once per reply,
// and returns the replies read on this page so their dots clear at once.
export function useReadReplies({
  enabled = true,
  sessions,
  activeId = "",
  generation = null,
}) {
  const [read, setRead] = useState({});
  const reported = useRef(new Set());
  const replyId = enabled ? openReply(sessions, activeId, generation) : "";
  useEffect(() => {
    if (!replyId) return;
    const key = `${activeId}\n${replyId}`;
    if (reported.current.has(key)) return;
    reported.current.add(key);
    setRead((old) =>
      old[activeId] === replyId ? old : { ...old, [activeId]: replyId },
    );
    write(`${sessionPath(activeId)}/read`, { generationId: replyId }).catch(
      () => {
        // Opening the conversation again retries.
        reported.current.delete(key);
      },
    );
  }, [activeId, replyId]);
  return read;
}

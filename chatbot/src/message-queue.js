// Messages sent while a reply is still running wait here, per conversation,
// and go out one by one once the running turn has ended (like Codex and other
// agent harnesses). Stopping a reply holds the queue instead of sending it.
import { localId } from "./local-id.js";

export const QUEUE_STORAGE_KEY = "agentflow.native-chat.queue.v1";

const entryOf = (queues, sessionId) =>
  queues[sessionId] || { items: [], held: false };

function withEntry(queues, sessionId, entry) {
  const next = { ...queues };
  if (entry.items.length) next[sessionId] = entry;
  else delete next[sessionId];
  return next;
}

export function queuedMessage({
  text = "",
  attachments = [],
  references = [],
  approvalMode = "confirm",
}) {
  return {
    id: localId("queued"),
    text: text.trim(),
    attachments,
    references,
    approvalMode,
    queuedAt: new Date().toISOString(),
  };
}

export function enqueue(queues, sessionId, item) {
  const entry = entryOf(queues, sessionId);
  return withEntry(queues, sessionId, { ...entry, items: [...entry.items, item] });
}

export function removeQueued(queues, sessionId, itemId) {
  const entry = entryOf(queues, sessionId);
  const items = entry.items.filter((item) => item.id !== itemId);
  return withEntry(queues, sessionId, { ...entry, items, held: items.length ? entry.held : false });
}

// A queued message that could not be sent goes back to the front. By default
// the queue then waits for the user instead of retrying in a loop; a message
// that only missed its chance to interrupt goes out next as usual.
export function restoreQueued(queues, sessionId, item, held = true) {
  const entry = entryOf(queues, sessionId);
  const items = [item, ...entry.items.filter((old) => old.id !== item.id)];
  return withEntry(queues, sessionId, { items, held: held || entry.held });
}

export function holdQueue(queues, sessionId, held) {
  const entry = entryOf(queues, sessionId);
  if (!entry.items.length || entry.held === held) return queues;
  return withEntry(queues, sessionId, { ...entry, held });
}

// The next message to send, or null while the queue must keep waiting.
export function nextQueued(queues, sessionId) {
  const entry = sessionId ? queues[sessionId] : null;
  return entry && !entry.held && entry.items.length ? entry.items[0] : null;
}

export function readQueues(storage = globalThis.localStorage) {
  try {
    const value = JSON.parse(storage?.getItem(QUEUE_STORAGE_KEY) || "{}");
    if (!value || typeof value !== "object" || Array.isArray(value)) return {};
    const out = {};
    for (const [id, entry] of Object.entries(value)) {
      const items = Array.isArray(entry?.items)
        ? entry.items.filter((item) => item?.id && typeof item.text === "string")
        : [];
      // A page reload must not fire queued messages behind the user's back.
      if (items.length) out[id] = { items, held: true };
    }
    return out;
  } catch {
    return {};
  }
}

export function writeQueues(queues, storage = globalThis.localStorage) {
  try {
    if (Object.keys(queues).length)
      storage?.setItem(QUEUE_STORAGE_KEY, JSON.stringify(queues));
    else storage?.removeItem(QUEUE_STORAGE_KEY);
  } catch {}
}

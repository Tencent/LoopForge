import { useCallback, useEffect, useState } from "react";

const KEY_PREFIX = "agentflow.native-chat.seen.";
const MAX_ENTRIES = 300;
// Everything that finished before the record existed counts as seen. Without
// it the first visit (or a new browser) put every reply of the last week into
// 进行中·待查看 at once.
export const SEEN_SINCE = "__since";

function storageKey(userId) {
  return `${KEY_PREFIX}${userId || "anon"}`;
}

function readSeen(key, persist = true) {
  // Until the user is known nothing is stored: an anonymous record would only
  // pile up next to the real one.
  if (!persist) return { [SEEN_SINCE]: new Date().toISOString() };
  try {
    const raw = localStorage.getItem(key);
    const value = raw ? JSON.parse(raw) : null;
    if (value && typeof value === "object" && value[SEEN_SINCE]) return value;
    const next = { ...(value && typeof value === "object" ? value : {}), [SEEN_SINCE]: new Date().toISOString() };
    localStorage.setItem(key, JSON.stringify(next));
    return next;
  } catch {
    return { [SEEN_SINCE]: new Date().toISOString() };
  }
}

function prune(entries) {
  const { [SEEN_SINCE]: since, ...rest } = entries;
  const rows = Object.entries(rest);
  if (rows.length <= MAX_ENTRIES) return entries;
  rows.sort((a, b) => (Date.parse(b[1]) || 0) - (Date.parse(a[1]) || 0));
  return { ...Object.fromEntries(rows.slice(0, MAX_ENTRIES)), [SEEN_SINCE]: since };
}

// Which conversations the user has already looked at. Kept in localStorage so
// it works without a backend field, the same way pins are stored.
export function useSessionSeen(userId) {
  const key = storageKey(userId);
  const known = Boolean(userId);
  const [seen, setSeen] = useState(() => readSeen(key, known));

  useEffect(() => {
    setSeen(readSeen(key, known));
  }, [key, known]);

  useEffect(() => {
    function sync(event) {
      if (known && event.key === key) setSeen(readSeen(key));
    }
    window.addEventListener("storage", sync);
    return () => window.removeEventListener("storage", sync);
  }, [key, known]);

  const markSeen = useCallback(
    (id, updatedAt) => {
      if (!id) return;
      const at = updatedAt || new Date().toISOString();
      setSeen((previous) => {
        if (previous[id] && (Date.parse(previous[id]) || 0) >= (Date.parse(at) || 0))
          return previous;
        const next = prune({ ...previous, [id]: at });
        if (!known) return next;
        try {
          localStorage.setItem(key, JSON.stringify(next));
        } catch {
          // Private mode / quota: the section still works for this session.
        }
        return next;
      });
    },
    [key, known],
  );

  return { seen, markSeen };
}

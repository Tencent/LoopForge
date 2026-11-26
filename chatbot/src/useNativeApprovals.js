import { useCallback, useEffect, useRef, useState } from "react";
import { api, sessionPath, write } from "./api";

// The one poll both the approval cards, the reply status and the tool rows
// read. One request per second per open conversation, only while a turn can
// still act on the answer: a local, read-only or idle conversation never asks.
export const APPROVAL_POLL_MS = 1000;

const isSame = (prev, next) => {
  if (prev.length !== next.length) return false;
  for (let i = 0; i < prev.length; i += 1) {
    const a = prev[i];
    const b = next[i];
    if (
      a.id !== b.id ||
      a.tool !== b.tool ||
      a.summary !== b.summary ||
      a.decision !== b.decision ||
      a.createdAt !== b.createdAt ||
      JSON.stringify(a.input) !== JSON.stringify(b.input)
    )
      return false;
  }
  return true;
};

const byCreatedAt = (a, b) =>
  String(a.createdAt || "").localeCompare(String(b.createdAt || ""));

// Returns the speaker's pending external writes for the open conversation.
// Keeping them here rather than inside the card lets the status line and the
// tool rows show the same truth as the cards, without a second poll.
export function useNativeApprovals(chat) {
  const id = chat?.session?.id;
  const userId = chat?.user?.id;
  const generationId = chat?.generation?.id;
  const enabled = Boolean(id) && !chat?.local && !chat?.readOnly && Boolean(chat?.busy);
  const [items, setItems] = useState([]);
  const [saving, setSaving] = useState("");
  const [loadError, setLoadError] = useState("");
  const [decideError, setDecideError] = useState("");
  const [expired, setExpired] = useState("");
  const activeTurn = useRef(null);
  useEffect(() => {
    // Approval requests and notices belong to this speaker's current turn.
    setExpired("");
    setItems((old) => (old.length ? [] : old));
    setSaving("");
    setLoadError("");
    setDecideError("");
    if (!enabled) return undefined;

    // A poll started before a decision must not bring its card back.
    const turn = { decidedAt: new Map() };
    activeTurn.current = turn;
    async function refresh() {
      const startedAt = Date.now();
      try {
        const result = await api(`${sessionPath(id)}/native-approvals`);
        if (activeTurn.current !== turn) return;
        const mine = (Array.isArray(result) ? result : [])
          .filter((item) => item.actorId === userId)
          .filter((item) => !(turn.decidedAt.get(item.id) >= startedAt))
          .sort(byCreatedAt);
        if (mine.length) setExpired("");
        setItems((old) => (isSame(old, mine) ? old : mine));
        setLoadError("");
      } catch (e) {
        if (activeTurn.current === turn) setLoadError(e.message || "获取待审批操作失败");
      }
    }
    void refresh();
    const timer = setInterval(refresh, APPROVAL_POLL_MS);
    return () => {
      activeTurn.current = null;
      clearInterval(timer);
    };
  }, [id, enabled, userId, generationId]);
  const decide = useCallback(
    async (item, decision) => {
      const turn = activeTurn.current;
      if (!item?.id || !turn) return;
      setSaving(item.id);
      setDecideError("");
      setExpired("");
      try {
        await write(`${sessionPath(id)}/native-approvals/${encodeURIComponent(item.id)}`, {
          decision,
        });
        if (activeTurn.current !== turn) return;
        turn.decidedAt.set(item.id, Date.now());
        setItems((old) => old.filter((row) => row.id !== item.id));
      } catch (e) {
        if (activeTurn.current !== turn) return;
        if (e?.status === 409) {
          // The turn this card belonged to is over; the approval cannot apply.
          turn.decidedAt.set(item.id, Date.now());
          setExpired(e.message || "本轮已结束，审批已失效");
          setItems((old) => old.filter((row) => row.id !== item.id));
        } else {
          setDecideError(e?.message || "审批失败，请重试");
        }
      } finally {
        if (activeTurn.current === turn) {
          setSaving((current) => current === item.id ? "" : current);
        }
      }
    },
    [id],
  );
  return {
    items,
    count: items.length,
    saving,
    error: decideError || loadError,
    expired,
    decide,
  };
}

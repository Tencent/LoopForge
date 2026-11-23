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
  const enabled = Boolean(id) && !chat?.local && !chat?.readOnly && Boolean(chat?.busy);
  const [items, setItems] = useState([]);
  const [saving, setSaving] = useState("");
  const [loadError, setLoadError] = useState("");
  const [decideError, setDecideError] = useState("");
  const [expired, setExpired] = useState("");
  // A decision can overtake a request that was already in flight: that older
  // answer must not bring a decided card back. Later answers are still trusted,
  // so a card the server still reports simply reappears.
  const decidedAt = useRef(new Map());
  // Switching conversations drops the previous one's cards and notices. An
  // "approval expired" line is only true of the turn it was raised in, so the
  // turn ending, another conversation or a fresh pending write all clear it.
  const openSession = useRef(id);
  useEffect(() => {
    if (openSession.current === id) return;
    openSession.current = id;
    decidedAt.current = new Map();
    setExpired("");
    // Another conversation has its own pending writes; none of this one's
    // cards or errors may survive the switch.
    setItems((old) => (old.length ? [] : old));
    setSaving("");
    setLoadError("");
    setDecideError("");
  }, [id]);
  useEffect(() => {
    if (!enabled) {
      setItems((old) => (old.length ? [] : old));
      setSaving("");
      setLoadError("");
      setDecideError("");
      setExpired("");
      return undefined;
    }
    let closed = false;
    async function refresh() {
      const startedAt = Date.now();
      try {
        const result = await api(`${sessionPath(id)}/native-approvals`);
        if (closed) return;
        const mine = (Array.isArray(result) ? result : [])
          .filter((item) => item.actorId === userId)
          .filter((item) => !(decidedAt.current.get(item.id) >= startedAt))
          .sort(byCreatedAt);
        if (mine.length) setExpired("");
        setItems((old) => (isSame(old, mine) ? old : mine));
        setLoadError("");
      } catch (e) {
        if (!closed) setLoadError(e.message || "获取待审批操作失败");
      }
    }
    void refresh();
    const timer = setInterval(refresh, APPROVAL_POLL_MS);
    return () => {
      closed = true;
      clearInterval(timer);
    };
  }, [id, enabled, userId]);
  const decide = useCallback(
    async (item, decision) => {
      if (!item?.id || !id) return;
      setSaving(item.id);
      setDecideError("");
      setExpired("");
      try {
        await write(`${sessionPath(id)}/native-approvals/${encodeURIComponent(item.id)}`, {
          decision,
        });
        decidedAt.current.set(item.id, Date.now());
        setItems((old) => old.filter((row) => row.id !== item.id));
      } catch (e) {
        if (e?.status === 409) {
          // The turn this card belonged to is over; the approval cannot apply.
          decidedAt.current.set(item.id, Date.now());
          setExpired(e.message || "本轮已结束，审批已失效");
          setItems((old) => old.filter((row) => row.id !== item.id));
        } else {
          setDecideError(e?.message || "审批失败，请重试");
        }
      } finally {
        setSaving("");
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

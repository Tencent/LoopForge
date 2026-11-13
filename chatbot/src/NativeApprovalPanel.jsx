import React, { useEffect, useState } from "react";
import { api, write, sessionPath } from "./api";
import "./native-approval.css";

export default function NativeApprovalPanel({ chat, onChange }) {
  const [items, setItems] = useState([]);
  const [error, setError] = useState("");
  const [saving, setSaving] = useState("");
  const id = chat.session?.id;
  useEffect(() => {
    setItems([]);
    if (!id || chat.local || chat.readOnly || !chat.busy) return;
    let closed = false;
    async function refresh() {
      try {
        const result = await api(`${sessionPath(id)}/native-approvals`);
        if (!closed) setItems(Array.isArray(result) ? result : []);
      } catch (e) { if (!closed) setError(e.message); }
    }
    void refresh();
    const timer = setInterval(refresh, 1000);
    return () => { closed = true; clearInterval(timer); };
  }, [id, chat.busy, chat.local, chat.readOnly]);
  async function decide(item, decision) {
    setSaving(item.id); setError("");
    try {
      await write(`${sessionPath(id)}/native-approvals/${encodeURIComponent(item.id)}`, { decision });
      setItems((old) => old.filter((row) => row.id !== item.id));
    } catch (e) { setError(e.message); }
    finally { setSaving(""); }
  }
  const mine = items.filter((item) => item.actorId === chat.user?.id);
  const pendingIds = mine.map((item) => item.id).join(",");
  useEffect(() => { onChange?.(); }, [pendingIds]);
  if (!mine.length) return null;
  return <section className="native-approvals" aria-label="待确认的原生操作" aria-live="polite">
    {mine.map((item) => <div className="native-approval" key={item.id}>
      <strong>确认执行 {item.tool}</strong>
      <pre>{JSON.stringify(item.input, null, 2)}</pre>
      <p>仅批准这一次操作，停止回复后取消。</p>
      <div className="native-approval-actions">
        <button type="button" disabled={!!saving} onClick={() => decide(item, "deny")}>拒绝</button>
        <button type="button" className="primary" disabled={!!saving} onClick={() => decide(item, "allow")}>批准本次操作</button>
      </div>
    </div>)}
    {error ? <p role="alert">{error}</p> : null}
  </section>;
}

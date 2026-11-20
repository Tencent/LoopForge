import React, { useEffect, useState } from "react";
import { api, write, sessionPath } from "./api";
import HarnessIcon from "./HarnessIcon";
import "./native-approval.css";

// External commands and external MCP writes can wait here for the speaker
// in "请求批准" mode.
function commandText(input) {
  const command = input?.command ?? input?.cmd ?? input?.commands;
  if (typeof command === "string") return command;
  if (Array.isArray(command)) return command.map(String).join(" ");
  return JSON.stringify(input, null, 2);
}

export default function NativeApprovalPanel({ chat, onChange }) {
  const [items, setItems] = useState([]);
  const [error, setError] = useState("");
  const [saving, setSaving] = useState("");
  const id = chat.session?.id;
  useEffect(() => {
    setItems([]);
    setError("");
    if (!id || chat.local || chat.readOnly || !chat.busy) return;
    let closed = false;
    async function refresh() {
      try {
        const result = await api(`${sessionPath(id)}/native-approvals`);
        if (!closed) setItems(Array.isArray(result) ? result : []);
      } catch (e) {
        if (!closed) setError(e.message);
      }
    }
    void refresh();
    const timer = setInterval(refresh, 1000);
    return () => {
      closed = true;
      clearInterval(timer);
    };
  }, [id, chat.busy, chat.local, chat.readOnly]);
  async function decide(item, decision) {
    setSaving(item.id);
    setError("");
    try {
      await write(`${sessionPath(id)}/native-approvals/${encodeURIComponent(item.id)}`, { decision });
      setItems((old) => old.filter((row) => row.id !== item.id));
    } catch (e) {
      setError(e.message);
    } finally {
      setSaving("");
    }
  }
  const mine = items.filter((item) => item.actorId === chat.user?.id);
  const pendingIds = mine.map((item) => item.id).join(",");
  useEffect(() => {
    onChange?.();
  }, [pendingIds]);
  if (!mine.length) return null;
  return (
    <section className="native-approvals" aria-label="待批准的外部写入" aria-live="polite">
      {mine.map((item) => (
        <div className="native-approval" key={item.id}>
          <div className="native-approval-title">
            <HarnessIcon name="warning" size={15} aria-hidden="true" />
            <strong>批准外部写入{item.summary ? `：${item.summary}` : ""}</strong>
          </div>
          <pre>{commandText(item.input)}</pre>
          <p>这项操作会写入外部系统。仅批准这一次。停止回复、调用取消或等待超时后，审批会失效。</p>
          <div className="native-approval-actions">
            <button type="button" disabled={!!saving} onClick={() => decide(item, "deny")}>
              拒绝
            </button>
            <button type="button" className="primary" disabled={!!saving} onClick={() => decide(item, "allow")}>
              批准本次操作
            </button>
          </div>
        </div>
      ))}
      {error ? <p role="alert">{error}</p> : null}
    </section>
  );
}

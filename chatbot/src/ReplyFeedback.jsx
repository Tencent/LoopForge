import { useState } from "react";
import { api } from "./api";

// Main Bot 回答旁的“有用/没用”。只写治理反馈，不改变对话或知识正文。
export default function ReplyFeedback({ projectId, sessionId, message, index }) {
  const [sent, setSent] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  if (!projectId) return null;
  const messageId = message?.id || message?.createdAt || `message-${index}`;

  async function send(kind) {
    if (busy || sent) return;
    setBusy(true);
    setError("");
    try {
      await api("/api/knowledge/feedback", {
        method: "POST",
        body: JSON.stringify({
          projectId,
          kind,
          surface: "main_bot",
          sessionId: sessionId || "",
          messageId,
        }),
      });
      setSent(kind);
    } catch (err) {
      setError(err.message || "反馈失败");
      setBusy(false);
    }
  }

  if (sent) {
    return (
      <span className="reply-feedback is-sent" role="status">
        {sent === "helpful" ? "已反馈：有用" : "已反馈：没用"}
      </span>
    );
  }
  return (
    <span className="reply-feedback" role="group" aria-label="这条回答是否有用">
      <button type="button" disabled={busy} onClick={() => send("helpful")}>
        有用
      </button>
      <button type="button" disabled={busy} onClick={() => send("not_helpful")}>
        没用
      </button>
      {error ? <span className="reply-feedback-error">{error}</span> : null}
    </span>
  );
}

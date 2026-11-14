import React, { useEffect, useLayoutEffect, useState } from "react";
import { api, formatElapsed, sessionPath } from "./api";
import "./run-alerts.css";
import "./workspace-opening.css";

// Read-only polling never materializes or renews the workspace. Resolve the
// binding during a tool call: the chat's full session refresh happens at finish.
export default function WorkspaceOpening({
  sessionId,
  runId,
  busy,
  readOnly,
  onRetry,
  onProgress,
  interval = 2000,
}) {
  const [current, setCurrent] = useState(null);
  const [error, setError] = useState("");
  useLayoutEffect(() => {
    if (current) onProgress?.();
  }, [current, error, onProgress]);
  useEffect(() => {
    setCurrent(null);
    setError("");
    if (!sessionId && !runId) return;
    const controller = new AbortController();
    let timer;
    const poll = async () => {
      let again = busy;
      try {
        if (document.hidden) {
          again = true;
          return;
        }
        let id = runId;
        if (busy && sessionId) {
          const session = await api(sessionPath(sessionId), {
            signal: controller.signal,
          });
          id = session?.workspaceRunId || session?.runId;
        }
        if (!id) return;
        const run = await api(`/api/runs/${encodeURIComponent(id)}`, {
          signal: controller.signal,
        });
        if (controller.signal.aborted) return;
        const event = (run?.events || []).findLast(
          (item) => item.workspaceOpening,
        );
        setCurrent(
          run?.kind === "workspace" && event
            ? { runId: id, ...event.workspaceOpening }
            : null,
        );
        setError("");
        again ||= event?.workspaceOpening?.state === "running";
      } catch (e) {
        if (!controller.signal.aborted) {
          setError("工作区进度暂时无法更新，正在重新连接。");
          again = true;
        }
      } finally {
        if (!controller.signal.aborted && again)
          timer = setTimeout(poll, interval);
      }
    };
    void poll();
    return () => {
      controller.abort();
      clearTimeout(timer);
    };
  }, [sessionId, runId, busy, interval]);

  if (!current || (current.state === "ready" && !current.warning)) return null;
  const failed = current.state === "failed";
  const seconds = Math.max(
    0,
    Math.floor((Date.now() - Date.parse(current.startedAt)) / 1000),
  );
  return (
    <section
      className={`run-alert is-settled workspace-opening ${failed ? "" : "is-busy"}`}
      aria-label="工作区准备进度"
    >
      <div role="status" aria-live="polite">
        <strong>
          {failed
            ? "工作区准备失败"
            : current.state === "ready"
              ? "工作区环境提示"
              : current.message}
        </strong>
        {current.state === "running" && Number.isFinite(seconds) ? (
          <span className="workspace-opening-time">
            已用 {formatElapsed(seconds)}
            {current.timeoutSeconds
              ? ` · 本阶段限时 ${formatElapsed(current.timeoutSeconds)}`
              : ""}
          </span>
        ) : null}
      </div>
      {error ? <p role="status">{error}</p> : null}
      {current.warning ? <p>{current.warning}</p> : null}
      {failed || current.output ? (
        <pre className="workspace-opening-output">
          {failed ? current.message : current.output}
        </pre>
      ) : null}
      {failed ? <p>已保留当前工作区和完成的检出，修复原因后可重试。</p> : null}
      {failed && !readOnly ? (
        <button
          className="run-alert-btn primary"
          disabled={busy}
          onClick={() => onRetry?.(current.runId)}
        >
          请助手重试当前工作区
        </button>
      ) : null}
    </section>
  );
}

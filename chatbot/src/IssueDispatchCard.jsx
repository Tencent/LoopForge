import React, { useEffect, useState } from "react";
import HarnessIcon from "./HarnessIcon";
import { api } from "./api";
import { dispatchableIssueLoop } from "./issue-dispatch.js";
import { issueHref } from "./IssueContext";
import { operationError } from "./operations.js";

const Check = (props) => <HarnessIcon name="check" {...props} />;
const GitBranch = (props) => <HarnessIcon name="workflow" {...props} />;
const ExternalLink = (props) => (
  <HarnessIcon name="preview-expand" {...props} />
);

/**
 * Surfaces a one-click LOOP dispatch card directly below an assistant turn
 * that saved a requirement with a bound LOOP, across confirm / assisted / auto
 * approval modes. Hidden automatically once the requirement starts running,
 * closes, or opts out of LOOP delivery.
 */
export default function IssueDispatchCard({
  issueId,
  fallbackTitle = "",
  projectId = "",
  sessionId = "",
  readOnly = false,
  local = false,
  runId = "",
  refreshKey = 0,
  onDispatched,
  onOpenRun,
}) {
  const [snapshot, setSnapshot] = useState(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [dispatched, setDispatched] = useState(null);

  const eligible =
    Boolean(issueId && projectId && sessionId) && !readOnly && !local && !runId;

  useEffect(() => {
    setDispatched(null);
    setError("");
  }, [issueId, sessionId]);

  useEffect(() => {
    if (!eligible) {
      setSnapshot(null);
      return;
    }
    let current = true;
    api(`/api/issues/${encodeURIComponent(issueId)}/requirement`)
      .then((data) => {
        if (current) setSnapshot(data || null);
      })
      .catch(() => {
        if (current) setSnapshot(null);
      });
    return () => {
      current = false;
    };
  }, [eligible, issueId, projectId, refreshKey]);

  const target = dispatched ? null : dispatchableIssueLoop(snapshot, projectId);
  if (!eligible || (!target && !dispatched)) return null;

  const issue = dispatched?.issue || target.issue;
  const workflowId = dispatched?.workflowId || target.workflowId;
  const workflowName = dispatched?.workflowName || target.workflowName;
  const laneCount = dispatched?.laneCount || target.laneCount;
  const numberPrefix = issue?.number ? `#${issue.number} ` : "";
  const title = `${numberPrefix}${issue?.title || fallbackTitle || issueId}`;
  const href = issueHref(issue.id, issue.projectId || projectId);

  const subtitle = dispatched
    ? `已派发 LOOP「${workflowName}」`
    : laneCount > 1
      ? `需求已保存 · 已绑定 ${laneCount} 条 LOOP，首步「${workflowName}」`
      : `需求已保存 · 已绑定 LOOP「${workflowName}」`;

  async function handleDispatch() {
    if (busy || dispatched) return;
    setBusy(true);
    setError("");
    try {
      const task = await api(
        `/api/issues/${encodeURIComponent(issue.id)}/dispatch`,
        {
          method: "POST",
          headers: { "x-agentflow-chat-session": sessionId },
          body: JSON.stringify({ workflowId }),
        },
      );
      const record = {
        issue,
        workflowId,
        workflowName,
        laneCount,
        runId: task?.runId || "",
        taskId: task?.id || "",
      };
      setDispatched(record);
      onDispatched?.(task, record);
    } catch (e) {
      setError(operationError(e));
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className="draft-card issue-dispatch-card">
      <a
        className="draft-title"
        href={href}
        target="_blank"
        rel="noopener noreferrer"
      >
        <GitBranch size={17} />
        <span>
          <strong>{title}</strong>
          <small>{subtitle}</small>
        </span>
        <ExternalLink size={16} />
      </a>
      {error ? (
        <p className="error" role="alert">
          {error}
        </p>
      ) : null}
      <div className="issue-dispatch-actions">
        {dispatched ? (
          <>
            <button type="button" className="quiet-button" disabled>
              <Check size={14} />
              已派发
            </button>
            {dispatched.runId && onOpenRun ? (
              <button
                type="button"
                className="quiet-button"
                onClick={() => onOpenRun(dispatched.runId, workflowName)}
              >
                查看运行
              </button>
            ) : null}
          </>
        ) : (
          <>
            <button
              type="button"
              className="primary-button"
              disabled={busy}
              onClick={handleDispatch}
            >
              {busy ? "派发中…" : "派发 LOOP"}
            </button>
            <a
              className="quiet-button"
              href={href}
              target="_blank"
              rel="noopener noreferrer"
            >
              查看需求
            </a>
          </>
        )}
      </div>
    </div>
  );
}

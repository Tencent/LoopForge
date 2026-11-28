import React, { useEffect, useState } from "react";
import HarnessIcon from "./HarnessIcon";
import {
  canPassScriptNode,
  canReplayNode,
  decideRunNode,
} from "./sop-review.js";

export default function RunNodeActions({ run, node, definition, onChanged }) {
  const [editing, setEditing] = useState("");
  const [opinion, setOpinion] = useState("");
  const [reason, setReason] = useState("");
  const [retryNote, setRetryNote] = useState("");
  const [busy, setBusy] = useState("");
  const [error, setError] = useState("");
  const [notice, setNotice] = useState("");

  useEffect(() => {
    setEditing("");
    setError("");
  }, [run.id, node.nodeId, node.status, node.attempt, run.pauseRequested]);

  const paused = !!run.pauseRequested;
  const waiting = node.status === "waiting_approval" && !paused;
  const failed = node.status === "failed" && run.status === "failed" && !paused;

  if (!paused && !waiting && !failed) {
    return notice ? (
      <p className="sop-inline-note" role="status">
        {notice}
      </p>
    ) : null;
  }

  const exhausted =
    waiting &&
    /max turns|轮次用尽|是否重试/i.test(
      [node.error, node.activity].filter(Boolean).join(" "),
    );
  const target = run.workflowSnapshot?.nodes?.find(
    ({ id }) => id === definition?.rejectTo,
  );
  const canOperate = run.reviewAccess?.canOperate;
  const canAddOpinion = !["script", "command"].includes(definition?.type);
  const canRetry = canReplayNode(run, node);
  const canPassScript = canPassScriptNode(run, node, definition);

  const instruction = paused
    ? "流程已暂停执行，可继续当前节点或调整说明后恢复运行。"
    : failed
      ? node.gateResult?.reason?.trim() ||
        node.error?.trim() ||
        "节点执行失败，可查看过程和产物后重试。"
      : node.gateResult?.actionRequired?.trim() ||
        definition?.taskPrompt?.trim() ||
        node.gateResult?.reason?.trim() ||
        (exhausted ? "这个节点已达到对话轮次上限。" : "请检查交付物并确认。");

  async function submit(action) {
    if (busy) return;
    setBusy("正在提交…");
    setError("");
    const body = {};
    if (action === "approve") {
      body.context = opinion.trim();
    } else if (action === "reject") {
      body.targetId = target.id;
      body.reason = reason.trim();
    } else if (action === "retry") {
      const note = retryNote.trim();
      body.reason = exhausted
        ? "对话轮次用尽后人工确认重试"
        : note || (failed ? "失败后人工重跑当前节点" : "人工确认重跑当前节点");
      if (note) body.context = note;
    } else if (action === "resume") {
      const note = opinion.trim();
      if (note) body.context = note;
    } else if (action === "pass") {
      const note = opinion.trim();
      if (note) body.context = note;
    }
    try {
      await decideRunNode(run, node, action, body, () =>
        setBusy("正在恢复工作区…"),
      );
      setNotice(
        {
          approve: "已通过审核",
          reject: "已打回修改",
          retry: "已提交重跑",
          "decline-retry": "已结束重试",
          resume: "已恢复运行",
          pass: "已手动通过，流程继续",
        }[action],
      );
      setEditing("");
      setOpinion("");
      setReason("");
      setRetryNote("");
      onChanged?.();
    } catch (e) {
      setError(e.message);
      if ([400, 409].includes(e.status)) onChanged?.();
    } finally {
      setBusy("");
    }
  }

  const iconName = paused ? "pause" : failed ? "alert" : "user-hold";
  const toneClass = paused ? "is-paused" : failed ? "is-failed" : "";
  const ariaLabel = paused ? "运行已暂停" : failed ? "失败处理" : "人工审核";

  return (
    <section
      className={`sop-review-decision ${toneClass}`.trim()}
      aria-label={ariaLabel}
    >
      <HarnessIcon name={iconName} className="sop-decision-icon" size={17} />
      <div className="sop-decision-body">
        <p className="sop-decision-instruction" title={instruction}>
          {instruction}
        </p>
        {!editing && opinion.trim() && waiting ? (
          <p className="sop-inline-note">
            已附审核意见，点击「通过并继续」将一并提交。
          </p>
        ) : null}
        {canOperate ? (
          <>
            {editing === "reject" ||
            editing === "opinion" ||
            editing === "retry" ? (
              <label className="sop-feedback">
                <span>
                  {editing === "reject"
                    ? "打回原因"
                    : editing === "retry"
                      ? "重跑说明（选填）"
                      : "审核意见（选填）"}
                </span>
                <textarea
                  rows={2}
                  autoFocus
                  disabled={!!busy}
                  value={
                    editing === "reject"
                      ? reason
                      : editing === "retry"
                        ? retryNote
                        : opinion
                  }
                  onChange={(e) =>
                    (editing === "reject"
                      ? setReason
                      : editing === "retry"
                        ? setRetryNote
                        : setOpinion)(e.target.value)
                  }
                  placeholder={
                    editing === "reject"
                      ? "说明需要修改的内容"
                      : editing === "retry"
                        ? "告诉节点本次重跑要注意或修复什么"
                        : "留给下游节点的说明"
                  }
                />
              </label>
            ) : null}
            <div className="sop-decision-buttons">
              {editing === "reject" ? (
                <>
                  <button
                    type="button"
                    className="sop-action-button primary"
                    disabled={!!busy || !reason.trim()}
                    onClick={() => submit("reject")}
                  >
                    确认打回
                  </button>
                  <button
                    type="button"
                    className="quiet-button"
                    disabled={!!busy}
                    onClick={() => setEditing("")}
                  >
                    取消
                  </button>
                </>
              ) : editing === "retry" ? (
                <>
                  <button
                    type="button"
                    className="sop-action-button primary"
                    disabled={!!busy}
                    onClick={() => submit("retry")}
                  >
                    <HarnessIcon name="refresh" size={14} />
                    {retryNote.trim() ? "补充信息并重跑" : "确认重跑本节点"}
                  </button>
                  <button
                    type="button"
                    className="quiet-button"
                    disabled={!!busy}
                    onClick={() => setEditing("")}
                  >
                    取消
                  </button>
                </>
              ) : editing === "pass-confirm" ? (
                <>
                  <button
                    type="button"
                    className="sop-action-button primary"
                    disabled={!!busy}
                    onClick={() => submit("pass")}
                  >
                    <HarnessIcon name="check" size={14} />
                    确认通过，跳过不重跑
                  </button>
                  <button
                    type="button"
                    className="quiet-button"
                    disabled={!!busy}
                    onClick={() => setEditing("")}
                  >
                    取消
                  </button>
                </>
              ) : paused ? (
                <>
                  <button
                    type="button"
                    className="sop-action-button primary"
                    disabled={!!busy}
                    onClick={() => submit("resume")}
                  >
                    <HarnessIcon name="play-continue" size={14} />
                    {opinion.trim() ? "补充信息并继续" : "继续运行"}
                  </button>
                  {canPassScript ? (
                    <button
                      type="button"
                      className="sop-action-button"
                      disabled={!!busy}
                      onClick={() => setEditing("pass-confirm")}
                    >
                      手动通过，跳过该脚本
                    </button>
                  ) : null}
                  {canAddOpinion && editing !== "opinion" ? (
                    <button
                      type="button"
                      className="quiet-button"
                      disabled={!!busy}
                      onClick={() => setEditing("opinion")}
                    >
                      补充说明
                    </button>
                  ) : editing === "opinion" ? (
                    <button
                      type="button"
                      className="quiet-button"
                      disabled={!!busy}
                      onClick={() => setEditing("")}
                    >
                      收起
                    </button>
                  ) : null}
                </>
              ) : failed ? (
                <>
                  {canRetry ? (
                    <button
                      type="button"
                      className="sop-action-button primary"
                      disabled={!!busy}
                      onClick={() =>
                        canAddOpinion && !editing
                          ? setEditing("retry")
                          : submit("retry")
                      }
                    >
                      <HarnessIcon name="refresh" size={14} />
                      {canAddOpinion ? "补充信息或重跑" : "重试节点"}
                    </button>
                  ) : null}
                  {canPassScript ? (
                    <button
                      type="button"
                      className="sop-action-button"
                      disabled={!!busy}
                      onClick={() => setEditing("pass-confirm")}
                    >
                      手动通过，继续下游
                    </button>
                  ) : null}
                </>
              ) : exhausted ? (
                <>
                  {canRetry ? (
                    <button
                      type="button"
                      className="sop-action-button"
                      disabled={!!busy}
                      onClick={() => submit("retry")}
                    >
                      重试节点
                    </button>
                  ) : null}
                  <button
                    type="button"
                    className="quiet-button"
                    disabled={!!busy}
                    onClick={() => submit("decline-retry")}
                  >
                    结束重试
                  </button>
                </>
              ) : (
                <>
                  <button
                    type="button"
                    className="sop-action-button primary"
                    disabled={!!busy}
                    onClick={() => submit("approve")}
                  >
                    <HarnessIcon name="check" size={14} />
                    通过并继续
                  </button>
                  {target ? (
                    <button
                      type="button"
                      className="sop-action-button"
                      title={"打回到" + (target.name || target.id)}
                      disabled={!!busy}
                      onClick={() => setEditing("reject")}
                    >
                      打回
                    </button>
                  ) : null}
                  {canRetry ? (
                    <button
                      type="button"
                      className="sop-action-button"
                      disabled={!!busy}
                      onClick={() => setEditing("retry")}
                    >
                      重跑本节点
                    </button>
                  ) : null}
                  {canAddOpinion ? (
                    editing === "opinion" ? (
                      <button
                        type="button"
                        className="quiet-button"
                        disabled={!!busy}
                        onClick={() => setEditing("")}
                      >
                        收起意见
                      </button>
                    ) : (
                      <button
                        type="button"
                        className="quiet-button"
                        disabled={!!busy}
                        onClick={() => setEditing("opinion")}
                      >
                        补充意见
                      </button>
                    )
                  ) : null}
                </>
              )}
            </div>
          </>
        ) : null}
        {run.reviewAccess?.blockReason ? (
          <p className="sop-inline-note">{run.reviewAccess.blockReason}</p>
        ) : null}
        {busy || notice ? (
          <p className="sop-inline-note" role="status">
            {busy || notice}
          </p>
        ) : null}
        {error ? (
          <p className="sop-inline-error" role="alert">
            {error}
          </p>
        ) : null}
      </div>
    </section>
  );
}

import React, { useState } from "react";
import HarnessIcon from "./HarnessIcon";
import { canReplayNode, decideRunNode } from "./sop-review.js";

export default function RunNodeActions({ run, node, definition, onChanged }) {
  const [editing, setEditing] = useState("");
  const [opinion, setOpinion] = useState("");
  const [reason, setReason] = useState("");
  const [busy, setBusy] = useState("");
  const [error, setError] = useState("");
  const [notice, setNotice] = useState("");
  const waiting = node.status === "waiting_approval" && !run.pauseRequested;

  if (!waiting) {
    return notice ? (
      <p className="sop-inline-note" role="status">
        {notice}
      </p>
    ) : null;
  }

  const exhausted = /max turns|轮次用尽|是否重试/i.test(
    [node.error, node.activity].filter(Boolean).join(" "),
  );
  const target = run.workflowSnapshot.nodes.find(
    ({ id }) => id === definition.rejectTo,
  );
  const instruction =
    node.gateResult?.actionRequired?.trim() ||
    definition.taskPrompt?.trim() ||
    node.gateResult?.reason?.trim() ||
    (exhausted ? "这个节点已达到对话轮次上限。" : "请检查交付物并确认。");
  const canOperate = run.reviewAccess?.canOperate;
  const canAddOpinion = !["script", "command"].includes(definition.type);

  async function submit(action) {
    if (busy) return;
    setBusy("正在提交…");
    setError("");
    const body = { context: opinion.trim() };
    if (action === "reject") {
      body.targetId = target.id;
      body.reason = reason.trim();
    }
    try {
      await decideRunNode(run, node, action, body, () =>
        setBusy("正在恢复工作区…"),
      );
      setNotice(
        {
          approve: "已通过审核",
          reject: "已打回修改",
          retry: "已提交重试",
          "decline-retry": "已结束重试",
        }[action],
      );
      setEditing("");
      setOpinion("");
      setReason("");
      onChanged?.();
    } catch (e) {
      setError(e.message);
      if ([400, 409].includes(e.status)) onChanged?.();
    } finally {
      setBusy("");
    }
  }

  return (
    <section className="sop-review-decision" aria-label="人工审核">
      <HarnessIcon name="user-hold" className="sop-decision-icon" size={17} />
      <div className="sop-decision-body">
        <p className="sop-decision-instruction">{instruction}</p>
        {canOperate ? (
          <>
            {editing ? (
              <label className="sop-feedback">
                <span>
                  {editing === "reject" ? "打回原因" : "审核意见（选填）"}
                </span>
                <textarea
                  rows={2}
                  autoFocus
                  disabled={!!busy}
                  value={editing === "reject" ? reason : opinion}
                  onChange={(e) =>
                    (editing === "reject" ? setReason : setOpinion)(
                      e.target.value,
                    )
                  }
                  placeholder={
                    editing === "reject"
                      ? "说明需要修改的内容"
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
              ) : exhausted ? (
                <>
                  {canReplayNode(run, node) ? (
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
                  {canAddOpinion && !editing ? (
                    <button
                      type="button"
                      className="quiet-button"
                      disabled={!!busy}
                      onClick={() => setEditing("opinion")}
                    >
                      补充意见
                    </button>
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

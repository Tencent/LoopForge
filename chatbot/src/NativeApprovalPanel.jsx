import React, { useEffect, useState } from "react";
import HarnessIcon from "./HarnessIcon";
import "./native-approval.css";

// The text the speaker is asked to approve, kept byte-for-byte as the server
// sent it: the server already redacted it, so reformatting could only leak or
// mislead.
export function approvalCommandText(input) {
  const command = input?.command ?? input?.cmd ?? input?.commands;
  if (typeof command === "string") return command;
  if (Array.isArray(command)) return command.map(String).join(" ");
  return JSON.stringify(input, null, 2);
}

export function hasHiddenFields(text) {
  return /\[redacted\]/.test(String(text || ""));
}

const repoWrites = [
  "GitHub CLI 写操作",
  "GitHub API 写请求",
  "GitLab CLI 写操作",
  "GitLab API 写请求",
  "Gitea CLI 写操作",
];
const releases = ["发布软件包", "推送镜像"];

// The summary the server sends names the kind of external write; this only
// words it for the card. It never decides whether the write is allowed.
export function approvalKind(summary) {
  const text = String(summary || "").trim();
  if (text.startsWith("git push"))
    return { kind: "git push", risk: "会把本地提交推送到远端仓库，推送后无法在平台侧撤销。" };
  if (repoWrites.some((prefix) => text.startsWith(prefix)))
    return {
      kind: text,
      risk: "会创建或修改远端仓库记录（PR、Issue、评论、Release 等），对他人可见。",
    };
  if (text.startsWith("HTTP 写请求"))
    return { kind: "HTTP 写请求", risk: "会向外部服务发起写请求，可能修改远端数据。" };
  if (releases.some((prefix) => text.startsWith(prefix)))
    return { kind: text, risk: "会把产物发布到外部仓库或镜像库，通常不可回收。" };
  if (text.startsWith("集群或基础设施变更"))
    return { kind: text, risk: "会改动外部集群或基础设施资源。" };
  if (text.includes(" · "))
    return { kind: "外部 MCP 写入", risk: "会通过平台网关写入第三方系统，影响该 MCP 服务中的数据。" };
  return { kind: "外部写入", risk: "会写入本会话沙箱之外的系统。" };
}

function waitedSeconds(item, now) {
  const created = Date.parse(item?.createdAt);
  if (!Number.isFinite(created) || created <= 0) return null;
  const seconds = Math.floor((now - created) / 1000);
  return seconds >= 0 ? seconds : null;
}

// A paused external write, shown where the reply is. The data belongs to
// useNativeApprovals so the status line and the tool rows agree with it.
export default function NativeApprovalPanel({ approvals, onChange }) {
  const items = approvals?.items || [];
  const [now, setNow] = useState(Date.now);
  useEffect(() => {
    if (!items.length) return undefined;
    setNow(Date.now());
    const timer = setInterval(() => setNow(Date.now()), 1000);
    return () => clearInterval(timer);
  }, [items.length]);
  const pendingIds = items.map((item) => item.id).join(",");
  useEffect(() => {
    onChange?.();
  }, [pendingIds]);
  const error = approvals?.error || "";
  const expired = approvals?.expired || "";
  const saving = approvals?.saving || "";
  const decide = approvals?.decide;
  if (!items.length && !error && !expired) return null;
  return (
    <section
      className="native-approvals"
      aria-label="待审批的外部写入"
      aria-live="polite"
      data-count={items.length}
    >
      {items.map((item) => {
        const text = approvalCommandText(item.input);
        const { kind, risk } = approvalKind(item.summary);
        const waited = waitedSeconds(item, now);
        return (
          <div className="native-approval" key={item.id} data-approval-id={item.id} data-tool={item.tool}>
            <div className="native-approval-head">
              <span className="native-approval-badge">待审批</span>
              <strong className="native-approval-summary">{item.summary || "外部写入"}</strong>
              <span className="native-approval-kind">{kind}</span>
            </div>
            <div className="native-approval-meta">
              <span className="native-approval-tool" title={item.tool}>
                {item.tool}
              </span>
              {waited !== null ? (
                <span className="native-approval-wait">已等待 {waited} 秒</span>
              ) : null}
            </div>
            <pre className="native-approval-input">{text}</pre>
            {hasHiddenFields(text) ? (
              <p className="native-approval-hidden">包含已隐藏的敏感字段</p>
            ) : null}
            <p className="native-approval-risk">
              <HarnessIcon name="warning" size={13} aria-hidden="true" />
              {risk}
            </p>
            <div className="native-approval-actions">
              <button
                type="button"
                className="is-deny"
                disabled={saving === item.id}
                onClick={() => decide?.(item, "deny")}
              >
                拒绝
              </button>
              <button
                type="button"
                className="primary"
                disabled={saving === item.id}
                onClick={() => decide?.(item, "allow")}
              >
                批准本次操作
              </button>
            </div>
            <p className="native-approval-expiry">
              停止回复、调用取消或等待超时后，本次审批会失效。
            </p>
          </div>
        );
      })}
      {error ? (
        <p role="alert" className="native-approval-error">
          {error}
        </p>
      ) : null}
      {expired ? (
        <p role="status" className="native-approval-expired">
          {expired}
        </p>
      ) : null}
    </section>
  );
}

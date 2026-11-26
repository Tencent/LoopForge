import React from "react";
import HarnessIcon from "./HarnessIcon";
import { RichContent } from "./RichContent";

const CHANNEL_META = {
  wecom: { short: "企微", label: "企业微信" },
  feishu: { short: "飞书", label: "飞书" },
  dingtalk: { short: "钉钉", label: "钉钉" },
};

const WRITE_MODE_LABEL = {
  readonly: "只读查询",
  confirm: "网页确认",
  auto: "自动执行",
};

const INTERACTION_STATUS = {
  queued: { label: "排队中", tone: "neutral" },
  running: { label: "处理中", tone: "info" },
  ready: { label: "待发送", tone: "info" },
  sending: { label: "发送中", tone: "info" },
  replied: { label: "已回复", tone: "success" },
  reply_failed: { label: "回复失败", tone: "error" },
  reply_unknown: { label: "发送状态待核对", tone: "warn" },
  cancelled: { label: "已取消", tone: "neutral" },
};

export function imChannelInfo(channel) {
  return CHANNEL_META[channel] || CHANNEL_META.wecom;
}

export function imConversationTitle(conv) {
  if (!conv) return "IM 会话";
  const kind = conv.chatType === "group" ? "群聊" : "单聊";
  if (conv.botName) return `${conv.botName} · ${kind}`;
  return `${imChannelInfo(conv.channel).label}${kind}`;
}

function formatTime(value) {
  if (!value) return "";
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return "";
  return date.toLocaleString("zh-CN", {
    month: "2-digit",
    day: "2-digit",
    hour: "2-digit",
    minute: "2-digit",
    hour12: false,
  });
}

export default function ImConversationPane({
  summary,
  detail,
  loading,
  error,
  continuing,
  onContinue,
  onRefresh,
}) {
  const conv = detail?.conversation || summary;
  const messages = Array.isArray(detail?.messages) ? detail.messages : [];
  const channel = imChannelInfo(conv?.channel);
  const chatKind = conv?.chatType === "group" ? "群聊" : "单聊";
  const modeLabel = WRITE_MODE_LABEL[conv?.writeApproval] || "只读查询";

  return (
    <div className="im-pane">
      <div className="im-context-banner" role="region" aria-label="IM 来源信息">
        <div className="im-context-meta">
          <span className="im-channel-pill" data-channel={conv?.channel || "wecom"}>
            {channel.label} · {chatKind}
          </span>
          {conv?.botName ? (
            <span className="im-context-item">Bot：{conv.botName}</span>
          ) : null}
          {conv?.projectName ? (
            <span className="im-context-item">项目：{conv.projectName}</span>
          ) : null}
          <span className="im-context-item">权限：{modeLabel}</span>
        </div>
        <div className="im-context-actions">
          <button
            type="button"
            className="im-action-btn"
            onClick={onRefresh}
            disabled={loading}
          >
            <HarnessIcon name="refresh" size={13} />
            刷新
          </button>
          <button
            type="button"
            className="im-action-btn is-primary"
            onClick={onContinue}
            disabled={continuing || !conv?.id}
          >
            <HarnessIcon name="branch" size={13} />
            {continuing ? "正在创建会话…" : "转入个人会话继续"}
          </button>
        </div>
      </div>

      <div className="im-messages-scroll">
        {loading && !messages.length ? (
          <div className="im-pane-state">
            <HarnessIcon name="arc-spinner" size={16} motion="busy" />
            <span>正在加载 IM 对话记录…</span>
          </div>
        ) : error ? (
          <div className="im-pane-state is-error" role="alert">
            <HarnessIcon name="alert" size={16} />
            <span>{error}</span>
          </div>
        ) : !messages.length ? (
          <div className="im-pane-state">
            <span>该 IM 会话暂时没有可展示的对话消息。</span>
          </div>
        ) : (
          <div className="im-messages-inner">
            {messages.map((item) => {
              const status = INTERACTION_STATUS[item.status];
              return (
                <div key={item.id} className="im-turn">
                  {item.content ? (
                    <article className="message user">
                      <div className="message-body">
                        <div className="message-author">
                          <span>{item.senderId || "IM 用户"}</span>
                          <span className="im-author-tag">
                            {channel.short} · {chatKind}
                          </span>
                          <time className="message-time">{formatTime(item.createdAt)}</time>
                        </div>
                        <div className="user-bubble">{item.content}</div>
                        {item.quote ? (
                          <blockquote className="im-quote">
                            引用：{item.quote}
                          </blockquote>
                        ) : null}
                      </div>
                    </article>
                  ) : null}
                  {item.reply || item.lastError || (status && item.status !== "replied") ? (
                    <article className="message assistant">
                      <div className="message-body">
                        <div className="message-author">
                          <span>{conv?.botName || "AI 助手"}</span>
                          {status && item.status !== "replied" ? (
                            <span className="im-status-tag" data-tone={status.tone}>
                              {status.label}
                            </span>
                          ) : null}
                          <time className="message-time">{formatTime(item.updatedAt)}</time>
                        </div>
                        {item.reply ? (
                          <div className="im-reply-content">
                            <RichContent message={item.reply} projectId={conv?.projectId || ""} />
                          </div>
                        ) : null}
                        {item.lastError ? (
                          <p className="im-reply-error">{item.lastError}</p>
                        ) : null}
                      </div>
                    </article>
                  ) : null}
                </div>
              );
            })}
          </div>
        )}
      </div>

      <div className="im-readonly-footer">
        <span>
          为避免误向{channel.label}{chatKind}推送消息，IM 会话在工作台默认只读回看。
        </span>
        <button
          type="button"
          className="im-action-btn is-primary"
          onClick={onContinue}
          disabled={continuing || !conv?.id}
        >
          {continuing ? "正在创建…" : "带入上下文到「个人」继续"}
        </button>
      </div>
    </div>
  );
}

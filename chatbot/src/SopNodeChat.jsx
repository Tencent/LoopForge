import React, { useEffect, useRef, useState } from "react";
import {
  api,
  isProposalTimeout,
  confirmationCaption,
  sessionPath,
} from "./api";
import { activityForReply, generationStored } from "./activity";
import {
  AssistantTurn,
  LiveMessage,
  MessageTime,
  UserTurn,
  replyText,
} from "./ChatTurns";
import { CLIInstallAction } from "./cli-install.jsx";
import { openCredentialSettings } from "./credentials";
import HarnessIcon from "./HarnessIcon";
import MessageInfo from "./MessageInfo";
import NativeApprovalPanel from "./NativeApprovalPanel";
import { CopyButton } from "./RichContent";
import { findNodeConversation, openNodeConversation } from "./sop-review.js";
import { CredentialAction } from "./ui";
import { useChat } from "./useChat";

export default function SopNodeChat({
  run,
  node,
  text,
  onTextChange,
  disabledReason,
  standalone,
  children,
}) {
  const [sessionId, setSessionId] = useState(undefined);
  const [error, setError] = useState("");
  const [reload, retry] = useState(0);
  useEffect(() => {
    if (disabledReason) return;
    let active = true;
    findNodeConversation(run, node)
      .then((session) => {
        if (active) {
          setSessionId(session?.id || null);
          setError("");
        }
      })
      .catch((error) => {
        if (active) setError(error.message);
      });
    return () => {
      active = false;
    };
  }, [
    run.id,
    node.nodeId,
    node.attempt,
    node.reworkCount,
    node.status,
    disabledReason,
    reload,
  ]);

  return (
    <section
      className={
        standalone ? "sop-node-chat conversation-main" : "sop-node-chat"
      }
      aria-label="节点会话"
    >
      {disabledReason || error || sessionId === undefined ? (
        <NodeChatLayout
          composer={
            <>
              <NodeQuestionForm
                text={text}
                onTextChange={onTextChange}
                disabled
                placeholder={
                  disabledReason || (error ? "会话加载失败" : "正在加载会话…")
                }
              />
              {error && !disabledReason ? (
                <p className="sop-inline-error" role="alert">
                  {error}{" "}
                  <button
                    type="button"
                    className="quiet-button"
                    onClick={() => retry((n) => n + 1)}
                  >
                    重试加载
                  </button>
                </p>
              ) : null}
            </>
          }
        >
          {children}
        </NodeChatLayout>
      ) : (
        <NodeComposer
          sessionId={sessionId}
          projectId={run.executionContext?.projectId}
          runId={run.id}
          createSession={async () =>
            api(sessionPath((await openNodeConversation(run, node)).id))
          }
          text={text}
          onTextChange={onTextChange}
        >
          {children}
        </NodeComposer>
      )}
    </section>
  );
}

function NodeChatLayout({ children, composer }) {
  return (
    <>
      <div className="message-scroll">
        <div className="message-column">{children}</div>
      </div>
      <div className="composer-region">
        <div className="composer-column">{composer}</div>
      </div>
    </>
  );
}

function NodeQuestionForm({
  text,
  onTextChange,
  send,
  stop,
  disabled = false,
  placeholder = "询问此节点…",
}) {
  return (
    <form
      className="composer"
      onSubmit={(e) => {
        e.preventDefault();
        send?.();
      }}
    >
      <textarea
        aria-label="询问此节点"
        placeholder={placeholder}
        disabled={disabled}
        rows={2}
        value={text}
        onChange={(e) => onTextChange(e.target.value)}
        onKeyDown={(e) => {
          if (e.key === "Enter" && !e.shiftKey && !e.nativeEvent.isComposing) {
            e.preventDefault();
            send?.();
          }
        }}
      />
      <footer className="composer-toolbar">
        <span className="toolbar-spacer" />
        {stop ? (
          <button
            type="button"
            className="send-button stop"
            aria-label="停止节点回复"
            disabled={stop.disabled}
            onClick={stop.action}
          >
            <HarnessIcon name="chat-stop" size={14} />
          </button>
        ) : (
          <button
            type="submit"
            className="send-button"
            aria-label="发送节点消息"
            disabled={!send}
          >
            <HarnessIcon name="chat-send" size={20} />
          </button>
        )}
      </footer>
    </form>
  );
}

function NodeComposer({
  sessionId,
  projectId,
  runId,
  createSession,
  text,
  onTextChange,
  children,
}) {
  const chat = useChat({ sessionId, projectId, runId, createSession });
  const follow = useRef(false);
  const bottom = useRef(null);
  const saved = chat.session?.messages || [];
  // The synthetic task and result already appear in the node's transcript.
  const firstQuestion = saved.findIndex(
    (message) => message.role === "user" && message.authorId,
  );
  const messages = firstQuestion < 0 ? [] : saved.slice(firstQuestion);
  const loading =
    !chat.ready || (!!chat.activeId && !chat.session) || chat.replyStatePending;
  // The failed reply already explains this error and offers its fix.
  const failureShown =
    !!chat.generation?.failure &&
    chat.error === chat.generation.error &&
    messages.some(
      (message) =>
        message.generationId === chat.generation.id && message.failure,
    );
  const canSend =
    !loading &&
    !!chat.executor &&
    !chat.missingServerCLI &&
    !chat.busy &&
    !!text.trim();
  function followReply() {
    const scroll = bottom.current?.closest(".message-scroll");
    if (follow.current && scroll) scroll.scrollTop = scroll.scrollHeight;
  }
  useEffect(followReply, [messages.length, chat.busy, chat.live.answer]);
  async function send() {
    if (!canSend) return;
    follow.current = true;
    const sent = await chat.send({ text });
    if (sent) onTextChange((current) => (current === text ? "" : current));
  }

  return (
    <NodeChatLayout
      composer={
        <>
          <NodeQuestionForm
            text={text}
            onTextChange={onTextChange}
            send={canSend ? send : undefined}
            stop={
              chat.busy
                ? {
                    action: chat.stop,
                    disabled:
                      chat.stopping || chat.generation?.status !== "running",
                  }
                : undefined
            }
          />
          {chat.error && !failureShown ? (
            <div className="sop-inline-error" role="alert">
              <span>{chat.error}</span>
              <CredentialAction text={chat.error} />
              <CLIInstallAction
                text={chat.error}
                onResult={chat.adoptCLIInstall}
              />
            </div>
          ) : null}
          {chat.ready && !chat.executor ? (
            <button
              type="button"
              className="quiet-button sop-inline-note"
              onClick={() => openCredentialSettings("executors")}
            >
              配置模型后可发送
            </button>
          ) : null}
        </>
      }
    >
      {children}
      {messages.map((message, index) => {
        const previous = messages[index - 1];
        if (isProposalTimeout(message, previous)) return null;
        const confirmation = confirmationCaption(message, previous);
        if (confirmation)
          return (
            <div
              key={index}
              className="confirmation-note"
              data-message-index={index}
            >
              <HarnessIcon name="check" size={14} aria-hidden="true" />
              {confirmation}
            </div>
          );
        const user = message.role === "user";
        const steps = user
          ? []
          : activityForReply(message, chat.generation, chat.live.activity);
        return (
          <article
            className={`message ${message.role}`}
            key={index}
            data-message-index={index}
            aria-label={user ? "追问" : "Agent 回复"}
          >
            {user ? (
              <UserTurn
                message={message}
                messages={messages}
                index={index}
                session={chat.session}
                users={chat.users}
              >
                <div className="user-actions">
                  <CopyButton value={message.content} label="复制消息" />
                </div>
              </UserTurn>
            ) : (
              <>
                <AssistantTurn
                  message={message}
                  steps={steps}
                  projectId={projectId}
                  sessionId={chat.session?.id}
                  readOnly={chat.readOnly}
                  onCLIInstall={chat.adoptCLIInstall}
                />
                <div className="message-actions">
                  <MessageTime messages={messages} index={index} />
                  <CopyButton
                    value={replyText(message, steps)}
                    label="复制回复"
                  />
                  <MessageInfo
                    message={message}
                    sessionExecutor={chat.session?.executor}
                  />
                </div>
              </>
            )}
          </article>
        );
      })}
      {chat.busy ? (
        <LiveMessage
          live={chat.live}
          generation={chat.generation}
          readOnly={chat.readOnly}
        />
      ) : chat.generation &&
        !generationStored(messages, chat.generation) &&
        !["completed", "running"].includes(chat.generation.status) &&
        !messages.at(-1)?.unfinished &&
        (chat.live.activity?.length || chat.live.answer) ? (
        <LiveMessage
          live={chat.live}
          generation={chat.generation}
          readOnly={chat.readOnly}
          finished
        />
      ) : null}
      <NativeApprovalPanel chat={chat} onChange={followReply} />
      <div ref={bottom} />
    </NodeChatLayout>
  );
}

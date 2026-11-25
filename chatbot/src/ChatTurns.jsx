import React from "react";
import ActivityTrace, { ReplyTimeline } from "./ActivityTrace";
import GenerationDiagnostics from "./GenerationDiagnostics";
import HarnessIcon from "./HarnessIcon";
import { Avatar, authorFor } from "./Participants";
import { RichContent } from "./RichContent";
import { finalReply, hasTextSegments, streamingAnswerOpen } from "./activity";
import { parseReply } from "./api";
import { elementTitle } from "./element-context.js";
import { fullTime, messageSentAt, messageTimeLabel } from "./message-info.js";

export function MessageTime({ messages, index }) {
  const date = messageSentAt(messages, index);
  if (!date) return null;
  return (
    <time
      className="message-time"
      dateTime={date.toISOString()}
      title={fullTime(date)}
    >
      {messageTimeLabel(date)}
    </time>
  );
}
export function UserTurn({
  message,
  messages,
  index,
  session,
  users,
  author = authorFor(message, session, users),
  body,
  children,
}) {
  return (
    <div className="message-body">
      <div className="message-author">
        <Avatar person={author} />
        <span>{author.name}</span>
        <MessageTime messages={messages} index={index} />
      </div>
      <div className="user-bubble">{body ?? message.content}</div>
      {message.references?.length ? (
        <div className="message-references">
          {message.references.map((ref) => (
            <span
              key={ref.type + ref.id}
              title={ref.type === "element" ? elementTitle(ref) : undefined}
            >
              {ref.type === "element" ? (
                <HarnessIcon name="crosshair" size={12} />
              ) : (
                "@"
              )}
              {ref.name || ref.id}
            </span>
          ))}
        </div>
      ) : null}
      {children}
    </div>
  );
}

export function replyText(message, steps) {
  const text = parseReply(message).message;
  return hasTextSegments(steps) ? finalReply(text, steps) : text;
}

function ReplyHeading({ message, steps }) {
  return (
    <div className="reply-heading">
      <ActivityTrace
        steps={steps}
        startedAt={message.startedAt}
        finishedAt={message.createdAt}
        interrupted={!!message.unfinished}
      />
    </div>
  );
}
export function AssistantTurn({
  message,
  steps,
  assets,
  projectId,
  sessionId,
  local,
  readOnly,
  onCLIInstall,
}) {
  const reply = parseReply(message);
  const answer = hasTextSegments(steps)
    ? finalReply(reply.message, steps)
    : message;
  return (
    <>
      {hasTextSegments(steps) ? (
        <ReplyTimeline
          steps={steps}
          startedAt={message.startedAt}
          finishedAt={message.createdAt}
        />
      ) : (
        <ReplyHeading message={message} steps={steps} />
      )}
      <div className="message-body">
        <RichContent message={answer} assets={assets} projectId={projectId} />
      </div>
      {message.failure && message.generationId && !local ? (
        <GenerationDiagnostics
          sessionId={sessionId}
          generationId={message.generationId}
          failure={message.failure}
          onCLIInstall={onCLIInstall}
          readOnly={readOnly}
        />
      ) : message.unfinished ? (
        <p className="reply-stopped">本轮未完成 · {message.unfinished}</p>
      ) : reply.stopped ? (
        <p className="reply-stopped">已停止，以上是停止前的内容。</p>
      ) : null}
    </>
  );
}
export function LiveMessage({
  live,
  generation,
  finished = false,
  local = false,
  readOnly = false,
  approvals = [],
}) {
  const steps = live.activity || [];
  const textual = hasTextSegments(steps);
  // A streamed paragraph followed by tool calls stays in the process above;
  // only the paragraph still being written shows as the answer.
  const answer =
    live.draftMessage ||
    (textual
      ? finished || streamingAnswerOpen(steps)
        ? finalReply(live.answer, steps)
        : ""
      : live.answer);
  return (
    <div
      className={`message assistant ${finished ? "interrupted" : "live"}`}
      aria-live="polite"
    >
      {textual ? (
        <ReplyTimeline
          steps={steps}
          startedAt={generation?.startedAt}
          finishedAt={generation?.updatedAt}
          live={!finished}
          interrupted={finished}
          approvals={approvals}
        />
      ) : (
        <div className="reply-heading">
          <ActivityTrace
            steps={steps}
            startedAt={generation?.startedAt}
            finishedAt={generation?.updatedAt}
            live={!finished}
            interrupted={finished}
            approvals={approvals}
          />
        </div>
      )}
      {answer ? <RichContent message={answer} /> : null}
      {!finished && !local && !readOnly && generation?.id ? (
        <GenerationDiagnostics
          sessionId={generation.sessionId}
          generationId={generation.id}
          compact
        />
      ) : null}
      {finished ? (
        <p className="reply-stopped">
          {generation?.status === "failed" && generation?.error
            ? generation.error
            : "已停止，以上是停止前的内容。"}
        </p>
      ) : null}
    </div>
  );
}

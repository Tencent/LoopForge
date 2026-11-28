import React, { useCallback, useEffect, useMemo, useRef, useState } from "react";
import ActivityTrace, { ReplyTimeline } from "./ActivityTrace";
import GenerationDiagnostics from "./GenerationDiagnostics";
import HarnessIcon from "./HarnessIcon";
import KnowledgeSources from "./KnowledgeSources";
import { Avatar, authorFor } from "./Participants";
import { RichContent } from "./RichContent";
import { finalReply, hasTextSegments, streamingAnswerOpen } from "./activity";
import { parseReply, visibleText } from "./api";
import { elementTitle } from "./element-context.js";
import { fullTime, messageSentAt, messageTimeLabel } from "./message-info.js";
import {
  buildKnowledgeSummary,
  formatTextForCopy,
} from "./knowledge-citations.js";

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

function narrationTexts(steps) {
  return steps.filter((s) => s.kind === "text" && s.text).map((s) => s.text);
}

export function replyText(message, steps) {
  const text = parseReply(message).message;
  const reply = hasTextSegments(steps) ? finalReply(text, steps) : text;
  if (message?.knowledgeReads?.length > 0) {
    return formatTextForCopy(reply, message.knowledgeReads, [
      ...narrationTexts(steps),
      reply,
    ]);
  }
  return reply;
}

function ReplyHeading({ message, steps, deferredActivity }) {
  return (
    <div className="reply-heading">
      <ActivityTrace
        steps={steps}
        startedAt={message.startedAt}
        finishedAt={message.createdAt}
        interrupted={!!message.unfinished}
        deferredActivity={deferredActivity}
      />
    </div>
  );
}
export function AssistantTurn({
  message,
  steps,
  deferredActivity,
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
  const answerText = typeof answer === "string" ? answer : visibleText(answer);
  const hasReads =
    Array.isArray(message.knowledgeReads) && message.knowledgeReads.length > 0;
  const knowledgeSummary = useMemo(() => {
    if (!hasReads) return null;
    return buildKnowledgeSummary(message.knowledgeReads, [
      ...narrationTexts(steps),
      answerText,
    ]);
  }, [hasReads, message.knowledgeReads, steps, answerText]);
  const [highlightedCite, setHighlightedCite] = useState(null);
  const sourcesRef = useRef(null);
  const highlightTimer = useRef(null);
  useEffect(() => () => window.clearTimeout(highlightTimer.current), []);
  const handleCitationClick = useCallback((citeIndex) => {
    setHighlightedCite(citeIndex);
    window.clearTimeout(highlightTimer.current);
    highlightTimer.current = window.setTimeout(
      () => setHighlightedCite(null),
      2000,
    );
  }, []);
  return (
    <>
      {hasTextSegments(steps) ? (
        <ReplyTimeline
          steps={steps}
          startedAt={message.startedAt}
          finishedAt={message.createdAt}
          deferredActivity={deferredActivity}
          knowledgeReads={message.knowledgeReads}
          knowledgeSummary={knowledgeSummary}
          onCitationClick={handleCitationClick}
        />
      ) : (
        <ReplyHeading
          message={message}
          steps={steps}
          deferredActivity={deferredActivity}
        />
      )}
      <div className="message-body">
        <RichContent
          message={answer}
          assets={assets}
          projectId={projectId}
          knowledgeReads={message.knowledgeReads}
          knowledgeSummary={knowledgeSummary}
          onCitationClick={handleCitationClick}
        />
      </div>
      <KnowledgeSources
        knowledgeReads={message.knowledgeReads}
        text={answerText}
        summary={knowledgeSummary}
        highlightedIndex={highlightedCite}
        containerRef={sourcesRef}
      />
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
  hasKnowledgeRefs = false,
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
          citationsEnabled={hasKnowledgeRefs}
          streaming={!finished}
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
            citationProps={{
              citationsEnabled: hasKnowledgeRefs,
              streaming: !finished,
            }}
            approvals={approvals}
          />
        </div>
      )}
      {answer ? (
        <RichContent
          message={answer}
          citationsEnabled={hasKnowledgeRefs}
          streaming={!finished}
        />
      ) : null}
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
